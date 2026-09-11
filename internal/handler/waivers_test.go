package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
)

func TestWaivePayment_Success(t *testing.T) {
	queries := dbtest.Tx(t)
	const memberNumber = int32(910001)
	feeStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMember(t, queries, memberNumber, &feeStart)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/payments/x/waive",
		bytes.NewBufferString(`{"year":2022,"month":6,"reason":"one-off miss, too old to chase"}`))
	request.SetPathValue("member_number", itoa(memberNumber))
	WaivePayment(queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got paymentWaiverResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.MemberNumber != memberNumber || got.Year != 2022 || got.Month != 6 {
		t.Errorf("got %+v, want member=%d year=2022 month=6", got, memberNumber)
	}
	if got.Reason != "one-off miss, too old to chase" {
		t.Errorf("Reason = %q", got.Reason)
	}

	// The waived month must no longer appear as missing for that member.
	missingRecorder := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodGet, "/payments/2022/6/missing", nil)
	missingRequest.SetPathValue("year", "2022")
	missingRequest.SetPathValue("month", "6")
	MissingPayments(queries)(missingRecorder, missingRequest)
	members := missingPaymentMembers(t, missingRecorder)
	if containsMember(members, memberNumber) {
		t.Errorf("member %d still listed as missing 2022-06 after waiving it: %+v", memberNumber, members)
	}
}

func TestWaivePayment_IdempotentKeepsFirstReason(t *testing.T) {
	queries := dbtest.Tx(t)
	const memberNumber = int32(910002)
	feeStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMember(t, queries, memberNumber, &feeStart)

	waive := func(reason string) paymentWaiverResponse {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/payments/x/waive",
			bytes.NewBufferString(`{"year":2022,"month":6,"reason":"`+reason+`"}`))
		request.SetPathValue("member_number", itoa(memberNumber))
		WaivePayment(queries)(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
		}
		var got paymentWaiverResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		return got
	}

	first := waive("first reason")
	second := waive("second reason")
	if second.Reason != first.Reason {
		t.Errorf("second call's reason = %q, want unchanged %q (already-waived month is a no-op)", second.Reason, first.Reason)
	}

	waivers, err := queries.ListWaiversForMember(context.Background(), memberNumber)
	if err != nil {
		t.Fatalf("ListWaiversForMember: %v", err)
	}
	if len(waivers) != 1 {
		t.Errorf("len(waivers) = %d, want 1 (repeat waive must not duplicate)", len(waivers))
	}
}

func TestWaivePayment_AlreadyCoveredByPaymentIsRejected(t *testing.T) {
	queries := dbtest.Tx(t)
	const memberNumber = int32(910003)
	feeStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMember(t, queries, memberNumber, &feeStart)

	account := seedBankAccount(t, queries, "9200000020")
	member := memberNumber
	pt := seedTransaction(t, queries, account.ID, 9101, "500.00", "membership_fee", "incoming", &member)
	if _, err := queries.InsertCoverageRow(context.Background(), db.InsertCoverageRowParams{
		ProcessedTransactionID: pt.ID,
		MemberNumber:           memberNumber,
		CoversYear:             2022,
		CoversMonth:            6,
	}); err != nil {
		t.Fatalf("InsertCoverageRow: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/payments/x/waive",
		bytes.NewBufferString(`{"year":2022,"month":6,"reason":"shouldn't be allowed"}`))
	request.SetPathValue("member_number", itoa(memberNumber))
	WaivePayment(queries)(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (month already covered by a payment): body = %s", recorder.Code, recorder.Body)
	}
}

func TestWaivePayment_Validation(t *testing.T) {
	feeStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		memberNumber int32
		body         string
		seedIt       bool
	}{
		{"missing reason", 910010, `{"year":2022,"month":6}`, true},
		{"blank reason", 910011, `{"year":2022,"month":6,"reason":"   "}`, true},
		{"invalid month", 910012, `{"year":2022,"month":13,"reason":"x"}`, true},
		{"invalid year", 910013, `{"year":1999,"month":6,"reason":"x"}`, true},
		{"unknown member", 910014, `{"year":2022,"month":6,"reason":"x"}`, false},
		{"invalid JSON", 910015, `not json`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queries := dbtest.Tx(t)
			if tt.seedIt {
				seedMember(t, queries, tt.memberNumber, &feeStart)
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/payments/x/waive", bytes.NewBufferString(tt.body))
			request.SetPathValue("member_number", itoa(tt.memberNumber))
			WaivePayment(queries)(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: body = %s", recorder.Code, recorder.Body)
			}
		})
	}
}

func TestWaivePayment_HasEverPaidUnaffected(t *testing.T) {
	queries := dbtest.Tx(t)
	const memberNumber = int32(910020)
	feeStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMember(t, queries, memberNumber, &feeStart)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/payments/x/waive",
		bytes.NewBufferString(`{"year":2020,"month":1,"reason":"never onboarded, too old to chase"}`))
	request.SetPathValue("member_number", itoa(memberNumber))
	WaivePayment(queries)(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}

	missingRecorder := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodGet, "/payments/2022/1/missing", nil)
	missingRequest.SetPathValue("year", "2022")
	missingRequest.SetPathValue("month", "1")
	MissingPayments(queries)(missingRecorder, missingRequest)
	members := missingPaymentMembers(t, missingRecorder)
	// Member still has other unwaived missing months, so still listed —
	// but has_ever_paid must stay false: waiving isn't paying.
	if got := findMember(t, members, memberNumber); got.HasEverPaid {
		t.Errorf("HasEverPaid = true, want false — a waived month is not a paid one")
	}
}

func TestUnwaivePayment(t *testing.T) {
	queries := dbtest.Tx(t)
	const memberNumber = int32(910030)
	feeStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMember(t, queries, memberNumber, &feeStart)

	waiveRecorder := httptest.NewRecorder()
	waiveRequest := httptest.NewRequest(http.MethodPost, "/payments/x/waive",
		bytes.NewBufferString(`{"year":2022,"month":6,"reason":"x"}`))
	waiveRequest.SetPathValue("member_number", itoa(memberNumber))
	WaivePayment(queries)(waiveRecorder, waiveRequest)
	if waiveRecorder.Code != http.StatusOK {
		t.Fatalf("waive status = %d, body = %s", waiveRecorder.Code, waiveRecorder.Body)
	}

	deleteRecorder := httptest.NewRecorder()
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/payments/x/waive/2022/6", nil)
	deleteRequest.SetPathValue("member_number", itoa(memberNumber))
	deleteRequest.SetPathValue("year", "2022")
	deleteRequest.SetPathValue("month", "6")
	UnwaivePayment(queries)(deleteRecorder, deleteRequest)
	if deleteRecorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", deleteRecorder.Code, deleteRecorder.Body)
	}

	// Waived month must reappear as missing after the waiver is undone.
	missingRecorder := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodGet, "/payments/2022/6/missing", nil)
	missingRequest.SetPathValue("year", "2022")
	missingRequest.SetPathValue("month", "6")
	MissingPayments(queries)(missingRecorder, missingRequest)
	members := missingPaymentMembers(t, missingRecorder)
	if !containsMember(members, memberNumber) {
		t.Errorf("member %d not listed as missing 2022-06 after undoing the waiver: %+v", memberNumber, members)
	}
}

func TestListWaivers_Empty(t *testing.T) {
	queries := dbtest.Tx(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/waivers", nil)
	ListWaivers(queries)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got []paymentWaiverResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0 for a fresh transaction: %+v", len(got), got)
	}
}

func TestListWaivers_ListsAcrossMembers(t *testing.T) {
	queries := dbtest.Tx(t)
	feeStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	const memberA = int32(910040)
	const memberB = int32(910041)
	seedMember(t, queries, memberA, &feeStart)
	seedMember(t, queries, memberB, &feeStart)

	for _, w := range []struct {
		member int32
		reason string
	}{
		{memberA, "member A's reason"},
		{memberB, "member B's reason"},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/payments/x/waive",
			bytes.NewBufferString(`{"year":2022,"month":6,"reason":"`+w.reason+`"}`))
		request.SetPathValue("member_number", itoa(w.member))
		WaivePayment(queries)(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("waive for member %d: status = %d, body = %s", w.member, recorder.Code, recorder.Body)
		}
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/payments/waivers", nil)
	ListWaivers(queries)(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got []paymentWaiverResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	byMember := make(map[int32]paymentWaiverResponse, len(got))
	for _, w := range got {
		byMember[w.MemberNumber] = w
	}
	if byMember[memberA].Reason != "member A's reason" {
		t.Errorf("member A's reason = %q", byMember[memberA].Reason)
	}
	if byMember[memberB].Reason != "member B's reason" {
		t.Errorf("member B's reason = %q", byMember[memberB].Reason)
	}
}

func TestUnwaivePayment_NotFound(t *testing.T) {
	queries := dbtest.Tx(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/payments/x/waive/2022/6", nil)
	request.SetPathValue("member_number", "910099")
	request.SetPathValue("year", "2022")
	request.SetPathValue("month", "6")
	UnwaivePayment(queries)(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: body = %s", recorder.Code, recorder.Body)
	}
}
