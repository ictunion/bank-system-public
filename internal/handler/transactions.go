package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

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
