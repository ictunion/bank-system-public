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
	"time"

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

	rules, ourAccounts, err := loadRules(requestContext, queries)
	if err != nil {
		return Result{}, err
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

// loadRules fetches what categorize needs: the enabled rules in evaluation
// order and our own account numbers (soft-deleted accounts excluded). Done
// once per run, not per transaction.
func loadRules(requestContext context.Context, queries *db.Queries) ([]db.CategorizationRule, map[string]bool, error) {
	rules, err := queries.ListEnabledCategorizationRules(requestContext)
	if err != nil {
		return nil, nil, fmt.Errorf("listing categorization rules: %w", err)
	}
	ourAccountNumbers, err := queries.ListActiveBankAccountNumbers(requestContext)
	if err != nil {
		return nil, nil, fmt.Errorf("listing active bank account numbers: %w", err)
	}
	ourAccounts := make(map[string]bool, len(ourAccountNumbers))
	for _, number := range ourAccountNumbers {
		ourAccounts[number] = true
	}
	return rules, ourAccounts, nil
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
// returns the first match, or the direction fallback when none matches.
func categorize(requestContext context.Context, queries *db.Queries, rt db.RawTransaction, direction string, rules []db.CategorizationRule, ourAccounts map[string]bool) (outcome, error) {
	for _, rule := range rules {
		result, matched, err := applyRule(requestContext, queries, rule, rt, direction, ourAccounts)
		if err != nil {
			return outcome{}, err
		}
		if matched {
			return result, nil
		}
	}
	return outcome{category: FallbackCategory(direction)}, nil
}

// applyRule checks one rule against rt. Only the member_variable_symbol rule
// queries the database; every other kind is decided from rt and ourAccounts
// alone.
func applyRule(requestContext context.Context, queries *db.Queries, rule db.CategorizationRule, rt db.RawTransaction, direction string, ourAccounts map[string]bool) (outcome, bool, error) {
	switch rule.Kind {
	case "own_account":
		// Matched on counter_account_number alone (no bank-code check —
		// this app only syncs Fio accounts, so a collision is unlikely).
		if rt.CounterAccountNumber != nil && ourAccounts[*rt.CounterAccountNumber] {
			return outcome{category: rule.Category, ruleID: &rule.ID}, true, nil
		}
	case "member_variable_symbol":
		if rt.VariableSymbol == nil || *rt.VariableSymbol == "" {
			return outcome{}, false, nil
		}
		member, err := queries.FindMemberByVariableSymbol(requestContext, db.FindMemberByVariableSymbolParams{
			VariableSymbol:  normalizeVariableSymbol(*rt.VariableSymbol),
			TransactionDate: rt.TransactionDate,
		})
		switch {
		case err == nil:
			matchedBy := "variable_symbol"
			return outcome{category: rule.Category, ruleID: &rule.ID, memberNumber: &member, matchedBy: &matchedBy}, true, nil
		case errors.Is(err, pgx.ErrNoRows):
			// no member currently claims this variable symbol — next rule.
		default:
			return outcome{}, false, fmt.Errorf("matching variable_symbol %q: %w", *rt.VariableSymbol, err)
		}
	case "pattern":
		if matchesPattern(rule, rt, direction) {
			return outcome{category: rule.Category, ruleID: &rule.ID}, true, nil
		}
	default:
		// The kind CHECK constraint makes this unreachable; skip rather
		// than fail the whole transaction if it ever isn't.
		log.Printf("processing: categorization rule %d has unknown kind %q, skipped", rule.ID, rule.Kind)
	}
	return outcome{}, false, nil
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
// padding). Used by every variable_symbol comparison (the member lookup in
// categorize, pattern rules in matchesPattern) — not applied
// at insert time, so raw_transactions stays an untouched mirror of Fio's
// response.
func normalizeVariableSymbol(variableSymbol string) string {
	normalized := strings.TrimLeft(variableSymbol, "0")
	if normalized == "" {
		return "0"
	}
	return normalized
}

// ReapplyResult summarizes one ReapplyRule run.
type ReapplyResult struct {
	Changed int
	Failed  int
}

var (
	// ErrRuleNotFound is returned by ReapplyRule for an id with no rule.
	ErrRuleNotFound = errors.New("categorization rule not found")
	// ErrRuleDisabled is returned by ReapplyRule for a disabled rule — it
	// doesn't apply to new transactions, so it isn't applied to history either.
	ErrRuleDisabled = errors.New("categorization rule is disabled")
)

// ReapplyRule applies one rule — and only that rule — to every
// already-processed transaction not set by hand dated on or after from
// (inclusive; older history stays untouched): where its conditions match,
// the transaction gets this rule's outcome. No other rule is evaluated. Run
// only ever touches unprocessed rows, so this is the one way a new or edited
// rule, a bank account added later (own_account) or a corrected member
// liability window (member_variable_symbol) reaches existing transactions.
// There's deliberately no "re-apply every rule" counterpart — re-running the
// whole chain over all history at once is too broad.
//
// Skips matched_by='manual' and category_set_manually rows — never override
// an admin's explicit decision. The remaining rows only ever have the one
// default payment_coverage row, so rewriting it is safe. Idempotent: a
// second call finds nothing left to change.
//
// One exception keeps history consistent with the rule order: a transaction
// currently decided by an enabled rule that runs before this one (lower
// priority, ties by id) is left alone, since that rule would win for a new
// transaction too. Transactions decided by a later rule, a disabled one, or
// the fallback are taken over. Transactions this rule decided earlier but no
// longer matches (after an edit) are not touched either.
func ReapplyRule(requestContext context.Context, pool *pgxpool.Pool, ruleID int64, from time.Time) (ReapplyResult, error) {
	queries := db.New(pool)

	target, err := queries.GetCategorizationRule(requestContext, ruleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReapplyResult{}, ErrRuleNotFound
	} else if err != nil {
		return ReapplyResult{}, fmt.Errorf("loading categorization rule: %w", err)
	}
	if !target.Enabled {
		return ReapplyResult{}, ErrRuleDisabled
	}

	rules, ourAccounts, err := loadRules(requestContext, queries)
	if err != nil {
		return ReapplyResult{}, err
	}
	// Position in evaluation order; only enabled rules are in it.
	order := make(map[int64]int, len(rules))
	for i, rule := range rules {
		order[rule.ID] = i
	}
	targetOrder := order[target.ID]

	candidates, err := queries.ListReapplyCandidates(requestContext, from)
	if err != nil {
		return ReapplyResult{}, fmt.Errorf("listing reapply candidates: %w", err)
	}

	var result ReapplyResult
	for _, candidate := range candidates {
		if candidate.CategorizationRuleID != nil {
			if position, enabled := order[*candidate.CategorizationRuleID]; enabled && position < targetOrder {
				continue
			}
		}
		outcome, matched, err := applyRule(requestContext, queries, target, candidateTransaction(candidate), candidate.Direction, ourAccounts)
		if err == nil && matched {
			err = result.record(writeOutcome(requestContext, pool, candidate, outcome))
		}
		if err != nil {
			result.Failed++
			log.Printf("reapply rule %d: processed_transaction_id=%d: %v", ruleID, candidate.ID, err)
		}
	}
	return result, nil
}

// record counts a writeOutcome result and passes its error through.
func (result *ReapplyResult) record(changed bool, err error) error {
	if changed {
		result.Changed++
	}
	return err
}

// candidateTransaction rebuilds the raw transaction fields the rules read.
func candidateTransaction(candidate db.ListReapplyCandidatesRow) db.RawTransaction {
	return db.RawTransaction{
		TransactionDate:      candidate.TransactionDate,
		Amount:               candidate.Amount,
		CounterAccountNumber: candidate.CounterAccountNumber,
		VariableSymbol:       candidate.VariableSymbol,
		ConstantSymbol:       candidate.ConstantSymbol,
		Comment:              candidate.Comment,
		UserIdentification:   candidate.UserIdentification,
		MessageForRecipient:  candidate.MessageForRecipient,
		TransactionType:      candidate.TransactionType,
	}
}

// writeOutcome rewrites candidate to result if they differ — category,
// member, matched_by, deciding rule, and the default payment_coverage row
// when the member changed. Reports whether anything was written.
func writeOutcome(requestContext context.Context, pool *pgxpool.Pool, candidate db.ListReapplyCandidatesRow, result outcome) (changed bool, err error) {
	memberChanged := !equalPointer(result.memberNumber, candidate.MemberNumber)
	if result.category == candidate.Category && !memberChanged &&
		equalPointer(result.matchedBy, candidate.MatchedBy) && equalPointer(result.ruleID, candidate.CategorizationRuleID) {
		return false, nil
	}

	tx, err := pool.Begin(requestContext)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(requestContext)
	txQueries := db.New(tx)

	rows, err := txQueries.ReapplyCategorization(requestContext, db.ReapplyCategorizationParams{
		ID:                   candidate.ID,
		Category:             result.category,
		MemberNumber:         result.memberNumber,
		MatchedBy:            result.matchedBy,
		CategorizationRuleID: result.ruleID,
	})
	if err != nil {
		return false, fmt.Errorf("updating processed_transactions: %w", err)
	}
	if rows == 0 {
		// An admin edited it by hand since it was listed — leave it.
		return false, nil
	}

	// Same member as before: its default coverage row is still right.
	if memberChanged {
		if err := txQueries.DeleteCoverageForTransaction(requestContext, candidate.ID); err != nil {
			return false, fmt.Errorf("clearing coverage: %w", err)
		}
		if result.memberNumber != nil {
			// Same arrears convention as processOne: month before the
			// transaction's own.
			coveredMonth := candidate.TransactionDate.AddDate(0, -1, 0)
			if err := txQueries.CreatePaymentCoverage(requestContext, db.CreatePaymentCoverageParams{
				ProcessedTransactionID: candidate.ID,
				MemberNumber:           *result.memberNumber,
				CoversYear:             int32(coveredMonth.Year()),
				CoversMonth:            int16(coveredMonth.Month()),
			}); err != nil {
				return false, fmt.Errorf("inserting payment_coverage: %w", err)
			}
		}
	}

	return true, tx.Commit(requestContext)
}

func equalPointer[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
