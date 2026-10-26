package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
	"github.com/kubik/bank-system/internal/keycloak"
)

func auditTestClaims() *keycloak.Claims {
	return &keycloak.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "3fa85f64-5717-4562-b3fc-2c963f66afa6"},
		Email:            "admin@example.com",
	}
}

// auditTestMux registers stub handlers on a real ServeMux, the way
// cmd/server/main.go does, so AuditLog sees a real matched Request.Pattern.
// authenticated stands in for RequireRole: it attaches claims via withClaims
// exactly like the real auth wrappers do.
func auditTestMux() *http.ServeMux {
	authenticated := func(status int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(withClaims(r.Context(), auditTestClaims(), "fake-token"))
			w.WriteHeader(status)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /categories", authenticated(http.StatusCreated))
	mux.HandleFunc("PUT /transactions/{id}/assignment", authenticated(http.StatusBadRequest))
	mux.HandleFunc("GET /transactions", authenticated(http.StatusOK))
	mux.HandleFunc("POST /unmapped", authenticated(http.StatusNoContent))
	mux.HandleFunc("DELETE /categories/{name}", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
	})
	return mux
}

func listAllAuditLogs(t *testing.T, queries *db.Queries) []db.ListAuditLogsRow {
	t.Helper()
	rows, err := queries.ListAuditLogs(context.Background(), db.ListAuditLogsParams{Lim: 100})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	return rows
}

func TestAuditLog_RecordsAuthenticatedMutation(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/categories", nil)
	AuditLog(queries, auditTestMux()).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusCreated)
	}

	rows := listAllAuditLogs(t, queries)
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	got := rows[0]
	claims := auditTestClaims()
	if got.ActorSub != claims.Subject {
		t.Errorf("ActorSub = %q, want %q", got.ActorSub, claims.Subject)
	}
	if got.ActorEmail == nil || *got.ActorEmail != "admin@example.com" {
		t.Errorf("ActorEmail = %v, want admin@example.com", got.ActorEmail)
	}
	if got.Category != string(AuditCategoryCategory) || got.Action != "create_category" {
		t.Errorf("Category/Action = %q/%q, want category/create_category", got.Category, got.Action)
	}
	if got.Method != http.MethodPost || got.Route != "POST /categories" || got.Path != "/categories" {
		t.Errorf("Method/Route/Path = %q/%q/%q", got.Method, got.Route, got.Path)
	}
	if got.StatusCode != http.StatusCreated {
		t.Errorf("StatusCode = %d, want %d", got.StatusCode, http.StatusCreated)
	}
}

func TestAuditLog_RecordsRouteAndConcretePath(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/transactions/42/assignment", nil)
	AuditLog(queries, auditTestMux()).ServeHTTP(recorder, request)

	rows := listAllAuditLogs(t, queries)
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.Route != "PUT /transactions/{id}/assignment" || got.Path != "/transactions/42/assignment" {
		t.Errorf("Route/Path = %q/%q", got.Route, got.Path)
	}
	if got.Action != "assign_transaction" {
		t.Errorf("Action = %q, want assign_transaction", got.Action)
	}
	if got.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want %d (rejected attempts are still recorded)", got.StatusCode, http.StatusBadRequest)
	}
}

func TestAuditLog_UnmappedRouteFallsBackToOther(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/unmapped", nil)
	AuditLog(queries, auditTestMux()).ServeHTTP(recorder, request)

	rows := listAllAuditLogs(t, queries)
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].Category != string(AuditCategoryOther) || rows[0].Action != "POST /unmapped" {
		t.Errorf("Category/Action = %q/%q, want other/POST /unmapped", rows[0].Category, rows[0].Action)
	}
}

func TestAuditLog_SkipsUnrecordedRequests(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"read-only GET", http.MethodGet, "/transactions"},
		{"unauthenticated mutation", http.MethodDelete, "/categories/foo"},
		{"unmatched route", http.MethodPost, "/does-not-exist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queries := dbtest.Tx(t)

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tt.method, tt.path, nil)
			AuditLog(queries, auditTestMux()).ServeHTTP(recorder, request)

			if rows := listAllAuditLogs(t, queries); len(rows) != 0 {
				t.Errorf("len(rows) = %d, want 0", len(rows))
			}
		})
	}
}

func TestListAuditLogs_ReturnsNewestFirst(t *testing.T) {
	queries := dbtest.Tx(t)
	mux := auditTestMux()

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/categories", nil),
		httptest.NewRequest(http.MethodPut, "/transactions/1/assignment", nil),
	} {
		AuditLog(queries, mux).ServeHTTP(httptest.NewRecorder(), request)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/audit-logs", nil)
	ListAuditLogs(queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got auditLogsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Total != 2 || len(got.Logs) != 2 {
		t.Fatalf("Total = %d, len(Logs) = %d, want 2/2", got.Total, len(got.Logs))
	}
	if got.Logs[0].Action != "assign_transaction" {
		t.Errorf("Action = %q, want assign_transaction", got.Logs[0].Action)
	}
}

func TestListAuditLogs_Validation(t *testing.T) {
	queries := dbtest.Tx(t)

	tests := []struct {
		name  string
		query string
	}{
		{"unknown category", "category=nope"},
		{"bad from", "from=2026-13-01"},
		{"bad to", "to=yesterday"},
		{"non-numeric limit", "limit=abc"},
		{"zero limit", "limit=0"},
		{"negative offset", "offset=-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/audit-logs?"+tt.query, nil)
			ListAuditLogs(queries)(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
			}
		})
	}
}

func TestListAuditLogs_Filters(t *testing.T) {
	queries := dbtest.Tx(t)
	mux := auditTestMux()

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/categories", nil),
		httptest.NewRequest(http.MethodPut, "/transactions/1/assignment", nil),
	} {
		AuditLog(queries, mux).ServeHTTP(httptest.NewRecorder(), request)
	}

	today := time.Now().Format("2006-01-02")
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")

	tests := []struct {
		name      string
		query     string
		wantTotal int64
	}{
		{"category", "category=transaction", 1},
		{"category with no entries", "category=waiver", 0},
		{"actor_sub", "actor_sub=" + auditTestClaims().Subject, 2},
		{"other actor_sub", "actor_sub=someone-else", 0},
		{"date range including today", "from=" + today + "&to=" + today, 2},
		{"date range after today", "from=" + tomorrow, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/audit-logs?"+tt.query, nil)
			ListAuditLogs(queries)(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
			}
			var got auditLogsResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding response: %v", err)
			}
			if got.Total != tt.wantTotal || int64(len(got.Logs)) != tt.wantTotal {
				t.Errorf("Total = %d, len(Logs) = %d, want %d", got.Total, len(got.Logs), tt.wantTotal)
			}
		})
	}
}
