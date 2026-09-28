package handler

import (
	"net/http"

	"github.com/kubik/bank-system/internal/db"
)

// paymentStats is GetPaymentStats's row, JSON-shaped for the API. Percentage
// is computed here rather than in SQL (float rounding is simpler in Go, and
// the raw counts are included too so a caller that wants to compute it
// differently — e.g. more decimal places — doesn't have to). PercentPaid is
// nil when LiableMembers is 0 (a brand new org with no currently-liable
// members) — avoids a division by zero reading as a misleading "0% paying"
// instead of "not applicable".
type paymentStats struct {
	WindowFrom    string   `json:"window_from"`
	WindowTo      string   `json:"window_to"`
	LiableMembers int32    `json:"liable_members"`
	PaidMembers   int32    `json:"paid_members"`
	PercentPaid   *float64 `json:"percent_paid"`
}

// PaymentStats handles GET /payments/stats — the org-wide % of
// currently-liable members who actually sent a membership_fee payment in the
// trailing rolling month (window_from/window_to, both inclusive — e.g.
// queried April 15th, that's March 15th through April 15th). Deliberately
// NOT the arrears/coverage-month convention the rest of this file's payment
// endpoints use — see GetPaymentStats in queries.sql. No params: always a
// live "as of now" snapshot, Orca is expected to poll it rather than link to
// a specific window.
//
// @Summary      Org-wide % of members paying dues in the trailing month
// @Description  Requires the payment-history role. window_from/window_to are the trailing rolling month (today minus 1 calendar month through today, both inclusive) — no year/month params, always a live "as of now" snapshot. Counts a real transaction.transaction_date landing in that window, not the arrears/coverage-month convention the other payment endpoints use.
// @Tags         payments
// @Security     BearerAuth
// @Produce      json
// @Success      200  {object}  handler.paymentStats
// @Failure      401,403  {object}  map[string]string
// @Router       /payments/stats [get]
func PaymentStats(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		row, err := queries.GetPaymentStats(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to compute payment stats")
			return
		}

		stats := paymentStats{
			WindowFrom:    row.WindowFrom.Format("2006-01-02"),
			WindowTo:      row.WindowTo.Format("2006-01-02"),
			LiableMembers: row.LiableMembers,
			PaidMembers:   row.PaidMembers,
		}
		if row.LiableMembers > 0 {
			percent := float64(row.PaidMembers) / float64(row.LiableMembers) * 100
			stats.PercentPaid = &percent
		}

		writeJSON(w, http.StatusOK, stats)
	}
}
