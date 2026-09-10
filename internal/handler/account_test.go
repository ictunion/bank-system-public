package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
)

const testEncryptionKey = "test-encryption-key"

// itoa formats a path-value ID for request.SetPathValue — handlers parse IDs
// with strconv.ParseInt, which only ever sees a string, same as it would
// from a real URL.
func itoa(id int32) string {
	return strconv.Itoa(int(id))
}

// createTestBankAccount issues a CreateBankAccount request and returns the
// decoded body, failing the test on anything but 201.
func createTestBankAccount(t *testing.T, queries *db.Queries, fioAccountID string) bankAccountResponse {
	t.Helper()

	body := `{"fio_account_id":"` + fioAccountID + `","display_name":"Test Account","fio_token":"tok123"}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/account", strings.NewReader(body))
	CreateBankAccount(queries, testEncryptionKey)(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusCreated, recorder.Body)
	}
	var got bankAccountResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return got
}

func listTestBankAccounts(t *testing.T, queries *db.Queries) []bankAccountResponse {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/account", nil)
	ListBankAccounts(queries)(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got []bankAccountResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return got
}

func TestCreateBankAccount_ThenList(t *testing.T) {
	queries := dbtest.Tx(t)

	created := createTestBankAccount(t, queries, "2000123456")
	if !created.HasToken {
		t.Error("HasToken = false, want true")
	}
	if !created.IsActive {
		t.Error("IsActive = false, want true")
	}
	if created.DisplayName != "Test Account" {
		t.Errorf("DisplayName = %q, want %q", created.DisplayName, "Test Account")
	}

	accounts := listTestBankAccounts(t, queries)
	found := false
	for _, a := range accounts {
		if a.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("created account id=%d not found in ListBankAccounts", created.ID)
	}
}

func TestCreateBankAccount_Validation(t *testing.T) {
	queries := dbtest.Tx(t)

	tests := []struct {
		name string
		body string
	}{
		{"missing fio_account_id", `{"display_name":"x","fio_token":"tok"}`},
		{"missing display_name", `{"fio_account_id":"2000000001","fio_token":"tok"}`},
		{"missing fio_token", `{"fio_account_id":"2000000002","display_name":"x"}`},
		{"currency wrong length", `{"fio_account_id":"2000000003","display_name":"x","fio_token":"tok","currency":"CZ"}`},
		{"invalid JSON", `not json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/account", strings.NewReader(tt.body))
			CreateBankAccount(queries, testEncryptionKey)(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
			}
		})
	}
}

func TestCreateBankAccount_DuplicateFioAccountID(t *testing.T) {
	queries := dbtest.Tx(t)

	createTestBankAccount(t, queries, "2000999999")

	body := `{"fio_account_id":"2000999999","display_name":"Dup","fio_token":"tok"}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/account", strings.NewReader(body))
	CreateBankAccount(queries, testEncryptionKey)(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusConflict, recorder.Body)
	}
}

func TestUpdateBankAccount(t *testing.T) {
	queries := dbtest.Tx(t)
	created := createTestBankAccount(t, queries, "2000111111")

	body := `{"display_name":"Renamed"}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/account/x", strings.NewReader(body))
	request.SetPathValue("id", itoa(created.ID))
	UpdateBankAccount(queries, testEncryptionKey)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got bankAccountResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.DisplayName != "Renamed" {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, "Renamed")
	}
	if !got.HasToken {
		t.Error("HasToken = false, want true — token was never touched by this update")
	}
}

func TestUpdateBankAccount_NotFound(t *testing.T) {
	queries := dbtest.Tx(t)

	body := `{"display_name":"Renamed"}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/account/999999", strings.NewReader(body))
	request.SetPathValue("id", "999999")
	UpdateBankAccount(queries, testEncryptionKey)(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusNotFound, recorder.Body)
	}
}

func TestUpdateBankAccount_Validation(t *testing.T) {
	queries := dbtest.Tx(t)
	created := createTestBankAccount(t, queries, "2000222222")

	tests := []struct {
		name string
		body string
	}{
		{"empty display_name", `{"display_name":""}`},
		{"blank fio_token", `{"display_name":"x","fio_token":"   "}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPatch, "/account/x", strings.NewReader(tt.body))
			request.SetPathValue("id", itoa(created.ID))
			UpdateBankAccount(queries, testEncryptionKey)(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
			}
		})
	}
}

func TestDeleteBankAccount(t *testing.T) {
	queries := dbtest.Tx(t)
	created := createTestBankAccount(t, queries, "2000333333")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/account/x", nil)
	request.SetPathValue("id", itoa(created.ID))
	DeleteBankAccount(queries)(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}

	// Soft delete: still present in the list, just inactive — not gone.
	accounts := listTestBankAccounts(t, queries)
	var found *bankAccountResponse
	for i := range accounts {
		if accounts[i].ID == created.ID {
			found = &accounts[i]
		}
	}
	if found == nil {
		t.Fatalf("deleted account id=%d no longer in ListBankAccounts — should be soft-deleted, not removed", created.ID)
	}
	if found.IsActive {
		t.Error("IsActive = true after delete, want false")
	}

	// Deleting again is a 404, not a silent success.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodDelete, "/account/x", nil)
	request.SetPathValue("id", itoa(created.ID))
	DeleteBankAccount(queries)(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Errorf("second delete status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestDeleteBankAccount_NotFound(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/account/999999", nil)
	request.SetPathValue("id", "999999")
	DeleteBankAccount(queries)(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusNotFound, recorder.Body)
	}
}
