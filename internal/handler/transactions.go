package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/db"
)

const (
	transactionsDefaultLimit = 100
	transactionsMaxLimit     = 500
)

var (
	validDirections = []string{"incoming", "outgoing"}
	validCategories = []string{"membership_fee", "salary", "other_income", "other_expense"}
	validMatchedBy  = []string{"variable_symbol", "manual", "amount_heuristic"}
)

type transactionsResponse struct {
	Total        int64                 `json:"total"`
	Limit        int32                 `json:"limit"`
	Offset       int32                 `json:"offset"`
	Transactions []transactionListItem `json:"transactions"`
}

type transactionListItem struct {
	ID                   int64   `json:"id"`
	TransactionDate      string  `json:"transaction_date"`
	Amount               string  `json:"amount"`
	Currency             string  `json:"currency"`
	Direction            string  `json:"direction"`
	Category             string  `json:"category"`
	MemberNumber         *int32  `json:"member_number"`
	MatchedBy            *string `json:"matched_by"`
	IsPublicVisible      bool    `json:"is_public_visible"`
	VariableSymbol       *string `json:"variable_symbol"`
	SpecificSymbol       *string `json:"specific_symbol"`
	ConstantSymbol       *string `json:"constant_symbol"`
	CounterAccountNumber *string `json:"counter_account_number"`
	CounterAccountName   *string `json:"counter_account_name"`
	MessageForRecipient  *string `json:"message_for_recipient"`
	UserIdentification   *string `json:"user_identification"`
	Comment              *string `json:"comment"`
}

// ListTransactions handles GET /transactions — the admin transaction browser.
// All filters are optional query params (see docs/logic-design.md "Transaction
// Browser"): assigned (true|false), direction, category, matched_by,
// member_number, from, to (YYYY-MM-DD), limit (<=500, default 100), offset.
func ListTransactions(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		params := db.ListTransactionsParams{
			Lim: transactionsDefaultLimit,
			Off: 0,
		}

		switch q.Get("assigned") {
		case "":
		case "true":
			t := true
			params.Assigned = &t
		case "false":
			f := false
			params.Assigned = &f
		default:
			writeError(w, http.StatusBadRequest, "assigned must be true or false")
			return
		}

		var ok bool
		if params.Direction, ok = enumParam(q.Get("direction"), validDirections); !ok {
			writeError(w, http.StatusBadRequest, "invalid direction")
			return
		}
		if params.Category, ok = enumParam(q.Get("category"), validCategories); !ok {
			writeError(w, http.StatusBadRequest, "invalid category")
			return
		}
		if params.MatchedBy, ok = enumParam(q.Get("matched_by"), validMatchedBy); !ok {
			writeError(w, http.StatusBadRequest, "invalid matched_by")
			return
		}

		if s := q.Get("member_number"); s != "" {
			n, err := strconv.ParseInt(s, 10, 32)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid member_number")
				return
			}
			v := int32(n)
			params.MemberNumber = &v
		}

		if params.DateFrom, ok = dateParam(q.Get("from")); !ok {
			writeError(w, http.StatusBadRequest, "from must be YYYY-MM-DD")
			return
		}
		if params.DateTo, ok = dateParam(q.Get("to")); !ok {
			writeError(w, http.StatusBadRequest, "to must be YYYY-MM-DD")
			return
		}

		if s := q.Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				writeError(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			if n > transactionsMaxLimit {
				n = transactionsMaxLimit
			}
			params.Lim = int32(n)
		}
		if s := q.Get("offset"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 {
				writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
				return
			}
			params.Off = int32(n)
		}

		rows, err := queries.ListTransactions(r.Context(), params)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list transactions")
			return
		}

		resp := transactionsResponse{
			Limit:        params.Lim,
			Offset:       params.Off,
			Transactions: make([]transactionListItem, 0, len(rows)),
		}
		if len(rows) > 0 {
			resp.Total = rows[0].TotalCount
		}
		for _, row := range rows {
			resp.Transactions = append(resp.Transactions, transactionListItem{
				ID:                   row.ID,
				TransactionDate:      row.TransactionDate.Format("2006-01-02"),
				Amount:               row.Amount,
				Currency:             row.Currency,
				Direction:            row.Direction,
				Category:             row.Category,
				MemberNumber:         row.MemberNumber,
				MatchedBy:            row.MatchedBy,
				IsPublicVisible:      row.IsPublicVisible,
				VariableSymbol:       row.VariableSymbol,
				SpecificSymbol:       row.SpecificSymbol,
				ConstantSymbol:       row.ConstantSymbol,
				CounterAccountNumber: row.CounterAccountNumber,
				CounterAccountName:   row.CounterAccountName,
				MessageForRecipient:  row.MessageForRecipient,
				UserIdentification:   row.UserIdentification,
				Comment:              row.Comment,
			})
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// enumParam returns (nil, true) for an empty value, (&v, true) if v is in
// allowed, and (nil, false) if v is set but not allowed.
func enumParam(v string, allowed []string) (*string, bool) {
	if v == "" {
		return nil, true
	}
	for _, a := range allowed {
		if v == a {
			return &v, true
		}
	}
	return nil, false
}

// dateParam parses an optional YYYY-MM-DD query param into a pgtype.Date.
// Empty -> zero (invalid) Date, ok. Malformed -> ok false.
func dateParam(v string) (pgtype.Date, bool) {
	if v == "" {
		return pgtype.Date{}, true
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return pgtype.Date{}, false
	}
	return pgtype.Date{Time: t, Valid: true}, true
}

// --- Transaction detail + manual assignment -------------------------------------

type monthRef struct {
	Year  int `json:"year"`
	Month int `json:"month"`
}

type transactionDetail struct {
	transactionListItem
	CoveredMonths []monthRef `json:"covered_months"`
}

type assignTransactionRequest struct {
	MemberNumber int32      `json:"member_number"`
	Category     *string    `json:"category"` // optional, defaults to "membership_fee"
	Covers       []monthRef `json:"covers"`   // optional; empty = the transaction's own month
}

func parseTransactionID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, false
	}
	return id, true
}

func detailRowToItem(row db.GetTransactionDetailRow) transactionListItem {
	return transactionListItem{
		ID:                   row.ID,
		TransactionDate:      row.TransactionDate.Format("2006-01-02"),
		Amount:               row.Amount,
		Currency:             row.Currency,
		Direction:            row.Direction,
		Category:             row.Category,
		MemberNumber:         row.MemberNumber,
		MatchedBy:            row.MatchedBy,
		IsPublicVisible:      row.IsPublicVisible,
		VariableSymbol:       row.VariableSymbol,
		SpecificSymbol:       row.SpecificSymbol,
		ConstantSymbol:       row.ConstantSymbol,
		CounterAccountNumber: row.CounterAccountNumber,
		CounterAccountName:   row.CounterAccountName,
		MessageForRecipient:  row.MessageForRecipient,
		UserIdentification:   row.UserIdentification,
		Comment:              row.Comment,
	}
}

func writeTransactionDetail(w http.ResponseWriter, r *http.Request, queries *db.Queries, id int64) {
	row, err := queries.GetTransactionDetail(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load transaction")
		return
	}
	cov, err := queries.ListCoverageForTransaction(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load coverage")
		return
	}
	detail := transactionDetail{
		transactionListItem: detailRowToItem(row),
		CoveredMonths:       make([]monthRef, 0, len(cov)),
	}
	for _, c := range cov {
		detail.CoveredMonths = append(detail.CoveredMonths, monthRef{Year: int(c.CoversYear), Month: int(c.CoversMonth)})
	}
	writeJSON(w, http.StatusOK, detail)
}

// GetTransaction handles GET /transactions/{id} — one transaction with its
// covered months, for the browser's detail / edit view.
func GetTransaction(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseTransactionID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		if _, err := queries.GetTransactionDetail(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "transaction not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load transaction")
			return
		}
		writeTransactionDetail(w, r, queries, id)
	}
}

// AssignTransaction handles PUT /transactions/{id}/assignment — manual member
// match plus payment_coverage, in one DB transaction (see docs/logic-design.md
// "Manual Assignment & Coverage"). matched_by is set to 'manual'. For
// category=membership_fee the coverage rows are replaced with `covers` (or the
// transaction's own month when `covers` is empty); other categories carry no
// coverage. A month already covered by a *different* transaction is a 409.
func AssignTransaction(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseTransactionID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}

		var req assignTransactionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.MemberNumber < 1 {
			writeError(w, http.StatusBadRequest, "member_number is required")
			return
		}

		category := "membership_fee"
		if req.Category != nil {
			category = *req.Category
		}
		if !slices.Contains(validCategories, category) {
			writeError(w, http.StatusBadRequest, "invalid category")
			return
		}
		coverable := category == "membership_fee"
		if !coverable && len(req.Covers) > 0 {
			writeError(w, http.StatusBadRequest, "covers is only valid for category membership_fee")
			return
		}
		maxYear := time.Now().Year() + 1
		for _, m := range req.Covers {
			if m.Month < 1 || m.Month > 12 || m.Year < 2000 || m.Year > maxYear {
				writeError(w, http.StatusBadRequest, "invalid covers entry")
				return
			}
		}

		queries := db.New(pool)

		exists, err := queries.MemberExists(r.Context(), req.MemberNumber)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check member")
			return
		}
		if !exists {
			writeError(w, http.StatusBadRequest, "member_number does not exist")
			return
		}

		detail, err := queries.GetTransactionDetail(r.Context(), id)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "transaction not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load transaction")
			return
		}

		var months []monthRef
		if coverable {
			if len(req.Covers) > 0 {
				months = req.Covers
			} else {
				months = []monthRef{{Year: detail.TransactionDate.Year(), Month: int(detail.TransactionDate.Month())}}
			}
		}

		tx, err := pool.Begin(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to start transaction")
			return
		}
		defer tx.Rollback(r.Context())
		q := db.New(tx)

		if _, err := q.AssignTransactionToMember(r.Context(), db.AssignTransactionToMemberParams{
			ID:           id,
			MemberNumber: &req.MemberNumber,
			Category:     category,
		}); errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "transaction not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to assign transaction")
			return
		}

		if err := q.DeleteCoverageForTransaction(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to clear coverage")
			return
		}

		var conflicts []monthRef
		for _, m := range months {
			n, err := q.InsertCoverageRow(r.Context(), db.InsertCoverageRowParams{
				ProcessedTransactionID: id,
				MemberNumber:           req.MemberNumber,
				CoversYear:             int32(m.Year),
				CoversMonth:            int16(m.Month),
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to write coverage")
				return
			}
			if n == 0 {
				conflicts = append(conflicts, m)
			}
		}
		if len(conflicts) > 0 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":     "some months are already covered by another transaction",
				"conflicts": conflicts,
			})
			return
		}

		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to commit")
			return
		}

		writeTransactionDetail(w, r, queries, id)
	}
}

// UnassignTransaction handles DELETE /transactions/{id}/assignment — clears the
// member match and matched_by, deletes the transaction's payment_coverage rows,
// and resets category to the direction-based default (other_income /
// other_expense). One DB transaction.
func UnassignTransaction(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseTransactionID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}

		queries := db.New(pool)
		detail, err := queries.GetTransactionDetail(r.Context(), id)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "transaction not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load transaction")
			return
		}

		category := "other_expense"
		if detail.Direction == "incoming" {
			category = "other_income"
		}

		tx, err := pool.Begin(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to start transaction")
			return
		}
		defer tx.Rollback(r.Context())
		q := db.New(tx)

		if err := q.DeleteCoverageForTransaction(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to clear coverage")
			return
		}
		if _, err := q.UnassignTransaction(r.Context(), db.UnassignTransactionParams{
			ID:       id,
			Category: category,
		}); errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "transaction not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to unassign transaction")
			return
		}

		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to commit")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
