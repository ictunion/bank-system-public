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
)

func decodePaymentStats(t *testing.T, recorder *httptest.ResponseRecorder) paymentStats {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got paymentStats
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return got
}

func TestPaymentStats_NoLiableMembers(t *testing.T) {
	queries := dbtest.Tx(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/stats", nil)
	PaymentStats(queries)(recorder, request)

	got := decodePaymentStats(t, recorder)
	if got.LiableMembers != 0 || got.PaidMembers != 0 {
		t.Errorf("liable_members = %d, paid_members = %d, want 0, 0", got.LiableMembers, got.PaidMembers)
	}
	if got.PercentPaid != nil {
		t.Errorf("percent_paid = %v, want nil (no liable members — avoid a misleading 0%%)", *got.PercentPaid)
	}
}

// TestPaymentStats_PaidInsideWindowVsOutside pins down the trailing-rolling-
// month window itself: a transaction dated a few days ago (inside window)
// counts, one dated well over a month ago (outside window) doesn't — the
// window is exactly "today minus 1 calendar month through today", not the
// arrears/coverage-month convention the rest of this file's payment
// endpoints use.
func TestPaymentStats_PaidInsideWindowVsOutside(t *testing.T) {
	queries := dbtest.Tx(t)

	feeStart := time.Now().AddDate(-5, 0, 0)
	paidMember := int32(900201)
	missingMember := int32(900202)
	seedMember(t, queries, paidMember, &feeStart)
	seedMember(t, queries, missingMember, &feeStart)

	account := seedBankAccount(t, queries, "9200000030")

	// Inside the trailing month (a few days ago) — should count as paid.
	seedTransactionDated(t, queries, account.ID, 9201, time.Now().AddDate(0, 0, -5), "membership_fee", &paidMember)

	// Well outside the trailing month (2 months ago) — must NOT count, even
	// though it's the member's only payment ever, to make sure this is a
	// real window check and not just "has ever paid."
	seedTransactionDated(t, queries, account.ID, 9202, time.Now().AddDate(0, -2, 0), "membership_fee", &missingMember)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/stats", nil)
	PaymentStats(queries)(recorder, request)

	got := decodePaymentStats(t, recorder)
	if got.LiableMembers != 2 {
		t.Errorf("liable_members = %d, want 2", got.LiableMembers)
	}
	if got.PaidMembers != 1 {
		t.Errorf("paid_members = %d, want 1 (only the in-window transaction should count)", got.PaidMembers)
	}
	if got.PercentPaid == nil {
		t.Fatal("percent_paid = nil, want a value (liable_members > 0)")
	}
	if want := 50.0; *got.PercentPaid < want-0.01 || *got.PercentPaid > want+0.01 {
		t.Errorf("percent_paid = %v, want ~%v", *got.PercentPaid, want)
	}
}

func TestPaymentStats_MemberLeftExcluded(t *testing.T) {
	queries := dbtest.Tx(t)
	const memberNumber = int32(900203)

	feeStart := time.Now().AddDate(-5, 0, 0)
	feeStop := time.Now().AddDate(0, -1, -1) // already stopped as of today
	if err := queries.UpsertMember(context.Background(), db.UpsertMemberParams{
		MemberNumber: memberNumber,
		FeeStartDate: pgtype.Date{Time: feeStart, Valid: true},
		FeeStopDate:  pgtype.Date{Time: feeStop, Valid: true},
		Active:       false,
	}); err != nil {
		t.Fatalf("UpsertMember: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/stats", nil)
	PaymentStats(queries)(recorder, request)

	got := decodePaymentStats(t, recorder)
	if got.LiableMembers != 0 {
		t.Errorf("liable_members = %d, want 0 — member already left, shouldn't count as currently liable", got.LiableMembers)
	}
}

// TestPaymentStatsHistory checks the series shape (365 days, oldest first,
// ending today) and that each day uses its own trailing window: a payment
// 60 days ago counts on that day but not today.
func TestPaymentStatsHistory(t *testing.T) {
	queries := dbtest.Tx(t)

	feeStart := time.Now().AddDate(-5, 0, 0)
	memberNumber := int32(900301)
	seedMember(t, queries, memberNumber, &feeStart)
	account := seedBankAccount(t, queries, "9200000031")
	paidOn := time.Now().AddDate(0, 0, -60)
	seedTransactionDated(t, queries, account.ID, 9301, paidOn, "membership_fee", &memberNumber)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/stats/history", nil)
	PaymentStatsHistory(queries)(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got []paymentStats
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if len(got) != 365 {
		t.Fatalf("len = %d, want 365", len(got))
	}
	today := got[364]
	if today.WindowTo != time.Now().Format("2006-01-02") {
		t.Errorf("last window_to = %s, want today", today.WindowTo)
	}
	if today.LiableMembers != 1 || today.PaidMembers != 0 {
		t.Errorf("today: liable = %d, paid = %d, want 1, 0", today.LiableMembers, today.PaidMembers)
	}
	paidDay := got[364-60]
	if paidDay.WindowTo != paidOn.Format("2006-01-02") {
		t.Fatalf("entry 304 window_to = %s, want %s", paidDay.WindowTo, paidOn.Format("2006-01-02"))
	}
	if paidDay.PaidMembers != 1 || paidDay.PercentPaid == nil || *paidDay.PercentPaid != 100 {
		t.Errorf("payment day: paid = %d, percent = %v, want 1, 100", paidDay.PaidMembers, paidDay.PercentPaid)
	}
}

func TestPaymentStatsHistory_Range(t *testing.T) {
	queries := dbtest.Tx(t)

	tests := []struct {
		name       string
		query      string
		wantStatus int
		wantLength int
	}{
		{"explicit range", "?from=2026-01-01&to=2026-01-31", http.StatusOK, 31},
		{"single day", "?from=2026-03-01&to=2026-03-01", http.StatusOK, 1},
		{"only to", "?to=2026-03-01", http.StatusOK, 365},
		{"from after to", "?from=2026-02-01&to=2026-01-01", http.StatusBadRequest, 0},
		{"bad date", "?from=2026-13-01", http.StatusBadRequest, 0},
		{"six years", "?from=2020-01-01&to=2025-12-31", http.StatusOK, 2192},
		{"future to capped to today", "?from=" + time.Now().AddDate(0, 0, -9).Format("2006-01-02") + "&to=2999-01-01", http.StatusOK, 10},
		{"future from", "?from=2999-01-01", http.StatusBadRequest, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/payments/stats/history"+test.query, nil)
			PaymentStatsHistory(queries)(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, test.wantStatus, recorder.Body)
			}
			if test.wantStatus != http.StatusOK {
				return
			}
			var got []paymentStats
			if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding response: %v", err)
			}
			if len(got) != test.wantLength {
				t.Errorf("len = %d, want %d", len(got), test.wantLength)
			}
		})
	}
}

// TestPaymentStats_WeekendSlack: a payment booked one calendar month and 2
// days ago (e.g. last month's standing order, with this month's delayed by a
// weekend) still counts — the window has 3 days of slack beyond the month.
func TestPaymentStats_WeekendSlack(t *testing.T) {
	queries := dbtest.Tx(t)

	feeStart := time.Now().AddDate(-5, 0, 0)
	insideSlack := int32(900211)
	outsideSlack := int32(900212)
	seedMember(t, queries, insideSlack, &feeStart)
	seedMember(t, queries, outsideSlack, &feeStart)
	account := seedBankAccount(t, queries, "9200000032")
	seedTransactionDated(t, queries, account.ID, 9211, time.Now().AddDate(0, -1, -2), "membership_fee", &insideSlack)
	seedTransactionDated(t, queries, account.ID, 9212, time.Now().AddDate(0, -1, -8), "membership_fee", &outsideSlack) // -8 not -4: Go's AddDate overflows at month end where Postgres clamps

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/stats", nil)
	PaymentStats(queries)(recorder, request)

	got := decodePaymentStats(t, recorder)
	if got.LiableMembers != 2 || got.PaidMembers != 1 {
		t.Errorf("liable = %d, paid = %d, want 2, 1 (only the payment within the 3-day slack counts)", got.LiableMembers, got.PaidMembers)
	}
}
