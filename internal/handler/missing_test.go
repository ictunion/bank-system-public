package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
	"github.com/kubik/bank-system/internal/keycloak"
	"github.com/kubik/bank-system/internal/keycloaktest"
)

// withWorkplaceRepContext simulates RequireRole having already run for a
// workplace rep — claims content doesn't matter here (WorkplaceMissingPayments
// and WorkplaceMissingPaymentsInYear never call ClaimsFromContext, only
// TokenFromContext), so an empty Claims is enough.
func withWorkplaceRepContext(r *http.Request) *http.Request {
	return r.WithContext(withClaims(r.Context(), &keycloak.Claims{}, "fake-token"))
}

func seedMember(t *testing.T, queries *db.Queries, memberNumber int32, feeStartDate *time.Time) {
	t.Helper()

	var fsd pgtype.Date
	if feeStartDate != nil {
		fsd = pgtype.Date{Time: *feeStartDate, Valid: true}
	}
	if err := queries.UpsertMember(context.Background(), db.UpsertMemberParams{
		MemberNumber: memberNumber,
		FeeStartDate: fsd,
		Active:       true,
	}); err != nil {
		t.Fatalf("seedMember(%d): %v", memberNumber, err)
	}
}

func missingPaymentMembers(t *testing.T, recorder *httptest.ResponseRecorder) []missingPaymentMember {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got []missingPaymentMember
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return got
}

func containsMember(members []missingPaymentMember, memberNumber int32) bool {
	for _, m := range members {
		if m.MemberNumber == memberNumber {
			return true
		}
	}
	return false
}

func findMember(t *testing.T, members []missingPaymentMember, memberNumber int32) missingPaymentMember {
	t.Helper()
	for _, m := range members {
		if m.MemberNumber == memberNumber {
			return m
		}
	}
	t.Fatalf("member %d not found in %+v", memberNumber, members)
	return missingPaymentMember{}
}

func TestMissingPayments_Validation(t *testing.T) {
	queries := dbtest.Tx(t)

	tests := []struct {
		name  string
		year  string
		month string
	}{
		{"non-numeric year", "abc", "1"},
		{"zero year", "0", "1"},
		{"year too far in the future", "9999", "1"},
		{"non-numeric month", "2026", "abc"},
		{"month zero", "2026", "0"},
		{"month thirteen", "2026", "13"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/payments/"+tt.year+"/"+tt.month+"/missing", nil)
			request.SetPathValue("year", tt.year)
			request.SetPathValue("month", tt.month)
			MissingPayments(queries)(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
			}
		})
	}
}

func TestMissingPayments_MemberNeverPaid(t *testing.T) {
	queries := dbtest.Tx(t)
	const memberNumber = int32(900001)

	feeStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMember(t, queries, memberNumber, &feeStart)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/2026/1/missing", nil)
	request.SetPathValue("year", "2026")
	request.SetPathValue("month", "1")
	MissingPayments(queries)(recorder, request)

	members := missingPaymentMembers(t, recorder)
	if !containsMember(members, memberNumber) {
		t.Errorf("member %d (liable since 2020, never paid) missing from response: %+v", memberNumber, members)
	}
	if got := findMember(t, members, memberNumber); got.HasEverPaid {
		t.Errorf("HasEverPaid = true, want false — member has zero payment_coverage rows ever")
	}
}

func TestMissingPayments_HasEverPaidWhenMissingADifferentMonth(t *testing.T) {
	queries := dbtest.Tx(t)
	const memberNumber = int32(900005)

	feeStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMember(t, queries, memberNumber, &feeStart)

	// Paid a month that isn't the one being queried below — has_ever_paid
	// must reflect "ever," not "for the queried month."
	account := seedBankAccount(t, queries, "9200000010")
	member := memberNumber
	pt := seedTransaction(t, queries, account.ID, 9001, "500.00", "membership_fee", "incoming", &member)
	if _, err := queries.InsertCoverageRow(context.Background(), db.InsertCoverageRowParams{
		ProcessedTransactionID: pt.ID,
		MemberNumber:           memberNumber,
		CoversYear:             2025,
		CoversMonth:            6,
	}); err != nil {
		t.Fatalf("InsertCoverageRow: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/2026/1/missing", nil)
	request.SetPathValue("year", "2026")
	request.SetPathValue("month", "1")
	MissingPayments(queries)(recorder, request)

	members := missingPaymentMembers(t, recorder)
	if !containsMember(members, memberNumber) {
		t.Fatalf("member %d missing from response for the unpaid month: %+v", memberNumber, members)
	}
	if got := findMember(t, members, memberNumber); !got.HasEverPaid {
		t.Errorf("HasEverPaid = false, want true — member has a payment_coverage row for a different month")
	}
}

// TestMissingPayments_OneMonthGracePeriod pins down the actual bug this test
// guards against: dues for month M are paid during month M+1, so M only
// becomes "missing" once M+1 has also fully elapsed. Computed relative to time.Now()
// rather than a fixed year/month so it stays meaningful whenever the suite
// runs, unlike the fixed-2026 tests above.
func TestMissingPayments_OneMonthGracePeriod(t *testing.T) {
	now := time.Now()
	feeStart := now.AddDate(-5, 0, 0) // liable well before any of the months below

	tests := []struct {
		name        string
		monthsAgo   int
		wantMissing bool
	}{
		{"current month — not due yet at all", 0, false},
		{"last month — still within its grace period", 1, false},
		{"two months ago — grace period has elapsed", 2, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queries := dbtest.Tx(t)
			memberNumber := int32(900010 + tt.monthsAgo)
			seedMember(t, queries, memberNumber, &feeStart)

			target := now.AddDate(0, -tt.monthsAgo, 0)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/payments/x/x/missing", nil)
			request.SetPathValue("year", itoa(int32(target.Year())))
			request.SetPathValue("month", itoa(int32(target.Month())))
			MissingPayments(queries)(recorder, request)

			members := missingPaymentMembers(t, recorder)
			if got := containsMember(members, memberNumber); got != tt.wantMissing {
				t.Errorf("containsMember = %v, want %v (querying %d-%02d, %d month(s) ago): %+v",
					got, tt.wantMissing, target.Year(), int(target.Month()), tt.monthsAgo, members)
			}
		})
	}
}

func TestMissingPayments_MemberNotYetLiable(t *testing.T) {
	queries := dbtest.Tx(t)
	const memberNumber = int32(900002)

	seedMember(t, queries, memberNumber, nil) // no fee_start_date

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/2026/1/missing", nil)
	request.SetPathValue("year", "2026")
	request.SetPathValue("month", "1")
	MissingPayments(queries)(recorder, request)

	members := missingPaymentMembers(t, recorder)
	if containsMember(members, memberNumber) {
		t.Errorf("member %d has no fee_start_date (not yet liable) but appears in missing list: %+v", memberNumber, members)
	}
}

func TestMissingPaymentsInYear_Validation(t *testing.T) {
	queries := dbtest.Tx(t)

	tests := []string{"abc", "0", "9999"}
	for _, year := range tests {
		t.Run(year, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/payments/"+year+"/missing", nil)
			request.SetPathValue("year", year)
			MissingPaymentsInYear(queries)(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
			}
		})
	}
}

func TestMissingPaymentsInYear_MemberNeverPaid(t *testing.T) {
	queries := dbtest.Tx(t)
	const memberNumber = int32(900003)

	feeStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMember(t, queries, memberNumber, &feeStart)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/2026/missing", nil)
	request.SetPathValue("year", "2026")
	MissingPaymentsInYear(queries)(recorder, request)

	members := missingPaymentMembers(t, recorder)
	if !containsMember(members, memberNumber) {
		t.Errorf("member %d (liable since 2020, never paid) missing from response: %+v", memberNumber, members)
	}
}

// seedLiableMemberInWorkplace seeds a member who's both liable (fee_start_date
// in the past, never paid) and assigned to workplaceSub — the combination
// WorkplaceMissingPayments/InYear actually need to have something to find.
func seedLiableMemberInWorkplace(t *testing.T, queries *db.Queries, memberNumber int32, workplaceSub string) {
	t.Helper()

	var workplaceValue pgtype.UUID
	if err := workplaceValue.Scan(workplaceSub); err != nil {
		t.Fatalf("parsing workplace sub %q: %v", workplaceSub, err)
	}
	feeStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := queries.UpsertMember(context.Background(), db.UpsertMemberParams{
		MemberNumber:                   memberNumber,
		Active:                         true,
		FeeStartDate:                   pgtype.Date{Time: feeStart, Valid: true},
		WorkplaceExecutiveCommitteeSub: workplaceValue,
	}); err != nil {
		t.Fatalf("seedLiableMemberInWorkplace(%d): %v", memberNumber, err)
	}
}

func TestWorkplaceMissingPayments_Success(t *testing.T) {
	const workplaceSub = "77777777-7777-7777-7777-777777777777"
	queries := dbtest.Tx(t)
	provider := keycloaktest.New(t)
	provider.SetGroups([]string{workplaceSub})

	seedLiableMemberInWorkplace(t, queries, 900401, workplaceSub)

	recorder := httptest.NewRecorder()
	request := withWorkplaceRepContext(httptest.NewRequest(http.MethodGet, "/payments/workplace/2026/1/missing", nil))
	request.SetPathValue("year", "2026")
	request.SetPathValue("month", "1")
	WorkplaceMissingPayments(provider.Provider, queries)(recorder, request)

	members := missingPaymentMembers(t, recorder)
	if !containsMember(members, 900401) {
		t.Errorf("member 900401 (liable, in the rep's workplace, never paid) missing from response: %+v", members)
	}
}

func TestWorkplaceMissingPayments_WrongWorkplaceIsEmptyNot403(t *testing.T) {
	// Unlike PaymentHistory's single-member lookup, the bulk workplace
	// routes have nothing to 403 about — a rep whose groups don't match any
	// member's workplace just gets an empty list, same as an admin querying
	// a workplace with no liable members at all.
	const memberWorkplace = "88888888-8888-8888-8888-888888888888"
	const repWorkplace = "99999999-9999-9999-9999-999999999999"
	queries := dbtest.Tx(t)
	provider := keycloaktest.New(t)
	provider.SetGroups([]string{repWorkplace})

	seedLiableMemberInWorkplace(t, queries, 900402, memberWorkplace)

	recorder := httptest.NewRecorder()
	request := withWorkplaceRepContext(httptest.NewRequest(http.MethodGet, "/payments/workplace/2026/1/missing", nil))
	request.SetPathValue("year", "2026")
	request.SetPathValue("month", "1")
	WorkplaceMissingPayments(provider.Provider, queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusOK, recorder.Body)
	}
	members := missingPaymentMembers(t, recorder)
	if containsMember(members, 900402) {
		t.Errorf("member from a different workplace leaked into the response: %+v", members)
	}
}

func TestWorkplaceMissingPayments_NoGroups(t *testing.T) {
	queries := dbtest.Tx(t)
	provider := keycloaktest.New(t)
	provider.SetGroups(nil)

	recorder := httptest.NewRecorder()
	request := withWorkplaceRepContext(httptest.NewRequest(http.MethodGet, "/payments/workplace/2026/1/missing", nil))
	request.SetPathValue("year", "2026")
	request.SetPathValue("month", "1")
	WorkplaceMissingPayments(provider.Provider, queries)(recorder, request)

	members := missingPaymentMembers(t, recorder)
	if len(members) != 0 {
		t.Errorf("got %+v, want empty — caller belongs to no workplace groups at all", members)
	}
}

func TestWorkplaceMissingPayments_Validation(t *testing.T) {
	queries := dbtest.Tx(t)
	provider := keycloaktest.New(t)

	recorder := httptest.NewRecorder()
	request := withWorkplaceRepContext(httptest.NewRequest(http.MethodGet, "/payments/workplace/abc/1/missing", nil))
	request.SetPathValue("year", "abc")
	request.SetPathValue("month", "1")
	WorkplaceMissingPayments(provider.Provider, queries)(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
	}
}

func TestWorkplaceMissingPaymentsInYear_Success(t *testing.T) {
	const workplaceSub = "77777777-7777-7777-7777-777777777778"
	queries := dbtest.Tx(t)
	provider := keycloaktest.New(t)
	provider.SetGroups([]string{workplaceSub})

	seedLiableMemberInWorkplace(t, queries, 900403, workplaceSub)

	recorder := httptest.NewRecorder()
	request := withWorkplaceRepContext(httptest.NewRequest(http.MethodGet, "/payments/workplace/2026/missing", nil))
	request.SetPathValue("year", "2026")
	WorkplaceMissingPaymentsInYear(provider.Provider, queries)(recorder, request)

	members := missingPaymentMembers(t, recorder)
	if !containsMember(members, 900403) {
		t.Errorf("member 900403 (liable, in the rep's workplace, never paid) missing from response: %+v", members)
	}
}
