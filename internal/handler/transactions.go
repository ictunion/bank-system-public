package handler

import (
	"encoding/json"
	"errors"
	"net/http"
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
		queryParams := r.URL.Query()

		params := db.ListTransactionsParams{
			Lim: transactionsDefaultLimit,
			Off: 0,
		}

		switch queryParams.Get("assigned") {
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
		if params.Direction, ok = enumParam(queryParams.Get("direction"), validDirections); !ok {
			writeError(w, http.StatusBadRequest, "invalid direction")
			return
		}
		if s := queryParams.Get("category"); s != "" {
			params.Category = &s
		}
		if params.MatchedBy, ok = enumParam(queryParams.Get("matched_by"), validMatchedBy); !ok {
			writeError(w, http.StatusBadRequest, "invalid matched_by")
			return
		}

		if s := queryParams.Get("member_number"); s != "" {
			n, err := strconv.ParseInt(s, 10, 32)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid member_number")
				return
			}
			v := int32(n)
			params.MemberNumber = &v
		}

		if params.DateFrom, ok = dateParam(queryParams.Get("from")); !ok {
			writeError(w, http.StatusBadRequest, "from must be YYYY-MM-DD")
			return
		}
		if params.DateTo, ok = dateParam(queryParams.Get("to")); !ok {
			writeError(w, http.StatusBadRequest, "to must be YYYY-MM-DD")
			return
		}

		if s := queryParams.Get("limit"); s != "" {
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
		if s := queryParams.Get("offset"); s != "" {
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

		response := transactionsResponse{
			Limit:        params.Lim,
			Offset:       params.Off,
			Transactions: make([]transactionListItem, 0, len(rows)),
		}
		if len(rows) > 0 {
			response.Total = rows[0].TotalCount
		}
		for _, row := range rows {
			response.Transactions = append(response.Transactions, transactionListItem{
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

		writeJSON(w, http.StatusOK, response)
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
	MemberNumber *int32     `json:"member_number"` // optional; omit for a category-only edit, no member match
	Category     *string    `json:"category"`      // optional, defaults to "membership_fee"
	Covers       []monthRef `json:"covers"`        // optional; empty = the transaction's own month
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

// AssignTransaction handles PUT /transactions/{id}/assignment — manual
// categorization, in one DB transaction (see docs/logic-design.md "Manual
// Assignment & Coverage"). member_number is optional: a lot of transactions
// (other_income/other_expense, even some salary rows) aren't tied to any
// member, so this also serves as a category-only edit — omit member_number
// to just change the category without matching anyone. matched_by is set to
// 'manual' when a member is given, NULL otherwise. For category=membership_fee
// *with* a member, the coverage rows are replaced with `covers` (or the
// transaction's own month when `covers` is empty); no member or a non-fee
// category carries no coverage. A month already covered by a *different*
// transaction is a 409.
func AssignTransaction(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseTransactionID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}

		var request assignTransactionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if request.MemberNumber != nil && *request.MemberNumber < 1 {
			writeError(w, http.StatusBadRequest, "member_number must be positive")
			return
		}

		category := "membership_fee"
		if request.Category != nil {
			category = *request.Category
		}
		coverable := category == "membership_fee" && request.MemberNumber != nil
		if len(request.Covers) > 0 {
			if category != "membership_fee" {
				writeError(w, http.StatusBadRequest, "covers is only valid for category membership_fee")
				return
			}
			if request.MemberNumber == nil {
				writeError(w, http.StatusBadRequest, "covers requires a member_number")
				return
			}
		}
		maxYear := time.Now().Year() + 1
		for _, m := range request.Covers {
			if m.Month < 1 || m.Month > 12 || m.Year < 2000 || m.Year > maxYear {
				writeError(w, http.StatusBadRequest, "invalid covers entry")
				return
			}
		}

		queries := db.New(pool)

		if request.MemberNumber != nil {
			exists, err := queries.MemberExists(r.Context(), *request.MemberNumber)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to check member")
				return
			}
			if !exists {
				writeError(w, http.StatusBadRequest, "member_number does not exist")
				return
			}
		}

		categoryExists, err := queries.CategoryExists(r.Context(), category)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check category")
			return
		}
		if !categoryExists {
			writeError(w, http.StatusBadRequest, "invalid category")
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
			if len(request.Covers) > 0 {
				months = request.Covers
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
		txQueries := db.New(tx)

		var matchedBy *string
		if request.MemberNumber != nil {
			manual := "manual"
			matchedBy = &manual
		}

		if _, err := txQueries.AssignTransactionToMember(r.Context(), db.AssignTransactionToMemberParams{
			ID:           id,
			MemberNumber: request.MemberNumber,
			Category:     category,
			MatchedBy:    matchedBy,
		}); errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "transaction not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to assign transaction")
			return
		}

		if err := txQueries.DeleteCoverageForTransaction(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to clear coverage")
			return
		}

		var conflicts []monthRef
		for _, m := range months {
			n, err := txQueries.InsertCoverageRow(r.Context(), db.InsertCoverageRowParams{
				ProcessedTransactionID: id,
				MemberNumber:           *request.MemberNumber,
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
		txQueries := db.New(tx)

		if err := txQueries.DeleteCoverageForTransaction(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to clear coverage")
			return
		}
		if _, err := txQueries.UnassignTransaction(r.Context(), db.UnassignTransactionParams{
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

type categoryTotal struct {
	Category string `json:"category"`
	Currency string `json:"currency"`
	Total    string `json:"total"`
}

type categorySummaryResponse struct {
	Incoming []categoryTotal `json:"incoming"`
	Outgoing []categoryTotal `json:"outgoing"`
}

// CategorySummary handles GET /transactions/summary — totals grouped by
// category and direction for the budgeting view (see docs/logic-design.md
// "Transaction Category Summary"). Optional from/to (YYYY-MM-DD, inclusive)
// query params scope it to a date range, same convention as ListTransactions.
// Gated by RoleViewBudget rather than RoleListTransactions: unlike the
// transaction browser, this never returns a member_number, counterparty, or
// any other per-transaction detail — only category/currency/total — so it's
// meant to be safe for every member to see, not just admins.
func CategorySummary(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		queryParams := r.URL.Query()

		dateFrom, ok := dateParam(queryParams.Get("from"))
		if !ok {
			writeError(w, http.StatusBadRequest, "from must be YYYY-MM-DD")
			return
		}
		dateTo, ok := dateParam(queryParams.Get("to"))
		if !ok {
			writeError(w, http.StatusBadRequest, "to must be YYYY-MM-DD")
			return
		}

		rows, err := queries.GetTransactionCategorySummary(r.Context(), db.GetTransactionCategorySummaryParams{
			DateFrom: dateFrom,
			DateTo:   dateTo,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to summarize transactions")
			return
		}

		response := categorySummaryResponse{
			Incoming: make([]categoryTotal, 0),
			Outgoing: make([]categoryTotal, 0),
		}
		for _, row := range rows {
			total := categoryTotal{Category: row.Category, Currency: row.Currency, Total: row.Total}
			if row.Direction == "incoming" {
				response.Incoming = append(response.Incoming, total)
			} else {
				response.Outgoing = append(response.Outgoing, total)
			}
		}

		writeJSON(w, http.StatusOK, response)
	}
}
