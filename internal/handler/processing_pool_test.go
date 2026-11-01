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

// RunProcessing calls processing.Run, which opens its own nested
// transactions on the concrete *pgxpool.Pool —
// same reasoning as TriggerFioSync/AssignTransaction elsewhere, dbtest.Pool
// instead of dbtest.Tx.

func manageBankAccountsClaims() *keycloak.Claims {
	return &keycloak.Claims{
		ResourceAccess: map[string]keycloak.RolesClaim{
			keycloaktest.ClientID: {Roles: []string{string(keycloak.RoleManageBankAccounts)}},
		},
	}
}

func claimsWithRoles(roles ...keycloak.Role) *keycloak.Claims {
	names := make([]string, len(roles))
	for i, role := range roles {
		names[i] = string(role)
	}
	return &keycloak.Claims{
		ResourceAccess: map[string]keycloak.RolesClaim{
			keycloaktest.ClientID: {Roles: names},
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

// ReapplyCategorizationRule takes the pool (processing.ReapplyRule opens its
// own transactions), so it lives with the other pool-backed processing tests.
func TestReapplyCategorizationRule(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	salaryRuleID := int64(-1)
	rules, err := queries.ListCategorizationRules(context.Background())
	if err != nil {
		t.Fatalf("ListCategorizationRules: %v", err)
	}
	for _, rule := range rules {
		if rule.Category == "salary" {
			salaryRuleID = rule.ID
		}
	}

	provider := keycloaktest.New(t)
	manager := claimsWithRoles(keycloak.RoleManageRules)
	superadmin := claimsWithRoles(keycloak.RoleManageRules, keycloak.RoleOverrideRules)

	yearStart := time.Date(time.Now().Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
	thisYear := `{"from":"` + yearStart.Format("2006-01-02") + `"}`
	lastYear := `{"from":"` + yearStart.AddDate(0, 0, -1).Format("2006-01-02") + `"}`
	tests := []struct {
		name       string
		claims     *keycloak.Claims
		id         string
		body       string
		wantStatus int
	}{
		{"no body defaults to this year", manager, itoa64(salaryRuleID), "", http.StatusOK},
		{"from start of this year", manager, itoa64(salaryRuleID), thisYear, http.StatusOK},
		{"from before this year", manager, itoa64(salaryRuleID), lastYear, http.StatusBadRequest},
		{"from before this year with override", superadmin, itoa64(salaryRuleID), lastYear, http.StatusOK},
		{"malformed from", superadmin, itoa64(salaryRuleID), `{"from":"01.01.2026"}`, http.StatusBadRequest},
		{"invalid JSON", manager, itoa64(salaryRuleID), `not json`, http.StatusBadRequest},
		{"missing rule", manager, "999999", "", http.StatusNotFound},
		{"invalid id", manager, "abc", "", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/categorization-rules/"+tt.id+"/reapply", strings.NewReader(tt.body))
			request = request.WithContext(withClaims(request.Context(), tt.claims, "fake-token"))
			request.SetPathValue("id", tt.id)
			ReapplyCategorizationRule(pool, provider.Provider)(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d, body = %s", recorder.Code, tt.wantStatus, recorder.Body)
			}
		})
	}
}
