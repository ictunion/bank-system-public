// Command seed inserts local fixture data — a handful of members and their
// raw_transactions — directly into Postgres, bypassing both Fio and Orca
// entirely. Local dev has no real Fio bank account/token and no real Orca
// instance, so without this there's no way to see the app's actual features
// (transaction browser, missed-payment detection, waivers, budget view)
// working against real-looking data. Run via `make seed`.
//
// Every insert here is idempotent (ON CONFLICT DO NOTHING / upsert), keyed
// on fixed member numbers and fio_transaction_ids, so re-running is safe and
// just a no-op on rows that already exist.
//
// After inserting raw_transactions, this runs the real processing.Run —
// the same matching/categorization function the daily sync job calls — so
// payment_coverage and processed_transactions end up exactly as they would
// from a real sync, without duplicating that logic here.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/processing"
)

// seedFioAccountID marks the one bank_accounts row this command owns —
// looked up by this value so re-running reuses it instead of creating a
// duplicate.
const seedFioAccountID = "SEED-FIXTURE-0001"

// fixtureMember describes one seeded member's payment history in terms of
// "months ago" so the fixture stays meaningful regardless of when `make
// seed` is run — feeStartMonthsAgo/feeStopMonthsAgo/paidMonthsAgo/
// waivedMonthsAgo are all offsets from the current month.
type fixtureMember struct {
	number            int32
	label             string
	feeStartMonthsAgo int
	feeStopMonthsAgo  int // 0 = still liable (no fee_stop_date)
	paidMonthsAgo     []int
	waivedMonthsAgo   []int
}

var fixtureMembers = []fixtureMember{
	{number: 900001, label: "Paying Pat — pays every month, nothing missing",
		feeStartMonthsAgo: 6, paidMonthsAgo: []int{6, 5, 4, 3, 2}},
	{number: 900002, label: "Never-Paid Nora — liable for months, has_ever_paid=false",
		feeStartMonthsAgo: 6},
	{number: 900003, label: "Sometimes Sam — paid some months, missed others",
		feeStartMonthsAgo: 6, paidMonthsAgo: []int{6, 4}},
	{number: 900004, label: "Departed Dana — left partway through, no longer expected to pay",
		feeStartMonthsAgo: 5, feeStopMonthsAgo: 3, paidMonthsAgo: []int{5, 4, 3}},
	{number: 900005, label: "Waived Wendy — one missed month written off, one still open",
		feeStartMonthsAgo: 6, paidMonthsAgo: []int{6, 4}, waivedMonthsAgo: []int{5}},
}

func main() {
	_ = godotenv.Load()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}
	encryptionKey := os.Getenv("BANK_TOKEN_ENCRYPTION_KEY")
	if encryptionKey == "" {
		log.Fatal("BANK_TOKEN_ENCRYPTION_KEY environment variable is required")
	}

	requestContext := context.Background()
	pool, err := pgxpool.New(requestContext, databaseURL)
	if err != nil {
		log.Fatalf("db pool: %v", err)
	}
	defer pool.Close()

	queries := db.New(pool)

	bankAccountID, err := seedBankAccount(requestContext, queries, encryptionKey)
	if err != nil {
		log.Fatalf("seeding bank account: %v", err)
	}

	// Deterministic per member/purpose so re-running never inserts
	// duplicates (raw_transactions dedupes on (bank_account_id,
	// fio_transaction_id)).
	fioTransactionID := int64(900_000_000)
	for _, m := range fixtureMembers {
		if err := seedMember(requestContext, queries, m); err != nil {
			log.Fatalf("seeding member %d: %v", m.number, err)
		}
		for _, monthsAgo := range m.paidMonthsAgo {
			fioTransactionID++
			if err := seedPaymentTransaction(requestContext, queries, bankAccountID, fioTransactionID, m.number, monthsAgo); err != nil {
				log.Fatalf("seeding payment for member %d: %v", m.number, err)
			}
		}
		for _, monthsAgo := range m.waivedMonthsAgo {
			year, month := coversYearMonth(monthsAgo)
			if _, err := queries.CreatePaymentWaiver(requestContext, db.CreatePaymentWaiverParams{
				MemberNumber: m.number,
				CoversYear:   year,
				CoversMonth:  month,
				Reason:       "seed fixture: one-off miss, too old to chase",
			}); err != nil {
				log.Fatalf("seeding waiver for member %d: %v", m.number, err)
			}
		}
	}

	fioTransactionID++
	if err := seedSalaryTransaction(requestContext, queries, bankAccountID, fioTransactionID); err != nil {
		log.Fatalf("seeding salary transaction: %v", err)
	}
	fioTransactionID++
	if err := seedUnmatchedTransaction(requestContext, queries, bankAccountID, fioTransactionID); err != nil {
		log.Fatalf("seeding unmatched transaction: %v", err)
	}

	result, err := processing.Run(requestContext, pool)
	if err != nil {
		log.Fatalf("running processing: %v", err)
	}

	fmt.Printf("seeded %d fixture members (processed=%d failed=%d):\n", len(fixtureMembers), result.TransactionsProcessed, result.TransactionsFailed)
	for _, m := range fixtureMembers {
		fmt.Printf("  %d — %s\n", m.number, m.label)
	}
}

// seedBankAccount creates the one fixture bank_accounts row this command
// owns, or reuses it if `make seed` already ran before — CreateBankAccount
// has no ON CONFLICT of its own (see internal/handler/account.go, which
// handles the resulting unique-violation as a 409 instead), so this checks
// first rather than relying on one.
func seedBankAccount(requestContext context.Context, queries *db.Queries, encryptionKey string) (int32, error) {
	accounts, err := queries.ListBankAccounts(requestContext)
	if err != nil {
		return 0, fmt.Errorf("listing bank accounts: %w", err)
	}
	for _, account := range accounts {
		if account.FioAccountID == seedFioAccountID {
			return account.ID, nil
		}
	}

	account, err := queries.CreateBankAccount(requestContext, db.CreateBankAccountParams{
		FioAccountID:  seedFioAccountID,
		Currency:      "CZK",
		DisplayName:   "Seed Fixture Account (not a real Fio account)",
		FioToken:      "seed-fixture-token-never-used",
		EncryptionKey: encryptionKey,
	})
	if err != nil {
		return 0, fmt.Errorf("creating bank account: %w", err)
	}
	return account.ID, nil
}

// seedMember upserts one fixture member and, mirroring what the real Orca
// sync does on every run (see CLAUDE.md "Orca member sync"), seeds their
// default payment identifier (variable_symbol = member_number) so
// processing.Run's variable-symbol matching actually finds them.
func seedMember(requestContext context.Context, queries *db.Queries, m fixtureMember) error {
	feeStart := monthOffset(m.feeStartMonthsAgo)

	var feeStop pgtype.Date
	if m.feeStopMonthsAgo > 0 {
		feeStop = pgtype.Date{Time: monthOffset(m.feeStopMonthsAgo), Valid: true}
	}

	if err := queries.UpsertMember(requestContext, db.UpsertMemberParams{
		MemberNumber: m.number,
		FeeStartDate: pgtype.Date{Time: feeStart, Valid: true},
		FeeStopDate:  feeStop,
		Active:       true,
	}); err != nil {
		return fmt.Errorf("upserting member: %w", err)
	}

	if err := queries.EnsureDefaultPaymentIdentifier(requestContext, db.EnsureDefaultPaymentIdentifierParams{
		MemberNumber:   m.number,
		VariableSymbol: strconv.Itoa(int(m.number)),
		ValidFrom:      feeStart,
	}); err != nil {
		return fmt.Errorf("ensuring default payment identifier: %w", err)
	}
	return nil
}

// seedPaymentTransaction inserts one incoming, VS-matched raw_transactions
// row for coveredMonthsAgo — dated the month *after* the covered month, same
// arrears convention processing.processOne uses for real payments (see
// CLAUDE.md "Transaction processing").
func seedPaymentTransaction(requestContext context.Context, queries *db.Queries, bankAccountID int32, fioTransactionID int64, memberNumber int32, coveredMonthsAgo int) error {
	transactionDate := monthOffset(coveredMonthsAgo - 1).AddDate(0, 0, 24) // the 25th
	variableSymbol := strconv.Itoa(int(memberNumber))
	counterName := fmt.Sprintf("Seed Member %d", memberNumber)

	_, err := queries.InsertRawTransaction(requestContext, db.InsertRawTransactionParams{
		BankAccountID:      bankAccountID,
		FioTransactionID:   fioTransactionID,
		TransactionDate:    transactionDate,
		Amount:             "300.00",
		Currency:           "CZK",
		VariableSymbol:     &variableSymbol,
		CounterAccountName: &counterName,
		RawPayload:         []byte("{}"),
	})
	return err
}

// seedSalaryTransaction inserts one outgoing transaction whose comment
// substring-matches processing.processOne's salary detection ("mzda"), so
// the budget view has something in the salary category.
func seedSalaryTransaction(requestContext context.Context, queries *db.Queries, bankAccountID int32, fioTransactionID int64) error {
	comment := "mzda za mesic"
	_, err := queries.InsertRawTransaction(requestContext, db.InsertRawTransactionParams{
		BankAccountID:    bankAccountID,
		FioTransactionID: fioTransactionID,
		TransactionDate:  monthOffset(1).AddDate(0, 0, 4),
		Amount:           "-45000.00",
		Currency:         "CZK",
		Comment:          &comment,
		RawPayload:       []byte("{}"),
	})
	return err
}

// seedUnmatchedTransaction inserts one incoming transaction with no
// variable symbol — falls through to other_income, unassigned, so the
// transaction browser's "unassigned" worklist has something to show.
func seedUnmatchedTransaction(requestContext context.Context, queries *db.Queries, bankAccountID int32, fioTransactionID int64) error {
	counterName := "Unknown Sender"
	message := "dar"
	_, err := queries.InsertRawTransaction(requestContext, db.InsertRawTransactionParams{
		BankAccountID:       bankAccountID,
		FioTransactionID:    fioTransactionID,
		TransactionDate:     monthOffset(1).AddDate(0, 0, 9),
		Amount:              "150.00",
		Currency:            "CZK",
		CounterAccountName:  &counterName,
		MessageForRecipient: &message,
		RawPayload:          []byte("{}"),
	})
	return err
}

// monthOffset returns the first day of the month n months before the
// current one (UTC) — every fixture date is expressed as an offset from
// "now" rather than a fixed date, so the seed stays meaningful (and its
// liability-window math keeps lining up with member_arrears' own
// date_trunc('month', CURRENT_DATE) arithmetic) no matter when it's run.
func monthOffset(n int) time.Time {
	now := time.Now().UTC()
	firstOfThisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return firstOfThisMonth.AddDate(0, -n, 0)
}

func coversYearMonth(monthsAgo int) (int32, int16) {
	t := monthOffset(monthsAgo)
	return int32(t.Year()), int16(t.Month())
}
