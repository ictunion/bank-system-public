package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/keycloak"
)

type missingPaymentMember struct {
	MemberNumber int32 `json:"member_number"`
	// TotalMissedMonths is the member's unpaid months across their whole
	// liability window, not just the queried month — a contact-priority signal.
	// Always >= 1 for a member in this list; rows come pre-sorted by it,
	// descending (top offenders first).
	TotalMissedMonths int32 `json:"total_missed_months"`
	// HasEverPaid is false when the member has no payment_coverage row at
	// all — never made a single matched payment, as opposed to usually
	// paying and missing one month. In practice a common, distinct case
	// (e.g. a member who never received/actioned their bank details) that
	// needs different follow-up than an occasional miss, so callers can
	// filter on it instead of inferring it from total_missed_months alone.
	HasEverPaid bool `json:"has_ever_paid"`
}

// MissingPayments handles GET /payments/{year}/{month}/missing — the members
// who were liable for the membership fee in that calendar month but have no
// payment_coverage row for it (see docs/logic-design.md "Missed Payment
// Detection"). Computed on read, no stored "missing" rows.
//
// @Summary      Members missing a payment for one month
// @Description  Requires the payment-history role. A waived month (see docs/logic-design.md "Payment Waivers") is excluded from this list, same as a paid one.
// @Tags         payments
// @Security     BearerAuth
// @Produce      json
// @Param        year   path  int  true  "1 to current year + 1"
// @Param        month  path  int  true  "1 to 12"
// @Success      200  {array}  handler.missingPaymentMember
// @Failure      400,401,403  {object}  map[string]string
// @Router       /payments/{year}/{month}/missing [get]
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
				HasEverPaid:       row.HasEverPaid,
			})
		}

		writeJSON(w, http.StatusOK, out)
	}
}

// MissingPaymentsInYear handles GET /payments/{year}/missing — every member who
// missed at least one liable month during that calendar year. Same
// {member_number, total_missed_months, has_ever_paid} response and
// top-offenders-first ordering as MissingPayments; total_missed_months/
// has_ever_paid are still the full-liability-window figures, not scoped to
// the year.
//
// @Summary      Members who missed at least one payment in a year
// @Description  Requires the payment-history role. total_missed_months/has_ever_paid are full-liability-window figures, not scoped to the queried year.
// @Tags         payments
// @Security     BearerAuth
// @Produce      json
// @Param        year  path  int  true  "1 to current year + 1"
// @Success      200  {array}  handler.missingPaymentMember
// @Failure      400,401,403  {object}  map[string]string
// @Router       /payments/{year}/missing [get]
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
				HasEverPaid:       row.HasEverPaid,
			})
		}

		writeJSON(w, http.StatusOK, out)
	}
}

// WorkplaceMissingPayments handles GET /payments/workplace/{year}/{month}/missing —
// the workplace-rep counterpart to MissingPayments, scoped to members sharing
// any of the caller's Keycloak groups (looked up live via
// Provider.UserGroupIDs) via workplace_executive_committee_sub instead of
// every member. See docs/logic-design.md "Workplace-Scoped Payment History".
// A caller in no workplace groups gets an empty list, not an error.
//
// @Summary      Workplace-scoped: members missing a payment for one month
// @Description  Requires the view-workplace-payment-history role. Scoped live to the caller's own Keycloak workplace group(s) via the Account API.
// @Tags         payments
// @Security     BearerAuth
// @Produce      json
// @Param        year   path  int  true  "1 to current year + 1"
// @Param        month  path  int  true  "1 to 12"
// @Success      200  {array}  handler.missingPaymentMember
// @Failure      400,401,403  {object}  map[string]string
// @Failure      500  {object}  map[string]string  "Keycloak Account API call failed"
// @Router       /payments/workplace/{year}/{month}/missing [get]
func WorkplaceMissingPayments(provider *keycloak.Provider, queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := TokenFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusInternalServerError, "missing token")
			return
		}

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

		groups, err := provider.UserGroupIDs(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check workplace membership")
			return
		}
		workplaceSubs := workplaceGroupUUIDs(groups)
		if len(workplaceSubs) == 0 {
			writeJSON(w, http.StatusOK, []missingPaymentMember{})
			return
		}

		rows, err := queries.ListMembersMissingPaymentForWorkplace(r.Context(), db.ListMembersMissingPaymentForWorkplaceParams{
			WorkplaceSubs: workplaceSubs,
			Year:          int32(year),
			Month:         int32(month),
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
				HasEverPaid:       row.HasEverPaid,
			})
		}

		writeJSON(w, http.StatusOK, out)
	}
}

// WorkplaceMissingPaymentsInYear handles GET /payments/workplace/{year}/missing
// — the workplace-rep, whole-year counterpart to MissingPaymentsInYear. See
// WorkplaceMissingPayments.
//
// @Summary      Workplace-scoped: members who missed a payment in a year
// @Description  Requires the view-workplace-payment-history role. Scoped live to the caller's own Keycloak workplace group(s) via the Account API.
// @Tags         payments
// @Security     BearerAuth
// @Produce      json
// @Param        year  path  int  true  "1 to current year + 1"
// @Success      200  {array}  handler.missingPaymentMember
// @Failure      400,401,403  {object}  map[string]string
// @Failure      500  {object}  map[string]string  "Keycloak Account API call failed"
// @Router       /payments/workplace/{year}/missing [get]
func WorkplaceMissingPaymentsInYear(provider *keycloak.Provider, queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := TokenFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusInternalServerError, "missing token")
			return
		}

		year, err := strconv.Atoi(r.PathValue("year"))
		if err != nil || year < 1 || year > time.Now().Year()+1 {
			writeError(w, http.StatusBadRequest, "invalid year")
			return
		}

		groups, err := provider.UserGroupIDs(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check workplace membership")
			return
		}
		workplaceSubs := workplaceGroupUUIDs(groups)
		if len(workplaceSubs) == 0 {
			writeJSON(w, http.StatusOK, []missingPaymentMember{})
			return
		}

		rows, err := queries.ListMembersMissingPaymentInYearForWorkplace(r.Context(), db.ListMembersMissingPaymentInYearForWorkplaceParams{
			WorkplaceSubs: workplaceSubs,
			Year:          int32(year),
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
				HasEverPaid:       row.HasEverPaid,
			})
		}

		writeJSON(w, http.StatusOK, out)
	}
}
