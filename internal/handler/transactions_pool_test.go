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
)

// AssignTransaction/UnassignTransaction open their own nested transactions on
// the concrete *pgxpool.Pool, so dbtest.Tx's single-rolled-back-transaction trick can't hand
// them an isolated tx — these use dbtest.Pool instead, cleaned up by
// truncation, not rollback.

func TestAssignTransaction_NotFound(t *testing.T) {
	pool := dbtest.Pool(t)

	body := strings.NewReader(`{"category":"other_income"}`)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/transactions/999999/assignment", body)
	request.SetPathValue("id", "999999")
	AssignTransaction(pool)(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusNotFound, recorder.Body)
	}
}

func TestAssignTransaction_InvalidCategory(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9300000001")
	pt := seedTransaction(t, queries, account.ID, 5001, "100.00", "other_income", "incoming", nil)

	body := strings.NewReader(`{"category":"not-a-real-category"}`)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/transactions/"+itoa64(pt.ID)+"/assignment", body)
	request.SetPathValue("id", itoa64(pt.ID))
	AssignTransaction(pool)(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
	}
}

func TestAssignTransaction_MemberDoesNotExist(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9300000002")
	pt := seedTransaction(t, queries, account.ID, 5002, "100.00", "other_income", "incoming", nil)

	body := strings.NewReader(`{"member_number":900501,"category":"membership_fee"}`)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/transactions/"+itoa64(pt.ID)+"/assignment", body)
	request.SetPathValue("id", itoa64(pt.ID))
	AssignTransaction(pool)(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
	}
}

func TestAssignTransaction_CategoryOnlyNoMember(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9300000003")
	pt := seedTransaction(t, queries, account.ID, 5003, "-40.00", "other_expense", "outgoing", nil)

	body := strings.NewReader(`{"category":"other_expense"}`)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/transactions/"+itoa64(pt.ID)+"/assignment", body)
	request.SetPathValue("id", itoa64(pt.ID))
	AssignTransaction(pool)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got transactionDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Category != "other_expense" {
		t.Errorf("Category = %q, want other_expense", got.Category)
	}
	if got.MemberNumber != nil {
		t.Errorf("MemberNumber = %v, want nil — category-only edit", got.MemberNumber)
	}
	if len(got.CoveredMonths) != 0 {
		t.Errorf("CoveredMonths = %v, want empty — no member given", got.CoveredMonths)
	}
}

func TestAssignTransaction_WithMemberDefaultsCoverageToMonthBeforeTransaction(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9300000004")
	pt := seedTransaction(t, queries, account.ID, 5004, "500.00", "other_income", "incoming", nil)
	seedMember(t, queries, 900502, nil)

	body := strings.NewReader(`{"member_number":900502,"category":"membership_fee"}`)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/transactions/"+itoa64(pt.ID)+"/assignment", body)
	request.SetPathValue("id", itoa64(pt.ID))
	AssignTransaction(pool)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got transactionDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.MemberNumber == nil || *got.MemberNumber != 900502 {
		t.Errorf("MemberNumber = %v, want 900502", got.MemberNumber)
	}
	if len(got.CoveredMonths) != 1 {
		t.Fatalf("CoveredMonths = %v, want exactly one entry (the month before the transaction's own)", got.CoveredMonths)
	}
	// Dues are paid a month in arrears — same convention processOne uses automatically.
	wantMonth := time.Now().AddDate(0, -1, 0)
	if got.CoveredMonths[0].Year != wantMonth.Year() || got.CoveredMonths[0].Month != int(wantMonth.Month()) {
		t.Errorf("CoveredMonths[0] = %+v, want {%d %d}", got.CoveredMonths[0], wantMonth.Year(), int(wantMonth.Month()))
	}
}

func TestAssignTransaction_CoverageConflict(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9300000005")
	seedMember(t, queries, 900503, nil)

	first := seedTransaction(t, queries, account.ID, 5005, "500.00", "other_income", "incoming", nil)
	second := seedTransaction(t, queries, account.ID, 5006, "500.00", "other_income", "incoming", nil)

	assign := func(pt db.ProcessedTransaction, covers string) *httptest.ResponseRecorder {
		body := strings.NewReader(`{"member_number":900503,"category":"membership_fee","covers":[` + covers + `]}`)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/transactions/"+itoa64(pt.ID)+"/assignment", body)
		request.SetPathValue("id", itoa64(pt.ID))
		AssignTransaction(pool)(recorder, request)
		return recorder
	}

	const month = `{"year":2026,"month":6}`
	if r := assign(first, month); r.Code != http.StatusOK {
		t.Fatalf("first assignment: status = %d, body = %s", r.Code, r.Body)
	}
	r := assign(second, month)
	if r.Code != http.StatusConflict {
		t.Errorf("second assignment (same month, different transaction): status = %d, want %d, body = %s", r.Code, http.StatusConflict, r.Body)
	}
}

func TestUnassignTransaction_NotFound(t *testing.T) {
	pool := dbtest.Pool(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/transactions/999999/assignment", nil)
	request.SetPathValue("id", "999999")
	UnassignTransaction(pool)(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusNotFound, recorder.Body)
	}
}

func TestUnassignTransaction_ClearsMemberAndCoverageResetsCategory(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9300000006")
	seedMember(t, queries, 900504, nil)
	pt := seedTransaction(t, queries, account.ID, 5007, "500.00", "membership_fee", "incoming", nil)

	assignBody := strings.NewReader(`{"member_number":900504,"category":"membership_fee"}`)
	assignRecorder := httptest.NewRecorder()
	assignRequest := httptest.NewRequest(http.MethodPut, "/transactions/"+itoa64(pt.ID)+"/assignment", assignBody)
	assignRequest.SetPathValue("id", itoa64(pt.ID))
	AssignTransaction(pool)(assignRecorder, assignRequest)
	if assignRecorder.Code != http.StatusOK {
		t.Fatalf("setup assignment: status = %d, body = %s", assignRecorder.Code, assignRecorder.Body)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/transactions/"+itoa64(pt.ID)+"/assignment", nil)
	request.SetPathValue("id", itoa64(pt.ID))
	UnassignTransaction(pool)(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusNoContent, recorder.Body)
	}

	detail, err := queries.GetTransactionDetail(context.Background(), pt.ID)
	if err != nil {
		t.Fatalf("GetTransactionDetail: %v", err)
	}
	if detail.MemberNumber != nil {
		t.Errorf("MemberNumber = %v, want nil after unassign", detail.MemberNumber)
	}
	if detail.Category != "other_income" {
		t.Errorf("Category = %q, want other_income (incoming default) after unassign", detail.Category)
	}
	coverage, err := queries.ListCoverageForTransaction(context.Background(), pt.ID)
	if err != nil {
		t.Fatalf("ListCoverageForTransaction: %v", err)
	}
	if len(coverage) != 0 {
		t.Errorf("coverage = %v, want empty after unassign", coverage)
	}
}
