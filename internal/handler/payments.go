package handler

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/bank-system/internal/db"
)

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
func PaymentHistory(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		memberNumber, err := strconv.ParseInt(r.PathValue("member_number"), 10, 32)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid member_number")
			return
		}

		rows, err := queries.GetPaymentHistory(r.Context(), int32(memberNumber))
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
}

// MyPaymentHistory handles GET /payments/me/history — the self-service
// counterpart to PaymentHistory. Resolves the caller's member_number from
// their token's sub (via RequireAuth, no role required) and delegates to
// PaymentHistory for the actual response, so both routes are guaranteed to
// return the exact same shape for the exact same member.
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

		r.SetPathValue("member_number", strconv.Itoa(int(memberNumber)))
		PaymentHistory(queries)(w, r)
	}
}
