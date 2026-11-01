package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubik/bank-system/internal/categoryid"
	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
)

func listRules(t *testing.T, queries *db.Queries) []categorizationRuleResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	ListCategorizationRules(queries)(recorder, httptest.NewRequest(http.MethodGet, "/categorization-rules", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got []categorizationRuleResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return got
}

// seededRule returns the migration-seeded rule of the given kind (or, for
// pattern rules, category).
func seededRule(t *testing.T, queries *db.Queries, kind, category string) categorizationRuleResponse {
	t.Helper()
	for _, rule := range listRules(t, queries) {
		if rule.Kind == kind && rule.CategoryID == categoryIDByName(t, queries, category) {
			return rule
		}
	}
	t.Fatalf("seeded %s rule for %q not found", kind, category)
	return categorizationRuleResponse{}
}

func postRule(t *testing.T, queries *db.Queries, body string, wantStatus int) categorizationRuleResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/categorization-rules", strings.NewReader(body))
	CreateCategorizationRule(queries)(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("create status = %d, want %d, body = %s", recorder.Code, wantStatus, recorder.Body)
	}
	var got categorizationRuleResponse
	if recorder.Code == http.StatusCreated {
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
	}
	return got
}

func putRule(t *testing.T, queries *db.Queries, id int64, body string, wantStatus int) categorizationRuleResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/categorization-rules/"+itoa64(id), strings.NewReader(body))
	request.SetPathValue("id", itoa64(id))
	UpdateCategorizationRule(queries)(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("update status = %d, want %d, body = %s", recorder.Code, wantStatus, recorder.Body)
	}
	var got categorizationRuleResponse
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
	}
	return got
}

func deleteRule(t *testing.T, queries *db.Queries, id int64) int {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/categorization-rules/"+itoa64(id), nil)
	request.SetPathValue("id", itoa64(id))
	DeleteCategorizationRule(queries)(recorder, request)
	return recorder.Code
}

func TestListCategorizationRules_SeededInOrder(t *testing.T) {
	queries := dbtest.Tx(t)

	got := listRules(t, queries)
	want := []struct {
		priority int32
		kind     string
		category string
	}{
		{100, "own_account", "internal_transfer"},
		{200, "pattern", "credited_interest"},
		{300, "member_variable_symbol", "membership_fee"},
		{500, "pattern", "salary"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rules, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Priority != w.priority || got[i].Kind != w.kind || got[i].CategoryID != categoryIDByName(t, queries, w.category) || !got[i].Enabled {
			t.Errorf("rule %d = %+v, want enabled %d %s %s", i, got[i], w.priority, w.kind, w.category)
		}
	}
}

func TestCreateCategorizationRule_ThenUpdateThenDelete(t *testing.T) {
	queries := dbtest.Tx(t)

	created := postRule(t, queries,
		`{"priority":400,"category_id":"00000000-0000-4000-8000-000000000004","counter_account_number":" 2900000001 ","direction":"outgoing","text_contains":"","note":"Lawyer"}`,
		http.StatusCreated)
	if created.Kind != "pattern" || !created.Enabled {
		t.Errorf("created = %+v, want an enabled pattern rule", created)
	}
	if created.CounterAccountNumber == nil || *created.CounterAccountNumber != "2900000001" {
		t.Errorf("counter_account_number = %v, want trimmed 2900000001", created.CounterAccountNumber)
	}
	if created.TextContains != nil {
		t.Errorf("text_contains = %q, want nil — blank counts as unset", *created.TextContains)
	}

	// Full overwrite: the omitted direction and note are cleared.
	updated := putRule(t, queries, created.ID,
		`{"priority":450,"category_id":"00000000-0000-4000-8000-000000000002","enabled":false,"counter_account_number":"2900000001"}`,
		http.StatusOK)
	if updated.Priority != 450 || updated.CategoryID != categoryid.Salary || updated.Enabled {
		t.Errorf("updated = %+v, want priority 450, category salary, disabled", updated)
	}
	if updated.Direction != nil || updated.Note != nil {
		t.Errorf("updated direction/note = %v/%v, want both cleared", updated.Direction, updated.Note)
	}

	if status := deleteRule(t, queries, created.ID); status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d", status, http.StatusNoContent)
	}
	if status := deleteRule(t, queries, created.ID); status != http.StatusNotFound {
		t.Errorf("second delete status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestCreateCategorizationRule_Validation(t *testing.T) {
	queries := dbtest.Tx(t)

	tests := []struct {
		name string
		body string
	}{
		{"invalid JSON", `not json`},
		{"missing priority", `{"category_id":"00000000-0000-4000-8000-000000000002","text_contains":"mzda"}`},
		{"missing category", `{"priority":400,"text_contains":"mzda"}`},
		{"malformed category_id", `{"priority":400,"category_id":"salary","text_contains":"mzda"}`},
		{"unknown category", `{"priority":400,"category_id":"11111111-1111-4111-8111-111111111111","text_contains":"mzda"}`},
		{"no condition", `{"priority":400,"category_id":"00000000-0000-4000-8000-000000000002"}`},
		{"direction only", `{"priority":400,"category_id":"00000000-0000-4000-8000-000000000002","direction":"outgoing"}`},
		{"blank conditions only", `{"priority":400,"category_id":"00000000-0000-4000-8000-000000000002","text_contains":"  "}`},
		{"invalid direction", `{"priority":400,"category_id":"00000000-0000-4000-8000-000000000002","text_contains":"mzda","direction":"sideways"}`},
		{"condition too long", `{"priority":400,"category_id":"00000000-0000-4000-8000-000000000002","text_contains":"` + strings.Repeat("x", ruleFieldMaxLength+1) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			postRule(t, queries, tt.body, http.StatusBadRequest)
		})
	}
}

func TestUpdateCategorizationRule_BuiltIn(t *testing.T) {
	queries := dbtest.Tx(t)
	memberRule := seededRule(t, queries, "member_variable_symbol", "membership_fee")

	// Priority, enabled and note are editable; category may be omitted.
	updated := putRule(t, queries, memberRule.ID, `{"priority":350,"enabled":false,"note":"moved"}`, http.StatusOK)
	if updated.Priority != 350 || updated.Enabled || updated.CategoryID != categoryid.MembershipFee || updated.Kind != "member_variable_symbol" {
		t.Errorf("updated = %+v, want disabled membership_fee rule at 350", updated)
	}

	tests := []struct {
		name string
		body string
	}{
		{"category change", `{"priority":300,"category_id":"00000000-0000-4000-8000-000000000002"}`},
		{"condition", `{"priority":300,"text_contains":"mzda"}`},
		{"direction", `{"priority":300,"direction":"incoming"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			putRule(t, queries, memberRule.ID, tt.body, http.StatusBadRequest)
		})
	}

	// Sending its own fixed category is fine.
	putRule(t, queries, memberRule.ID, `{"priority":300,"category_id":"00000000-0000-4000-8000-000000000001"}`, http.StatusOK)
}

func TestUpdateCategorizationRule_NotFound(t *testing.T) {
	queries := dbtest.Tx(t)
	putRule(t, queries, 999999, `{"priority":400,"category_id":"00000000-0000-4000-8000-000000000002","text_contains":"mzda"}`, http.StatusNotFound)
}

func TestDeleteCategorizationRule_BuiltInRefused(t *testing.T) {
	queries := dbtest.Tx(t)
	ownAccountRule := seededRule(t, queries, "own_account", "internal_transfer")

	if status := deleteRule(t, queries, ownAccountRule.ID); status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
	seededRule(t, queries, "own_account", "internal_transfer") // still there
}

// Deleting a rule leaves the transactions it decided alone: same category,
// categorization_rule_id cleared.
func TestDeleteCategorizationRule_KeepsTransactions(t *testing.T) {
	queries := dbtest.Tx(t)
	account := seedBankAccount(t, queries, "9100000021")
	rule := postRule(t, queries, `{"priority":400,"category_id":"00000000-0000-4000-8000-000000000002","counter_account_number":"2900000002"}`, http.StatusCreated)
	pt := seedTransactionWithRule(t, queries, account.ID, 2104, "-100.00", "salary", "outgoing", nil, &rule.ID)

	if status := deleteRule(t, queries, rule.ID); status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d", status, http.StatusNoContent)
	}

	detail, err := queries.GetTransactionDetail(t.Context(), pt.ID)
	if err != nil {
		t.Fatalf("GetTransactionDetail: %v", err)
	}
	if detail.Category != "salary" {
		t.Errorf("category = %q, want unchanged salary", detail.Category)
	}
}
