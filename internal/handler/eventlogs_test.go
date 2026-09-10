package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
)

func TestListEventLogs_Empty(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/event-logs", nil)
	ListEventLogs(queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}

	var got eventLogsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Total != 0 {
		t.Errorf("Total = %d, want 0", got.Total)
	}
	if len(got.Events) != 0 {
		t.Errorf("len(Events) = %d, want 0", len(got.Events))
	}
}

func TestListEventLogs_Validation(t *testing.T) {
	queries := dbtest.Tx(t)

	tests := []struct {
		name  string
		query string
	}{
		{"non-numeric limit", "limit=abc"},
		{"zero limit", "limit=0"},
		{"negative limit", "limit=-1"},
		{"non-numeric offset", "offset=abc"},
		{"negative offset", "offset=-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/event-logs?"+tt.query, nil)
			ListEventLogs(queries)(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
			}
		})
	}
}

func TestListEventLogs_ReturnsOrcaRun(t *testing.T) {
	queries := dbtest.Tx(t)
	ctx := context.Background()

	run, err := queries.CreateSyncOrcaRun(ctx)
	if err != nil {
		t.Fatalf("CreateSyncOrcaRun: %v", err)
	}
	fetched := int32(5)
	upserted := int32(5)
	if err := queries.FinishSyncOrcaRun(ctx, db.FinishSyncOrcaRunParams{
		ID:              run.ID,
		Status:          "success",
		MembersFetched:  &fetched,
		MembersUpserted: &upserted,
	}); err != nil {
		t.Fatalf("FinishSyncOrcaRun: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/event-logs", nil)
	ListEventLogs(queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got eventLogsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Total != 1 {
		t.Fatalf("Total = %d, want 1", got.Total)
	}
	if len(got.Events) != 1 {
		t.Fatalf("len(Events) = %d, want 1", len(got.Events))
	}

	event := got.Events[0]
	if event.EventType != "orca_sync" {
		t.Errorf("EventType = %q, want orca_sync", event.EventType)
	}
	if event.Status != "success" {
		t.Errorf("Status = %q, want success", event.Status)
	}
	if event.FinishedAt == nil {
		t.Error("FinishedAt = nil, want set — FinishSyncOrcaRun should have populated it")
	}
	if event.Fetched == nil || *event.Fetched != 5 {
		t.Errorf("Fetched = %v, want 5", event.Fetched)
	}
	if event.Detail != nil {
		t.Errorf("Detail = %v, want nil — orca_sync rows never carry a bank account detail", event.Detail)
	}
}
