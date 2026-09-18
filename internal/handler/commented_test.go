package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
	"github.com/kubik/bank-system/internal/keycloaktest"
)

// seedTransactionDated is seedTransaction (transactions_test.go) with a
// caller-chosen transaction_date instead of a fixed time.Now() — the
// /commented endpoints filter on transaction_date, so the month/year-scoping
// tests below need control over it.
func seedTransactionDated(t *testing.T, queries *db.Queries, bankAccountID int32, fioTransactionID int64, transactionDate time.Time, category string, memberNumber *int32) db.ProcessedTransaction {
	t.Helper()
	ctx := context.Background()

	if _, err := queries.InsertRawTransaction(ctx, db.InsertRawTransactionParams{
		BankAccountID:    bankAccountID,
		FioTransactionID: fioTransactionID,
		TransactionDate:  transactionDate,
		Amount:           "500.00",
		Currency:         "CZK",
		RawPayload:       []byte("{}"),
	}); err != nil {
		t.Fatalf("InsertRawTransaction: %v", err)
	}

	unprocessed, err := queries.ListUnprocessedTransactions(ctx)
	if err != nil {
		t.Fatalf("ListUnprocessedTransactions: %v", err)
	}
	var rawTransactionID int64 = -1
	for _, raw := range unprocessed {
		if raw.FioTransactionID == fioTransactionID && raw.BankAccountID == bankAccountID {
			rawTransactionID = raw.ID
		}
	}
	if rawTransactionID == -1 {
		t.Fatalf("seedTransactionDated: just-inserted fio_transaction_id=%d not found via ListUnprocessedTransactions", fioTransactionID)
	}

	var matchedBy *string
	if memberNumber != nil {
		manual := "manual"
		matchedBy = &manual
	}
	pt, err := queries.CreateProcessedTransaction(ctx, db.CreateProcessedTransactionParams{
		RawTransactionID: rawTransactionID,
		MemberNumber:     memberNumber,
		Category:         category,
		Direction:        "incoming",
		MatchedBy:        matchedBy,
	})
	if err != nil {
		t.Fatalf("CreateProcessedTransaction: %v", err)
	}
	return pt
}

// setAdminComment sets processed_transactions.admin_comment on an
// already-created row via the same AssignTransactionToMember query the real
// PUT /transactions/{id}/assignment endpoint uses, so these fixtures go
// through the same write path production traffic does.
func setAdminComment(t *testing.T, queries *db.Queries, pt db.ProcessedTransaction, comment string) {
	t.Helper()
	matchedBy := "manual"
	if _, err := queries.AssignTransactionToMember(context.Background(), db.AssignTransactionToMemberParams{
		ID:           pt.ID,
		MemberNumber: pt.MemberNumber,
		Category:     pt.Category,
		MatchedBy:    &matchedBy,
		AdminComment: &comment,
	}); err != nil {
		t.Fatalf("setAdminComment(id=%d): %v", pt.ID, err)
	}
}

func commentedTransactions(t *testing.T, recorder *httptest.ResponseRecorder) []commentedTransaction {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got []commentedTransaction
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return got
}

func containsCommentedTransactionID(rows []commentedTransaction, processedTransactionID int64) bool {
	for _, r := range rows {
		if r.ProcessedTransactionID == processedTransactionID {
			return true
		}
	}
	return false
}

func TestCommentedTransactions_Validation(t *testing.T) {
	queries := dbtest.Tx(t)

	tests := []struct {
		name  string
		year  string
		month string
	}{
		{"non-numeric year", "abc", "1"},
		{"zero year", "0", "1"},
		{"year too far in the future", "9999", "1"},
		{"non-numeric month", "2026", "abc"},
		{"month zero", "2026", "0"},
		{"month thirteen", "2026", "13"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/payments/"+tt.year+"/"+tt.month+"/commented", nil)
			request.SetPathValue("year", tt.year)
			request.SetPathValue("month", tt.month)
			CommentedTransactions(queries)(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
			}
		})
	}
}

func TestCommentedTransactions_Empty(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/2026/1/commented", nil)
	request.SetPathValue("year", "2026")
	request.SetPathValue("month", "1")
	CommentedTransactions(queries)(recorder, request)

	if got := commentedTransactions(t, recorder); len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

func TestCommentedTransactions_ReturnsCommentedTransactionInQueriedMonth(t *testing.T) {
	queries := dbtest.Tx(t)
	account := seedBankAccount(t, queries, "9300000101")
	const memberNumber = int32(900501)
	seedMember(t, queries, memberNumber, nil)

	now := time.Now()
	member := memberNumber
	pt := seedTransactionDated(t, queries, account.ID, 8001, now, "membership_fee", &member)
	setAdminComment(t, queries, pt, "missing VS")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/"+itoa(int32(now.Year()))+"/"+itoa(int32(now.Month()))+"/commented", nil)
	request.SetPathValue("year", itoa(int32(now.Year())))
	request.SetPathValue("month", itoa(int32(now.Month())))
	CommentedTransactions(queries)(recorder, request)

	got := commentedTransactions(t, recorder)
	if !containsCommentedTransactionID(got, pt.ID) {
		t.Fatalf("commented transaction %d missing from response: %+v", pt.ID, got)
	}
	var row commentedTransaction
	for _, r := range got {
		if r.ProcessedTransactionID == pt.ID {
			row = r
		}
	}
	if row.MemberNumber != memberNumber {
		t.Errorf("MemberNumber = %d, want %d", row.MemberNumber, memberNumber)
	}
	if row.AdminComment != "missing VS" {
		t.Errorf("AdminComment = %q, want %q", row.AdminComment, "missing VS")
	}
	if row.Amount != "500.00" || row.Currency != "CZK" {
		t.Errorf("Amount/Currency = %q/%q, want 500.00/CZK", row.Amount, row.Currency)
	}
}

func TestCommentedTransactions_ExcludesDifferentMonth(t *testing.T) {
	queries := dbtest.Tx(t)
	account := seedBankAccount(t, queries, "9300000102")
	const memberNumber = int32(900502)
	seedMember(t, queries, memberNumber, nil)

	lastMonth := time.Now().AddDate(0, -1, 0)
	member := memberNumber
	pt := seedTransactionDated(t, queries, account.ID, 8002, lastMonth, "membership_fee", &member)
	setAdminComment(t, queries, pt, "missing VS")

	now := time.Now()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/"+itoa(int32(now.Year()))+"/"+itoa(int32(now.Month()))+"/commented", nil)
	request.SetPathValue("year", itoa(int32(now.Year())))
	request.SetPathValue("month", itoa(int32(now.Month())))
	CommentedTransactions(queries)(recorder, request)

	got := commentedTransactions(t, recorder)
	if containsCommentedTransactionID(got, pt.ID) {
		t.Errorf("transaction dated last month leaked into this month's response: %+v", got)
	}
}

func TestCommentedTransactions_ExcludesUncommented(t *testing.T) {
	queries := dbtest.Tx(t)
	account := seedBankAccount(t, queries, "9300000103")
	const memberNumber = int32(900503)
	seedMember(t, queries, memberNumber, nil)

	member := memberNumber
	pt := seedTransactionDated(t, queries, account.ID, 8003, time.Now(), "membership_fee", &member)
	// no setAdminComment call — admin_comment stays NULL

	now := time.Now()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/"+itoa(int32(now.Year()))+"/"+itoa(int32(now.Month()))+"/commented", nil)
	request.SetPathValue("year", itoa(int32(now.Year())))
	request.SetPathValue("month", itoa(int32(now.Month())))
	CommentedTransactions(queries)(recorder, request)

	got := commentedTransactions(t, recorder)
	if containsCommentedTransactionID(got, pt.ID) {
		t.Errorf("uncommented transaction leaked into response: %+v", got)
	}
}

func TestCommentedTransactions_ExcludesUnassignedMember(t *testing.T) {
	queries := dbtest.Tx(t)
	account := seedBankAccount(t, queries, "9300000104")

	// A comment can be set without a member match (category-only edit) —
	// member_number is NULL, so this can't attribute to anyone and must be
	// excluded, same as the missing-payment endpoints only ever report on
	// matched members.
	pt := seedTransactionDated(t, queries, account.ID, 8004, time.Now(), "other_income", nil)
	setAdminComment(t, queries, pt, "needs review")

	now := time.Now()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/"+itoa(int32(now.Year()))+"/"+itoa(int32(now.Month()))+"/commented", nil)
	request.SetPathValue("year", itoa(int32(now.Year())))
	request.SetPathValue("month", itoa(int32(now.Month())))
	CommentedTransactions(queries)(recorder, request)

	got := commentedTransactions(t, recorder)
	if containsCommentedTransactionID(got, pt.ID) {
		t.Errorf("commented transaction with no member match leaked into response: %+v", got)
	}
}

func TestCommentedTransactionsInYear_Validation(t *testing.T) {
	queries := dbtest.Tx(t)

	tests := []string{"abc", "0", "9999"}
	for _, year := range tests {
		t.Run(year, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/payments/"+year+"/commented", nil)
			request.SetPathValue("year", year)
			CommentedTransactionsInYear(queries)(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
			}
		})
	}
}

func TestCommentedTransactionsInYear_ReturnsCommentedTransactionInQueriedYear(t *testing.T) {
	queries := dbtest.Tx(t)
	account := seedBankAccount(t, queries, "9300000105")
	const memberNumber = int32(900504)
	seedMember(t, queries, memberNumber, nil)

	now := time.Now()
	member := memberNumber
	pt := seedTransactionDated(t, queries, account.ID, 8005, now, "membership_fee", &member)
	setAdminComment(t, queries, pt, "missing VS")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/"+itoa(int32(now.Year()))+"/commented", nil)
	request.SetPathValue("year", itoa(int32(now.Year())))
	CommentedTransactionsInYear(queries)(recorder, request)

	got := commentedTransactions(t, recorder)
	if !containsCommentedTransactionID(got, pt.ID) {
		t.Errorf("commented transaction %d missing from response: %+v", pt.ID, got)
	}
}

func TestCommentedTransactionsInYear_ExcludesDifferentYear(t *testing.T) {
	queries := dbtest.Tx(t)
	account := seedBankAccount(t, queries, "9300000106")
	const memberNumber = int32(900505)
	seedMember(t, queries, memberNumber, nil)

	lastYear := time.Now().AddDate(-1, 0, 0)
	member := memberNumber
	pt := seedTransactionDated(t, queries, account.ID, 8006, lastYear, "membership_fee", &member)
	setAdminComment(t, queries, pt, "missing VS")

	now := time.Now()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/"+itoa(int32(now.Year()))+"/commented", nil)
	request.SetPathValue("year", itoa(int32(now.Year())))
	CommentedTransactionsInYear(queries)(recorder, request)

	got := commentedTransactions(t, recorder)
	if containsCommentedTransactionID(got, pt.ID) {
		t.Errorf("transaction dated last year leaked into this year's response: %+v", got)
	}
}

func TestWorkplaceCommentedTransactions_Success(t *testing.T) {
	const workplaceSub = "66666666-6666-6666-6666-666666666666"
	queries := dbtest.Tx(t)
	provider := keycloaktest.New(t)
	provider.SetGroups([]string{workplaceSub})

	account := seedBankAccount(t, queries, "9300000107")
	const memberNumber = int32(900506)
	seedLiableMemberInWorkplace(t, queries, memberNumber, workplaceSub)

	now := time.Now()
	member := memberNumber
	pt := seedTransactionDated(t, queries, account.ID, 8007, now, "membership_fee", &member)
	setAdminComment(t, queries, pt, "missing VS")

	recorder := httptest.NewRecorder()
	request := withWorkplaceRepContext(httptest.NewRequest(http.MethodGet, "/payments/workplace/"+itoa(int32(now.Year()))+"/"+itoa(int32(now.Month()))+"/commented", nil))
	request.SetPathValue("year", itoa(int32(now.Year())))
	request.SetPathValue("month", itoa(int32(now.Month())))
	WorkplaceCommentedTransactions(provider.Provider, queries)(recorder, request)

	got := commentedTransactions(t, recorder)
	if !containsCommentedTransactionID(got, pt.ID) {
		t.Errorf("commented transaction %d (member in the rep's workplace) missing from response: %+v", pt.ID, got)
	}
}

func TestWorkplaceCommentedTransactions_WrongWorkplaceIsEmpty(t *testing.T) {
	const memberWorkplace = "55555555-5555-5555-5555-555555555555"
	const repWorkplace = "44444444-4444-4444-4444-444444444444"
	queries := dbtest.Tx(t)
	provider := keycloaktest.New(t)
	provider.SetGroups([]string{repWorkplace})

	account := seedBankAccount(t, queries, "9300000108")
	const memberNumber = int32(900507)
	seedLiableMemberInWorkplace(t, queries, memberNumber, memberWorkplace)

	now := time.Now()
	member := memberNumber
	pt := seedTransactionDated(t, queries, account.ID, 8008, now, "membership_fee", &member)
	setAdminComment(t, queries, pt, "missing VS")

	recorder := httptest.NewRecorder()
	request := withWorkplaceRepContext(httptest.NewRequest(http.MethodGet, "/payments/workplace/"+itoa(int32(now.Year()))+"/"+itoa(int32(now.Month()))+"/commented", nil))
	request.SetPathValue("year", itoa(int32(now.Year())))
	request.SetPathValue("month", itoa(int32(now.Month())))
	WorkplaceCommentedTransactions(provider.Provider, queries)(recorder, request)

	got := commentedTransactions(t, recorder)
	if containsCommentedTransactionID(got, pt.ID) {
		t.Errorf("commented transaction from a different workplace leaked into response: %+v", got)
	}
}

func TestWorkplaceCommentedTransactions_NoGroups(t *testing.T) {
	queries := dbtest.Tx(t)
	provider := keycloaktest.New(t)
	provider.SetGroups(nil)

	recorder := httptest.NewRecorder()
	request := withWorkplaceRepContext(httptest.NewRequest(http.MethodGet, "/payments/workplace/2026/1/commented", nil))
	request.SetPathValue("year", "2026")
	request.SetPathValue("month", "1")
	WorkplaceCommentedTransactions(provider.Provider, queries)(recorder, request)

	got := commentedTransactions(t, recorder)
	if len(got) != 0 {
		t.Errorf("got %+v, want empty — caller belongs to no workplace groups at all", got)
	}
}

func TestWorkplaceCommentedTransactionsInYear_Success(t *testing.T) {
	const workplaceSub = "66666666-6666-6666-6666-666666666667"
	queries := dbtest.Tx(t)
	provider := keycloaktest.New(t)
	provider.SetGroups([]string{workplaceSub})

	account := seedBankAccount(t, queries, "9300000109")
	const memberNumber = int32(900508)
	seedLiableMemberInWorkplace(t, queries, memberNumber, workplaceSub)

	now := time.Now()
	member := memberNumber
	pt := seedTransactionDated(t, queries, account.ID, 8009, now, "membership_fee", &member)
	setAdminComment(t, queries, pt, "missing VS")

	recorder := httptest.NewRecorder()
	request := withWorkplaceRepContext(httptest.NewRequest(http.MethodGet, "/payments/workplace/"+itoa(int32(now.Year()))+"/commented", nil))
	request.SetPathValue("year", itoa(int32(now.Year())))
	WorkplaceCommentedTransactionsInYear(provider.Provider, queries)(recorder, request)

	got := commentedTransactions(t, recorder)
	if !containsCommentedTransactionID(got, pt.ID) {
		t.Errorf("commented transaction %d (member in the rep's workplace) missing from response: %+v", pt.ID, got)
	}
}

