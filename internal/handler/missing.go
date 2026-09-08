package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kubik/bank-system/internal/db"
)

type missingPaymentMember struct {
	MemberNumber int32 `json:"member_number"`
	// TotalMissedMonths is the member's unpaid months across their whole
	// liability window, not just the queried month — a contact-priority signal.
	// Always >= 1 for a member in this list; rows come pre-sorted by it,
	// descending (top offenders first).
	TotalMissedMonths int32 `json:"total_missed_months"`
}

// MissingPayments handles GET /payments/{year}/{month}/missing — the members
// who were liable for the membership fee in that calendar month but have no
// payment_coverage row for it (see docs/logic-design.md "Missed Payment
// Detection"). Computed on read, no stored "missing" rows.
func MissingPayments(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, err := strconv.Atoi(r.PathValue("year"))
		if err != nil || year < 1 || year > time.Now().Year()+1 {
			writeError(w, http.StatusBadRequest, "invalid year")
			return
		}
		month, err := strconv.Atoi(r.PathValue("month"))
		if err != nil || month < 1 || month > 12 {
			writeError(w, http.StatusBadRequest, "invalid month")
			return
		}

		rows, err := queries.ListMembersMissingPayment(r.Context(), db.ListMembersMissingPaymentParams{
			Year:  int32(year),
			Month: int32(month),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to compute missing payments")
			return
		}

		out := make([]missingPaymentMember, 0, len(rows))
		for _, row := range rows {
			out = append(out, missingPaymentMember{
				MemberNumber:      row.MemberNumber,
				TotalMissedMonths: row.TotalMissedMonths,
			})
		}

		writeJSON(w, http.StatusOK, out)
	}
}

// MissingPaymentsInYear handles GET /payments/{year}/missing — every member who
// missed at least one liable month during that calendar year. Same
// {member_number, total_missed_months} response and top-offenders-first ordering
// as MissingPayments; total_missed_months is still the full-liability-window
// arrears count, not scoped to the year.
func MissingPaymentsInYear(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, err := strconv.Atoi(r.PathValue("year"))
		if err != nil || year < 1 || year > time.Now().Year()+1 {
			writeError(w, http.StatusBadRequest, "invalid year")
			return
		}

		rows, err := queries.ListMembersMissingPaymentInYear(r.Context(), int32(year))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to compute missing payments")
			return
		}

		out := make([]missingPaymentMember, 0, len(rows))
		for _, row := range rows {
			out = append(out, missingPaymentMember{
				MemberNumber:      row.MemberNumber,
				TotalMissedMonths: row.TotalMissedMonths,
			})
		}

		writeJSON(w, http.StatusOK, out)
	}
}
