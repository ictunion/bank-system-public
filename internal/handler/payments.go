package handler

import (
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/keycloak"
)

// workplaceGroupUUIDs parses Keycloak group IDs (from Provider.UserGroupIDs)
// into the []pgtype.UUID the workplace-scoped queries need. Entries that
// aren't UUID-shaped (a rep can belong to other, unrelated Keycloak groups
// alongside their workplace one) are silently dropped rather than failing
// the request — they can't match any members.workplace_executive_committee_sub
// value anyway.
func workplaceGroupUUIDs(groups []string) []pgtype.UUID {
	out := make([]pgtype.UUID, 0, len(groups))
	for _, g := range groups {
		var u pgtype.UUID
		if err := u.Scan(g); err == nil {
			out = append(out, u)
		}
	}
	return out
}

type coveredMonth struct {
	Year  int32 `json:"year"`
	Month int16 `json:"month"`
}

type paymentHistoryEntry struct {
	ProcessedTransactionID int64          `json:"processed_transaction_id"`
	TransactionDate        string         `json:"transaction_date"`
	Amount                 string         `json:"amount"`
	Currency               string         `json:"currency"`
	CoveredMonths          []coveredMonth `json:"covered_months"`
}

// PaymentHistory handles GET /payments/{member_number}/history — a member's
// payment history grouped by transaction (see docs/logic-design.md "Payment
// History Endpoint"). Driven by payment_coverage, not processed_transactions
// directly, so a lump-sum payment covering several months comes back as one
// entry with several covered_months rather than one row per month.
//
// Two ways in, gated by RequireAnyRole(RolePaymentHistory,
// RoleViewWorkplacePaymentHistory): an admin (RolePaymentHistory) can look up any
// member; a workplace rep (RoleViewWorkplacePaymentHistory only) can look up a
// member only if that member's workplace_executive_committee_sub matches one
// of the rep's own Keycloak groups (looked up live via Provider.UserGroupIDs)
// — same scoping WorkplaceMissingPayments uses, just for one member_number
// instead of the whole workplace.
func PaymentHistory(provider *keycloak.Provider, queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		memberNumber, err := strconv.ParseInt(r.PathValue("member_number"), 10, 32)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid member_number")
			return
		}

		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusInternalServerError, "missing token claims")
			return
		}

		if !provider.HasRole(claims, keycloak.RolePaymentHistory) {
			token, ok := TokenFromContext(r.Context())
			if !ok {
				writeError(w, http.StatusInternalServerError, "missing token")
				return
			}
			allowed, err := memberInCallerWorkplace(r, provider, queries, token, int32(memberNumber))
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to check member")
				return
			}
			if !allowed {
				writeError(w, http.StatusForbidden, "not authorized")
				return
			}
		}

		writePaymentHistory(w, r, queries, int32(memberNumber))
	}
}

// memberInCallerWorkplace reports whether memberNumber's
// workplace_executive_committee_sub matches one of the caller's Keycloak
// groups (fetched live via Provider.UserGroupIDs, forwarding the caller's
// own token) — the non-admin access check for PaymentHistory. All three ways
// this can come back false collapse to the same client-facing 403 ("not
// authorized") — distinguishing "no such member" from "member has no
// workplace" from "wrong workplace" in the response would let a caller probe
// which member_numbers exist. The distinction is only logged, server-side.
func memberInCallerWorkplace(r *http.Request, provider *keycloak.Provider, queries *db.Queries, token string, memberNumber int32) (bool, error) {
	workplaceSub, err := queries.GetMemberWorkplaceSub(r.Context(), memberNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		log.Printf("payment history: member_number=%d not found", memberNumber)
		return false, nil
	} else if err != nil {
		return false, err
	}
	if !workplaceSub.Valid {
		log.Printf("payment history: member_number=%d has no workplace assigned", memberNumber)
		return false, nil
	}

	groups, err := provider.UserGroupIDs(r.Context(), token)
	if err != nil {
		return false, err
	}
	if !slices.Contains(workplaceGroupUUIDs(groups), workplaceSub) {
		log.Printf("payment history: caller's keycloak groups don't include member_number=%d's workplace", memberNumber)
		return false, nil
	}
	return true, nil
}

func writePaymentHistory(w http.ResponseWriter, r *http.Request, queries *db.Queries, memberNumber int32) {
	rows, err := queries.GetPaymentHistory(r.Context(), memberNumber)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch payment history")
		return
	}

	// Group rows by processed_transaction_id, preserving the query's
	// covers_year/covers_month DESC order for first appearance of each
	// transaction, so a lump-sum payment covering several months comes
	// back as one entry with several covered_months.
	order := make([]int64, 0, len(rows))
	entries := make(map[int64]*paymentHistoryEntry, len(rows))
	for _, row := range rows {
		entry, ok := entries[row.ProcessedTransactionID]
		if !ok {
			entry = &paymentHistoryEntry{
				ProcessedTransactionID: row.ProcessedTransactionID,
				TransactionDate:        row.TransactionDate.Format("2006-01-02"),
				Amount:                 row.Amount,
				Currency:               row.Currency,
			}
			entries[row.ProcessedTransactionID] = entry
			order = append(order, row.ProcessedTransactionID)
		}
		entry.CoveredMonths = append(entry.CoveredMonths, coveredMonth{Year: row.CoversYear, Month: row.CoversMonth})
	}

	out := make([]*paymentHistoryEntry, 0, len(order))
	for _, id := range order {
		out = append(out, entries[id])
	}

	writeJSON(w, http.StatusOK, out)
}

// MyPaymentHistory handles GET /payments/me/history — the self-service
// counterpart to PaymentHistory. Resolves the caller's member_number from
// their token's sub (via RequireAuth, no role required) and renders the same
// way PaymentHistory does, so both routes are guaranteed to return an
// identical response shape for the same member. Bypasses PaymentHistory's
// own role/workplace check entirely — a self-service caller is authorized by
// the sub match itself, regardless of which roles (if any) their token
// carries.
func MyPaymentHistory(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "missing token claims")
			return
		}

		var sub pgtype.UUID
		if err := sub.Scan(claims.Subject); err != nil {
			writeError(w, http.StatusForbidden, "token has no valid sub")
			return
		}

		memberNumber, err := queries.GetMemberNumberBySub(r.Context(), sub)
		if err != nil {
			writeError(w, http.StatusForbidden, "no member found for this account")
			return
		}

		writePaymentHistory(w, r, queries, memberNumber)
	}
}
