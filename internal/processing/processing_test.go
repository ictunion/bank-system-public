package processing

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
)

const testEncryptionKey = "test-encryption-key"

func seedBankAccount(t *testing.T, queries *db.Queries, fioAccountID string) db.CreateBankAccountRow {
	t.Helper()
	account, err := queries.CreateBankAccount(context.Background(), db.CreateBankAccountParams{
		FioAccountID:  fioAccountID,
		Currency:      "CZK",
		DisplayName:   "Fixture Account",
		FioToken:      "tok",
		EncryptionKey: testEncryptionKey,
	})
	if err != nil {
		t.Fatalf("seedBankAccount(%q): %v", fioAccountID, err)
	}
	return account
}

// seedRawTransaction inserts one raw_transactions row — the only fixture
// Run/processOne actually reads (they never touch bank_accounts beyond the FK).
func seedRawTransaction(t *testing.T, queries *db.Queries, bankAccountID int32, fioTransactionID int64, amount string, variableSymbol, comment *string) {
	t.Helper()
	if _, err := queries.InsertRawTransaction(context.Background(), db.InsertRawTransactionParams{
		BankAccountID:    bankAccountID,
		FioTransactionID: fioTransactionID,
		TransactionDate:  time.Now(),
		Amount:           amount,
		Currency:         "CZK",
		VariableSymbol:   variableSymbol,
		Comment:          comment,
		RawPayload:       []byte("{}"),
	}); err != nil {
		t.Fatalf("InsertRawTransaction(fio_transaction_id=%d): %v", fioTransactionID, err)
	}
}

func seedMemberWithIdentifier(t *testing.T, queries *db.Queries, memberNumber int32, variableSymbol string) {
	t.Helper()
	ctx := context.Background()
	feeStart := time.Now().AddDate(-1, 0, 0)
	if err := queries.UpsertMember(ctx, db.UpsertMemberParams{
		MemberNumber: memberNumber,
		FeeStartDate: pgtype.Date{Time: feeStart, Valid: true},
		Active:       true,
	}); err != nil {
		t.Fatalf("UpsertMember(%d): %v", memberNumber, err)
	}
	if err := queries.EnsureDefaultPaymentIdentifier(ctx, db.EnsureDefaultPaymentIdentifierParams{
		MemberNumber:   memberNumber,
		VariableSymbol: variableSymbol,
		ValidFrom:      feeStart,
	}); err != nil {
		t.Fatalf("EnsureDefaultPaymentIdentifier(%d, %q): %v", memberNumber, variableSymbol, err)
	}
}

type processedRow struct {
	category     string
	direction    string
	memberNumber *int32
	matchedBy    *string
}

// queryProcessed looks up the processed_transactions row for a given
// fio_transaction_id — Run doesn't return per-transaction IDs, and this is
// simpler than routing through every intermediate sqlc query just to get
// back to what processOne actually wrote.
func queryProcessed(t *testing.T, pool *pgxpool.Pool, fioTransactionID int64) processedRow {
	t.Helper()
	var row processedRow
	err := pool.QueryRow(context.Background(), `
		SELECT pt.category, pt.direction, pt.member_number, pt.matched_by
		FROM processed_transactions pt
		JOIN raw_transactions rt ON rt.id = pt.raw_transaction_id
		WHERE rt.fio_transaction_id = $1
	`, fioTransactionID).Scan(&row.category, &row.direction, &row.memberNumber, &row.matchedBy)
	if err != nil {
		t.Fatalf("queryProcessed(fio_transaction_id=%d): %v", fioTransactionID, err)
	}
	return row
}

func coverageMonthsFor(t *testing.T, pool *pgxpool.Pool, memberNumber int32) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM payment_coverage WHERE member_number = $1`, memberNumber,
	).Scan(&count); err != nil {
		t.Fatalf("counting payment_coverage for member %d: %v", memberNumber, err)
	}
	return count
}

func strPtr(s string) *string { return &s }

func TestRun_VariableSymbolMatchWritesCoverage(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000001")
	seedMemberWithIdentifier(t, queries, 900601, "900601")
	seedRawTransaction(t, queries, account.ID, 6001, "500.00", strPtr("900601"), nil)

	result, err := Run(context.Background(), pool)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.TransactionsProcessed != 1 || result.TransactionsFailed != 0 {
		t.Fatalf("result = %+v, want {1 0}", result)
	}

	got := queryProcessed(t, pool, 6001)
	if got.category != "membership_fee" {
		t.Errorf("category = %q, want membership_fee", got.category)
	}
	if got.memberNumber == nil || *got.memberNumber != 900601 {
		t.Errorf("member_number = %v, want 900601", got.memberNumber)
	}
	if got.matchedBy == nil || *got.matchedBy != "variable_symbol" {
		t.Errorf("matched_by = %v, want variable_symbol", got.matchedBy)
	}
	if n := coverageMonthsFor(t, pool, 900601); n != 1 {
		t.Errorf("payment_coverage rows for member 900601 = %d, want 1", n)
	}
}

func TestRun_SalaryDetectionBySubstring(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000002")
	seedRawTransaction(t, queries, account.ID, 6002, "-3000.00", nil, strPtr("vyplata mzda za srpen"))

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := queryProcessed(t, pool, 6002)
	if got.category != "salary" {
		t.Errorf("category = %q, want salary", got.category)
	}
	if got.memberNumber != nil {
		t.Errorf("member_number = %v, want nil — salary detection never matches a member", got.memberNumber)
	}
}

func TestRun_UnmatchedDefaultsByDirection(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000003")
	seedRawTransaction(t, queries, account.ID, 6003, "250.00", nil, nil)  // incoming, no VS, no mzda
	seedRawTransaction(t, queries, account.ID, 6004, "-80.00", nil, nil) // outgoing, no VS, no mzda

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}

	incoming := queryProcessed(t, pool, 6003)
	if incoming.category != "other_income" || incoming.direction != "incoming" {
		t.Errorf("incoming row = %+v, want category=other_income direction=incoming", incoming)
	}
	outgoing := queryProcessed(t, pool, 6004)
	if outgoing.category != "other_expense" || outgoing.direction != "outgoing" {
		t.Errorf("outgoing row = %+v, want category=other_expense direction=outgoing", outgoing)
	}
}

func TestRun_IsIdempotent(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000004")
	seedRawTransaction(t, queries, account.ID, 6005, "100.00", nil, nil)

	first, err := Run(context.Background(), pool)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if first.TransactionsProcessed != 1 {
		t.Fatalf("first Run processed = %d, want 1", first.TransactionsProcessed)
	}

	second, err := Run(context.Background(), pool)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if second.TransactionsProcessed != 0 || second.TransactionsFailed != 0 {
		t.Errorf("second Run = %+v, want {0 0} — already-processed rows must not be reprocessed", second)
	}
}
