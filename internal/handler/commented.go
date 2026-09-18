package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/keycloak"
)

// commentedTransaction is one transaction matched to a member that carries a
// staff-authored admin_comment (see "Manual assignment & coverage" in
// CLAUDE.md — distinct from Fio's own comment field). Deliberately a
// separate endpoint/response from the missing-payment cohort endpoints
// (handler.missingPaymentMember) rather than folded into them: keeps
// "missing" strictly meaning "no coverage row" and lets the caller (Orca)
// merge the two lists client-side by member_number, since a commented
// transaction can exist on a member who owes nothing.
type commentedTransaction struct {
	MemberNumber            int32  `json:"member_number"`
	ProcessedTransactionID  int64  `json:"processed_transaction_id"`
	TransactionDate         string `json:"transaction_date"`
	Amount                  string `json:"amount"`
	Currency                string `json:"currency"`
	AdminComment            string `json:"admin_comment"`
}

// CommentedTransactions handles GET /payments/{year}/{month}/commented — every
// commented transaction matched to a member, dated in that calendar month,
// regardless of whether that member is otherwise missing a payment.
//
// @Summary      Commented transactions in one month
// @Description  Requires the payment-history role. Every admin_comment'd transaction dated in this month, independent of missed-payment status.
// @Tags         payments
// @Security     BearerAuth
// @Produce      json
// @Param        year   path  int  true  "1 to current year + 1"
// @Param        month  path  int  true  "1 to 12"
// @Success      200  {array}  handler.commentedTransaction
// @Failure      400,401,403  {object}  map[string]string
// @Router       /payments/{year}/{month}/commented [get]
func CommentedTransactions(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, err := strconv.Atoi(r.PathValue("year"))
		if err != nil || year < 1 || year > time.Now().Year()+1 {
			writeError(w, http.StatusBadRequest, "invalid year")
			return
		}
		month, err := strconv.Atoi(r.PathValue("month"))
		if err != nil || month < 1 || month > 12 {
			writeError(w, http.StatusBadRequest, "invalid month")
			return
		}

		rows, err := queries.ListCommentedTransactionsInMonth(r.Context(), db.ListCommentedTransactionsInMonthParams{
			Year:  int32(year),
			Month: int32(month),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load commented transactions")
			return
		}

		out := make([]commentedTransaction, 0, len(rows))
		for _, row := range rows {
			out = append(out, commentedTransaction{
				MemberNumber:           *row.MemberNumber,
				ProcessedTransactionID: row.ProcessedTransactionID,
				TransactionDate:        row.TransactionDate.Format("2006-01-02"),
				Amount:                 row.Amount,
				Currency:               row.Currency,
				AdminComment:           *row.AdminComment,
			})
		}

		writeJSON(w, http.StatusOK, out)
	}
}

// CommentedTransactionsInYear handles GET /payments/{year}/commented — every
// commented transaction matched to a member, dated anywhere in that calendar
// year. See CommentedTransactions.
//
// @Summary      Commented transactions in a year
// @Description  Requires the payment-history role. Every admin_comment'd transaction dated in this year, independent of missed-payment status.
// @Tags         payments
// @Security     BearerAuth
// @Produce      json
// @Param        year  path  int  true  "1 to current year + 1"
// @Success      200  {array}  handler.commentedTransaction
// @Failure      400,401,403  {object}  map[string]string
// @Router       /payments/{year}/commented [get]
func CommentedTransactionsInYear(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, err := strconv.Atoi(r.PathValue("year"))
		if err != nil || year < 1 || year > time.Now().Year()+1 {
			writeError(w, http.StatusBadRequest, "invalid year")
			return
		}

		rows, err := queries.ListCommentedTransactionsInYear(r.Context(), int32(year))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load commented transactions")
			return
		}

		out := make([]commentedTransaction, 0, len(rows))
		for _, row := range rows {
			out = append(out, commentedTransaction{
				MemberNumber:           *row.MemberNumber,
				ProcessedTransactionID: row.ProcessedTransactionID,
				TransactionDate:        row.TransactionDate.Format("2006-01-02"),
				Amount:                 row.Amount,
				Currency:               row.Currency,
				AdminComment:           *row.AdminComment,
			})
		}

		writeJSON(w, http.StatusOK, out)
	}
}

// WorkplaceCommentedTransactions handles
// GET /payments/workplace/{year}/{month}/commented — the workplace-rep
// counterpart to CommentedTransactions, scoped to members sharing any of the
// caller's Keycloak groups the same way WorkplaceMissingPayments is. A caller
// in no workplace groups gets an empty list, not an error.
//
// @Summary      Workplace-scoped: commented transactions in one month
// @Description  Requires the view-workplace-payment-history role. Scoped live to the caller's own Keycloak workplace group(s) via the Account API.
// @Tags         payments
// @Security     BearerAuth
// @Produce      json
// @Param        year   path  int  true  "1 to current year + 1"
// @Param        month  path  int  true  "1 to 12"
// @Success      200  {array}  handler.commentedTransaction
// @Failure      400,401,403  {object}  map[string]string
// @Failure      500  {object}  map[string]string  "Keycloak Account API call failed"
// @Router       /payments/workplace/{year}/{month}/commented [get]
func WorkplaceCommentedTransactions(provider *keycloak.Provider, queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := TokenFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusInternalServerError, "missing token")
			return
		}

		year, err := strconv.Atoi(r.PathValue("year"))
		if err != nil || year < 1 || year > time.Now().Year()+1 {
			writeError(w, http.StatusBadRequest, "invalid year")
			return
		}
		month, err := strconv.Atoi(r.PathValue("month"))
		if err != nil || month < 1 || month > 12 {
			writeError(w, http.StatusBadRequest, "invalid month")
			return
		}

		groups, err := provider.UserGroupIDs(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check workplace membership")
			return
		}
		workplaceSubs := workplaceGroupUUIDs(groups)
		if len(workplaceSubs) == 0 {
			writeJSON(w, http.StatusOK, []commentedTransaction{})
			return
		}

		rows, err := queries.ListCommentedTransactionsInMonthForWorkplace(r.Context(), db.ListCommentedTransactionsInMonthForWorkplaceParams{
			WorkplaceSubs: workplaceSubs,
			Year:          int32(year),
			Month:         int32(month),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load commented transactions")
			return
		}

		out := make([]commentedTransaction, 0, len(rows))
		for _, row := range rows {
			out = append(out, commentedTransaction{
				MemberNumber:           *row.MemberNumber,
				ProcessedTransactionID: row.ProcessedTransactionID,
				TransactionDate:        row.TransactionDate.Format("2006-01-02"),
				Amount:                 row.Amount,
				Currency:               row.Currency,
				AdminComment:           *row.AdminComment,
			})
		}

		writeJSON(w, http.StatusOK, out)
	}
}

// WorkplaceCommentedTransactionsInYear handles
// GET /payments/workplace/{year}/commented — the workplace-rep, whole-year
// counterpart to CommentedTransactionsInYear. See WorkplaceCommentedTransactions.
//
// @Summary      Workplace-scoped: commented transactions in a year
// @Description  Requires the view-workplace-payment-history role. Scoped live to the caller's own Keycloak workplace group(s) via the Account API.
// @Tags         payments
// @Security     BearerAuth
// @Produce      json
// @Param        year  path  int  true  "1 to current year + 1"
// @Success      200  {array}  handler.commentedTransaction
// @Failure      400,401,403  {object}  map[string]string
// @Failure      500  {object}  map[string]string  "Keycloak Account API call failed"
// @Router       /payments/workplace/{year}/commented [get]
func WorkplaceCommentedTransactionsInYear(provider *keycloak.Provider, queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := TokenFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusInternalServerError, "missing token")
			return
		}

		year, err := strconv.Atoi(r.PathValue("year"))
		if err != nil || year < 1 || year > time.Now().Year()+1 {
			writeError(w, http.StatusBadRequest, "invalid year")
			return
		}

		groups, err := provider.UserGroupIDs(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check workplace membership")
			return
		}
		workplaceSubs := workplaceGroupUUIDs(groups)
		if len(workplaceSubs) == 0 {
			writeJSON(w, http.StatusOK, []commentedTransaction{})
			return
		}

		rows, err := queries.ListCommentedTransactionsInYearForWorkplace(r.Context(), db.ListCommentedTransactionsInYearForWorkplaceParams{
			WorkplaceSubs: workplaceSubs,
			Year:          int32(year),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load commented transactions")
			return
		}

		out := make([]commentedTransaction, 0, len(rows))
		for _, row := range rows {
			out = append(out, commentedTransaction{
				MemberNumber:           *row.MemberNumber,
				ProcessedTransactionID: row.ProcessedTransactionID,
				TransactionDate:        row.TransactionDate.Format("2006-01-02"),
				Amount:                 row.Amount,
				Currency:               row.Currency,
				AdminComment:           *row.AdminComment,
			})
		}

		writeJSON(w, http.StatusOK, out)
	}
}
