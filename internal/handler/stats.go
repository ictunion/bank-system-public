package handler

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/bank-system/internal/categoryid"
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
// trailing rolling month plus 3 days of weekend/holiday slack
// (window_from/window_to, both inclusive — e.g. queried April 15th, that's
// March 12th through April 15th). Deliberately
// NOT the arrears/coverage-month convention the rest of this file's payment
// endpoints use — see GetPaymentStats in queries.sql. No params: always a
// live "as of now" snapshot, Orca is expected to poll it rather than link to
// a specific window.
//
// @Summary      Org-wide % of members paying dues in the trailing month
// @Description  Requires the payment-history role. window_from/window_to are the trailing rolling month plus 3 days of slack for standing orders booked late over a weekend/holiday (today minus 1 calendar month minus 3 days through today, both inclusive) — no year/month params, always a live "as of now" snapshot. Counts a real transaction.transaction_date landing in that window, not the arrears/coverage-month convention the other payment endpoints use.
// @Tags         payments
// @Security     BearerAuth
// @Produce      json
// @Success      200  {object}  handler.paymentStats
// @Failure      401,403  {object}  map[string]string
// @Router       /payments/stats [get]
func PaymentStats(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		row, err := queries.GetPaymentStats(r.Context(), categoryid.MembershipFee)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to compute payment stats")
			return
		}

		writeJSON(w, http.StatusOK, newPaymentStats(row.WindowFrom, row.WindowTo, row.LiableMembers, row.PaidMembers))
	}
}

func newPaymentStats(windowFrom, windowTo time.Time, liableMembers, paidMembers int32) paymentStats {
	stats := paymentStats{
		WindowFrom:    windowFrom.Format("2006-01-02"),
		WindowTo:      windowTo.Format("2006-01-02"),
		LiableMembers: liableMembers,
		PaidMembers:   paidMembers,
	}
	if liableMembers > 0 {
		percent := float64(paidMembers) / float64(liableMembers) * 100
		stats.PercentPaid = &percent
	}
	return stats
}

// PaymentStatsHistory handles GET /payments/stats/history — what
// GET /payments/stats would have returned on each day from `from` through
// `to` (both inclusive, default: the last 365 days ending today; a `to` in the
// future is capped to today), oldest first. Each entry's window_to is that day.
// No range cap — the role is granted to few people; the query costs ~1.5 ms
// per day at ~800 members.
//
// @Summary      Daily history of the org-wide % of members paying dues
// @Description  Requires the payment-history role. One entry per day from `from` through `to`, oldest first: each is what GET /payments/stats would have returned on that day (window_to = that day, window_from = one calendar month and 3 days earlier, liable = liable on that day). Defaults: to = today, from = to minus 364 days (365 entries). A to in the future is capped to today; from after (capped) to is a 400.
// @Tags         payments
// @Security     BearerAuth
// @Produce      json
// @Param        from  query  string  false  "YYYY-MM-DD, inclusive. Default: to minus 364 days"
// @Param        to    query  string  false  "YYYY-MM-DD, inclusive. Default and maximum: today"
// @Success      200  {array}   handler.paymentStats
// @Failure      400,401,403  {object}  map[string]string
// @Router       /payments/stats/history [get]
func PaymentStatsHistory(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		queryParams := r.URL.Query()
		dateFrom, ok := dateParam(queryParams.Get("from"))
		if !ok {
			writeError(w, http.StatusBadRequest, "from must be YYYY-MM-DD")
			return
		}
		dateTo, ok := dateParam(queryParams.Get("to"))
		if !ok {
			writeError(w, http.StatusBadRequest, "to must be YYYY-MM-DD")
			return
		}
		now := time.Now()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		if !dateTo.Valid || dateTo.Time.After(today) {
			dateTo = pgtype.Date{Time: today, Valid: true}
		}
		if !dateFrom.Valid {
			dateFrom = pgtype.Date{Time: dateTo.Time.AddDate(0, 0, -364), Valid: true}
		}
		if dateFrom.Time.After(dateTo.Time) {
			writeError(w, http.StatusBadRequest, "from must not be after to")
			return
		}

		rows, err := queries.ListDailyPaymentStats(r.Context(), db.ListDailyPaymentStatsParams{
			MembershipFeeCategoryID: categoryid.MembershipFee,
			DateFrom:                dateFrom.Time,
			DateTo:                  dateTo.Time,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to compute payment stats history")
			return
		}

		history := make([]paymentStats, 0, len(rows))
		for _, row := range rows {
			history = append(history, newPaymentStats(row.WindowFrom, row.WindowTo, row.LiableMembers, row.PaidMembers))
		}
		writeJSON(w, http.StatusOK, history)
	}
}
