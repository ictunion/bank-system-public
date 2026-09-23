package handler

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/processing"
)

type processingResultResponse struct {
	TransactionsProcessed int `json:"transactions_processed"`
	TransactionsFailed    int `json:"transactions_failed"`
}

// TriggerProcessing handles POST /processing/run — an admin "run processing"
// button that re-runs transaction processing/matching on demand, independent
// of any one bank account. On top of the scheduled daily cycle (see
// cmd/server/main.go) and the synchronous run TriggerFioSync/BackfillAccount
// already do right after a sync, this covers cases where waiting for the
// next raw_transactions insert isn't good enough — e.g. after manually
// editing member_payment_identifiers, or after a matching-logic change.
// processing.Run only ever touches raw_transactions rows that don't have a
// processed_transactions row yet, so this is idempotent and safe to call any
// time.
//
// @Summary      Trigger transaction processing/matching on demand
// @Description  Requires the manage-bank-accounts role. Processes every raw_transactions row that doesn't have a processed_transactions row yet — idempotent, safe to call any time, not scoped to one bank account.
// @Tags         transactions
// @Security     BearerAuth
// @Produce      json
// @Success      200  {object}  handler.processingResultResponse
// @Failure      401,403  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /processing/run [post]
func TriggerProcessing(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := processing.Run(r.Context(), pool)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "processing failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, processingResultResponse{
			TransactionsProcessed: result.TransactionsProcessed,
			TransactionsFailed:    result.TransactionsFailed,
		})
	}
}
