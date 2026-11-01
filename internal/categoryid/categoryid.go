// Package categoryid holds the fixed ids of the six mandatory transaction
// categories. Every other category has a random id, but these are assigned
// by migrations/20261101000005_category_ids.sql and code refers to them
// directly — processing's built-in rules and fallback, the summary's
// internal-transfer exclusion, the membership-fee checks. Their names are
// only labels and may change; these ids never do.
package categoryid

import "github.com/jackc/pgx/v5/pgtype"

var (
	MembershipFee    = mustParse("00000000-0000-4000-8000-000000000001")
	Salary           = mustParse("00000000-0000-4000-8000-000000000002")
	OtherIncome      = mustParse("00000000-0000-4000-8000-000000000003")
	OtherExpense     = mustParse("00000000-0000-4000-8000-000000000004")
	InternalTransfer = mustParse("00000000-0000-4000-8000-000000000005")
	CreditedInterest = mustParse("00000000-0000-4000-8000-000000000006")
)

// Parse reads a category id from its text form (e.g. a path or query value).
func Parse(value string) (pgtype.UUID, bool) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil || !id.Valid {
		return pgtype.UUID{}, false
	}
	return id, true
}

func mustParse(value string) pgtype.UUID {
	id, ok := Parse(value)
	if !ok {
		panic("categoryid: invalid fixed id " + value)
	}
	return id
}
