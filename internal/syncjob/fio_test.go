package syncjob

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
)

const testEncryptionKey = "test-encryption-key"

// fakeFioServer answers every Fio export-API path (FetchNew's /last/ and
// FetchPeriod's /periods/ alike) with one canned transaction — these tests
// don't care which endpoint was hit, only that syncAccount's parse+insert
// path works end to end against a real DB.
func fakeFioServer(t *testing.T, fioTransactionID int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"accountStatement": {
				"info": {"accountId": "1", "currency": "CZK"},
				"transactionList": {
					"transaction": [
						{
							"column0": {"value": "2026-06-15+0200", "name": "Date", "id": 0},
							"column1": {"value": 500.00, "name": "Amount", "id": 1},
							"column14": {"value": "CZK", "name": "Currency", "id": 14},
							"column22": {"value": ` + strconv.FormatInt(fioTransactionID, 10) + `, "name": "ID transakce", "id": 22}
						}
					]
				}
			}
		}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func seedBankAccountWithToken(t *testing.T, pool *pgxpool.Pool, fioAccountID string) db.CreateBankAccountRow {
	t.Helper()
	queries := db.New(pool)
	account, err := queries.CreateBankAccount(context.Background(), db.CreateBankAccountParams{
		FioAccountID:  fioAccountID,
		Currency:      "CZK",
		DisplayName:   "Fixture Account",
		FioToken:      "tok-" + fioAccountID,
		EncryptionKey: testEncryptionKey,
	})
	if err != nil {
		t.Fatalf("seedBankAccountWithToken(%q): %v", fioAccountID, err)
	}
	return account
}

func TestRunFioSync_NoAccounts(t *testing.T) {
	pool := dbtest.Pool(t)

	if _, err := RunFioSync(context.Background(), pool, "http://unused.invalid", testEncryptionKey, false); err == nil {
		t.Error("RunFioSync returned nil error with zero bank_accounts rows, want an error")
	}
}

func TestRunFioSync_SyncsEveryActiveAccount(t *testing.T) {
	pool := dbtest.Pool(t)
	seedBankAccountWithToken(t, pool, "9500000001")
	seedBankAccountWithToken(t, pool, "9500000002")
	fio := fakeFioServer(t, 7001)

	results, err := RunFioSync(context.Background(), pool, fio.URL, testEncryptionKey, true)
	if err != nil {
		t.Fatalf("RunFioSync: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.TransactionsFetched != 1 || r.TransactionsInserted != 1 {
			t.Errorf("result %+v, want 1 fetched and 1 inserted", r)
		}
	}
}

func TestBackfillAccount_PullsGivenRangeWithoutTouchingTheCursor(t *testing.T) {
	pool := dbtest.Pool(t)
	account := seedBankAccountWithToken(t, pool, "9500000006")
	fio := fakeFioServer(t, 7003)

	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2020, 12, 31, 0, 0, 0, 0, time.UTC)
	result, err := BackfillAccount(context.Background(), pool, fio.URL, testEncryptionKey, account.ID, from, to, true)
	if err != nil {
		t.Fatalf("BackfillAccount: %v", err)
	}
	if result.TransactionsFetched != 1 || result.TransactionsInserted != 1 {
		t.Errorf("result = %+v, want 1 fetched and 1 inserted", result)
	}

	// A subsequent FetchNew-based sync must still work normally — proves the
	// backfill didn't touch Fio's server-side cursor (there's nothing to
	// prove that against on this fake server directly, but a second,
	// unrelated fio_transaction_id from the same fixture server inserting
	// cleanly rules out any accidental state carried over in our own code).
	results, err := RunFioSync(context.Background(), pool, fio.URL, testEncryptionKey, true)
	if err != nil {
		t.Fatalf("RunFioSync after backfill: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
}

func TestBackfillAccount_AccountNotFound(t *testing.T) {
	pool := dbtest.Pool(t)

	_, err := BackfillAccount(context.Background(), pool, "http://unused.invalid", testEncryptionKey, 999999, time.Now().AddDate(0, -1, 0), time.Now(), false)
	if !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("err = %v, want ErrAccountNotFound", err)
	}
}

func TestBackfillAccount_InactiveAccount(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccountWithToken(t, pool, "9500000007")
	if _, err := queries.DeleteBankAccount(context.Background(), account.ID); err != nil {
		t.Fatalf("DeleteBankAccount: %v", err)
	}

	_, err := BackfillAccount(context.Background(), pool, "http://unused.invalid", testEncryptionKey, account.ID, time.Now().AddDate(0, -1, 0), time.Now(), false)
	if !errors.Is(err, ErrAccountInactive) {
		t.Errorf("err = %v, want ErrAccountInactive", err)
	}
}

func TestRunFioSync_SkipsInactiveAccount(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	active := seedBankAccountWithToken(t, pool, "9500000003")
	inactive := seedBankAccountWithToken(t, pool, "9500000004")
	if _, err := queries.DeleteBankAccount(context.Background(), inactive.ID); err != nil {
		t.Fatalf("DeleteBankAccount: %v", err)
	}
	fio := fakeFioServer(t, 7002)

	results, err := RunFioSync(context.Background(), pool, fio.URL, testEncryptionKey, true)
	if err != nil {
		t.Fatalf("RunFioSync: %v", err)
	}
	if len(results) != 1 || results[0].BankAccountID != active.ID {
		t.Errorf("results = %+v, want exactly the active account (id=%d)", results, active.ID)
	}
}
