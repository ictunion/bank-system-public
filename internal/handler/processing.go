package handler

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/keycloak"
	"github.com/kubik/bank-system/internal/processing"
)

type processingRunRequest struct {
	Action string `json:"action"`
}

// processingActionResponse is the one response shape every action returns —
// Succeeded/Failed means whatever that action's own unit of work is
// (transactions processed for "run", transactions reclassified for
// "reclassify_internal_transfers"). Keeping one generic {action, succeeded,
// failed} contract, rather than a bespoke shape per action, is what lets new
// actions get added here later without changing the response contract too.
type processingActionResponse struct {
	Action    string `json:"action"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
}

// RunProcessing handles POST /processing/run — a single admin-triggered entry
// point for on-demand processing actions, dispatched on the JSON body's
// "action" field rather than one route per action, since more actions are
// expected to be added here over time (see cmd/server/main.go) and each one
// would otherwise need its own route wired through the same
// RequireAnyRole/role-check boilerplate. Route-level auth
// (RequireAnyRole(RoleManageBankAccounts, RoleManageTransactions)) only
// establishes the caller holds *some* processing-admin capability; each
// action below checks the specific role it actually needs itself, since
// different actions can require different roles (a plain re-match is a
// bank-accounts-admin concern, but reclassifying already-processed rows is a
// manage-transactions write, same as PUT /transactions/{id}/assignment).
//
// @Summary      Run an on-demand processing action
// @Description  Requires manage-bank-accounts or manage-transactions (which, depends on the action). Body selects the action: "run" (re-run matching for unprocessed transactions, needs manage-bank-accounts), "reclassify_internal_transfers" (re-check already-processed transactions against the current bank_accounts roster, needs manage-transactions), or "rematch_unmatched" (re-check already-processed-but-unmatched transactions against member_payment_identifiers, needs manage-transactions).
// @Tags         transactions
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        request  body  handler.processingRunRequest  true  "Which action to run"
// @Success      200  {object}  handler.processingActionResponse
// @Failure      400,401,403  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /processing/run [post]
func RunProcessing(pool *pgxpool.Pool, provider *keycloak.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request processingRunRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusForbidden, "not authorized")
			return
		}

		switch request.Action {
		case "run":
			if !provider.HasRole(claims, keycloak.RoleManageBankAccounts) {
				writeError(w, http.StatusForbidden, "not authorized")
				return
			}
			result, err := processing.Run(r.Context(), pool)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "processing failed: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, processingActionResponse{
				Action:    request.Action,
				Succeeded: result.TransactionsProcessed,
				Failed:    result.TransactionsFailed,
			})

		case "reclassify_internal_transfers":
			if !provider.HasRole(claims, keycloak.RoleManageTransactions) {
				writeError(w, http.StatusForbidden, "not authorized")
				return
			}
			result, err := processing.ReclassifyInternalTransfers(r.Context(), pool)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "reclassification failed: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, processingActionResponse{
				Action:    request.Action,
				Succeeded: result.Reclassified,
				Failed:    result.Failed,
			})

		case "rematch_unmatched":
			if !provider.HasRole(claims, keycloak.RoleManageTransactions) {
				writeError(w, http.StatusForbidden, "not authorized")
				return
			}
			result, err := processing.RematchUnmatched(r.Context(), pool)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "rematch failed: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, processingActionResponse{
				Action:    request.Action,
				Succeeded: result.Matched,
				Failed:    result.Failed,
			})

		default:
			writeError(w, http.StatusBadRequest, "unknown action: "+request.Action)
		}
	}
}
