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

// TestRun_VariableSymbolMatchStripsLeadingZeroes covers Fio sometimes
// zero-padding a variable symbol (e.g. "00900602" for VS 900602) — the stored
// member_payment_identifiers row is unpadded (seeded from member_number, an
// int), so matching must strip leading zeroes off the transaction's VS first.
func TestRun_VariableSymbolMatchStripsLeadingZeroes(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000009")
	seedMemberWithIdentifier(t, queries, 900602, "900602")
	seedRawTransaction(t, queries, account.ID, 6009, "500.00", strPtr("00900602"), nil)

	result, err := Run(context.Background(), pool)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.TransactionsProcessed != 1 || result.TransactionsFailed != 0 {
		t.Fatalf("result = %+v, want {1 0}", result)
	}

	got := queryProcessed(t, pool, 6009)
	if got.category != "membership_fee" {
		t.Errorf("category = %q, want membership_fee", got.category)
	}
	if got.memberNumber == nil || *got.memberNumber != 900602 {
		t.Errorf("member_number = %v, want 900602", got.memberNumber)
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

func setMatchedBy(t *testing.T, pool *pgxpool.Pool, fioTransactionID int64, matchedBy string) {
	t.Helper()
	tag, err := pool.Exec(context.Background(), `
		UPDATE processed_transactions SET matched_by = $2
		FROM raw_transactions rt
		WHERE processed_transactions.raw_transaction_id = rt.id AND rt.fio_transaction_id = $1
	`, fioTransactionID, matchedBy)
	if err != nil {
		t.Fatalf("setMatchedBy(fio_transaction_id=%d): %v", fioTransactionID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("setMatchedBy(fio_transaction_id=%d): affected %d rows, want 1", fioTransactionID, tag.RowsAffected())
	}
}

// TestReclassifyInternalTransfers_FixesTransferProcessedBeforeCounterpartyAccountExisted
// reproduces the real bug: a transfer's counterparty account gets registered
// in bank_accounts *after* the transfer was already synced and processed —
// internal-transfer detection in Run only ever sees the roster as of that
// moment, so it's miscategorized, and Run alone can never fix it afterward
// since it skips already-processed rows.
func TestReclassifyInternalTransfers_FixesTransferProcessedBeforeCounterpartyAccountExisted(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	accountA := seedBankAccount(t, queries, "9400000017")
	// accountB doesn't exist yet — the transfer's counterparty isn't
	// recognized as ours at processing time.
	seedTransferTransaction(t, queries, accountA.ID, 6015, "-750.00", "9400000018", nil)

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}
	before := queryProcessed(t, pool, 6015)
	if before.category != "other_expense" {
		t.Fatalf("category before accountB exists = %q, want other_expense", before.category)
	}

	// Re-running Run() alone must not fix it — it only touches unprocessed rows.
	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := queryProcessed(t, pool, 6015); got.category != "other_expense" {
		t.Fatalf("category after a second Run = %q, want still other_expense (Run must not revisit processed rows)", got.category)
	}

	seedBankAccount(t, queries, "9400000018")

	result, err := ReclassifyInternalTransfers(context.Background(), pool)
	if err != nil {
		t.Fatalf("ReclassifyInternalTransfers: %v", err)
	}
	if result.Reclassified != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want {1 0}", result)
	}

	got := queryProcessed(t, pool, 6015)
	if got.category != "internal_transfer" {
		t.Errorf("category = %q, want internal_transfer", got.category)
	}
	if got.memberNumber != nil || got.matchedBy != nil {
		t.Errorf("got = %+v, want no member/matched_by", got)
	}

	// Idempotent: nothing left to reclassify on a second call.
	second, err := ReclassifyInternalTransfers(context.Background(), pool)
	if err != nil {
		t.Fatalf("second ReclassifyInternalTransfers: %v", err)
	}
	if second.Reclassified != 0 || second.Failed != 0 {
		t.Errorf("second result = %+v, want {0 0}", second)
	}
}

// TestReclassifyInternalTransfers_SkipsManuallyMatchedRows makes sure an
// admin's explicit manual match is never silently overridden, even if the
// counterparty later turns out to be one of our own accounts.
func TestReclassifyInternalTransfers_SkipsManuallyMatchedRows(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	accountA := seedBankAccount(t, queries, "9400000019")
	seedTransferTransaction(t, queries, accountA.ID, 6016, "-200.00", "9400000020", nil)

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}
	setMatchedBy(t, pool, 6016, "manual")

	seedBankAccount(t, queries, "9400000020")

	result, err := ReclassifyInternalTransfers(context.Background(), pool)
	if err != nil {
		t.Fatalf("ReclassifyInternalTransfers: %v", err)
	}
	if result.Reclassified != 0 || result.Failed != 0 {
		t.Fatalf("result = %+v, want {0 0} — manually-matched rows must be skipped", result)
	}

	got := queryProcessed(t, pool, 6016)
	if got.category != "other_expense" {
		t.Errorf("category = %q, want unchanged other_expense", got.category)
	}
}

// seedRawTransactionOnDate is seedRawTransaction with an explicit
// transaction_date — RematchUnmatched's tests need transactions dated well
// before "now" to simulate a member whose liability window was wrong at
// original processing time.
func seedRawTransactionOnDate(t *testing.T, queries *db.Queries, bankAccountID int32, fioTransactionID int64, amount string, variableSymbol *string, transactionDate time.Time) {
	t.Helper()
	if _, err := queries.InsertRawTransaction(context.Background(), db.InsertRawTransactionParams{
		BankAccountID:    bankAccountID,
		FioTransactionID: fioTransactionID,
		TransactionDate:  transactionDate,
		Amount:           amount,
		Currency:         "CZK",
		VariableSymbol:   variableSymbol,
		RawPayload:       []byte("{}"),
	}); err != nil {
		t.Fatalf("InsertRawTransaction(fio_transaction_id=%d): %v", fioTransactionID, err)
	}
}

// TestRematchUnmatched_MatchesAfterMemberStartDateCorrected reproduces the
// real reported bug: a member's fee_start_date was wrong in Orca (too late),
// so member_payment_identifiers had no row covering their actual, earlier
// payments — those transactions processed as unmatched other_income. Once
// Orca's data is corrected and re-synced (simulated here directly via
// EnsureDefaultPaymentIdentifier, same call the Orca sync itself makes),
// RematchUnmatched must find and fix them.
func TestRematchUnmatched_MatchesAfterMemberStartDateCorrected(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000021")
	const memberNumber = int32(900610)

	// Wrong fee_start_date (too recent) — the transaction below, dated 6
	// months ago, predates this window entirely, so it won't match yet.
	wrongStart := time.Now().AddDate(0, -2, 0)
	if err := queries.UpsertMember(context.Background(), db.UpsertMemberParams{
		MemberNumber: memberNumber,
		FeeStartDate: pgtype.Date{Time: wrongStart, Valid: true},
		Active:       true,
	}); err != nil {
		t.Fatalf("UpsertMember: %v", err)
	}
	if err := queries.EnsureDefaultPaymentIdentifier(context.Background(), db.EnsureDefaultPaymentIdentifierParams{
		MemberNumber:   memberNumber,
		VariableSymbol: "900610",
		ValidFrom:      wrongStart,
	}); err != nil {
		t.Fatalf("EnsureDefaultPaymentIdentifier (wrong): %v", err)
	}

	oldTransactionDate := time.Now().AddDate(0, -6, 0)
	seedRawTransactionOnDate(t, queries, account.ID, 6021, "500.00", strPtr("900610"), oldTransactionDate)

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}
	before := queryProcessed(t, pool, 6021)
	if before.category != "other_income" || before.memberNumber != nil {
		t.Fatalf("before correction: got %+v, want unmatched other_income", before)
	}

	// The correction: a real Orca sync re-runs EnsureDefaultPaymentIdentifier
	// with the fixed fee_start_date. Its ON CONFLICT target is
	// (variable_symbol, valid_from), so a changed valid_from inserts a
	// *second* row rather than updating the first — see CLAUDE.md "Orca
	// member sync".
	correctedStart := time.Now().AddDate(-1, 0, 0)
	if err := queries.EnsureDefaultPaymentIdentifier(context.Background(), db.EnsureDefaultPaymentIdentifierParams{
		MemberNumber:   memberNumber,
		VariableSymbol: "900610",
		ValidFrom:      correctedStart,
	}); err != nil {
		t.Fatalf("EnsureDefaultPaymentIdentifier (corrected): %v", err)
	}

	result, err := RematchUnmatched(context.Background(), pool)
	if err != nil {
		t.Fatalf("RematchUnmatched: %v", err)
	}
	if result.Matched != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want {1 0}", result)
	}

	got := queryProcessed(t, pool, 6021)
	if got.category != "membership_fee" {
		t.Errorf("category = %q, want membership_fee", got.category)
	}
	if got.memberNumber == nil || *got.memberNumber != memberNumber {
		t.Errorf("member_number = %v, want %d", got.memberNumber, memberNumber)
	}
	if got.matchedBy == nil || *got.matchedBy != "variable_symbol" {
		t.Errorf("matched_by = %v, want variable_symbol", got.matchedBy)
	}
	if n := coverageMonthsFor(t, pool, memberNumber); n != 1 {
		t.Errorf("payment_coverage rows for member %d = %d, want 1", memberNumber, n)
	}

	// Idempotent: nothing left to rematch on a second call.
	second, err := RematchUnmatched(context.Background(), pool)
	if err != nil {
		t.Fatalf("second RematchUnmatched: %v", err)
	}
	if second.Matched != 0 || second.Failed != 0 {
		t.Errorf("second result = %+v, want {0 0}", second)
	}
}

// TestRematchUnmatched_SkipsManuallyMatchedRows makes sure an admin's
// explicit manual match/category is never silently overridden even if a
// member now happens to resolve for that variable symbol/date.
func TestRematchUnmatched_SkipsManuallyMatchedRows(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccount(t, queries, "9400000022")

	oldTransactionDate := time.Now().AddDate(0, -6, 0)
	seedRawTransactionOnDate(t, queries, account.ID, 6022, "500.00", strPtr("900611"), oldTransactionDate)
	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}
	setMatchedBy(t, pool, 6022, "manual")

	seedMemberWithIdentifier(t, queries, 900611, "900611")

	result, err := RematchUnmatched(context.Background(), pool)
	if err != nil {
		t.Fatalf("RematchUnmatched: %v", err)
	}
	if result.Matched != 0 || result.Failed != 0 {
		t.Fatalf("result = %+v, want {0 0} — manually-matched rows must be skipped", result)
	}

	got := queryProcessed(t, pool, 6022)
	if got.category != "other_income" {
		t.Errorf("category = %q, want unchanged other_income", got.category)
	}
}

// TestRematchUnmatched_SkipsInternalTransfer makes sure a transaction already
// categorized internal_transfer is never reconsidered for member matching,
// even if its variable_symbol happens to coincide with a live member's — same
// precedence rule processOne itself enforces (see
// TestRun_InternalTransferTakesPriorityOverVariableSymbolMatch).
func TestRematchUnmatched_SkipsInternalTransfer(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	accountA := seedBankAccount(t, queries, "9400000023")
	seedBankAccount(t, queries, "9400000024")
	seedMemberWithIdentifier(t, queries, 900612, "900612")

	oldTransactionDate := time.Now().AddDate(0, -6, 0)
	if _, err := queries.InsertRawTransaction(context.Background(), db.InsertRawTransactionParams{
		BankAccountID:        accountA.ID,
		FioTransactionID:     6023,
		TransactionDate:      oldTransactionDate,
		Amount:               "-500.00",
		Currency:             "CZK",
		CounterAccountNumber: strPtr("9400000024"),
		VariableSymbol:       strPtr("900612"),
		RawPayload:           []byte("{}"),
	}); err != nil {
		t.Fatalf("InsertRawTransaction: %v", err)
	}

	if _, err := Run(context.Background(), pool); err != nil {
		t.Fatalf("Run: %v", err)
	}
	before := queryProcessed(t, pool, 6023)
	if before.category != "internal_transfer" {
		t.Fatalf("before: category = %q, want internal_transfer", before.category)
	}

	result, err := RematchUnmatched(context.Background(), pool)
	if err != nil {
		t.Fatalf("RematchUnmatched: %v", err)
	}
	if result.Matched != 0 || result.Failed != 0 {
		t.Fatalf("result = %+v, want {0 0} — internal_transfer rows must be skipped", result)
	}

	got := queryProcessed(t, pool, 6023)
	if got.category != "internal_transfer" || got.memberNumber != nil {
		t.Errorf("got = %+v, want unchanged internal_transfer with no member", got)
	}
}
