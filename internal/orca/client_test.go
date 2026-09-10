package orca

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchMembers_Success(t *testing.T) {
	const token = "the-shared-secret-token"
	var gotPath, gotAuthHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"members": [
				{
					"member_number": 900701,
					"fee_start_date": "2020-01-01",
					"fee_stop_date": null,
					"active": true,
					"sub": "11111111-1111-1111-1111-111111111111",
					"workplace_executive_committee_sub": "22222222-2222-2222-2222-222222222222"
				},
				{
					"member_number": 900702,
					"fee_start_date": null,
					"fee_stop_date": null,
					"active": false,
					"sub": null,
					"workplace_executive_committee_sub": null
				}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, token, false)
	members, err := client.FetchMembers(context.Background())
	if err != nil {
		t.Fatalf("FetchMembers: %v", err)
	}

	if gotPath != "/sync/bank/members" {
		t.Errorf("request path = %q, want /sync/bank/members", gotPath)
	}
	if gotAuthHeader != "Bearer "+token {
		t.Errorf("Authorization header = %q, want %q", gotAuthHeader, "Bearer "+token)
	}

	if len(members) != 2 {
		t.Fatalf("got %d members, want 2", len(members))
	}

	first := members[0]
	if first.MemberNumber != 900701 {
		t.Errorf("first.MemberNumber = %d, want 900701", first.MemberNumber)
	}
	if first.FeeStartDate == nil || first.FeeStartDate.Format("2006-01-02") != "2020-01-01" {
		t.Errorf("first.FeeStartDate = %v, want 2020-01-01", first.FeeStartDate)
	}
	if first.FeeStopDate != nil {
		t.Errorf("first.FeeStopDate = %v, want nil", first.FeeStopDate)
	}
	if !first.Active {
		t.Error("first.Active = false, want true")
	}
	if first.Sub == nil || *first.Sub != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("first.Sub = %v, want the seeded UUID", first.Sub)
	}
	if first.WorkplaceExecutiveCommitteeSub == nil || *first.WorkplaceExecutiveCommitteeSub != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("first.WorkplaceExecutiveCommitteeSub = %v, want the seeded UUID", first.WorkplaceExecutiveCommitteeSub)
	}

	second := members[1]
	if second.MemberNumber != 900702 {
		t.Errorf("second.MemberNumber = %d, want 900702", second.MemberNumber)
	}
	if second.FeeStartDate != nil || second.Sub != nil || second.WorkplaceExecutiveCommitteeSub != nil {
		t.Errorf("second = %+v, want all-nil optional fields", second)
	}
	if second.Active {
		t.Error("second.Active = true, want false")
	}
}

func TestFetchMembers_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewClient(server.URL, "bad-token", false)
	if _, err := client.FetchMembers(context.Background()); err == nil {
		t.Error("FetchMembers returned nil error on a 401 response, want an error")
	}
}

func TestFetchMembers_MalformedDate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"members": [{"member_number": 1, "fee_start_date": "not-a-date", "active": true}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", false)
	if _, err := client.FetchMembers(context.Background()); err == nil {
		t.Error("FetchMembers returned nil error on a malformed fee_start_date, want an error")
	}
}

func TestFetchMembers_InvalidSubUUID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"members": [{"member_number": 1, "active": true, "sub": "not-a-uuid"}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", false)
	if _, err := client.FetchMembers(context.Background()); err == nil {
		t.Error("FetchMembers returned nil error on an invalid sub UUID, want an error")
	}
}

func TestFetchMembers_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", false)
	if _, err := client.FetchMembers(context.Background()); err == nil {
		t.Error("FetchMembers returned nil error on malformed JSON, want an error")
	}
}
