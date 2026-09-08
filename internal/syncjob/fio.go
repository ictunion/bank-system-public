// Package syncjob orchestrates syncing external data (Fio Bank transactions,
// later Orca members) into Postgres. Callable directly (server startup) or from
// a scheduler (daily 3am run) — it has no knowledge of what triggers it.
package syncjob

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/fio"
)

// fioSCAWindow is how far back Fio's classic API serves transactions without
// requiring strong customer authorization (SCA) in Fio's own Internet Banking.
const fioSCAWindow = 90 * 24 * time.Hour

// Result summarizes one bank account's sync attempt.
type Result struct {
	BankAccountID         int32
	TransactionsFetched   int
	TransactionsInserted  int
}

// RunFioSync fetches new transactions for every bank account and upserts them
// into raw_transactions, recording each attempt in sync_fio_runs. One client
// (one Fio token) covers one bank account; today there's exactly one
// bank_accounts row, but the loop already generalizes since the schema supports
// more (see db-design.md) — it just doesn't yet have anywhere to look up a
// second account's token.
func RunFioSync(ctx context.Context, pool *pgxpool.Pool, client *fio.FioClient, debug bool) ([]Result, error) {
	queries := db.New(pool)

	accounts, err := queries.ListBankAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing bank accounts: %w", err)
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("no bank_accounts row exists yet — insert one before running the sync")
	}

	var results []Result
	for _, account := range accounts {
		res, err := syncAccount(ctx, pool, queries, client, account.ID, debug)
		if err != nil {
			log.Printf("fio sync: bank_account_id=%d failed: %v", account.ID, err)
			continue
		}
		results = append(results, res)
	}
	return results, nil
}

func syncAccount(ctx context.Context, pool *pgxpool.Pool, queries *db.Queries, client *fio.FioClient, bankAccountID int32, debug bool) (Result, error) {
	run, err := queries.CreateSyncFioRun(ctx, bankAccountID)
	if err != nil {
		return Result{}, fmt.Errorf("creating sync_fio_runs row: %w", err)
	}

	// In debug/dev mode, fetch only the last 90 days via /periods/ instead of
	// the cursor-based /last/ — Fio requires strong authorization (SCA) in its
	// own Internet Banking to serve anything older, which isn't practical for
	// local dev. This never advances Fio's server-side cursor, so there's
	// nothing to rewind on a failed insert below, and production (non-debug)
	// keeps doing a full cursor-based sync.
	var resp *fio.TransactionsResponse
	if debug {
		now := time.Now()
		resp, err = client.FetchPeriod(ctx, now.Add(-fioSCAWindow+24*time.Hour), now)
	} else {
		resp, err = client.FetchNew(ctx)
	}
	if err != nil {
		finishRun(ctx, queries, run.ID, "failed", 0, 0, err)
		return Result{}, fmt.Errorf("fetching from fio: %w", err)
	}

	txs, err := resp.Transactions()
	if err != nil {
		finishRun(ctx, queries, run.ID, "failed", 0, 0, err)
		return Result{}, fmt.Errorf("parsing fio response: %w", err)
	}

	if len(txs) == 0 {
		finishRun(ctx, queries, run.ID, "success", 0, 0, nil)
		return Result{BankAccountID: bankAccountID}, nil
	}

	// Fio already advanced its server-side cursor past these transactions by the
	// time FetchNew returned. If our own insert fails below, rewind the cursor
	// back to the last transaction we know we have, so tomorrow's run re-fetches
	// this batch instead of silently losing it — raw_transactions' unique
	// constraint only guards against re-inserting rows we already received, not
	// rows we never got a second chance at. Not applicable to the debug/periods
	// path above since that never advanced any cursor.
	var previousMaxID int64
	if !debug {
		previousMaxID, err = queries.GetMaxFioTransactionID(ctx, bankAccountID)
		if err != nil {
			finishRun(ctx, queries, run.ID, "failed", len(txs), 0, err)
			return Result{}, fmt.Errorf("reading previous max fio_transaction_id: %w", err)
		}
	}

	inserted, insertErr := insertTransactions(ctx, pool, bankAccountID, txs)
	if insertErr != nil {
		if !debug {
			if rewindErr := client.RewindTo(ctx, previousMaxID); rewindErr != nil {
				log.Printf("fio sync: bank_account_id=%d: rewind after failed insert also failed: %v", bankAccountID, rewindErr)
			}
		}
		finishRun(ctx, queries, run.ID, "failed", len(txs), 0, insertErr)
		return Result{}, fmt.Errorf("inserting raw_transactions: %w", insertErr)
	}

	finishRun(ctx, queries, run.ID, "success", len(txs), inserted, nil)
	return Result{
		BankAccountID:        bankAccountID,
		TransactionsFetched:  len(txs),
		TransactionsInserted: inserted,
	}, nil
}

// insertTransactions runs the whole batch in one DB transaction, per
// db-design.md ("Wrap each run in a DB transaction") — a partially-applied
// batch never gets committed.
func insertTransactions(ctx context.Context, pool *pgxpool.Pool, bankAccountID int32, txs []fio.Transaction) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	queries := db.New(tx)
	inserted := 0
	for _, t := range txs {
		rows, err := queries.InsertRawTransaction(ctx, toInsertParams(bankAccountID, t))
		if err != nil {
			return 0, fmt.Errorf("fio_transaction_id=%d: %w", t.FioTransactionID, err)
		}
		inserted += int(rows)
	}

	if err := tx.Commit(ctx); err != nil {
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

func finishRun(ctx context.Context, queries *db.Queries, runID int64, status string, fetched, inserted int, runErr error) {
	var errMsg *string
	if runErr != nil {
		msg := runErr.Error()
		errMsg = &msg
	}
	fetched32 := int32(fetched)
	inserted32 := int32(inserted)
	if err := queries.FinishSyncFioRun(ctx, db.FinishSyncFioRunParams{
		ID:                   runID,
		Status:               status,
		TransactionsFetched:  &fetched32,
		TransactionsInserted: &inserted32,
		ErrorMessage:         errMsg,
	}); err != nil {
		log.Printf("fio sync: recording sync_fio_runs id=%d failed: %v", runID, err)
	}
}
