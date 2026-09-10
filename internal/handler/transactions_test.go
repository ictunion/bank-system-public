package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
)

// itoa64 formats a path-value ID for request.SetPathValue — same idea as
// itoa in account_test.go, but for the int64 IDs processed_transactions
// uses.
func itoa64(id int64) string {
	return strconv.FormatInt(id, 10)
}

// seedBankAccount creates a bank account directly via *db.Queries (not
// through the HTTP handler — these tests are about the transactions
// endpoints, the account is just a fixture dependency).
func seedBankAccount(t *testing.T, queries *db.Queries, fioAccountID string) db.CreateBankAccountRow {
	t.Helper()
	account, err := queries.CreateBankAccount(context.Background(), db.CreateBankAccountParams{
		FioAccountID:  fioAccountID,
		Currency:      "CZK",
		DisplayName:   "Fixture Account",
		FioToken:      "tok",
		EncryptionKey: testEncryptionKey,
	})
	if err != nil {
		t.Fatalf("seedBankAccount(%q): %v", fioAccountID, err)
	}
	return account
}

// seedTransaction inserts a raw_transactions row and its processed_transactions
// counterpart — the same two-step shape the real Fio sync + processing job
// produce, just driven directly instead of through a fake bank feed.
func seedTransaction(t *testing.T, queries *db.Queries, bankAccountID int32, fioTransactionID int64, amount, category, direction string, memberNumber *int32) db.ProcessedTransaction {
	t.Helper()
	ctx := context.Background()

	if _, err := queries.InsertRawTransaction(ctx, db.InsertRawTransactionParams{
		BankAccountID:    bankAccountID,
		FioTransactionID: fioTransactionID,
		TransactionDate:  time.Now(),
		Amount:           amount,
		Currency:         "CZK",
		RawPayload:       []byte("{}"), // NOT NULL; content unused by anything these tests exercise
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
		t.Fatalf("seedTransaction: just-inserted fio_transaction_id=%d not found via ListUnprocessedTransactions", fioTransactionID)
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
		Direction:        direction,
		MatchedBy:        matchedBy,
	})
	if err != nil {
		t.Fatalf("CreateProcessedTransaction: %v", err)
	}
	return pt
}

func TestListTransactions_Empty(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/transactions", nil)
	ListTransactions(queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got transactionsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Total != 0 || len(got.Transactions) != 0 {
		t.Errorf("got %+v, want an empty result", got)
	}
}

func TestListTransactions_Validation(t *testing.T) {
	queries := dbtest.Tx(t)

	tests := []struct {
		name  string
		query string
	}{
		{"invalid assigned", "assigned=maybe"},
		{"invalid direction", "direction=sideways"},
		{"invalid matched_by", "matched_by=guessing"},
		{"non-numeric member_number", "member_number=abc"},
		{"malformed from date", "from=not-a-date"},
		{"malformed to date", "to=2026-13-40"},
		{"zero limit", "limit=0"},
		{"negative offset", "offset=-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/transactions?"+tt.query, nil)
			ListTransactions(queries)(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
			}
		})
	}
}

func TestListTransactions_FiltersByDirectionAndCategory(t *testing.T) {
	queries := dbtest.Tx(t)
	account := seedBankAccount(t, queries, "9100000001")

	seedTransaction(t, queries, account.ID, 1001, "1200.00", "membership_fee", "incoming", nil)
	seedTransaction(t, queries, account.ID, 1002, "-500.00", "salary", "outgoing", nil)

	listWith := func(query string) transactionsResponse {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/transactions?"+query, nil)
		ListTransactions(queries)(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
		}
		var got transactionsResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		return got
	}

	byDirection := listWith("direction=incoming")
	if len(byDirection.Transactions) != 1 || byDirection.Transactions[0].Category != "membership_fee" {
		t.Errorf("direction=incoming: got %+v, want exactly the membership_fee row", byDirection.Transactions)
	}

	byCategory := listWith("category=salary")
	if len(byCategory.Transactions) != 1 || byCategory.Transactions[0].Direction != "outgoing" {
		t.Errorf("category=salary: got %+v, want exactly the salary row", byCategory.Transactions)
	}
}

func TestGetTransaction_NotFound(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/transactions/999999", nil)
	request.SetPathValue("id", "999999")
	GetTransaction(queries)(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusNotFound, recorder.Body)
	}
}

func TestGetTransaction_ReturnsDetail(t *testing.T) {
	queries := dbtest.Tx(t)
	account := seedBankAccount(t, queries, "9100000002")
	pt := seedTransaction(t, queries, account.ID, 2001, "750.50", "other_income", "incoming", nil)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/transactions/x", nil)
	request.SetPathValue("id", itoa64(pt.ID))
	GetTransaction(queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got transactionDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.ID != pt.ID {
		t.Errorf("ID = %d, want %d", got.ID, pt.ID)
	}
	if got.Category != "other_income" {
		t.Errorf("Category = %q, want other_income", got.Category)
	}
	if got.Amount != "750.50" {
		t.Errorf("Amount = %q, want 750.50", got.Amount)
	}
	if len(got.CoveredMonths) != 0 {
		t.Errorf("CoveredMonths = %v, want empty — nothing inserted into payment_coverage", got.CoveredMonths)
	}
}

func TestCategorySummary_Empty(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/transactions/summary", nil)
	CategorySummary(queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got categorySummaryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got.Incoming) != 0 || len(got.Outgoing) != 0 {
		t.Errorf("got %+v, want both empty", got)
	}
}

func TestCategorySummary_GroupsByCategoryAndDirection(t *testing.T) {
	queries := dbtest.Tx(t)
	account := seedBankAccount(t, queries, "9100000003")

	seedTransaction(t, queries, account.ID, 3001, "100.00", "membership_fee", "incoming", nil)
	seedTransaction(t, queries, account.ID, 3002, "-40.00", "salary", "outgoing", nil)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/transactions/summary", nil)
	CategorySummary(queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got categorySummaryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if len(got.Incoming) != 1 || got.Incoming[0].Category != "membership_fee" {
		t.Fatalf("Incoming = %+v, want one membership_fee entry", got.Incoming)
	}
	if total := parseAmount(t, got.Incoming[0].Total); total != 100 {
		t.Errorf("Incoming membership_fee total = %v, want 100", total)
	}

	if len(got.Outgoing) != 1 || got.Outgoing[0].Category != "salary" {
		t.Fatalf("Outgoing = %+v, want one salary entry", got.Outgoing)
	}
	// SUM(ABS(amount)) — the stored -40.00 should come back as a positive 40,
	// not a signed figure (see docs/logic-design.md "Transaction Category Summary").
	if total := parseAmount(t, got.Outgoing[0].Total); total != 40 {
		t.Errorf("Outgoing salary total = %v, want 40 (positive, despite the negative stored amount)", total)
	}
}

func parseAmount(t *testing.T, s string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("parsing amount %q: %v", s, err)
	}
	return f
}
