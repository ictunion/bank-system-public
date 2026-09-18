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

// seedTransferTransaction inserts a raw_transactions row with a
// counter_account_number set (and optionally a variable_symbol) — for the
// internal-transfer detection tests, which need a counterparty account
// number to compare against ListActiveBankAccountNumbers.
func seedTransferTransaction(t *testing.T, queries *db.Queries, bankAccountID int32, fioTransactionID int64, amount, counterAccountNumber string, variableSymbol *string) {
	t.Helper()
	if _, err := queries.InsertRawTransaction(context.Background(), db.InsertRawTransactionParams{
		BankAccountID:        bankAccountID,
		FioTransactionID:     fioTransactionID,
		TransactionDate:      time.Now(),
		Amount:               amount,
		Currency:             "CZK",
		CounterAccountNumber: &counterAccountNumber,
		VariableSymbol:       variableSymbol,
		RawPayload:           []byte("{}"),
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

// soleCoverageMonth returns the single payment_coverage row's (year, month)
// for a member expected to have exactly one — fails the test otherwise.
func soleCoverageMonth(t *testing.T, pool *pgxpool.Pool, memberNumber int32) (int32, int16) {
	t.Helper()
	var year int32
	var month int16
	if err := pool.QueryRow(context.Background(),
		`SELECT covers_year, covers_month FROM payment_coverage WHERE member_number = $1`, memberNumber,
	).Scan(&year, &month); err != nil {
		t.Fatalf("querying sole payment_coverage row for member %d: %v", memberNumber, err)
	}
	return year, month
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

	// Dues are paid a month in arrears — a transaction received "now" defaults to covering
	// *last* month, not its own.
	wantMonth := time.Now().AddDate(0, -1, 0)
	gotYear, gotMonth := soleCoverageMonth(t, pool, 900601)
	if gotYear != int32(wantMonth.Year()) || gotMonth != int16(wantMonth.Month()) {
		t.Errorf("covered month = %d-%02d, want %d-%02d (one month before the transaction's own)",
			gotYear, gotMonth, wantMonth.Year(), int(wantMonth.Month()))
	}
}

func TestRun_SecondPaymentInSameMonthGetsNoCoverageRowOfItsOwn(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000005")
	seedMemberWithIdentifier(t, queries, 900602, "900602")
	// Two membership-fee payments landing in the same month, both matched to
	// the same member — e.g. a duplicate, or a catch-up payment processOne has
	// no way to distinguish from a normal one (it always defaults to "last
	// month," see CreatePaymentCoverage's caller).
	seedRawTransaction(t, queries, account.ID, 6006, "500.00", strPtr("900602"), nil)
	seedRawTransaction(t, queries, account.ID, 6007, "500.00", strPtr("900602"), nil)

	result, err := Run(context.Background(), pool)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.TransactionsProcessed != 2 || result.TransactionsFailed != 0 {
		t.Fatalf("result = %+v, want {2 0} — the second payment's conflicting coverage insert is not a processing failure", result)
	}

	first := queryProcessed(t, pool, 6006)
	second := queryProcessed(t, pool, 6007)
	if first.category != "membership_fee" || second.category != "membership_fee" {
		t.Errorf("both transactions should still be categorized membership_fee: first=%+v second=%+v", first, second)
	}
	if first.memberNumber == nil || *first.memberNumber != 900602 || second.memberNumber == nil || *second.memberNumber != 900602 {
		t.Errorf("both transactions should still be matched to the member: first=%+v second=%+v", first, second)
	}

	// Exactly one payment_coverage row exists for the member, no matter which
	// of the two transactions it ended up attached to — the second insert was
	// a no-op (ON CONFLICT DO NOTHING), not a second row.
	if n := coverageMonthsFor(t, pool, 900602); n != 1 {
		t.Errorf("payment_coverage rows for member 900602 = %d, want 1 (one payment covers the month, the other's insert is a no-op)", n)
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

func TestRun_InternalTransferBetweenOwnAccounts(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	accountA := seedBankAccount(t, queries, "9400000010")
	accountB := seedBankAccount(t, queries, "9400000011")
	// Both legs of the same transfer: the outgoing side on A (counterparty B)
	// and the incoming side on B (counterparty A) — a real transfer between
	// our own accounts produces one raw_transactions row per account.
	seedTransferTransaction(t, queries, accountA.ID, 6010, "-1000.00", "9400000011", nil)
	seedTransferTransaction(t, queries, accountB.ID, 6011, "1000.00", "9400000010", nil)

	result, err := Run(context.Background(), pool)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.TransactionsProcessed != 2 || result.TransactionsFailed != 0 {
		t.Fatalf("result = %+v, want {2 0}", result)
	}

	outgoingLeg := queryProcessed(t, pool, 6010)
	if outgoingLeg.category != "internal_transfer" {
		t.Errorf("outgoing leg category = %q, want internal_transfer", outgoingLeg.category)
	}
	if outgoingLeg.memberNumber != nil || outgoingLeg.matchedBy != nil {
		t.Errorf("outgoing leg = %+v, want no member/matched_by", outgoingLeg)
	}

	incomingLeg := queryProcessed(t, pool, 6011)
	if incomingLeg.category != "internal_transfer" {
		t.Errorf("incoming leg category = %q, want internal_transfer", incomingLeg.category)
	}
	if incomingLeg.memberNumber != nil || incomingLeg.matchedBy != nil {
		t.Errorf("incoming leg = %+v, want no member/matched_by", incomingLeg)
	}
}

func TestRun_InternalTransferTakesPriorityOverVariableSymbolMatch(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	accountA := seedBankAccount(t, queries, "9400000012")
	seedBankAccount(t, queries, "9400000013")
	seedMemberWithIdentifier(t, queries, 900603, "900603")
	// Coincidentally carries a live member's variable_symbol, but the
	// counterparty is one of our own accounts — internal_transfer must win,
	// since it's checked first (see processOne).
	seedTransferTransaction(t, queries, accountA.ID, 6012, "-500.00", "9400000013", strPtr("900603"))

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := queryProcessed(t, pool, 6012)
	if got.category != "internal_transfer" {
		t.Errorf("category = %q, want internal_transfer", got.category)
	}
	if got.memberNumber != nil {
		t.Errorf("member_number = %v, want nil — internal_transfer must not also match a member", got.memberNumber)
	}
	if n := coverageMonthsFor(t, pool, 900603); n != 0 {
		t.Errorf("payment_coverage rows for member 900603 = %d, want 0", n)
	}
}

func TestRun_CounterAccountNotOursIsNotInternalTransfer(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000014")
	// counter_account_number is set, but doesn't match any of our own
	// bank_accounts — must fall through to the normal direction fallback,
	// not be swept up as an internal transfer.
	seedTransferTransaction(t, queries, account.ID, 6013, "150.00", "1111111111", nil)

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := queryProcessed(t, pool, 6013)
	if got.category != "other_income" {
		t.Errorf("category = %q, want other_income", got.category)
	}
}

func TestRun_SoftDeletedAccountNotTreatedAsOurs(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000015")
	deletedAccount := seedBankAccount(t, queries, "9400000016")
	if rows, err := queries.DeleteBankAccount(context.Background(), deletedAccount.ID); err != nil || rows != 1 {
		t.Fatalf("DeleteBankAccount(%d): rows=%d err=%v", deletedAccount.ID, rows, err)
	}
	// The counterparty account number used to be one of ours, but it's
	// soft-deleted now — no live account there to be the other leg of a
	// current transfer, so this must not be detected as internal_transfer.
	seedTransferTransaction(t, queries, account.ID, 6014, "300.00", "9400000016", nil)

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := queryProcessed(t, pool, 6014)
	if got.category != "other_income" {
		t.Errorf("category = %q, want other_income (soft-deleted account shouldn't count as ours)", got.category)
	}
}
