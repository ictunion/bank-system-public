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
}

type paymentWaiverResponse struct {
	MemberNumber int32  `json:"member_number"`
	Year         int32  `json:"year"`
	Month        int16  `json:"month"`
	Reason       string `json:"reason"`
	CreatedAt    string `json:"created_at"`
}

// WaivePayment handles POST /payments/{member_number}/waive — writes off one
// month without a matching transaction (see docs/logic-design.md "Payment
// Waivers"): a member who genuinely missed a payment years ago shouldn't be
// chased forever, but there's nothing to match, so this records an explicit
// admin decision instead of a payment_coverage row. Once waived, the month
// stops appearing in missing-payment lists (member_arrears and the four
// ListMembersMissingPayment* queries all exclude waived months) but is not
// counted as paid (has_ever_paid still reflects payment_coverage only).
//
// reason is required — with no admin-identity column anywhere in this
// schema, it's the only record of why a debt was written off. A month
// already covered by a real payment is a 400: waiving it would be
// meaningless, and could mask a mismatch worth investigating instead.
// Gated by RoleManageTransactions — the same trust level as manually
// re-assigning a transaction's coverage, since this is the same kind of
// manual override of the payment record.
//
// @Summary      Write off a member's missed month
// @Description  Requires the manage-transactions role. Idempotent (repeat calls keep the original reason). 400 if the month is already covered by a real payment.
// @Tags         payments
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        member_number  path  int                        true  "Member number"
// @Param        request        body  handler.waiveRequest  true  "Month to waive, and why"
// @Success      200  {object}  handler.paymentWaiverResponse
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

		exists, err := queries.MemberExists(r.Context(), memberNumber)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check member")
			return
		}
		if !exists {
			writeError(w, http.StatusBadRequest, "member_number does not exist")
			return
		}

		alreadyPaid, err := queries.PaymentCoverageExists(r.Context(), db.PaymentCoverageExistsParams{
			MemberNumber: memberNumber,
			CoversYear:   int32(request.Year),
			CoversMonth:  int16(request.Month),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check coverage")
			return
		}
		if alreadyPaid {
			writeError(w, http.StatusBadRequest, "month is already covered by a payment")
			return
		}

		if _, err := queries.CreatePaymentWaiver(r.Context(), db.CreatePaymentWaiverParams{
			MemberNumber: memberNumber,
			CoversYear:   int32(request.Year),
			CoversMonth:  int16(request.Month),
			Reason:       reason,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create waiver")
			return
		}

		// ON CONFLICT DO NOTHING above means a repeat call for an already-waived
		// month is a no-op — re-fetch either way to return the (first) reason on
		// record, not the one just submitted.
		waiver, err := queries.GetPaymentWaiver(r.Context(), db.GetPaymentWaiverParams{
			MemberNumber: memberNumber,
			CoversYear:   int32(request.Year),
			CoversMonth:  int16(request.Month),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load waiver")
			return
		}

		writeJSON(w, http.StatusOK, paymentWaiverResponse{
			MemberNumber: memberNumber,
			Year:         waiver.CoversYear,
			Month:        waiver.CoversMonth,
			Reason:       waiver.Reason,
			CreatedAt:    waiver.CreatedAt.Format(time.RFC3339),
		})
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
