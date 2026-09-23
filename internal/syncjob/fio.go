// Package syncjob orchestrates syncing external data (Fio Bank transactions,
// later Orca members) into Postgres. Callable directly (server startup) or from
// a scheduler (daily 3am run) — it has no knowledge of what triggers it.
package syncjob

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/fio"
)

// Sentinel errors from SyncOneAccount — distinct from a Fio-API/DB failure so
// handler.TriggerFioSync can map them to 404/400 instead of a generic 502.
var (
	ErrAccountNotFound = errors.New("bank account not found")
	ErrAccountInactive = errors.New("bank account is deleted")
	ErrNoToken         = errors.New("bank account has no fio token configured")
)

// Result summarizes one bank account's sync attempt.
type Result struct {
	BankAccountID         int32
	TransactionsFetched   int
	TransactionsInserted  int
}

// RunFioSync fetches new transactions for every active bank account and
// upserts them into raw_transactions, recording each attempt in
// sync_fio_runs. Each account carries its own encrypted Fio token (see
// queries.sql), decrypted here with encryptionKey (BANK_TOKEN_ENCRYPTION_KEY)
// and used to build a client scoped to just that account — Fio's cursor state
// lives server-side per token, not per request (see internal/fio.NewClient).
// ListBankAccountsWithToken returns soft-deleted accounts too (so the admin
// UI can still show them); skipping IsActive == false here, rather than
// filtering in SQL, keeps "should this account sync" as sync-job business
// logic instead of a storage-layer concern. An active account with no token
// yet (not backfilled after the token moved from env var to DB) is skipped
// too, not failed.
func RunFioSync(requestContext context.Context, pool *pgxpool.Pool, fioAPIURL, encryptionKey string, debug bool) ([]Result, error) {
	queries := db.New(pool)

	accounts, err := queries.ListBankAccountsWithToken(requestContext, encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("listing bank accounts: %w", err)
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("no bank_accounts row exists yet — insert one before running the sync")
	}

	var results []Result
	for _, account := range accounts {
		if !account.IsActive {
			log.Printf("fio sync: bank_account_id=%d is deleted, skipping", account.ID)
			continue
		}
		if account.FioToken == "" {
			log.Printf("fio sync: bank_account_id=%d has no token configured, skipping", account.ID)
			continue
		}
		client := fio.NewClient(fioAPIURL, account.FioToken, debug)
		result, err := syncAccount(requestContext, pool, queries, client, account.ID)
		if err != nil {
			log.Printf("fio sync: bank_account_id=%d failed: %v", account.ID, err)
			continue
		}
		results = append(results, result)
	}
	return results, nil
}

// SyncOneAccount runs the same sync as one RunFioSync loop iteration, but for
// a single account on demand — the admin "sync now" button (see
// handler.TriggerFioSync), on top of the scheduled daily RunFioSync. Callers
// are responsible for checking config.Debug before calling this (local dev
// has no real Fio account/token) — unlike the scheduled job, this has no
// scheduler wrapper to do that check for it.
func SyncOneAccount(requestContext context.Context, pool *pgxpool.Pool, fioAPIURL, encryptionKey string, bankAccountID int32, debug bool) (Result, error) {
	queries := db.New(pool)

	account, err := queries.GetBankAccountWithToken(requestContext, db.GetBankAccountWithTokenParams{
		ID:            bankAccountID,
		EncryptionKey: encryptionKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, ErrAccountNotFound
	} else if err != nil {
		return Result{}, fmt.Errorf("loading bank account: %w", err)
	}
	if !account.IsActive {
		return Result{}, ErrAccountInactive
	}
	if account.FioToken == "" {
		return Result{}, ErrNoToken
	}

	client := fio.NewClient(fioAPIURL, account.FioToken, debug)
	return syncAccount(requestContext, pool, queries, client, account.ID)
}

// BackfillAccount pulls transactions for one bank account across an explicit
// date range via Fio's /periods/ endpoint — for history that predates an
// account's first cursor-based sync. Data older than 90 days needs a manual
// strong-authorization (SCA) unlock in Fio's own Internet Banking before this
// will return anything for it. Unlike SyncOneAccount's FetchNew path,
// /periods/ never touches Fio's server-side cursor, so this is safe to call
// before, after, or
// interleaved with the regular daily sync — raw_transactions' unique
// constraint on (bank_account_id, fio_transaction_id) makes any overlap
// between a backfill range and the cursor-based history idempotent, not a
// duplicate insert. No rewind-on-failure logic here (unlike syncAccount):
// there's no cursor to rewind, since /periods/ never advanced one.
func BackfillAccount(requestContext context.Context, pool *pgxpool.Pool, fioAPIURL, encryptionKey string, bankAccountID int32, from, to time.Time, debug bool) (result Result, err error) {
	queries := db.New(pool)

	account, err := queries.GetBankAccountWithToken(requestContext, db.GetBankAccountWithTokenParams{
		ID:            bankAccountID,
		EncryptionKey: encryptionKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, ErrAccountNotFound
	} else if err != nil {
		return Result{}, fmt.Errorf("loading bank account: %w", err)
	}
	if !account.IsActive {
		return Result{}, ErrAccountInactive
	}
	if account.FioToken == "" {
		return Result{}, ErrNoToken
	}

	client := fio.NewClient(fioAPIURL, account.FioToken, debug)

	run, err := queries.CreateSyncFioRun(requestContext, bankAccountID)
	if err != nil {
		return Result{}, fmt.Errorf("creating sync_fio_runs row: %w", err)
	}

	// See syncAccount's identical comment: a panic here would otherwise crash
	// the whole process and leave this row stuck at status='running' forever.
	var fetched, inserted int
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
			finishRun(requestContext, queries, run.ID, "failed", fetched, inserted, err)
			result = Result{}
			log.Printf("fio backfill: bank_account_id=%d: recovered from panic: %v", bankAccountID, r)
		}
	}()

	response, err := client.FetchPeriod(requestContext, from, to)
	if err != nil {
		finishRun(requestContext, queries, run.ID, "failed", 0, 0, err)
		return Result{}, fmt.Errorf("fetching from fio: %w", err)
	}

	txs, err := response.Transactions()
	if err != nil {
		finishRun(requestContext, queries, run.ID, "failed", 0, 0, err)
		return Result{}, fmt.Errorf("parsing fio response: %w", err)
	}
	fetched = len(txs)

	if len(txs) == 0 {
		finishRun(requestContext, queries, run.ID, "success", 0, 0, nil)
		return Result{BankAccountID: bankAccountID}, nil
	}

	inserted, err = insertTransactions(requestContext, pool, bankAccountID, txs)
	if err != nil {
		finishRun(requestContext, queries, run.ID, "failed", len(txs), 0, err)
		return Result{}, fmt.Errorf("inserting raw_transactions: %w", err)
	}

	finishRun(requestContext, queries, run.ID, "success", len(txs), inserted, nil)
	return Result{
		BankAccountID:        bankAccountID,
		TransactionsFetched:  len(txs),
		TransactionsInserted: inserted,
	}, nil
}

func syncAccount(requestContext context.Context, pool *pgxpool.Pool, queries *db.Queries, client *fio.FioClient, bankAccountID int32) (result Result, err error) {
	run, err := queries.CreateSyncFioRun(requestContext, bankAccountID)
	if err != nil {
		return Result{}, fmt.Errorf("creating sync_fio_runs row: %w", err)
	}

	// A panic anywhere below would otherwise crash the whole process (an
	// unrecovered panic kills the program, not just this goroutine) and leave
	// this row stuck at status='running' forever — recover it into a normal
	// failed run instead.
	var fetched, inserted int
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
			finishRun(requestContext, queries, run.ID, "failed", fetched, inserted, err)
			result = Result{}
			log.Printf("fio sync: bank_account_id=%d: recovered from panic: %v", bankAccountID, r)
		}
	}()

	response, err := client.FetchNew(requestContext)
	if errors.Is(err, fio.ErrStrongAuthRequired) {
		// A brand-new token (or one whose cursor drifted stale for any other
		// reason) has no cursor established yet, so Fio serves full history
		// and trips the 90-day SCA rule on the very first FetchNew. Seed the
		// cursor to just inside the SCA-free window and retry once — no SCA
		// unlock needed for this, since <90 days old needs no extra auth.
		log.Printf("fio sync: bank_account_id=%d: fio cursor needs strong auth, seeding to 89 days ago and retrying", bankAccountID)
		if seedErr := client.SetLastDate(requestContext, time.Now().AddDate(0, 0, -89)); seedErr != nil {
			finishRun(requestContext, queries, run.ID, "failed", 0, 0, seedErr)
			return Result{}, fmt.Errorf("seeding fio cursor: %w", seedErr)
		}
		response, err = client.FetchNew(requestContext)
	}
	if err != nil {
		finishRun(requestContext, queries, run.ID, "failed", 0, 0, err)
		return Result{}, fmt.Errorf("fetching from fio: %w", err)
	}

	txs, err := response.Transactions()
	if err != nil {
		finishRun(requestContext, queries, run.ID, "failed", 0, 0, err)
		return Result{}, fmt.Errorf("parsing fio response: %w", err)
	}
	fetched = len(txs)

	if len(txs) == 0 {
		finishRun(requestContext, queries, run.ID, "success", 0, 0, nil)
		return Result{BankAccountID: bankAccountID}, nil
	}

	// Fio already advanced its server-side cursor past these transactions by the
	// time FetchNew returned. If our own insert fails below, rewind the cursor
	// back to the last transaction we know we have, so tomorrow's run re-fetches
	// this batch instead of silently losing it — raw_transactions' unique
	// constraint only guards against re-inserting rows we already received, not
	// rows we never got a second chance at.
	previousMaxID, err := queries.GetMaxFioTransactionID(requestContext, bankAccountID)
	if err != nil {
		finishRun(requestContext, queries, run.ID, "failed", len(txs), 0, err)
		return Result{}, fmt.Errorf("reading previous max fio_transaction_id: %w", err)
	}

	inserted, insertErr := insertTransactions(requestContext, pool, bankAccountID, txs)
	if insertErr != nil {
		if rewindErr := client.RewindTo(requestContext, previousMaxID); rewindErr != nil {
			log.Printf("fio sync: bank_account_id=%d: rewind after failed insert also failed: %v", bankAccountID, rewindErr)
		}
		finishRun(requestContext, queries, run.ID, "failed", len(txs), 0, insertErr)
		return Result{}, fmt.Errorf("inserting raw_transactions: %w", insertErr)
	}

	finishRun(requestContext, queries, run.ID, "success", len(txs), inserted, nil)
	return Result{
		BankAccountID:        bankAccountID,
		TransactionsFetched:  len(txs),
		TransactionsInserted: inserted,
	}, nil
}

// insertTransactions runs the whole batch in one DB transaction — a
// partially-applied batch never gets committed.
func insertTransactions(requestContext context.Context, pool *pgxpool.Pool, bankAccountID int32, txs []fio.Transaction) (int, error) {
	tx, err := pool.Begin(requestContext)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(requestContext)

	queries := db.New(tx)
	inserted := 0
	for _, t := range txs {
		rows, err := queries.InsertRawTransaction(requestContext, toInsertParams(bankAccountID, t))
		if err != nil {
			return 0, fmt.Errorf("fio_transaction_id=%d: %w", t.FioTransactionID, err)
		}
		inserted += int(rows)
	}

	if err := tx.Commit(requestContext); err != nil {
		return 0, err
	}
	return inserted, nil
}

func toInsertParams(bankAccountID int32, t fio.Transaction) db.InsertRawTransactionParams {
	return db.InsertRawTransactionParams{
		BankAccountID:        bankAccountID,
		FioTransactionID:     t.FioTransactionID,
		TransactionDate:      t.TransactionDate,
		Amount:               strconv.FormatFloat(t.Amount, 'f', 2, 64),
		Currency:             t.Currency,
		CounterAccountNumber: t.CounterAccountNumber,
		CounterAccountName:   t.CounterAccountName,
		CounterBankCode:      t.CounterBankCode,
		CounterBankName:      t.CounterBankName,
		Bic:                  t.BIC,
		VariableSymbol:       t.VariableSymbol,
		SpecificSymbol:       t.SpecificSymbol,
		ConstantSymbol:       t.ConstantSymbol,
		UserIdentification:   t.UserIdentification,
		MessageForRecipient:  t.MessageForRecipient,
		TransactionType:      t.TransactionType,
		Executor:             t.Executor,
		Specification:        t.Specification,
		Comment:              t.Comment,
		InstructionID:        t.InstructionID,
		RawPayload:           t.RawPayload,
	}
}

func finishRun(requestContext context.Context, queries *db.Queries, runID int64, status string, fetched, inserted int, runErr error) {
	var errorMessage *string
	if runErr != nil {
		message := runErr.Error()
		errorMessage = &message
	}
	fetched32 := int32(fetched)
	inserted32 := int32(inserted)
	if err := queries.FinishSyncFioRun(requestContext, db.FinishSyncFioRunParams{
		ID:                   runID,
		Status:               status,
		TransactionsFetched:  &fetched32,
		TransactionsInserted: &inserted32,
		ErrorMessage:         errorMessage,
	}); err != nil {
		log.Printf("fio sync: recording sync_fio_runs id=%d failed: %v", runID, err)
	}
}
