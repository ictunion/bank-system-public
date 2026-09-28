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
