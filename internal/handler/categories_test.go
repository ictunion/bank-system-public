package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/bank-system/internal/categoryid"
	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
)

func TestListCategories_SeededMandatory(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/categories", nil)
	ListCategories(queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}

	var got []categoryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	// The mandatory categories carry the fixed ids Go code refers to.
	want := map[string]pgtype.UUID{
		"membership_fee":    categoryid.MembershipFee,
		"salary":            categoryid.Salary,
		"other_income":      categoryid.OtherIncome,
		"other_expense":     categoryid.OtherExpense,
		"internal_transfer": categoryid.InternalTransfer,
		"credited_interest": categoryid.CreditedInterest,
	}
	found := make(map[string]bool, len(want))
	for _, c := range got {
		if id, ok := want[c.Name]; ok {
			found[c.Name] = true
			if !c.IsMandatory {
				t.Errorf("category %q: is_mandatory = false, want true", c.Name)
			}
			if c.ID != id {
				t.Errorf("category %q: id = %v, want the fixed %v", c.Name, c.ID, id)
			}
		}
	}
	for name := range want {
		if !found[name] {
			t.Errorf("mandatory category %q missing from response", name)
		}
	}
}

func TestCreateCategory_ThenListThenDelete(t *testing.T) {
	queries := dbtest.Tx(t)
	const name = "test_custom_category"

	// Create.
	created := postCategory(t, queries, name, http.StatusCreated)
	if created.Name != name {
		t.Errorf("created.Name = %q, want %q", created.Name, name)
	}
	if created.IsMandatory {
		t.Error("created.IsMandatory = true, want false — CreateCategory should never mark one mandatory")
	}

	// Shows up in the list.
	if !categoryListContains(t, queries, name) {
		t.Errorf("newly created category %q not found in ListCategories", name)
	}

	// Delete.
	if status := deleteCategory(t, queries, uuidText(t, created.ID)); status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d", status, http.StatusNoContent)
	}

	// No longer in the list.
	if categoryListContains(t, queries, name) {
		t.Errorf("deleted category %q still present in ListCategories", name)
	}

	// Deleting again is a 404, not a silent success.
	if status := deleteCategory(t, queries, uuidText(t, created.ID)); status != http.StatusNotFound {
		t.Errorf("second delete status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestCreateCategory_Duplicate(t *testing.T) {
	queries := dbtest.Tx(t)
	const name = "test_duplicate_category"

	postCategory(t, queries, name, http.StatusCreated)
	postCategory(t, queries, name, http.StatusConflict)
}

func TestCreateCategory_Validation(t *testing.T) {
	queries := dbtest.Tx(t)

	tests := []struct {
		name string
		body string
	}{
		{name: "empty name", body: `{"name":""}`},
		{name: "whitespace-only name", body: `{"name":"   "}`},
		{name: "name too long", body: `{"name":"` + strings.Repeat("x", categoryNameMaxLength+1) + `"}`},
		{name: "invalid JSON", body: `not json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/categories", strings.NewReader(tt.body))
			CreateCategory(queries)(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
			}
		})
	}
}

func TestDeleteCategory_Mandatory(t *testing.T) {
	queries := dbtest.Tx(t)

	if status := deleteCategory(t, queries, uuidText(t, categoryid.MembershipFee)); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if categoryListMissing(t, queries, "membership_fee") {
		t.Error("mandatory category membership_fee was deleted")
	}
}

func TestDeleteCategory_NotFound(t *testing.T) {
	queries := dbtest.Tx(t)

	if status := deleteCategory(t, queries, "11111111-1111-4111-8111-111111111111"); status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
	if status := deleteCategory(t, queries, "not-a-uuid"); status != http.StatusBadRequest {
		t.Errorf("invalid id status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestCreateCategory_WithParent(t *testing.T) {
	queries := dbtest.Tx(t)
	const childName = "test_legal"

	created := postCategoryBody(t, queries, `{"name":"`+childName+`","parent_id":"`+uuidText(t, categoryid.Salary)+`"}`, http.StatusCreated)
	if created.ParentID != categoryid.Salary {
		t.Errorf("created.ParentID = %v, want salary's id", created.ParentID)
	}
	if !created.ID.Valid {
		t.Error("created.ID is null, want a generated id")
	}

	listed, ok := findListedCategory(t, queries, childName)
	if !ok {
		t.Fatalf("sub-category %q not found in ListCategories", childName)
	}
	if listed.ParentID != categoryid.Salary {
		t.Errorf("listed.ParentID = %v, want salary's id", listed.ParentID)
	}

	topLevel, ok := findListedCategory(t, queries, "salary")
	if !ok {
		t.Fatal("salary not found in ListCategories")
	}
	if topLevel.ParentID.Valid {
		t.Errorf("salary.ParentID = %v, want null", topLevel.ParentID)
	}
}

func TestCreateCategory_NullParentIsTopLevel(t *testing.T) {
	queries := dbtest.Tx(t)

	created := postCategoryBody(t, queries, `{"name":"test_no_parent","parent_id":null}`, http.StatusCreated)
	if created.ParentID.Valid {
		t.Errorf("created.ParentID = %v, want null", created.ParentID)
	}
}

func TestCreateCategory_InvalidParent(t *testing.T) {
	queries := dbtest.Tx(t)
	child := postCategoryBody(t, queries, `{"name":"test_child","parent_id":"`+uuidText(t, categoryid.Salary)+`"}`, http.StatusCreated)

	tests := []struct {
		name string
		body string
	}{
		{name: "parent does not exist", body: `{"name":"test_orphan","parent_id":"11111111-1111-4111-8111-111111111111"}`},
		{name: "parent is itself a sub-category", body: `{"name":"test_grandchild","parent_id":"` + uuidText(t, child.ID) + `"}`},
		{name: "parent id malformed", body: `{"name":"test_orphan","parent_id":"salary"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			postCategoryBody(t, queries, tt.body, http.StatusBadRequest)
		})
	}
}

func TestDeleteCategory_HasChildren(t *testing.T) {
	queries := dbtest.Tx(t)
	parent := postCategory(t, queries, "test_parent", http.StatusCreated)
	child := postCategoryBody(t, queries, `{"name":"test_child","parent_id":"`+uuidText(t, parent.ID)+`"}`, http.StatusCreated)

	if status := deleteCategory(t, queries, uuidText(t, parent.ID)); status != http.StatusConflict {
		t.Fatalf("status = %d, want %d", status, http.StatusConflict)
	}

	// The child itself is deletable, after which the parent is too.
	for _, category := range []categoryResponse{child, parent} {
		if status := deleteCategory(t, queries, uuidText(t, category.ID)); status != http.StatusNoContent {
			t.Errorf("delete %q status = %d, want %d", category.Name, status, http.StatusNoContent)
		}
	}
}

// deleteCategory issues a DeleteCategory request for an id (text form) and
// returns the status.
func deleteCategory(t *testing.T, queries *db.Queries, id string) int {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/categories/"+id, nil)
	request.SetPathValue("id", id)
	DeleteCategory(queries)(recorder, request)
	return recorder.Code
}

// postCategory issues a CreateCategory request and returns the decoded body,
// failing the test if the response status doesn't match wantStatus.
func postCategory(t *testing.T, queries *db.Queries, name string, wantStatus int) categoryResponse {
	t.Helper()
	return postCategoryBody(t, queries, `{"name":"`+name+`"}`, wantStatus)
}

// postCategoryBody is postCategory with a raw JSON body, for requests that
// carry more than a name (e.g. parent_id).
func postCategoryBody(t *testing.T, queries *db.Queries, body string, wantStatus int) categoryResponse {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/categories", strings.NewReader(body))
	CreateCategory(queries)(recorder, request)

	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, wantStatus, recorder.Body)
	}
	var got categoryResponse
	if recorder.Code == http.StatusCreated {
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
	}
	return got
}

// categoryListContains reports whether name is present in a fresh
// ListCategories call.
func categoryListContains(t *testing.T, queries *db.Queries, name string) bool {
	t.Helper()
	_, ok := findListedCategory(t, queries, name)
	return ok
}

// findListedCategory returns name's entry from a fresh ListCategories call.
func findListedCategory(t *testing.T, queries *db.Queries, name string) (categoryResponse, bool) {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/categories", nil)
	ListCategories(queries)(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}

	var got []categoryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	for _, c := range got {
		if c.Name == name {
			return c, true
		}
	}
	return categoryResponse{}, false
}

// categoryListMissing is categoryListContains negated — named separately
// (rather than `!categoryListContains(...)` at the call site) so the
// mandatory-category-survives assertion in TestDeleteCategory_Mandatory
// reads as what it's actually checking.
func categoryListMissing(t *testing.T, queries *db.Queries, name string) bool {
	t.Helper()
	return !categoryListContains(t, queries, name)
}
