package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
	"github.com/kubik/bank-system/internal/keycloak"
	"github.com/kubik/bank-system/internal/keycloaktest"
)

// RunProcessing calls processing.Run/ReclassifyInternalTransfers, both of
// which open their own nested transactions on the concrete *pgxpool.Pool —
// same reasoning as TriggerFioSync/AssignTransaction elsewhere, dbtest.Pool
// instead of dbtest.Tx.

func manageBankAccountsClaims() *keycloak.Claims {
	return &keycloak.Claims{
		ResourceAccess: map[string]keycloak.RolesClaim{
			keycloaktest.ClientID: {Roles: []string{string(keycloak.RoleManageBankAccounts)}},
		},
	}
}

func manageTransactionsClaims() *keycloak.Claims {
	return &keycloak.Claims{
		ResourceAccess: map[string]keycloak.RolesClaim{
			keycloaktest.ClientID: {Roles: []string{string(keycloak.RoleManageTransactions)}},
		},
	}
}

func runProcessingRequest(t *testing.T, claims *keycloak.Claims, action string) *http.Request {
	t.Helper()
	body := `{"action":"` + action + `"}`
	request := httptest.NewRequest(http.MethodPost, "/processing/run", strings.NewReader(body))
	return request.WithContext(withClaims(request.Context(), claims, "fake-token"))
}

func TestRunProcessing_Run_RequiresManageBankAccountsRole(t *testing.T) {
	pool := dbtest.Pool(t)
	provider := keycloaktest.New(t)

	recorder := httptest.NewRecorder()
	RunProcessing(pool, provider.Provider)(recorder, runProcessingRequest(t, manageTransactionsClaims(), "run"))

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusForbidden, recorder.Body)
	}
}

func TestRunProcessing_Run_Success(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	provider := keycloaktest.New(t)
	account := seedBankAccount(t, queries, "9300000020")
	if _, err := queries.InsertRawTransaction(context.Background(), db.InsertRawTransactionParams{
		BankAccountID:    account.ID,
		FioTransactionID: 89001,
		TransactionDate:  time.Now(),
		Amount:           "100.00",
		Currency:         "CZK",
		RawPayload:       []byte("{}"),
	}); err != nil {
		t.Fatalf("InsertRawTransaction: %v", err)
	}

	recorder := httptest.NewRecorder()
	RunProcessing(pool, provider.Provider)(recorder, runProcessingRequest(t, manageBankAccountsClaims(), "run"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got processingActionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Action != "run" || got.Succeeded != 1 || got.Failed != 0 {
		t.Errorf("got %+v, want {run 1 0}", got)
	}
}

func TestRunProcessing_ReclassifyInternalTransfers_RequiresManageTransactionsRole(t *testing.T) {
	pool := dbtest.Pool(t)
	provider := keycloaktest.New(t)

	recorder := httptest.NewRecorder()
	RunProcessing(pool, provider.Provider)(recorder, runProcessingRequest(t, manageBankAccountsClaims(), "reclassify_internal_transfers"))

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusForbidden, recorder.Body)
	}
}

func TestRunProcessing_ReclassifyInternalTransfers_Success(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	provider := keycloaktest.New(t)
	accountA := seedBankAccount(t, queries, "9300000021")

	// accountB doesn't exist yet when this gets processed — same real bug
	// covered by internal/processing.
	counterAccountNumber := "9300000022"
	if _, err := queries.InsertRawTransaction(context.Background(), db.InsertRawTransactionParams{
		BankAccountID:        accountA.ID,
		FioTransactionID:     89002,
		TransactionDate:      time.Now(),
		Amount:               "-500.00",
		Currency:             "CZK",
		CounterAccountNumber: &counterAccountNumber,
		RawPayload:           []byte("{}"),
	}); err != nil {
		t.Fatalf("InsertRawTransaction: %v", err)
	}
	recorder := httptest.NewRecorder()
	RunProcessing(pool, provider.Provider)(recorder, runProcessingRequest(t, manageBankAccountsClaims(), "run"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("seeding run: status = %d, body = %s", recorder.Code, recorder.Body)
	}

	seedBankAccount(t, queries, counterAccountNumber)

	recorder = httptest.NewRecorder()
	RunProcessing(pool, provider.Provider)(recorder, runProcessingRequest(t, manageTransactionsClaims(), "reclassify_internal_transfers"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got processingActionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Action != "reclassify_internal_transfers" || got.Succeeded != 1 || got.Failed != 0 {
		t.Errorf("got %+v, want {reclassify_internal_transfers 1 0}", got)
	}
}

func TestRunProcessing_UnknownAction(t *testing.T) {
	pool := dbtest.Pool(t)
	provider := keycloaktest.New(t)

	recorder := httptest.NewRecorder()
	RunProcessing(pool, provider.Provider)(recorder, runProcessingRequest(t, manageBankAccountsClaims(), "something-else"))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
	}
}

func TestRunProcessing_InvalidJSON(t *testing.T) {
	pool := dbtest.Pool(t)
	provider := keycloaktest.New(t)

	request := httptest.NewRequest(http.MethodPost, "/processing/run", strings.NewReader("not json"))
	request = request.WithContext(withClaims(request.Context(), manageBankAccountsClaims(), "fake-token"))

	recorder := httptest.NewRecorder()
	RunProcessing(pool, provider.Provider)(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
	}
}
