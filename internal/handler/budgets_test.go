package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

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
	CreateBudget(db.New(pool))(recorder, budgetRequest(http.MethodPost, "/budgets", body, nil))
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
