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

	var result Result
	for _, rt := range rows {
		if err := processOne(requestContext, pool, queries, rt); err != nil {
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
func processOne(requestContext context.Context, pool *pgxpool.Pool, queries *db.Queries, rt db.RawTransaction) error {
	direction := "incoming"
	if strings.HasPrefix(rt.Amount, "-") {
		direction = "outgoing"
	}

	var memberNumber *int32
	var matchedBy *string
	category := ""

	if rt.VariableSymbol != nil && *rt.VariableSymbol != "" {
		member, err := queries.FindMemberByVariableSymbol(requestContext, db.FindMemberByVariableSymbolParams{
			VariableSymbol:  *rt.VariableSymbol,
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
