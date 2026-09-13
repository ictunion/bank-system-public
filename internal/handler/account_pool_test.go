package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
)

// TriggerFioSync opens its own nested transactions on the concrete
// *pgxpool.Pool (via internal/syncjob), so — like AssignTransaction/
// UnassignTransaction in transactions_pool_test.go — these use dbtest.Pool
// instead of dbtest.Tx.

// fakeFioServer returns an httptest.Server that answers any Fio export-API
// path with one canned transaction, so syncjob never needs a real Fio API
// call. FIO_API_URL points at it via the fioAPIURL param, not env — matching
// how internal/fio.NewClient takes its base URL as an argument, not a
// fallback default (see internal/fio/client.go).
func fakeFioServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"accountStatement": {
				"info": {"accountId": "9300000010", "currency": "CZK"},
				"transactionList": {
					"transaction": [
						{
							"column0": {"value": "2026-06-15+0200", "name": "Date", "id": 0},
							"column1": {"value": 500.00, "name": "Amount", "id": 1},
							"column14": {"value": "CZK", "name": "Currency", "id": 14},
							"column22": {"value": 88001, "name": "ID transakce", "id": 22}
						}
					]
				}
			}
		}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestTriggerFioSync_RefusedInDebugMode(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9300000007")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/account/"+itoa(account.ID)+"/sync", nil)
	request.SetPathValue("id", itoa(account.ID))
	TriggerFioSync(pool, "http://unused.invalid", testEncryptionKey, true)(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusConflict, recorder.Body)
	}
}

func TestTriggerFioSync_AccountNotFound(t *testing.T) {
	pool := dbtest.Pool(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/account/999999/sync", nil)
	request.SetPathValue("id", "999999")
	TriggerFioSync(pool, "http://unused.invalid", testEncryptionKey, false)(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusNotFound, recorder.Body)
	}
}

func TestTriggerFioSync_InactiveAccount(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9300000008")
	if _, err := queries.DeleteBankAccount(context.Background(), account.ID); err != nil {
		t.Fatalf("DeleteBankAccount: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/account/"+itoa(account.ID)+"/sync", nil)
	request.SetPathValue("id", itoa(account.ID))
	TriggerFioSync(pool, "http://unused.invalid", testEncryptionKey, false)(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
	}
}

func TestBackfillAccount_RefusedInDebugMode(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9300000011")

	body := strings.NewReader(`{"from":"2020-01-01","to":"2020-12-31"}`)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/account/"+itoa(account.ID)+"/backfill", body)
	request.SetPathValue("id", itoa(account.ID))
	BackfillAccount(pool, "http://unused.invalid", testEncryptionKey, true)(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusConflict, recorder.Body)
	}
}

func TestBackfillAccount_Validation(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9300000012")

	tests := []struct {
		name string
		body string
	}{
		{"malformed from", `{"from":"not-a-date","to":"2020-12-31"}`},
		{"malformed to", `{"from":"2020-01-01","to":"not-a-date"}`},
		{"to before from", `{"from":"2020-12-31","to":"2020-01-01"}`},
		{"to in the future", `{"from":"2020-01-01","to":"2099-01-01"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/account/"+itoa(account.ID)+"/backfill", strings.NewReader(tt.body))
			request.SetPathValue("id", itoa(account.ID))
			BackfillAccount(pool, "http://unused.invalid", testEncryptionKey, false)(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
			}
		})
	}
}

func TestBackfillAccount_AccountNotFound(t *testing.T) {
	pool := dbtest.Pool(t)

	body := strings.NewReader(`{"from":"2020-01-01","to":"2020-12-31"}`)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/account/999999/backfill", body)
	request.SetPathValue("id", "999999")
	BackfillAccount(pool, "http://unused.invalid", testEncryptionKey, false)(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d, body = %s", recorder.Code, http.StatusNotFound, recorder.Body)
	}
}

func TestBackfillAccount_SuccessRunsProcessingSynchronously(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9300000013")
	fio := fakeFioServer(t)

	body := strings.NewReader(`{"from":"2020-01-01","to":"2020-12-31"}`)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/account/"+itoa(account.ID)+"/backfill", body)
	request.SetPathValue("id", itoa(account.ID))
	BackfillAccount(pool, fio.URL, testEncryptionKey, false)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got syncResultResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.TransactionsFetched != 1 || got.TransactionsInserted != 1 {
		t.Errorf("got %+v, want 1 fetched and 1 inserted", got)
	}
	if got.TransactionsProcessed != 1 {
		t.Errorf("TransactionsProcessed = %d, want 1 — backfill must run matching synchronously", got.TransactionsProcessed)
	}
}

func TestTriggerFioSync_Success(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9300000009")
	fio := fakeFioServer(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/account/"+itoa(account.ID)+"/sync", nil)
	request.SetPathValue("id", itoa(account.ID))
	TriggerFioSync(pool, fio.URL, testEncryptionKey, false)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got syncResultResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.TransactionsFetched != 1 || got.TransactionsInserted != 1 {
		t.Errorf("got %+v, want 1 fetched and 1 inserted", got)
	}
	if got.TransactionsProcessed != 1 {
		t.Errorf("TransactionsProcessed = %d, want 1 — sync now must run matching synchronously, same as backfill", got.TransactionsProcessed)
	}

	// A synchronously-processed transaction no longer shows up as
	// unprocessed — confirms processing.Run actually ran, not just that the
	// response claimed it did.
	raw, err := queries.ListUnprocessedTransactions(context.Background())
	if err != nil {
		t.Fatalf("ListUnprocessedTransactions: %v", err)
	}
	for _, r := range raw {
		if r.FioTransactionID == 88001 {
			t.Errorf("raw_transactions: fio_transaction_id=88001 still unprocessed after sync")
		}
	}
}
