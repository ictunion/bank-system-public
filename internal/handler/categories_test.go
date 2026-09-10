package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

	want := map[string]bool{
		"membership_fee": true,
		"salary":         true,
		"other_income":   true,
		"other_expense":  true,
	}
	found := make(map[string]bool, len(want))
	for _, c := range got {
		if mandatory, ok := want[c.Name]; ok {
			found[c.Name] = true
			if c.IsMandatory != mandatory {
				t.Errorf("category %q: is_mandatory = %v, want %v", c.Name, c.IsMandatory, mandatory)
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
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/categories/"+name, nil)
	request.SetPathValue("name", name)
	DeleteCategory(queries)(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", recorder.Code, recorder.Body)
	}

	// No longer in the list.
	if categoryListContains(t, queries, name) {
		t.Errorf("deleted category %q still present in ListCategories", name)
	}

	// Deleting again is a 404, not a silent success.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodDelete, "/categories/"+name, nil)
	request.SetPathValue("name", name)
	DeleteCategory(queries)(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Errorf("second delete status = %d, want %d", recorder.Code, http.StatusNotFound)
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

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/categories/membership_fee", nil)
	request.SetPathValue("name", "membership_fee")
	DeleteCategory(queries)(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
	}
	if categoryListMissing(t, queries, "membership_fee") {
		t.Error("mandatory category membership_fee was deleted")
	}
}

func TestDeleteCategory_NotFound(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/categories/does_not_exist_xyz", nil)
	request.SetPathValue("name", "does_not_exist_xyz")
	DeleteCategory(queries)(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusNotFound, recorder.Body)
	}
}

// postCategory issues a CreateCategory request and returns the decoded body,
// failing the test if the response status doesn't match wantStatus.
func postCategory(t *testing.T, queries *db.Queries, name string, wantStatus int) categoryResponse {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/categories", strings.NewReader(`{"name":"`+name+`"}`))
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
			return true
		}
	}
	return false
}

// categoryListMissing is categoryListContains negated — named separately
// (rather than `!categoryListContains(...)` at the call site) so the
// mandatory-category-survives assertion in TestDeleteCategory_Mandatory
// reads as what it's actually checking.
func categoryListMissing(t *testing.T, queries *db.Queries, name string) bool {
	t.Helper()
	return !categoryListContains(t, queries, name)
}
