package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

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
		{"invalid include_children", "include_children=yes"},
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

// seedSubCategory creates a custom category with the given parent, directly
// via *db.Queries — a fixture dependency, not what these tests are about.
func seedSubCategory(t *testing.T, queries *db.Queries, name, parentName string) {
	t.Helper()
	if _, err := queries.CreateCategory(context.Background(), db.CreateCategoryParams{
		Name:       name,
		ParentName: &parentName,
	}); err != nil {
		t.Fatalf("CreateCategory(%q, parent %q): %v", name, parentName, err)
	}
}

func TestListTransactions_IncludeChildren(t *testing.T) {
	queries := dbtest.Tx(t)
	account := seedBankAccount(t, queries, "9100000005")
	seedSubCategory(t, queries, "test_legal", "salary")

	seedTransaction(t, queries, account.ID, 1101, "-500.00", "salary", "outgoing", nil)
	seedTransaction(t, queries, account.ID, 1102, "-300.00", "test_legal", "outgoing", nil)
	seedTransaction(t, queries, account.ID, 1103, "-100.00", "other_expense", "outgoing", nil)

	tests := []struct {
		name  string
		query string
		want  map[string]bool
	}{
		{"parent only by default", "category=salary", map[string]bool{"salary": true}},
		{"parent only when false", "category=salary&include_children=false", map[string]bool{"salary": true}},
		{"parent and children", "category=salary&include_children=true", map[string]bool{"salary": true, "test_legal": true}},
		{"child alone", "category=test_legal&include_children=true", map[string]bool{"test_legal": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/transactions?"+tt.query, nil)
			ListTransactions(queries)(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
			}
			var got transactionsResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding response: %v", err)
			}
			gotCategories := make(map[string]bool)
			for _, transaction := range got.Transactions {
				gotCategories[transaction.Category] = true
			}
			if len(got.Transactions) != len(tt.want) || len(gotCategories) != len(tt.want) {
				t.Fatalf("got categories %v (%d rows), want %v", gotCategories, len(got.Transactions), tt.want)
			}
			for category := range tt.want {
				if !gotCategories[category] {
					t.Errorf("missing category %q, got %v", category, gotCategories)
				}
			}
		})
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
	// not a signed figure.
	if total := parseAmount(t, got.Outgoing[0].Total); total != 40 {
		t.Errorf("Outgoing salary total = %v, want 40 (positive, despite the negative stored amount)", total)
	}
}

func TestCategorySummary_ExcludesInternalTransfer(t *testing.T) {
	queries := dbtest.Tx(t)
	account := seedBankAccount(t, queries, "9100000004")

	seedTransaction(t, queries, account.ID, 3003, "100.00", "membership_fee", "incoming", nil)
	// A transfer between our own bank_accounts isn't real income/expense —
	// must be excluded from the summary entirely, not just zeroed, so it
	// can't inflate either total.
	seedTransaction(t, queries, account.ID, 3004, "500.00", "internal_transfer", "incoming", nil)
	seedTransaction(t, queries, account.ID, 3005, "-500.00", "internal_transfer", "outgoing", nil)

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

	if len(got.Outgoing) != 0 {
		t.Errorf("Outgoing = %+v, want empty — the only outgoing row is internal_transfer", got.Outgoing)
	}
	if len(got.Incoming) != 1 || got.Incoming[0].Category != "membership_fee" {
		t.Fatalf("Incoming = %+v, want exactly one membership_fee entry, no internal_transfer", got.Incoming)
	}
	if total := parseAmount(t, got.Incoming[0].Total); total != 100 {
		t.Errorf("Incoming membership_fee total = %v, want 100 (unaffected by the excluded internal_transfer row)", total)
	}
}

// numericBalance builds the pgtype.Numeric UpdateBankAccountBalance expects
// — the nullable balance column falls back to pgtype's own wrapper rather
// than the plain-string sqlc override (which only applies to NOT NULL
// numeric columns elsewhere, e.g. raw_transactions.amount).
func numericBalance(t *testing.T, s string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("numericBalance(%q): %v", s, err)
	}
	return n
}

func TestCategorySummary_CurrentBalanceSumsAcrossAccounts(t *testing.T) {
	queries := dbtest.Tx(t)
	accountA := seedBankAccount(t, queries, "9100000005")
	accountB := seedBankAccount(t, queries, "9100000006")

	if err := queries.UpdateBankAccountBalance(context.Background(), db.UpdateBankAccountBalanceParams{
		ID: accountA.ID, Balance: numericBalance(t, "1000.00"),
	}); err != nil {
		t.Fatalf("UpdateBankAccountBalance(A): %v", err)
	}
	if err := queries.UpdateBankAccountBalance(context.Background(), db.UpdateBankAccountBalanceParams{
		ID: accountB.ID, Balance: numericBalance(t, "234.56"),
	}); err != nil {
		t.Fatalf("UpdateBankAccountBalance(B): %v", err)
	}

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

	if len(got.CurrentBalance) != 1 {
		t.Fatalf("CurrentBalance = %+v, want one currency entry", got.CurrentBalance)
	}
	if got.CurrentBalance[0].Currency != "CZK" {
		t.Errorf("currency = %q, want CZK", got.CurrentBalance[0].Currency)
	}
	if total := parseAmount(t, got.CurrentBalance[0].Total); total != 1234.56 {
		t.Errorf("total = %v, want 1234.56", total)
	}
}

func TestCategorySummary_CurrentBalanceExcludesNeverSyncedAccount(t *testing.T) {
	queries := dbtest.Tx(t)
	seedBankAccount(t, queries, "9100000007") // never synced — balance still NULL

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
	if len(got.CurrentBalance) != 0 {
		t.Errorf("CurrentBalance = %+v, want empty — account never synced, no balance known", got.CurrentBalance)
	}
}

func TestCategorySummary_CurrentBalanceExcludesSoftDeletedAccount(t *testing.T) {
	queries := dbtest.Tx(t)
	account := seedBankAccount(t, queries, "9100000008")
	if err := queries.UpdateBankAccountBalance(context.Background(), db.UpdateBankAccountBalanceParams{
		ID: account.ID, Balance: numericBalance(t, "500.00"),
	}); err != nil {
		t.Fatalf("UpdateBankAccountBalance: %v", err)
	}
	if _, err := queries.DeleteBankAccount(context.Background(), account.ID); err != nil {
		t.Fatalf("DeleteBankAccount: %v", err)
	}

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
	if len(got.CurrentBalance) != 0 {
		t.Errorf("CurrentBalance = %+v, want empty — soft-deleted account excluded", got.CurrentBalance)
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

func TestCategorySummary_Rollup(t *testing.T) {
	queries := dbtest.Tx(t)
	account := seedBankAccount(t, queries, "9100000006")
	seedSubCategory(t, queries, "test_legal", "salary")
	seedSubCategory(t, queries, "test_transfer_child", "internal_transfer")

	seedTransaction(t, queries, account.ID, 3101, "-500.00", "salary", "outgoing", nil)
	seedTransaction(t, queries, account.ID, 3102, "-300.00", "test_legal", "outgoing", nil)
	// A sub-category of internal_transfer is excluded like its parent,
	// rolled up or not.
	seedTransaction(t, queries, account.ID, 3103, "-900.00", "test_transfer_child", "outgoing", nil)

	summaryWith := func(query string) map[string]float64 {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/transactions/summary?"+query, nil)
		CategorySummary(queries)(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
		}
		var got categorySummaryResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		totals := make(map[string]float64)
		for _, total := range got.Outgoing {
			if _, duplicate := totals[total.Category]; duplicate {
				t.Errorf("category %q listed twice in Outgoing", total.Category)
			}
			totals[total.Category] = parseAmount(t, total.Total)
		}
		return totals
	}

	tests := []struct {
		name  string
		query string
		want  map[string]float64
	}{
		{"separate by default", "", map[string]float64{"salary": 500, "test_legal": 300}},
		{"rolled up into parent", "rollup=true", map[string]float64{"salary": 800}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summaryWith(tt.query)
			if len(got) != len(tt.want) {
				t.Fatalf("Outgoing = %v, want %v", got, tt.want)
			}
			for category, wantTotal := range tt.want {
				if got[category] != wantTotal {
					t.Errorf("Outgoing[%q] = %v, want %v", category, got[category], wantTotal)
				}
			}
		})
	}
}

func TestCategorySummary_InvalidRollup(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/transactions/summary?rollup=yes", nil)
	CategorySummary(queries)(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
	}
}
