// Package processing turns raw_transactions into processed_transactions
// (member match + category) and, for the default membership_fee case,
// payment_coverage — the derived-data step on top of the sync jobs.
// Categorization walks the enabled categorization_rules in priority order,
// first match wins, with a direction-based fallback when none matches. Runs
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

	// Rules and our own account numbers are both fetched once per run, not
	// per transaction. Soft-deleted accounts excluded (ListActiveBankAccountNumbers).
	rules, err := queries.ListEnabledCategorizationRules(requestContext)
	if err != nil {
		return Result{}, fmt.Errorf("listing categorization rules: %w", err)
	}
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
		if err := processOne(requestContext, pool, queries, rt, rules, ourAccounts); err != nil {
			result.TransactionsFailed++
			log.Printf("processing: raw_transaction_id=%d: %v", rt.ID, err)
			continue
		}
		result.TransactionsProcessed++
	}
	return result, nil
}

// outcome is what categorize decided for one transaction. ruleID is nil
// when no rule matched and the direction fallback applied; memberNumber and
// matchedBy are only set by the member_variable_symbol rule.
type outcome struct {
	category     string
	ruleID       *int64
	memberNumber *int32
	matchedBy    *string
}

func transactionDirection(amount string) string {
	if strings.HasPrefix(amount, "-") {
		return "outgoing"
	}
	return "incoming"
}

// FallbackCategory is the category for a transaction no rule matched —
// direction alone. Also what handler.UnassignTransaction resets a row to, so
// the two can't drift.
func FallbackCategory(direction string) string {
	if direction == "incoming" {
		return "other_income"
	}
	return "other_expense"
}

// categorize walks rules (already in evaluation order, enabled only) and
// returns the first match. Only the member_variable_symbol rule queries the
// database; every other kind is decided from rt and ourAccounts alone.
func categorize(requestContext context.Context, queries *db.Queries, rt db.RawTransaction, direction string, rules []db.CategorizationRule, ourAccounts map[string]bool) (outcome, error) {
	for _, rule := range rules {
		switch rule.Kind {
		case "own_account":
			// Matched on counter_account_number alone (no bank-code check —
			// this app only syncs Fio accounts, so a collision is unlikely).
			if rt.CounterAccountNumber != nil && ourAccounts[*rt.CounterAccountNumber] {
				return outcome{category: rule.Category, ruleID: &rule.ID}, nil
			}
		case "member_variable_symbol":
			if rt.VariableSymbol == nil || *rt.VariableSymbol == "" {
				continue
			}
			member, err := queries.FindMemberByVariableSymbol(requestContext, db.FindMemberByVariableSymbolParams{
				VariableSymbol:  normalizeVariableSymbol(*rt.VariableSymbol),
				TransactionDate: rt.TransactionDate,
			})
			switch {
			case err == nil:
				matchedBy := "variable_symbol"
				return outcome{category: rule.Category, ruleID: &rule.ID, memberNumber: &member, matchedBy: &matchedBy}, nil
			case errors.Is(err, pgx.ErrNoRows):
				// no member currently claims this variable symbol — next rule.
			default:
				return outcome{}, fmt.Errorf("matching variable_symbol %q: %w", *rt.VariableSymbol, err)
			}
		case "pattern":
			if matchesPattern(rule, rt, direction) {
				return outcome{category: rule.Category, ruleID: &rule.ID}, nil
			}
		default:
			// The kind CHECK constraint makes this unreachable; skip rather
			// than fail the whole transaction if it ever isn't.
			log.Printf("processing: categorization rule %d has unknown kind %q, skipped", rule.ID, rule.Kind)
		}
	}
	return outcome{category: FallbackCategory(direction)}, nil
}

// matchesPattern reports whether every non-NULL condition of a pattern rule
// holds for rt. The schema guarantees at least one condition besides
// direction is set.
func matchesPattern(rule db.CategorizationRule, rt db.RawTransaction, direction string) bool {
	if rule.Direction != nil && *rule.Direction != direction {
		return false
	}
	if rule.CounterAccountNumber != nil && !equalField(rt.CounterAccountNumber, *rule.CounterAccountNumber) {
		return false
	}
	if rule.VariableSymbol != nil &&
		(rt.VariableSymbol == nil || normalizeVariableSymbol(*rt.VariableSymbol) != normalizeVariableSymbol(*rule.VariableSymbol)) {
		return false
	}
	if rule.ConstantSymbol != nil && !equalField(rt.ConstantSymbol, *rule.ConstantSymbol) {
		return false
	}
	if rule.TextContains != nil &&
		!containsFold(rt.Comment, *rule.TextContains) &&
		!containsFold(rt.UserIdentification, *rule.TextContains) &&
		!containsFold(rt.MessageForRecipient, *rule.TextContains) {
		return false
	}
	if rule.TransactionTypeContains != nil && !containsFold(rt.TransactionType, *rule.TransactionTypeContains) {
		return false
	}
	return true
}

func equalField(field *string, value string) bool {
	return field != nil && *field == value
}

// containsFold is a case-insensitive substring check on a nullable field.
func containsFold(field *string, substring string) bool {
	return field != nil && strings.Contains(strings.ToLower(*field), strings.ToLower(substring))
}

// processOne categorizes and matches one raw transaction, then writes the
// processed_transactions row (and, for a matched membership fee, the default
// payment_coverage row for the transaction's own month) in one DB transaction
// so the two inserts can't split — a payment_coverage failure after a
// committed processed_transactions row would silently and permanently drop
// that month's coverage, since the row would no longer show up as
// unprocessed on the next run.
func processOne(requestContext context.Context, pool *pgxpool.Pool, queries *db.Queries, rt db.RawTransaction, rules []db.CategorizationRule, ourAccounts map[string]bool) error {
	direction := transactionDirection(rt.Amount)

	result, err := categorize(requestContext, queries, rt, direction, rules, ourAccounts)
	if err != nil {
		return err
	}

	tx, err := pool.Begin(requestContext)
	if err != nil {
		return err
	}
	defer tx.Rollback(requestContext)
	txQueries := db.New(tx)

	pt, err := txQueries.CreateProcessedTransaction(requestContext, db.CreateProcessedTransactionParams{
		RawTransactionID:     rt.ID,
		MemberNumber:         result.memberNumber,
		Category:             result.category,
		Direction:            direction,
		MatchedBy:            result.matchedBy,
		CategorizationRuleID: result.ruleID,
	})
	if err != nil {
		return fmt.Errorf("inserting processed_transactions: %w", err)
	}

	if result.memberNumber != nil {
		// Dues for month M are paid during month M+1 — a transaction received this month covers
		// *last* month's fee by default, not its own month. AssignTransaction's
		// manual default follows the same convention; either can be overridden
		// explicitly (there, via `covers` — not possible here, since automatic
		// matching has no other signal to go on).
		coveredMonth := rt.TransactionDate.AddDate(0, -1, 0)
		// A month already covered by another of this member's transactions
		// (dues split across two payments) just gets a second row.
		if err := txQueries.CreatePaymentCoverage(requestContext, db.CreatePaymentCoverageParams{
			ProcessedTransactionID: pt.ID,
			MemberNumber:           *result.memberNumber,
			CoversYear:             int32(coveredMonth.Year()),
			CoversMonth:            int16(coveredMonth.Month()),
		}); err != nil {
			return fmt.Errorf("inserting payment_coverage: %w", err)
		}
	}

	return tx.Commit(requestContext)
}

// normalizeVariableSymbol strips leading zeroes Fio sometimes pads a
// variable symbol with (e.g. "00000123" for VS 123) — member_payment_identifiers
// stores it unpadded (Orca sync seeds it from member_number, an int, with no
// padding). Used by every variable_symbol match against
// member_payment_identifiers (processOne and rematchOne alike) — not applied
// at insert time, so raw_transactions stays an untouched mirror of Fio's
// response.
func normalizeVariableSymbol(variableSymbol string) string {
	normalized := strings.TrimLeft(variableSymbol, "0")
	if normalized == "" {
		return "0"
	}
	return normalized
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
// matched_by='manual' and category_set_manually rows (never override an admin's explicit decision) and
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
	return reclassifyOneToCategory(requestContext, pool, processedTransactionID, "internal_transfer")
}

// reclassifyOneToCategory clears any existing member match/coverage and sets
// category on an already-processed transaction — the shared write both
// ReclassifyInternalTransfers and ReclassifyInterest use, since both are
// "detection step added after some rows were already processed" fixes that
// only differ in which category they reclassify to and how a candidate is
// selected (see ListInternalTransferCandidates / ListInterestReclassifyCandidates).
func reclassifyOneToCategory(requestContext context.Context, pool *pgxpool.Pool, processedTransactionID int64, category string) error {
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
		Category: category,
	}); err != nil {
		return fmt.Errorf("updating category: %w", err)
	}

	return tx.Commit(requestContext)
}

// ReclassifyInterestResult summarizes one ReclassifyInterest run.
type ReclassifyInterestResult struct {
	Reclassified int
	Failed       int
}

// ReclassifyInterest re-checks already-processed transactions against
// transaction_type for interest wording and flips any match to
// category=credited_interest — same "detection added after some rows were
// already processed" fix as ReclassifyInternalTransfers, for the
// credited_interest category instead of internal_transfer (see
// ListInterestReclassifyCandidates). Skips matched_by='manual' and category_set_manually rows and
// anything already category=credited_interest, so repeat calls are
// safe/idempotent.
func ReclassifyInterest(requestContext context.Context, pool *pgxpool.Pool) (ReclassifyInterestResult, error) {
	queries := db.New(pool)

	candidateIDs, err := queries.ListInterestReclassifyCandidates(requestContext)
	if err != nil {
		return ReclassifyInterestResult{}, fmt.Errorf("listing interest reclassify candidates: %w", err)
	}

	var result ReclassifyInterestResult
	for _, id := range candidateIDs {
		if err := reclassifyOneToCategory(requestContext, pool, id, "credited_interest"); err != nil {
			result.Failed++
			log.Printf("reclassify interest: processed_transaction_id=%d: %v", id, err)
			continue
		}
		result.Reclassified++
	}
	return result, nil
}

// RematchResult summarizes one RematchUnmatched run.
type RematchResult struct {
	Matched int
	Failed  int
}

// RematchUnmatched re-checks every currently-unmatched processed transaction
// against member_payment_identifiers and matches it to a member if one now
// resolves for its variable_symbol/date. Unlike Run, this deliberately
// revisits rows that already have a processed_transactions row — the classic
// case is a member whose fee_start_date was wrong in Orca at the time a
// transaction was first processed (so member_payment_identifiers had no row
// covering that date, and the transaction fell through to
// salary/other_income/other_expense), corrected upstream in Orca, and
// re-synced (see "Orca member sync") — the corrected liability window only
// helps *new* processing, since Run only ever touches unprocessed rows.
// Skips matched_by='manual' and category_set_manually rows (never override an admin's explicit
// decision) and anything already category='internal_transfer' (that
// precedence is fixed by processOne, not up for re-evaluation here). Safe to
// call repeatedly — a transaction that still doesn't resolve to a member is
// silently left alone, not an error.
func RematchUnmatched(requestContext context.Context, pool *pgxpool.Pool) (RematchResult, error) {
	queries := db.New(pool)

	candidates, err := queries.ListRematchCandidates(requestContext)
	if err != nil {
		return RematchResult{}, fmt.Errorf("listing rematch candidates: %w", err)
	}

	var result RematchResult
	for _, candidate := range candidates {
		matched, err := rematchOne(requestContext, pool, queries, candidate)
		if err != nil {
			result.Failed++
			log.Printf("rematch unmatched: processed_transaction_id=%d: %v", candidate.ID, err)
			continue
		}
		if matched {
			result.Matched++
		}
	}
	return result, nil
}

// rematchOne looks up candidate's variable_symbol/date the same way
// processOne's member-match step does. A "no member currently claims this
// variable symbol" result isn't an error — it's the expected outcome for
// most candidates on any given run — so it's reported back as matched=false
// rather than failing the row.
func rematchOne(requestContext context.Context, pool *pgxpool.Pool, queries *db.Queries, candidate db.ListRematchCandidatesRow) (matched bool, err error) {
	member, err := queries.FindMemberByVariableSymbol(requestContext, db.FindMemberByVariableSymbolParams{
		VariableSymbol:  normalizeVariableSymbol(*candidate.VariableSymbol),
		TransactionDate: candidate.TransactionDate,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("matching variable_symbol %q: %w", *candidate.VariableSymbol, err)
	}

	tx, err := pool.Begin(requestContext)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(requestContext)
	txQueries := db.New(tx)

	if err := txQueries.RematchTransactionToMember(requestContext, db.RematchTransactionToMemberParams{
		ID:           candidate.ID,
		MemberNumber: &member,
	}); err != nil {
		return false, fmt.Errorf("updating processed_transactions: %w", err)
	}

	// Same arrears convention as processOne's default single-month coverage:
	// month before the transaction's own.
	coveredMonth := candidate.TransactionDate.AddDate(0, -1, 0)
	if err := txQueries.CreatePaymentCoverage(requestContext, db.CreatePaymentCoverageParams{
		ProcessedTransactionID: candidate.ID,
		MemberNumber:           member,
		CoversYear:             int32(coveredMonth.Year()),
		CoversMonth:            int16(coveredMonth.Month()),
	}); err != nil {
		return false, fmt.Errorf("inserting payment_coverage: %w", err)
	}

	return true, tx.Commit(requestContext)
}
