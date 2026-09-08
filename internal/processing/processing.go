// Package processing turns raw_transactions into processed_transactions
// (member match + category) and, for the default membership_fee case,
// payment_coverage — the derived-data step described in db-design.md
// "processed_transactions" and logic-design.md. Runs after both the Orca
// and Fio syncs (see cmd/server/main.go) so member/payment-identifier and
// transaction data are both fresh before matching.
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
func Run(ctx context.Context, pool *pgxpool.Pool) (Result, error) {
	queries := db.New(pool)

	rows, err := queries.ListUnprocessedTransactions(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("listing unprocessed transactions: %w", err)
	}

	var result Result
	for _, rt := range rows {
		if err := processOne(ctx, pool, queries, rt); err != nil {
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
func processOne(ctx context.Context, pool *pgxpool.Pool, queries *db.Queries, rt db.RawTransaction) error {
	direction := "incoming"
	if strings.HasPrefix(rt.Amount, "-") {
		direction = "outgoing"
	}

	var memberNumber *int32
	var matchedBy *string
	category := ""

	if rt.VariableSymbol != nil && *rt.VariableSymbol != "" {
		member, err := queries.FindMemberByVariableSymbol(ctx, db.FindMemberByVariableSymbolParams{
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
	// in — not error-proof, but a good-enough first version (see
	// logic-design.md "Transaction Processing"). Only checked when not
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

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	txQueries := db.New(tx)

	pt, err := txQueries.CreateProcessedTransaction(ctx, db.CreateProcessedTransactionParams{
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
		if err := txQueries.CreatePaymentCoverage(ctx, db.CreatePaymentCoverageParams{
			ProcessedTransactionID: pt.ID,
			MemberNumber:           *memberNumber,
			CoversYear:             int32(rt.TransactionDate.Year()),
			CoversMonth:            int16(rt.TransactionDate.Month()),
		}); err != nil {
			return fmt.Errorf("inserting payment_coverage: %w", err)
		}
	}

	return tx.Commit(ctx)
}

func containsMzda(field *string) bool {
	return field != nil && strings.Contains(strings.ToLower(*field), "mzda")
}
