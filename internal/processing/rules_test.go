package processing

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
)

func TestMatchesPattern(t *testing.T) {
	transaction := db.RawTransaction{
		CounterAccountNumber: strPtr("123456789"),
		VariableSymbol:       strPtr("00042"),
		ConstantSymbol:       strPtr("0308"),
		Comment:              strPtr("Faktura 2026/11"),
		UserIdentification:   strPtr("Advokátní kancelář XY"),
		MessageForRecipient:  strPtr("Mzda listopad"),
		TransactionType:      strPtr("Připsaný úrok"),
	}

	tests := []struct {
		name      string
		rule      db.CategorizationRule
		direction string
		want      bool
	}{
		{"counter account exact", db.CategorizationRule{CounterAccountNumber: strPtr("123456789")}, "outgoing", true},
		{"counter account differs", db.CategorizationRule{CounterAccountNumber: strPtr("12345678")}, "outgoing", false},
		{"variable symbol ignores leading zeroes", db.CategorizationRule{VariableSymbol: strPtr("42")}, "outgoing", true},
		{"variable symbol differs", db.CategorizationRule{VariableSymbol: strPtr("43")}, "outgoing", false},
		{"constant symbol exact", db.CategorizationRule{ConstantSymbol: strPtr("0308")}, "outgoing", true},
		{"constant symbol keeps leading zeroes", db.CategorizationRule{ConstantSymbol: strPtr("308")}, "outgoing", false},
		{"text in comment", db.CategorizationRule{TextContains: strPtr("faktura")}, "outgoing", true},
		{"text in user identification", db.CategorizationRule{TextContains: strPtr("ADVOKÁTNÍ")}, "outgoing", true},
		{"text in message for recipient", db.CategorizationRule{TextContains: strPtr("mzda")}, "outgoing", true},
		{"text nowhere", db.CategorizationRule{TextContains: strPtr("nájem")}, "outgoing", false},
		{"transaction type", db.CategorizationRule{TransactionTypeContains: strPtr("ÚROK")}, "incoming", true},
		{"direction matches", db.CategorizationRule{CounterAccountNumber: strPtr("123456789"), Direction: strPtr("outgoing")}, "outgoing", true},
		{"direction differs", db.CategorizationRule{CounterAccountNumber: strPtr("123456789"), Direction: strPtr("outgoing")}, "incoming", false},
		{"all conditions must hold", db.CategorizationRule{CounterAccountNumber: strPtr("123456789"), TextContains: strPtr("nájem")}, "outgoing", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesPattern(tt.rule, transaction, tt.direction); got != tt.want {
				t.Errorf("matchesPattern = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("NULL field never matches", func(t *testing.T) {
		rule := db.CategorizationRule{TextContains: strPtr("mzda")}
		if matchesPattern(rule, db.RawTransaction{}, "outgoing") {
			t.Error("matchesPattern on a transaction with no text fields = true, want false")
		}
	})
}

// insertPatternRule adds a pattern rule for one test. dbtest.Pool doesn't
// truncate categorization_rules (the seeded rows must survive), so the rule
// is deleted again in t.Cleanup.
func insertPatternRule(t *testing.T, pool *pgxpool.Pool, priority int32, category string, counterAccountNumber, variableSymbol *string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO categorization_rules (priority, kind, category, counter_account_number, variable_symbol)
		VALUES ($1, 'pattern', $2, $3, $4)
		RETURNING id
	`, priority, category, counterAccountNumber, variableSymbol).Scan(&id); err != nil {
		t.Fatalf("inserting pattern rule: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM categorization_rules WHERE id = $1`, id); err != nil {
			t.Errorf("deleting pattern rule %d: %v", id, err)
		}
	})
	return id
}

// insertTestCategory adds a custom category for one test; removed again in
// t.Cleanup after the rules referencing it (cleanups run last-in first-out,
// so call this before insertPatternRule).
func insertTestCategory(t *testing.T, pool *pgxpool.Pool, name string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO transaction_categories (name, is_mandatory) VALUES ($1, false)`, name); err != nil {
		t.Fatalf("inserting category %q: %v", name, err)
	}
	t.Cleanup(func() {
		// processed_transactions referencing it are truncated by dbtest.Pool's
		// own cleanup only after this runs, so clear them first.
		if _, err := pool.Exec(context.Background(), `DELETE FROM processed_transactions WHERE category = $1`, name); err != nil {
			t.Errorf("clearing transactions of category %q: %v", name, err)
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM transaction_categories WHERE name = $1`, name); err != nil {
			t.Errorf("deleting category %q: %v", name, err)
		}
	})
}

// disableSeededRule turns off the seeded rule of the given category for one
// test and turns it back on in t.Cleanup.
func disableSeededRule(t *testing.T, pool *pgxpool.Pool, category string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE categorization_rules SET enabled = false WHERE category = $1`, category); err != nil {
		t.Fatalf("disabling %q rule: %v", category, err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `UPDATE categorization_rules SET enabled = true WHERE category = $1`, category); err != nil {
			t.Errorf("re-enabling %q rule: %v", category, err)
		}
	})
}

// ruleIDFor returns the processed row's categorization_rule_id.
func ruleIDFor(t *testing.T, pool *pgxpool.Pool, fioTransactionID int64) *int64 {
	t.Helper()
	var id *int64
	if err := pool.QueryRow(context.Background(), `
		SELECT pt.categorization_rule_id
		FROM processed_transactions pt
		JOIN raw_transactions rt ON rt.id = pt.raw_transaction_id
		WHERE rt.fio_transaction_id = $1
	`, fioTransactionID).Scan(&id); err != nil {
		t.Fatalf("reading categorization_rule_id (fio_transaction_id=%d): %v", fioTransactionID, err)
	}
	return id
}

// seededRuleID returns the id of the migration-seeded rule for category.
func seededRuleID(t *testing.T, pool *pgxpool.Pool, category string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM categorization_rules WHERE category = $1 AND priority IN (100, 200, 300, 500)`, category,
	).Scan(&id); err != nil {
		t.Fatalf("looking up seeded %q rule: %v", category, err)
	}
	return id
}

func TestRun_RecordsDecidingRule(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000041")
	seedMemberWithIdentifier(t, queries, 900641, "900641")
	seedRawTransaction(t, queries, account.ID, 6041, "500.00", strPtr("900641"), nil)
	seedRawTransaction(t, queries, account.ID, 6042, "-80.00", nil, nil)

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got, want := ruleIDFor(t, pool, 6041), seededRuleID(t, pool, "membership_fee"); got == nil || *got != want {
		t.Errorf("member match rule id = %v, want %d", got, want)
	}
	if got := ruleIDFor(t, pool, 6042); got != nil {
		t.Errorf("fallback rule id = %d, want NULL", *got)
	}
}

func TestRun_InterestDetectionByTransactionType(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000042")
	if _, err := queries.InsertRawTransaction(context.Background(), db.InsertRawTransactionParams{
		BankAccountID:    account.ID,
		FioTransactionID: 6043,
		TransactionDate:  time.Now(),
		Amount:           "12.34",
		Currency:         "CZK",
		TransactionType:  strPtr("Připsaný úrok"),
		RawPayload:       []byte("{}"),
	}); err != nil {
		t.Fatalf("InsertRawTransaction: %v", err)
	}

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := queryProcessed(t, pool, 6043); got.category != "credited_interest" {
		t.Errorf("category = %q, want credited_interest", got.category)
	}
}

// The seeded salary rule searches message_for_recipient too — a deliberate
// widening compared to the old hardcoded heuristic, which only read
// comment/user_identification.
func TestRun_SalaryDetectedInMessageForRecipient(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000043")
	if _, err := queries.InsertRawTransaction(context.Background(), db.InsertRawTransactionParams{
		BankAccountID:       account.ID,
		FioTransactionID:    6044,
		TransactionDate:     time.Now(),
		Amount:              "-30000.00",
		Currency:            "CZK",
		MessageForRecipient: strPtr("Mzda 10/2026"),
		RawPayload:          []byte("{}"),
	}); err != nil {
		t.Fatalf("InsertRawTransaction: %v", err)
	}

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := queryProcessed(t, pool, 6044); got.category != "salary" {
		t.Errorf("category = %q, want salary", got.category)
	}
}

// A pattern rule on a specific variable symbol placed before the member
// match (priority < 300) wins over a member whose number collides with it,
// e.g. a supplier invoice paid with VS 900642 while member 900642 exists.
func TestRun_PatternRuleBeforeMemberMatchWins(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000044")
	seedMemberWithIdentifier(t, queries, 900642, "900642")
	ruleID := insertPatternRule(t, pool, 250, "other_income", nil, strPtr("900642"))
	seedRawTransaction(t, queries, account.ID, 6045, "500.00", strPtr("900642"), nil)

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := queryProcessed(t, pool, 6045)
	if got.category != "other_income" || got.memberNumber != nil || got.matchedBy != nil {
		t.Errorf("got %+v, want other_income with no member", got)
	}
	if id := ruleIDFor(t, pool, 6045); id == nil || *id != ruleID {
		t.Errorf("rule id = %v, want %d", id, ruleID)
	}
	if n := coverageMonthsFor(t, pool, 900642); n != 0 {
		t.Errorf("payment_coverage rows = %d, want 0", n)
	}
}

// The lawyer case: a counter-account rule between member match and the
// salary heuristic wins over "mzda" in the payment text.
func TestRun_CounterAccountRuleBeatsSalaryHeuristic(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000045")
	insertTestCategory(t, pool, "test_legal")
	insertPatternRule(t, pool, 400, "test_legal", strPtr("2900000001"), nil)
	if _, err := queries.InsertRawTransaction(context.Background(), db.InsertRawTransactionParams{
		BankAccountID:        account.ID,
		FioTransactionID:     6046,
		TransactionDate:      time.Now(),
		Amount:               "-5000.00",
		Currency:             "CZK",
		CounterAccountNumber: strPtr("2900000001"),
		Comment:              strPtr("mzda pravni sluzby"),
		RawPayload:           []byte("{}"),
	}); err != nil {
		t.Fatalf("InsertRawTransaction: %v", err)
	}

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := queryProcessed(t, pool, 6046); got.category != "test_legal" {
		t.Errorf("category = %q, want test_legal", got.category)
	}
}

func TestRun_DisabledRuleIsSkipped(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000046")
	disableSeededRule(t, pool, "salary")
	seedRawTransaction(t, queries, account.ID, 6047, "-3000.00", nil, strPtr("mzda"))

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := queryProcessed(t, pool, 6047); got.category != "other_expense" {
		t.Errorf("category = %q, want other_expense (salary rule disabled)", got.category)
	}
}

// A rule added after a transaction was processed reaches it through
// ReapplyRules — the lawyer payment tagged salary by "mzda" moves to its own
// category.
func TestReapplyRules_NewRuleRecategorizesHistory(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000047")
	if _, err := queries.InsertRawTransaction(context.Background(), db.InsertRawTransactionParams{
		BankAccountID:        account.ID,
		FioTransactionID:     6048,
		TransactionDate:      time.Now(),
		Amount:               "-5000.00",
		Currency:             "CZK",
		CounterAccountNumber: strPtr("2900000003"),
		Comment:              strPtr("mzda pravni sluzby"),
		RawPayload:           []byte("{}"),
	}); err != nil {
		t.Fatalf("InsertRawTransaction: %v", err)
	}
	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := queryProcessed(t, pool, 6048); got.category != "salary" {
		t.Fatalf("category before the rule = %q, want salary", got.category)
	}

	insertTestCategory(t, pool, "test_legal")
	ruleID := insertPatternRule(t, pool, 400, "test_legal", strPtr("2900000003"), nil)

	result, err := ReapplyRules(context.Background(), pool)
	if err != nil {
		t.Fatalf("ReapplyRules: %v", err)
	}
	if result.Changed != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want {1 0}", result)
	}
	if got := queryProcessed(t, pool, 6048); got.category != "test_legal" {
		t.Errorf("category = %q, want test_legal", got.category)
	}
	if id := ruleIDFor(t, pool, 6048); id == nil || *id != ruleID {
		t.Errorf("rule id = %v, want %d", id, ruleID)
	}

	second, err := ReapplyRules(context.Background(), pool)
	if err != nil {
		t.Fatalf("second ReapplyRules: %v", err)
	}
	if second.Changed != 0 || second.Failed != 0 {
		t.Errorf("second result = %+v, want {0 0}", second)
	}
}

// When the member rule no longer matches (here: disabled), an automatic
// member match is undone — member cleared, default coverage removed.
func TestReapplyRules_UndoesMemberMatchThatNoLongerApplies(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000048")
	seedMemberWithIdentifier(t, queries, 900648, "900648")
	seedRawTransaction(t, queries, account.ID, 6049, "500.00", strPtr("900648"), nil)
	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := coverageMonthsFor(t, pool, 900648); n != 1 {
		t.Fatalf("coverage before = %d, want 1", n)
	}

	disableSeededRule(t, pool, "membership_fee")

	result, err := ReapplyRules(context.Background(), pool)
	if err != nil {
		t.Fatalf("ReapplyRules: %v", err)
	}
	if result.Changed != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want {1 0}", result)
	}
	got := queryProcessed(t, pool, 6049)
	if got.category != "other_income" || got.memberNumber != nil || got.matchedBy != nil {
		t.Errorf("got %+v, want other_income with no member", got)
	}
	if n := coverageMonthsFor(t, pool, 900648); n != 0 {
		t.Errorf("coverage after = %d, want 0", n)
	}
}
