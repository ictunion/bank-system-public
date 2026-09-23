// Package processing turns raw_transactions into processed_transactions
// (member match + category) and, for the default membership_fee case,
// payment_coverage — the derived-data step on top of the sync jobs. Runs
// after both the Orca and Fio syncs (see cmd/server/main.go) so
// member/payment-identifier and transaction data are both fresh before
// matching.
package processing

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/db"
)

// Result summarizes one processing run.
type Result struct {
	TransactionsProcessed int
	TransactionsFailed    int
}

// Run processes every raw_transaction that doesn't have a processed_transactions
// row yet — idempotent by construction (ListUnprocessedTransactions only
// returns unprocessed rows), so it's safe to call on every daily cycle without
// double-processing already-handled transactions.
func Run(requestContext context.Context, pool *pgxpool.Pool) (Result, error) {
	queries := db.New(pool)

	rows, err := queries.ListUnprocessedTransactions(requestContext)
	if err != nil {
		return Result{}, fmt.Errorf("listing unprocessed transactions: %w", err)
	}

	// Fetched once per run, not per transaction — a transfer between our own
	// accounts is recognized by its counterparty being one of these, see
	// processOne. Soft-deleted accounts excluded (ListActiveBankAccountNumbers).
	ourAccountNumbers, err := queries.ListActiveBankAccountNumbers(requestContext)
	if err != nil {
		return Result{}, fmt.Errorf("listing active bank account numbers: %w", err)
	}
	ourAccounts := make(map[string]bool, len(ourAccountNumbers))
	for _, number := range ourAccountNumbers {
		ourAccounts[number] = true
	}

	var result Result
	for _, rt := range rows {
		if err := processOne(requestContext, pool, queries, rt, ourAccounts); err != nil {
			result.TransactionsFailed++
			log.Printf("processing: raw_transaction_id=%d: %v", rt.ID, err)
			continue
		}
		result.TransactionsProcessed++
	}
	return result, nil
}

// processOne categorizes and matches one raw transaction, then writes the
// processed_transactions row (and, for a matched membership fee, the default
// payment_coverage row for the transaction's own month) in one DB transaction
// so the two inserts can't split — a payment_coverage failure after a
// committed processed_transactions row would silently and permanently drop
// that month's coverage, since the row would no longer show up as
// unprocessed on the next run.
func processOne(requestContext context.Context, pool *pgxpool.Pool, queries *db.Queries, rt db.RawTransaction, ourAccounts map[string]bool) error {
	direction := "incoming"
	if strings.HasPrefix(rt.Amount, "-") {
		direction = "outgoing"
	}

	var memberNumber *int32
	var matchedBy *string
	category := ""

	// Transfer between our own bank_accounts: checked first, ahead of
	// variable_symbol/salary, since it's the most specific signal available
	// and shouldn't be shadowed by a coincidental VS collision. Matched on
	// counter_account_number alone (no bank-code check — this app only syncs
	// Fio accounts, so a mismatch here is unlikely; revisit if it misfires,
	// same as the "mzda" salary heuristic below).
	if rt.CounterAccountNumber != nil && ourAccounts[*rt.CounterAccountNumber] {
		category = "internal_transfer"
	}

	if category == "" && rt.VariableSymbol != nil && *rt.VariableSymbol != "" {
		// Fio sometimes zero-pads the variable symbol (e.g. "00000123" for VS
		// 123), but member_payment_identifiers stores it unpadded (Orca sync
		// seeds it from member_number, an int, with no padding) — strip
		// leading zeroes before matching, not at insert time, so
		// raw_transactions stays an untouched mirror of Fio's response.
		normalizedVariableSymbol := strings.TrimLeft(*rt.VariableSymbol, "0")
		if normalizedVariableSymbol == "" {
			normalizedVariableSymbol = "0"
		}
		member, err := queries.FindMemberByVariableSymbol(requestContext, db.FindMemberByVariableSymbolParams{
			VariableSymbol:  normalizedVariableSymbol,
			TransactionDate: rt.TransactionDate,
		})
		switch {
		case err == nil:
			memberNumber = &member
			by := "variable_symbol"
			matchedBy = &by
			category = "membership_fee"
		case errors.Is(err, pgx.ErrNoRows):
			// no member currently claims this variable symbol — falls through
			// to the salary/other_income/other_expense categorization below.
		default:
			return fmt.Errorf("matching variable_symbol %q: %w", *rt.VariableSymbol, err)
		}
	}

	// Salary detection: substring match on "mzda" (Czech for wage/salary) in
	// either free-text field payroll transactions were observed to carry it
	// in — not error-proof, but a good-enough first version. Only checked when not
	// already matched to a member above.
	if category == "" && (containsMzda(rt.Comment) || containsMzda(rt.UserIdentification)) {
		category = "salary"
	}

	if category == "" {
		if direction == "incoming" {
			category = "other_income"
		} else {
			category = "other_expense"
		}
	}

	tx, err := pool.Begin(requestContext)
	if err != nil {
		return err
	}
	defer tx.Rollback(requestContext)
	txQueries := db.New(tx)

	pt, err := txQueries.CreateProcessedTransaction(requestContext, db.CreateProcessedTransactionParams{
		RawTransactionID: rt.ID,
		MemberNumber:     memberNumber,
		Category:         category,
		Direction:        direction,
		MatchedBy:        matchedBy,
	})
	if err != nil {
		return fmt.Errorf("inserting processed_transactions: %w", err)
	}

	if category == "membership_fee" && memberNumber != nil {
		// Dues for month M are paid during month M+1 — a transaction received this month covers
		// *last* month's fee by default, not its own month. AssignTransaction's
		// manual default follows the same convention; either can be overridden
		// explicitly (there, via `covers` — not possible here, since automatic
		// matching has no other signal to go on).
		coveredMonth := rt.TransactionDate.AddDate(0, -1, 0)
		rows, err := txQueries.CreatePaymentCoverage(requestContext, db.CreatePaymentCoverageParams{
			ProcessedTransactionID: pt.ID,
			MemberNumber:           *memberNumber,
			CoversYear:             int32(coveredMonth.Year()),
			CoversMonth:            int16(coveredMonth.Month()),
		})
		if err != nil {
			return fmt.Errorf("inserting payment_coverage: %w", err)
		}
		if rows == 0 {
			// member_number/covers_year/covers_month already covered by a
			// different transaction — e.g. two payments in the same month, or a
			// catch-up payment for a month processOne has no way to know is the
			// actually-missing one (it always defaults to "last month," see
			// above). Not an error: this transaction still commits, categorized
			// correctly, just without a payment_coverage row of its own until an
			// admin manually re-points it at a different month (PUT
			// /transactions/{id}/assignment with an explicit `covers`). Logged
			// because — unlike that manual endpoint, which 409s — this path has
			// no caller to report it to otherwise.
			log.Printf("processing: raw_transaction_id=%d: member_number=%d already has payment_coverage for %d-%02d, no coverage row created for this transaction",
				rt.ID, *memberNumber, coveredMonth.Year(), int(coveredMonth.Month()))
		}
	}

	return tx.Commit(requestContext)
}

func containsMzda(field *string) bool {
	return field != nil && strings.Contains(strings.ToLower(*field), "mzda")
}

// ReclassifyResult summarizes one ReclassifyInternalTransfers run.
type ReclassifyResult struct {
	Reclassified int
	Failed       int
}

// ReclassifyInternalTransfers re-checks already-processed transactions
// against the *current* bank_accounts roster and flips any that are now
// recognizable as a transfer between our own accounts to
// category=internal_transfer. Unlike Run, this deliberately revisits rows
// that already have a processed_transactions row — internal-transfer
// detection in processOne only ever sees the roster as of the moment a
// transaction was first processed (see ListActiveBankAccountNumbers there),
// so a transaction synced/processed before a second (or newly taken-over)
// bank account was registered here stays miscategorized forever otherwise,
// even after the account is added and Run is called again. Skips
// matched_by='manual' rows (never override an admin's explicit decision) and
// anything already internal_transfer, so repeat calls are safe/idempotent —
// only ever touching what's still wrong.
func ReclassifyInternalTransfers(requestContext context.Context, pool *pgxpool.Pool) (ReclassifyResult, error) {
	queries := db.New(pool)

	ourAccountNumbers, err := queries.ListActiveBankAccountNumbers(requestContext)
	if err != nil {
		return ReclassifyResult{}, fmt.Errorf("listing active bank account numbers: %w", err)
	}

	candidateIDs, err := queries.ListInternalTransferCandidates(requestContext, ourAccountNumbers)
	if err != nil {
		return ReclassifyResult{}, fmt.Errorf("listing internal transfer candidates: %w", err)
	}

	var result ReclassifyResult
	for _, id := range candidateIDs {
		if err := reclassifyOne(requestContext, pool, id); err != nil {
			result.Failed++
			log.Printf("reclassify internal transfers: processed_transaction_id=%d: %v", id, err)
			continue
		}
		result.Reclassified++
	}
	return result, nil
}

func reclassifyOne(requestContext context.Context, pool *pgxpool.Pool, processedTransactionID int64) error {
	tx, err := pool.Begin(requestContext)
	if err != nil {
		return err
	}
	defer tx.Rollback(requestContext)
	txQueries := db.New(tx)

	if err := txQueries.DeleteCoverageForTransaction(requestContext, processedTransactionID); err != nil {
		return fmt.Errorf("clearing coverage: %w", err)
	}
	if _, err := txQueries.UnassignTransaction(requestContext, db.UnassignTransactionParams{
		ID:       processedTransactionID,
		Category: "internal_transfer",
	}); err != nil {
		return fmt.Errorf("updating category: %w", err)
	}

	return tx.Commit(requestContext)
}
