package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kubik/bank-system/internal/db"
)

type waiveRequest struct {
	Year   int    `json:"year"`
	Month  int    `json:"month"`
	Reason string `json:"reason"`
	// EndYear/EndMonth are optional — together they turn a single-month waive
	// into a range [Year/Month .. EndYear/EndMonth] inclusive. Both or
	// neither: providing one without the other is a 400.
	EndYear  *int `json:"end_year"`
	EndMonth *int `json:"end_month"`
}

type paymentWaiverResponse struct {
	MemberNumber int32  `json:"member_number"`
	Year         int32  `json:"year"`
	Month        int16  `json:"month"`
	Reason       string `json:"reason"`
	CreatedAt    string `json:"created_at"`
}

// waiveResponse is always the shape WavePayment returns now, whether the
// request was a single month or a range — a single-month call just comes
// back with exactly one Waived entry and an empty Skipped. Skipped lists the
// months in a *range* request that already had a real payment_coverage row
// (see WavePayment) and were therefore left alone rather than waived.
type waiveResponse struct {
	MemberNumber int32                   `json:"member_number"`
	Waived       []paymentWaiverResponse `json:"waived"`
	Skipped      []monthRef              `json:"skipped"`
}

// monthIndex turns a (year, month) pair into a single comparable/steppable
// int (year*12+month) so a range can be walked and ordered without manual
// month-rollover arithmetic scattered through the handler.
func monthIndex(year, month int) int { return year*12 + month }

func monthFromIndex(index int) (year, month int) {
	year = (index - 1) / 12
	month = index - year*12
	return
}

// WaivePayment handles POST /payments/{member_number}/waive — writes off one
// month without a matching transaction: a member who genuinely missed a
// payment years ago shouldn't be
// chased forever, but there's nothing to match, so this records an explicit
// admin decision instead of a payment_coverage row. Once waived, the month
// stops appearing in missing-payment lists (member_arrears and the four
// ListMembersMissingPayment* queries all exclude waived months) but is not
// counted as paid (has_ever_paid still reflects payment_coverage only).
//
// reason is required — with no admin-identity column anywhere in this
// schema, it's the only record of why a debt was written off.
//
// end_year/end_month (optional, both-or-neither) turn this into a range:
// every month from year/month through end_year/end_month inclusive gets the
// same reason. Within a range, a month already covered by a real payment is
// silently *skipped* rather than erroring — the whole point of a range is
// bulk convenience, and one matched month in the middle (e.g. member paid
// April separately) shouldn't block writing off the rest. Without an end
// (single-month request), that same situation is still a 400 as before:
// waiving a paid month would be meaningless and could mask a mismatch worth
// investigating, and a single deliberately-targeted month deserves that
// pushback rather than a silent no-op.
//
// Gated by RoleManageTransactions — the same trust level as manually
// re-assigning a transaction's coverage, since this is the same kind of
// manual override of the payment record.
//
// @Summary      Write off a member's missed month (or range of months)
// @Description  Requires the manage-transactions role. Idempotent per month (repeat calls keep the original reason). Without end_year/end_month, 400 if the month is already covered by a real payment; with them, such months are skipped instead (see the `skipped` field).
// @Tags         payments
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        member_number  path  int                        true  "Member number"
// @Param        request        body  handler.waiveRequest  true  "Month (or start/end range) to waive, and why"
// @Success      200  {object}  handler.waiveResponse
// @Failure      400,401,403  {object}  map[string]string
// @Router       /payments/{member_number}/waive [post]
func WaivePayment(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		memberNumber, ok := parseMemberNumber(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid member_number")
			return
		}

		var request waiveRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		maxYear := time.Now().Year() + 1
		if request.Month < 1 || request.Month > 12 || request.Year < 2000 || request.Year > maxYear {
			writeError(w, http.StatusBadRequest, "invalid year/month")
			return
		}
		reason := strings.TrimSpace(request.Reason)
		if reason == "" {
			writeError(w, http.StatusBadRequest, "reason is required")
			return
		}

		isRange := request.EndYear != nil || request.EndMonth != nil
		endYear, endMonth := request.Year, request.Month
		if isRange {
			if request.EndYear == nil || request.EndMonth == nil {
				writeError(w, http.StatusBadRequest, "end_year and end_month must be provided together")
				return
			}
			endYear, endMonth = *request.EndYear, *request.EndMonth
			if endMonth < 1 || endMonth > 12 || endYear < 2000 || endYear > maxYear {
				writeError(w, http.StatusBadRequest, "invalid end_year/end_month")
				return
			}
			if monthIndex(endYear, endMonth) < monthIndex(request.Year, request.Month) {
				writeError(w, http.StatusBadRequest, "end must be on or after start")
				return
			}
		}

		exists, err := queries.MemberExists(r.Context(), memberNumber)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check member")
			return
		}
		if !exists {
			writeError(w, http.StatusBadRequest, "member_number does not exist")
			return
		}

		response := waiveResponse{MemberNumber: memberNumber, Waived: []paymentWaiverResponse{}, Skipped: []monthRef{}}

		for index := monthIndex(request.Year, request.Month); index <= monthIndex(endYear, endMonth); index++ {
			year, month := monthFromIndex(index)

			alreadyPaid, err := queries.PaymentCoverageExists(r.Context(), db.PaymentCoverageExistsParams{
				MemberNumber: memberNumber,
				CoversYear:   int32(year),
				CoversMonth:  int16(month),
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to check coverage")
				return
			}
			if alreadyPaid {
				if !isRange {
					writeError(w, http.StatusBadRequest, "month is already covered by a payment")
					return
				}
				response.Skipped = append(response.Skipped, monthRef{Year: year, Month: month})
				continue
			}

			if _, err := queries.CreatePaymentWaiver(r.Context(), db.CreatePaymentWaiverParams{
				MemberNumber: memberNumber,
				CoversYear:   int32(year),
				CoversMonth:  int16(month),
				Reason:       reason,
			}); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to create waiver")
				return
			}

			// ON CONFLICT DO NOTHING above means a repeat call for an
			// already-waived month is a no-op — re-fetch either way to return
			// the (first) reason on record, not the one just submitted.
			waiver, err := queries.GetPaymentWaiver(r.Context(), db.GetPaymentWaiverParams{
				MemberNumber: memberNumber,
				CoversYear:   int32(year),
				CoversMonth:  int16(month),
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to load waiver")
				return
			}

			response.Waived = append(response.Waived, paymentWaiverResponse{
				MemberNumber: memberNumber,
				Year:         waiver.CoversYear,
				Month:        waiver.CoversMonth,
				Reason:       waiver.Reason,
				CreatedAt:    waiver.CreatedAt.Format(time.RFC3339),
			})
		}

		writeJSON(w, http.StatusOK, response)
	}
}

// ListWaivers handles GET /payments/waivers — every waiver across every
// member, for the dedicated Waivers admin tab (list + delete + create). Not
// scoped to one member like ListWaiversForMember; gated by
// RoleManageTransactions, same as WaivePayment/UnwaivePayment, since seeing
// which debts were written off and why carries the same trust level as
// writing one off.
//
// @Summary      List every waiver
// @Description  Requires the manage-transactions role. All members, newest-created first.
// @Tags         payments
// @Security     BearerAuth
// @Produce      json
// @Success      200  {array}  handler.paymentWaiverResponse
// @Failure      401,403  {object}  map[string]string
// @Router       /payments/waivers [get]
func ListWaivers(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := queries.ListWaivers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list waivers")
			return
		}

		out := make([]paymentWaiverResponse, 0, len(rows))
		for _, row := range rows {
			out = append(out, paymentWaiverResponse{
				MemberNumber: row.MemberNumber,
				Year:         row.CoversYear,
				Month:        row.CoversMonth,
				Reason:       row.Reason,
				CreatedAt:    row.CreatedAt.Format(time.RFC3339),
			})
		}

		writeJSON(w, http.StatusOK, out)
	}
}

// UnwaivePayment handles DELETE /payments/{member_number}/waive/{year}/{month}
// — undoes a waiver (the admin changed their mind, or waived the wrong
// month). The month reappears in missing-payment lists on the next read,
// same as any other computed-on-read state here. 404 if no such waiver
// exists. Gated by RoleManageTransactions, same as WaivePayment.
//
// @Summary      Undo a waiver
// @Description  Requires the manage-transactions role. The month reappears in missing-payment lists on the next read.
// @Tags         payments
// @Security     BearerAuth
// @Param        member_number  path  int  true  "Member number"
// @Param        year           path  int  true  "Waived year"
// @Param        month          path  int  true  "Waived month, 1-12"
// @Success      204  "no content"
// @Failure      400,401,403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Router       /payments/{member_number}/waive/{year}/{month} [delete]
func UnwaivePayment(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		memberNumber, ok := parseMemberNumber(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid member_number")
			return
		}
		year, err := strconv.Atoi(r.PathValue("year"))
		if err != nil || year < 1 {
			writeError(w, http.StatusBadRequest, "invalid year")
			return
		}
		month, err := strconv.Atoi(r.PathValue("month"))
		if err != nil || month < 1 || month > 12 {
			writeError(w, http.StatusBadRequest, "invalid month")
			return
		}

		rowsAffected, err := queries.DeletePaymentWaiver(r.Context(), db.DeletePaymentWaiverParams{
			MemberNumber: memberNumber,
			CoversYear:   int32(year),
			CoversMonth:  int16(month),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete waiver")
			return
		}
		if rowsAffected == 0 {
			writeError(w, http.StatusNotFound, "no waiver for this member/month")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func parseMemberNumber(r *http.Request) (int32, bool) {
	n, err := strconv.ParseInt(r.PathValue("member_number"), 10, 32)
	if err != nil || n < 1 {
		return 0, false
	}
	return int32(n), true
}
