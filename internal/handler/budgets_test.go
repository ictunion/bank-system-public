package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/categoryid"
	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
)

// Budget line create/update open their own DB transaction on the pool, so
// these tests use dbtest.Pool throughout.

func budgetRequest(method, path, body string, pathValues map[string]string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for key, value := range pathValues {
		request.SetPathValue(key, value)
	}
	return request
}

func createBudget(t *testing.T, pool *pgxpool.Pool, body string, wantStatus int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	CreateBudget(pool)(recorder, budgetRequest(http.MethodPost, "/budgets", body, nil))
	if recorder.Code != wantStatus {
		t.Fatalf("create budget status = %d, want %d, body = %s", recorder.Code, wantStatus, recorder.Body)
	}
}

func getBudget(t *testing.T, pool *pgxpool.Pool, year string) budgetResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	GetBudget(db.New(pool))(recorder, budgetRequest(http.MethodGet, "/budgets/"+year, "", map[string]string{"year": year}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("get budget status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got budgetResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return got
}

// postLine adds a line to year's budget and returns the recorder, so callers
// can check error bodies too.
func postLine(t *testing.T, pool *pgxpool.Pool, year, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	CreateBudgetLine(pool)(recorder, budgetRequest(http.MethodPost, "/budgets/"+year+"/lines", body, map[string]string{"year": year}))
	return recorder
}

func putLine(t *testing.T, pool *pgxpool.Pool, year string, id int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	idText := strconv.FormatInt(id, 10)
	UpdateBudgetLine(pool)(recorder, budgetRequest(http.MethodPut, "/budgets/"+year+"/lines/"+idText, body,
		map[string]string{"year": year, "id": idText}))
	return recorder
}

func decodeLine(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int) budgetLineResponse {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, wantStatus, recorder.Body)
	}
	var got budgetLineResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return got
}

func TestCreateBudget(t *testing.T) {
	pool := dbtest.Pool(t)

	createBudget(t, pool, `{"year":2026,"note":" approved 2026-03-14 "}`, http.StatusCreated)
	createBudget(t, pool, `{"year":2026}`, http.StatusConflict)

	tests := []struct {
		name string
		body string
	}{
		{"invalid JSON", `not json`},
		{"missing year", `{}`},
		{"year too early", `{"year":1999}`},
		{"note too long", `{"year":2027,"note":"` + strings.Repeat("x", budgetTextMaxLength+1) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			createBudget(t, pool, tt.body, http.StatusBadRequest)
		})
	}

	got := getBudget(t, pool, "2026")
	if got.Note == nil || *got.Note != "approved 2026-03-14" || len(got.Lines) != 0 {
		t.Errorf("budget = %+v, want trimmed note and no lines", got)
	}
}

func TestBudgetLines_CreateUpdateDelete(t *testing.T) {
	pool := dbtest.Pool(t)
	createBudget(t, pool, `{"year":2026}`, http.StatusCreated)

	wages := decodeLine(t, postLine(t, pool, "2026",
		`{"kind":"expense","group_name":"Employment expenses","name":"Employee wages","projected":"300000","category_ids":["00000000-0000-4000-8000-000000000002","00000000-0000-4000-8000-000000000002"],"sort_order":10}`),
		http.StatusCreated)
	if wages.Projected != "300000.00" || len(wages.CategoryIDs) != 1 || wages.CategoryIDs[0] != categoryid.Salary {
		t.Errorf("created = %+v, want projected 300000.00 and category_ids [salary] (deduplicated)", wages)
	}
	decodeLine(t, postLine(t, pool, "2026", `{"kind":"income","name":"Membership fees","projected":"600000.50","category_ids":["00000000-0000-4000-8000-000000000001"]}`),
		http.StatusCreated)

	// Re-saving a line with its own categories isn't a conflict with itself.
	updated := decodeLine(t, putLine(t, pool, "2026", wages.ID,
		`{"kind":"expense","name":"Wages","projected":"310000","category_ids":["00000000-0000-4000-8000-000000000002","00000000-0000-4000-8000-000000000004"],"sort_order":5}`),
		http.StatusOK)
	if updated.Name != "Wages" || updated.GroupName != nil || len(updated.CategoryIDs) != 2 || updated.SortOrder != 5 {
		t.Errorf("updated = %+v, want renamed, ungrouped, two categories, sort_order 5", updated)
	}

	got := getBudget(t, pool, "2026")
	if len(got.Lines) != 2 || got.Lines[0].Name != "Membership fees" || got.Lines[1].Name != "Wages" {
		t.Fatalf("lines = %+v, want Membership fees (sort 0) then Wages (sort 5)", got.Lines)
	}

	recorder := httptest.NewRecorder()
	idText := strconv.FormatInt(wages.ID, 10)
	DeleteBudgetLine(db.New(pool))(recorder, budgetRequest(http.MethodDelete, "/budgets/2026/lines/"+idText, "",
		map[string]string{"year": "2026", "id": idText}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if got := getBudget(t, pool, "2026"); len(got.Lines) != 1 {
		t.Errorf("lines after delete = %+v, want 1", got.Lines)
	}

	// The deleted line's categories are free again.
	decodeLine(t, postLine(t, pool, "2026", `{"kind":"expense","name":"Salaries","category_ids":["00000000-0000-4000-8000-000000000002"]}`), http.StatusCreated)
}

func TestBudgetLines_CategoryUsedByAnotherLine(t *testing.T) {
	pool := dbtest.Pool(t)
	createBudget(t, pool, `{"year":2026}`, http.StatusCreated)
	createBudget(t, pool, `{"year":2027}`, http.StatusCreated)
	decodeLine(t, postLine(t, pool, "2026", `{"kind":"expense","name":"Employee wages","category_ids":["00000000-0000-4000-8000-000000000002"]}`), http.StatusCreated)

	recorder := postLine(t, pool, "2026", `{"kind":"expense","name":"Legal","category_ids":["00000000-0000-4000-8000-000000000004","00000000-0000-4000-8000-000000000002"]}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusConflict, recorder.Body)
	}
	var conflict categoryConflictResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &conflict); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if conflict.CategoryID != categoryid.Salary || conflict.Line != "Employee wages" {
		t.Errorf("conflict = %+v, want salary owned by Employee wages", conflict)
	}

	// Another year's budget maps categories independently.
	decodeLine(t, postLine(t, pool, "2027", `{"kind":"expense","name":"Legal","category_ids":["00000000-0000-4000-8000-000000000002"]}`), http.StatusCreated)
}

func TestBudgetLines_Validation(t *testing.T) {
	pool := dbtest.Pool(t)
	createBudget(t, pool, `{"year":2026}`, http.StatusCreated)
	decodeLine(t, postLine(t, pool, "2026", `{"kind":"income","name":"Interest"}`), http.StatusCreated)

	tests := []struct {
		name       string
		year       string
		body       string
		wantStatus int
	}{
		{"invalid JSON", "2026", `not json`, http.StatusBadRequest},
		{"invalid kind", "2026", `{"kind":"other","name":"X"}`, http.StatusBadRequest},
		{"missing name", "2026", `{"kind":"income","name":"  "}`, http.StatusBadRequest},
		{"negative projected", "2026", `{"kind":"income","name":"X","projected":"-5"}`, http.StatusBadRequest},
		{"three decimals", "2026", `{"kind":"income","name":"X","projected":"1.005"}`, http.StatusBadRequest},
		{"null category id", "2026", `{"kind":"income","name":"X","category_ids":[null]}`, http.StatusBadRequest},
		{"malformed category id", "2026", `{"kind":"income","name":"X","category_ids":["salary"]}`, http.StatusBadRequest},
		{"unknown category", "2026", `{"kind":"income","name":"X","category_ids":["11111111-1111-4111-8111-111111111111"]}`, http.StatusBadRequest},
		{"invalid year", "1999", `{"kind":"income","name":"X"}`, http.StatusBadRequest},
		{"duplicate name", "2026", `{"kind":"income","name":"Interest"}`, http.StatusConflict},
		{"no budget for year", "2030", `{"kind":"income","name":"X"}`, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if recorder := postLine(t, pool, tt.year, tt.body); recorder.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d, body = %s", recorder.Code, tt.wantStatus, recorder.Body)
			}
		})
	}
}

// A line id belongs to one year — editing it through another year's URL is
// a 404, not a cross-year edit.
func TestUpdateBudgetLine_OtherYearNotFound(t *testing.T) {
	pool := dbtest.Pool(t)
	createBudget(t, pool, `{"year":2026}`, http.StatusCreated)
	createBudget(t, pool, `{"year":2027}`, http.StatusCreated)
	line := decodeLine(t, postLine(t, pool, "2026", `{"kind":"income","name":"Interest"}`), http.StatusCreated)

	if recorder := putLine(t, pool, "2027", line.ID, `{"kind":"income","name":"Interest"}`); recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusNotFound, recorder.Body)
	}
}

func TestUpdateAndDeleteBudget(t *testing.T) {
	pool := dbtest.Pool(t)
	createBudget(t, pool, `{"year":2026,"note":"draft"}`, http.StatusCreated)
	decodeLine(t, postLine(t, pool, "2026", `{"kind":"income","name":"Interest","category_ids":["00000000-0000-4000-8000-000000000006"]}`), http.StatusCreated)

	recorder := httptest.NewRecorder()
	UpdateBudget(db.New(pool))(recorder, budgetRequest(http.MethodPatch, "/budgets/2026", `{"note":""}`, map[string]string{"year": "2026"}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if got := getBudget(t, pool, "2026"); got.Note != nil {
		t.Errorf("note = %q, want cleared", *got.Note)
	}

	deleteBudget := func() int {
		recorder := httptest.NewRecorder()
		DeleteBudget(db.New(pool))(recorder, budgetRequest(http.MethodDelete, "/budgets/2026", "", map[string]string{"year": "2026"}))
		return recorder.Code
	}
	if status := deleteBudget(); status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d", status, http.StatusNoContent)
	}
	if status := deleteBudget(); status != http.StatusNotFound {
		t.Errorf("second delete status = %d, want %d", status, http.StatusNotFound)
	}

	// Lines and mappings went with it: the year can be recreated and its
	// categories mapped again.
	createBudget(t, pool, `{"year":2026}`, http.StatusCreated)
	decodeLine(t, postLine(t, pool, "2026", `{"kind":"income","name":"Interest","category_ids":["00000000-0000-4000-8000-000000000006"]}`), http.StatusCreated)
}

// seedBudgetTransaction inserts one processed transaction with a chosen date,
// amount, currency and category — the inputs the budget actuals depend on.
func seedBudgetTransaction(t *testing.T, queries *db.Queries, bankAccountID int32, fioTransactionID int64, date time.Time, amount, currency, category string) {
	t.Helper()
	ctx := context.Background()
	if _, err := queries.InsertRawTransaction(ctx, db.InsertRawTransactionParams{
		BankAccountID:    bankAccountID,
		FioTransactionID: fioTransactionID,
		TransactionDate:  date,
		Amount:           amount,
		Currency:         currency,
		RawPayload:       []byte("{}"),
	}); err != nil {
		t.Fatalf("InsertRawTransaction(%d): %v", fioTransactionID, err)
	}
	unprocessed, err := queries.ListUnprocessedTransactions(ctx)
	if err != nil {
		t.Fatalf("ListUnprocessedTransactions: %v", err)
	}
	for _, raw := range unprocessed {
		if raw.FioTransactionID != fioTransactionID || raw.BankAccountID != bankAccountID {
			continue
		}
		direction := "incoming"
		if strings.HasPrefix(amount, "-") {
			direction = "outgoing"
		}
		if _, err := queries.CreateProcessedTransaction(ctx, db.CreateProcessedTransactionParams{
			RawTransactionID: raw.ID,
			CategoryID:       categoryIDByName(t, queries, category),
			Direction:        direction,
		}); err != nil {
			t.Fatalf("CreateProcessedTransaction(%d): %v", fioTransactionID, err)
		}
		return
	}
	t.Fatalf("seedBudgetTransaction: fio_transaction_id=%d not found among unprocessed", fioTransactionID)
}

func TestGetBudget_Actuals(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9500000001")
	createBudget(t, pool, `{"year":2026}`, http.StatusCreated)
	fees := decodeLine(t, postLine(t, pool, "2026", `{"kind":"income","name":"Membership fees","projected":"600000","category_ids":["`+uuidText(t, categoryid.MembershipFee)+`"]}`), http.StatusCreated)
	wages := decodeLine(t, postLine(t, pool, "2026", `{"kind":"expense","name":"Employee wages","projected":"300000","category_ids":["`+uuidText(t, categoryid.Salary)+`"]}`), http.StatusCreated)
	empty := decodeLine(t, postLine(t, pool, "2026", `{"kind":"expense","name":"Legal","projected":"60000"}`), http.StatusCreated)

	day := func(month time.Month, dayOfMonth, year int) time.Time {
		return time.Date(year, month, dayOfMonth, 0, 0, 0, 0, time.UTC)
	}
	seedBudgetTransaction(t, queries, account.ID, 7001, day(time.January, 1, 2026), "100.00", "CZK", "membership_fee")
	seedBudgetTransaction(t, queries, account.ID, 7002, day(time.December, 31, 2026), "200.00", "CZK", "membership_fee")
	// Left out: another year, another currency.
	seedBudgetTransaction(t, queries, account.ID, 7003, day(time.December, 31, 2025), "999.00", "CZK", "membership_fee")
	seedBudgetTransaction(t, queries, account.ID, 7004, day(time.March, 1, 2026), "50.00", "EUR", "membership_fee")
	// A refund lowers the expense line: 500 - 50.
	seedBudgetTransaction(t, queries, account.ID, 7005, day(time.March, 5, 2026), "-500.00", "CZK", "salary")
	seedBudgetTransaction(t, queries, account.ID, 7006, day(time.March, 6, 2026), "50.00", "CZK", "salary")
	seedBudgetTransaction(t, queries, account.ID, 7007, day(time.April, 1, 2026), "-80.00", "CZK", "other_expense")
	seedBudgetTransaction(t, queries, account.ID, 7008, day(time.April, 2, 2026), "-700.00", "CZK", "internal_transfer")

	got := getBudget(t, pool, "2026")

	wantReal := map[int64]string{fees.ID: "300.00", wages.ID: "450.00", empty.ID: "0.00"}
	if len(got.Lines) != len(wantReal) {
		t.Fatalf("lines = %+v, want %d", got.Lines, len(wantReal))
	}
	for _, line := range got.Lines {
		if line.Real != wantReal[line.ID] {
			t.Errorf("line %q real = %q, want %q", line.Name, line.Real, wantReal[line.ID])
		}
	}

	// other_expense is unmapped; internal_transfer is left out entirely.
	if len(got.Unbudgeted) != 1 {
		t.Fatalf("unbudgeted = %+v, want only other_expense", got.Unbudgeted)
	}
	unbudgeted := got.Unbudgeted[0]
	if unbudgeted.CategoryID != categoryid.OtherExpense || unbudgeted.Direction != "outgoing" || unbudgeted.Real != "80.00" {
		t.Errorf("unbudgeted = %+v, want other_expense outgoing 80.00", unbudgeted)
	}
	if got.OtherCurrencyCount != 1 {
		t.Errorf("other_currency_count = %d, want 1", got.OtherCurrencyCount)
	}
	if got.AsOf == "" {
		t.Error("as_of is empty")
	}
}

func TestCreateBudget_CopyFromYear(t *testing.T) {
	pool := dbtest.Pool(t)
	createBudget(t, pool, `{"year":2026}`, http.StatusCreated)
	decodeLine(t, postLine(t, pool, "2026", `{"kind":"expense","group_name":"Employment expenses","name":"Employee wages","projected":"300000","category_ids":["`+uuidText(t, categoryid.Salary)+`"],"sort_order":10}`), http.StatusCreated)
	decodeLine(t, postLine(t, pool, "2026", `{"kind":"income","name":"Interest","projected":"500","category_ids":["`+uuidText(t, categoryid.CreditedInterest)+`"]}`), http.StatusCreated)

	createBudget(t, pool, `{"year":2027,"copy_from_year":2026}`, http.StatusCreated)

	source := getBudget(t, pool, "2026")
	copied := getBudget(t, pool, "2027")
	if len(copied.Lines) != len(source.Lines) {
		t.Fatalf("copied lines = %+v, want %d", copied.Lines, len(source.Lines))
	}
	for i, line := range copied.Lines {
		want := source.Lines[i]
		if line.ID == want.ID || line.Name != want.Name || line.Kind != want.Kind || line.Projected != want.Projected ||
			line.SortOrder != want.SortOrder || len(line.CategoryIDs) != 1 || line.CategoryIDs[0] != want.CategoryIDs[0] {
			t.Errorf("copied line %d = %+v, want a new line like %+v", i, line, want)
		}
	}

	// The copy is independent: the copied categories are mapped in 2027, not
	// taken from 2026.
	if recorder := postLine(t, pool, "2027", `{"kind":"expense","name":"Other wages","category_ids":["`+uuidText(t, categoryid.Salary)+`"]}`); recorder.Code != http.StatusConflict {
		t.Errorf("mapping salary again in 2027: status = %d, want %d", recorder.Code, http.StatusConflict)
	}

	createBudget(t, pool, `{"year":2028,"copy_from_year":2020}`, http.StatusBadRequest)
}
