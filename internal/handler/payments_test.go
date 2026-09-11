package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
	"github.com/kubik/bank-system/internal/keycloak"
	"github.com/kubik/bank-system/internal/keycloaktest"
)

// seedMemberWithSub is seedMember (missing_test.go) plus a Keycloak sub, for
// the self-service /payments/me/history path that joins on it.
func seedMemberWithSub(t *testing.T, queries *db.Queries, memberNumber int32, sub string) {
	t.Helper()

	var subValue pgtype.UUID
	if err := subValue.Scan(sub); err != nil {
		t.Fatalf("parsing sub %q: %v", sub, err)
	}
	if err := queries.UpsertMember(context.Background(), db.UpsertMemberParams{
		MemberNumber: memberNumber,
		Active:       true,
		Sub:          subValue,
	}); err != nil {
		t.Fatalf("seedMemberWithSub(%d): %v", memberNumber, err)
	}
}

// withFakeClaims simulates RequireAuth/RequireRole already having run:
// attaches claims (just a sub here — MyPaymentHistory reads nothing else)
// and a placeholder token to the request context via the same withClaims
// helper the real middleware uses. No live Keycloak involved.
func withFakeClaims(r *http.Request, subject string) *http.Request {
	claims := &keycloak.Claims{}
	claims.Subject = subject
	return r.WithContext(withClaims(r.Context(), claims, "fake-token"))
}

func TestMyPaymentHistory_NoMemberForSub(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := withFakeClaims(httptest.NewRequest(http.MethodGet, "/payments/me/history", nil), "22222222-2222-2222-2222-222222222222")
	MyPaymentHistory(queries)(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusForbidden, recorder.Body)
	}
}

func TestMyPaymentHistory_InvalidSub(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := withFakeClaims(httptest.NewRequest(http.MethodGet, "/payments/me/history", nil), "not-a-uuid")
	MyPaymentHistory(queries)(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusForbidden, recorder.Body)
	}
}

func TestMyPaymentHistory_EmptyHistory(t *testing.T) {
	queries := dbtest.Tx(t)
	const sub = "33333333-3333-3333-3333-333333333333"
	seedMemberWithSub(t, queries, 900101, sub)

	recorder := httptest.NewRecorder()
	request := withFakeClaims(httptest.NewRequest(http.MethodGet, "/payments/me/history", nil), sub)
	MyPaymentHistory(queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got []paymentHistoryEntry
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want empty — member exists but has no payment_coverage rows", got)
	}
}

func TestMyPaymentHistory_ReturnsCoveredMonths(t *testing.T) {
	queries := dbtest.Tx(t)
	const sub = "44444444-4444-4444-4444-444444444444"
	const memberNumber = int32(900102)
	seedMemberWithSub(t, queries, memberNumber, sub)

	account := seedBankAccount(t, queries, "9200000001")
	member := memberNumber
	pt := seedTransaction(t, queries, account.ID, 4001, "500.00", "membership_fee", "incoming", &member)

	if _, err := queries.InsertCoverageRow(context.Background(), db.InsertCoverageRowParams{
		ProcessedTransactionID: pt.ID,
		MemberNumber:           memberNumber,
		CoversYear:             2026,
		CoversMonth:            1,
	}); err != nil {
		t.Fatalf("InsertCoverageRow: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := withFakeClaims(httptest.NewRequest(http.MethodGet, "/payments/me/history", nil), sub)
	MyPaymentHistory(queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got []paymentHistoryEntry
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	if len(got[0].CoveredMonths) != 1 || got[0].CoveredMonths[0].Month != 1 || got[0].CoveredMonths[0].Year != 2026 {
		t.Errorf("CoveredMonths = %+v, want [{2026 1}]", got[0].CoveredMonths)
	}
}

func TestMyPaymentHistory_WaivedMonthShowsAsWaived(t *testing.T) {
	queries := dbtest.Tx(t)
	const sub = "77777777-7777-7777-7777-777777777777"
	const memberNumber = int32(900103)
	seedMemberWithSub(t, queries, memberNumber, sub)

	if _, err := queries.CreatePaymentWaiver(context.Background(), db.CreatePaymentWaiverParams{
		MemberNumber: memberNumber,
		CoversYear:   2022,
		CoversMonth:  6,
		Reason:       "one-off miss, too old to chase",
	}); err != nil {
		t.Fatalf("CreatePaymentWaiver: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := withFakeClaims(httptest.NewRequest(http.MethodGet, "/payments/me/history", nil), sub)
	MyPaymentHistory(queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got []paymentHistoryEntry
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	if got[0].Amount != "Waived" {
		t.Errorf("Amount = %q, want %q", got[0].Amount, "Waived")
	}
	if len(got[0].CoveredMonths) != 1 || got[0].CoveredMonths[0] != (coveredMonth{Year: 2022, Month: 6}) {
		t.Errorf("CoveredMonths = %+v, want [{2022 6}]", got[0].CoveredMonths)
	}
	// The reason must never reach this response — it's admin-only.
	if strings.Contains(recorder.Body.String(), "too old to chase") {
		t.Errorf("response leaks the waiver reason: %s", recorder.Body.String())
	}
}

func TestMyPaymentHistory_PaidMonthWinsOverWaivedMonth(t *testing.T) {
	queries := dbtest.Tx(t)
	const sub = "88888888-8888-8888-8888-888888888888"
	const memberNumber = int32(900104)
	seedMemberWithSub(t, queries, memberNumber, sub)

	if _, err := queries.CreatePaymentWaiver(context.Background(), db.CreatePaymentWaiverParams{
		MemberNumber: memberNumber,
		CoversYear:   2026,
		CoversMonth:  1,
		Reason:       "x",
	}); err != nil {
		t.Fatalf("CreatePaymentWaiver: %v", err)
	}

	account := seedBankAccount(t, queries, "9200000030")
	member := memberNumber
	pt := seedTransaction(t, queries, account.ID, 4002, "500.00", "membership_fee", "incoming", &member)
	if _, err := queries.InsertCoverageRow(context.Background(), db.InsertCoverageRowParams{
		ProcessedTransactionID: pt.ID,
		MemberNumber:           memberNumber,
		CoversYear:             2026,
		CoversMonth:            1,
	}); err != nil {
		t.Fatalf("InsertCoverageRow: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := withFakeClaims(httptest.NewRequest(http.MethodGet, "/payments/me/history", nil), sub)
	MyPaymentHistory(queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got []paymentHistoryEntry
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	// A payment landed for a month that was already (redundantly) waived —
	// must appear once, as the real payment, not twice.
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1 (real payment must win, month not double-reported): %+v", len(got), got)
	}
	if got[0].Amount == "Waived" {
		t.Errorf("Amount = %q, want the real payment amount", got[0].Amount)
	}
}

// seedMemberWithWorkplace is seedMember plus a workplace_executive_committee_sub
// — for the workplace-rep access path on PaymentHistory.
func seedMemberWithWorkplace(t *testing.T, queries *db.Queries, memberNumber int32, workplaceSub string) {
	t.Helper()

	var workplaceValue pgtype.UUID
	if err := workplaceValue.Scan(workplaceSub); err != nil {
		t.Fatalf("parsing workplace sub %q: %v", workplaceSub, err)
	}
	if err := queries.UpsertMember(context.Background(), db.UpsertMemberParams{
		MemberNumber:                   memberNumber,
		Active:                         true,
		WorkplaceExecutiveCommitteeSub: workplaceValue,
	}); err != nil {
		t.Fatalf("seedMemberWithWorkplace(%d): %v", memberNumber, err)
	}
}

func adminClaims() *keycloak.Claims {
	return &keycloak.Claims{
		ResourceAccess: map[string]keycloak.RolesClaim{
			keycloaktest.ClientID: {Roles: []string{string(keycloak.RolePaymentHistory)}},
		},
	}
}

func workplaceRepClaims() *keycloak.Claims {
	return &keycloak.Claims{
		ResourceAccess: map[string]keycloak.RolesClaim{
			keycloaktest.ClientID: {Roles: []string{string(keycloak.RoleViewWorkplacePaymentHistory)}},
		},
	}
}

func TestPaymentHistory_AdminSeesAnyMember(t *testing.T) {
	queries := dbtest.Tx(t)
	provider := keycloaktest.New(t)

	// Admins aren't scoped to any workplace — a member with none set at all
	// must still be visible.
	seedMember(t, queries, 900201, nil)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/900201/history", nil)
	request = request.WithContext(withClaims(request.Context(), adminClaims(), "fake-token"))
	request.SetPathValue("member_number", "900201")
	PaymentHistory(provider.Provider, queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
}

func TestPaymentHistory_AdminSeesNonexistentMemberAsEmpty(t *testing.T) {
	// Documenting existing behavior, not just asserting it: the admin path
	// has never checked "does this member exist" — GetPaymentHistory just
	// returns zero rows for one that doesn't, same as it always has.
	queries := dbtest.Tx(t)
	provider := keycloaktest.New(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/999999/history", nil)
	request = request.WithContext(withClaims(request.Context(), adminClaims(), "fake-token"))
	request.SetPathValue("member_number", "999999")
	PaymentHistory(provider.Provider, queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got []paymentHistoryEntry
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

func TestPaymentHistory_WorkplaceRep(t *testing.T) {
	const workplaceSub = "55555555-5555-5555-5555-555555555555"
	const otherWorkplaceSub = "66666666-6666-6666-6666-666666666666"

	tests := []struct {
		name           string
		memberNumber   int32
		seedWorkplace  string // "" = no workplace assigned to the member
		repGroups      []string
		wantStatusCode int
	}{
		{"rep's own workplace", 900301, workplaceSub, []string{workplaceSub}, http.StatusOK},
		{"member in a different workplace", 900302, otherWorkplaceSub, []string{workplaceSub}, http.StatusForbidden},
		{"member has no workplace at all", 900303, "", []string{workplaceSub}, http.StatusForbidden},
		{"rep in no groups", 900304, workplaceSub, nil, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queries := dbtest.Tx(t)
			provider := keycloaktest.New(t)
			provider.SetGroups(tt.repGroups)

			if tt.seedWorkplace != "" {
				seedMemberWithWorkplace(t, queries, tt.memberNumber, tt.seedWorkplace)
			} else {
				seedMember(t, queries, tt.memberNumber, nil)
			}

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/payments/x/history", nil)
			request = request.WithContext(withClaims(request.Context(), workplaceRepClaims(), "fake-token"))
			request.SetPathValue("member_number", itoa(tt.memberNumber))
			PaymentHistory(provider.Provider, queries)(recorder, request)

			if recorder.Code != tt.wantStatusCode {
				t.Errorf("status = %d, want %d, body = %s", recorder.Code, tt.wantStatusCode, recorder.Body)
			}
		})
	}
}

func TestPaymentHistory_WorkplaceRepMemberNotFound(t *testing.T) {
	queries := dbtest.Tx(t)
	provider := keycloaktest.New(t)
	provider.SetGroups([]string{"55555555-5555-5555-5555-555555555555"})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/999999/history", nil)
	request = request.WithContext(withClaims(request.Context(), workplaceRepClaims(), "fake-token"))
	request.SetPathValue("member_number", "999999")
	PaymentHistory(provider.Provider, queries)(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d — a non-admin caller gets the same 403 for a nonexistent member as any other denial", recorder.Code, http.StatusForbidden)
	}
}
