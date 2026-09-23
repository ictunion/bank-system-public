package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/processing"
	"github.com/kubik/bank-system/internal/syncjob"
)

// numericToStringPtr renders a nullable pgtype.Numeric the same way every
// other amount in this API is represented — a JSON string (avoids JS
// floating-point precision loss), nil when the column is NULL. Needed
// because the plain-string sqlc override for numeric only applies to NOT
// NULL columns (see bank_accounts.balance in queries.sql).
func numericToStringPtr(n pgtype.Numeric) *string {
	if !n.Valid {
		return nil
	}
	value, err := n.Value()
	if err != nil {
		return nil
	}
	s, _ := value.(string)
	return &s
}

// timestamptzToStringPtr is BankAccount.BalanceAsOf's equivalent of
// numericToStringPtr — nullable timestamptz also falls back to
// pgtype.Timestamptz rather than the NOT-NULL-only time.Time override.
func timestamptzToStringPtr(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.Format(time.RFC3339)
	return &s
}

type createBankAccountRequest struct {
	FioAccountID string  `json:"fio_account_id"`
	IBAN         *string `json:"iban"`
	Currency     string  `json:"currency"`
	DisplayName  string  `json:"display_name"`
	FioToken     string  `json:"fio_token"`
}

// updateBankAccountRequest covers the only two things about a bank account
// that are actually ours to edit: our own label for it, and the credential we
// use to sync it. fio_account_id/iban/currency are properties of the real
// bank account (assigned by Fio), not editable metadata. FioToken is a
// pointer: nil means "leave the existing token untouched", set means
// "replace it" — the admin UI only sends it on a deliberate token rotation,
// not on every save.
type updateBankAccountRequest struct {
	DisplayName string  `json:"display_name"`
	FioToken    *string `json:"fio_token"`
}

type bankAccountResponse struct {
	ID           int32     `json:"id"`
	FioAccountID string    `json:"fio_account_id"`
	IBAN         *string   `json:"iban"`
	Currency     string    `json:"currency"`
	DisplayName  string    `json:"display_name"`
	CreatedAt    time.Time `json:"created_at"`
	HasToken     bool      `json:"has_token"`
	IsActive     bool      `json:"is_active"`
	// Balance/BalanceAsOf are both nil until the account's first successful
	// sync — see bank_accounts.balance in "Database schema" (CLAUDE.md).
	Balance     *string `json:"balance"`
	BalanceAsOf *string `json:"balance_as_of"`
}

// ListBankAccounts handles GET /account — lists registered bank accounts for
// the admin UI, including soft-deleted ones (IsActive false) so admins still
// see the historical record. Never includes the Fio token itself, only
// whether one is set.
//
// @Summary      List bank accounts
// @Description  Requires the manage-bank-accounts role. Excludes the Fio token itself, only whether one is set. balance/balance_as_of are both null until the account's first successful sync.
// @Tags         accounts
// @Security     BearerAuth
// @Produce      json
// @Success      200  {array}  handler.bankAccountResponse
// @Failure      401,403  {object}  map[string]string
// @Router       /account [get]
func ListBankAccounts(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accounts, err := queries.ListBankAccounts(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list bank accounts")
			return
		}

		response := make([]bankAccountResponse, len(accounts))
		for i, account := range accounts {
			response[i] = bankAccountResponse{
				ID:           account.ID,
				FioAccountID: account.FioAccountID,
				IBAN:         account.Iban,
				Currency:     account.Currency,
				DisplayName:  account.DisplayName,
				CreatedAt:    account.CreatedAt,
				HasToken:     account.HasToken,
				IsActive:     account.IsActive,
				Balance:      numericToStringPtr(account.Balance),
				BalanceAsOf:  timestamptzToStringPtr(account.BalanceAsOf),
			}
		}
		writeJSON(w, http.StatusOK, response)
	}
}

// CreateBankAccount handles POST /account — registers a bank_accounts row so
// the Fio sync job (internal/syncjob) has something to sync against, including
// the Fio API token it should use (encrypted at rest, see queries.sql). This is
// a one-time-per-account setup call, not part of the daily sync flow.
//
// @Summary      Register a bank account
// @Description  Requires the manage-bank-accounts role. fio_token is encrypted at rest and never echoed back.
// @Tags         accounts
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        request  body  handler.createBankAccountRequest  true  "New bank account"
// @Success      201  {object}  handler.bankAccountResponse
// @Failure      400,401,403  {object}  map[string]string
// @Failure      409  {object}  map[string]string  "fio_account_id already registered"
// @Router       /account [post]
func CreateBankAccount(queries *db.Queries, encryptionKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request createBankAccountRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		request.FioAccountID = strings.TrimSpace(request.FioAccountID)
		request.DisplayName = strings.TrimSpace(request.DisplayName)
		request.Currency = strings.ToUpper(strings.TrimSpace(request.Currency))
		request.FioToken = strings.TrimSpace(request.FioToken)
		if request.Currency == "" {
			request.Currency = "CZK"
		}

		if request.FioAccountID == "" {
			writeError(w, http.StatusBadRequest, "fio_account_id is required")
			return
		}
		if request.DisplayName == "" {
			writeError(w, http.StatusBadRequest, "display_name is required")
			return
		}
		if len(request.Currency) != 3 {
			writeError(w, http.StatusBadRequest, "currency must be a 3-letter code")
			return
		}
		if request.FioToken == "" {
			writeError(w, http.StatusBadRequest, "fio_token is required")
			return
		}

		account, err := queries.CreateBankAccount(r.Context(), db.CreateBankAccountParams{
			FioAccountID:  request.FioAccountID,
			Iban:          request.IBAN,
			Currency:      request.Currency,
			DisplayName:   request.DisplayName,
			FioToken:      request.FioToken,
			EncryptionKey: encryptionKey,
		})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				writeError(w, http.StatusConflict, "a bank account with this fio_account_id already exists")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to create bank account")
			return
		}

		writeJSON(w, http.StatusCreated, bankAccountResponse{
			ID:           account.ID,
			FioAccountID: account.FioAccountID,
			IBAN:         account.Iban,
			Currency:     account.Currency,
			DisplayName:  account.DisplayName,
			CreatedAt:    account.CreatedAt,
			HasToken:     true,
			IsActive:     true,
		})
	}
}

// UpdateBankAccount handles PATCH /account/{id} — edits our own label for the
// account and/or rotates its Fio token. See updateBankAccountRequest for why
// fio_account_id/iban/currency aren't here.
//
// @Summary      Update a bank account's label and/or Fio token
// @Description  Requires the manage-bank-accounts role. fio_account_id/iban/currency are Fio-assigned and not editable here.
// @Tags         accounts
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id       path  int                                true  "Bank account ID"
// @Param        request  body  handler.updateBankAccountRequest  true  "Fields to update"
// @Success      200  {object}  handler.bankAccountResponse
// @Failure      400,401,403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Router       /account/{id} [patch]
func UpdateBankAccount(queries *db.Queries, encryptionKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseBankAccountID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}

		var request updateBankAccountRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		request.DisplayName = strings.TrimSpace(request.DisplayName)
		if request.DisplayName == "" {
			writeError(w, http.StatusBadRequest, "display_name is required")
			return
		}

		var fioToken *string
		if request.FioToken != nil {
			trimmed := strings.TrimSpace(*request.FioToken)
			if trimmed == "" {
				writeError(w, http.StatusBadRequest, "fio_token cannot be blank")
				return
			}
			fioToken = &trimmed
		}

		account, err := queries.UpdateBankAccount(r.Context(), db.UpdateBankAccountParams{
			ID:            id,
			DisplayName:   request.DisplayName,
			FioToken:      fioToken,
			EncryptionKey: encryptionKey,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "bank account not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update bank account")
			return
		}

		writeJSON(w, http.StatusOK, bankAccountResponse{
			ID:           account.ID,
			FioAccountID: account.FioAccountID,
			IBAN:         account.Iban,
			Currency:     account.Currency,
			DisplayName:  account.DisplayName,
			CreatedAt:    account.CreatedAt,
			HasToken:     account.HasToken,
			IsActive:     true,
		})
	}
}

// DeleteBankAccount handles DELETE /account/{id} — soft delete (see
// queries.sql: raw_transactions/sync_fio_runs reference bank_accounts.id with
// no ON DELETE clause, and the row needs to stick around for
// ListBankAccounts to still show it). 0 rows affected means the id doesn't
// exist or was already deleted — both read as 404.
//
// @Summary      Soft-delete a bank account
// @Description  Requires the manage-bank-accounts role. The row and its sync history are kept, just marked inactive.
// @Tags         accounts
// @Security     BearerAuth
// @Param        id  path  int  true  "Bank account ID"
// @Success      204  "no content"
// @Failure      400,401,403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Router       /account/{id} [delete]
func DeleteBankAccount(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseBankAccountID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}

		rows, err := queries.DeleteBankAccount(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete bank account")
			return
		}
		if rows == 0 {
			writeError(w, http.StatusNotFound, "bank account not found")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

type syncResultResponse struct {
	BankAccountID         int32 `json:"bank_account_id"`
	TransactionsFetched   int   `json:"transactions_fetched"`
	TransactionsInserted  int   `json:"transactions_inserted"`
	TransactionsProcessed int   `json:"transactions_processed"`
	TransactionsFailed    int   `json:"transactions_failed"`
}

// TriggerFioSync handles POST /account/{id}/sync — an admin "sync now" button
// for one bank account, on top of the scheduled daily run
// (internal/scheduler + cmd/server/main.go). Refuses to run at all in debug
// mode (config.Debug), same local-dev safety net as the scheduled job —
// local dev has no real Fio account/token, so this would just fail or
// overwrite `make seed`'s fixture data.
//
// Runs transaction processing/matching synchronously right after a
// successful sync, same as BackfillAccount and for the same reason: an admin
// pressing "sync now" wants to see the result categorized immediately, not
// wait for the next 3am scheduled cycle. If processing fails, the
// already-committed raw_transactions rows aren't lost — left for the next
// scheduled cycle, same as any other unprocessed row — so that's reported as
// a 200 with zeroed processing counts, not an error.
//
// @Summary      Trigger an immediate Fio sync for one account
// @Description  Requires the manage-bank-accounts role. Refuses (409) in debug mode (DEBUG=true). Runs transaction processing synchronously afterward.
// @Tags         accounts
// @Security     BearerAuth
// @Produce      json
// @Param        id  path  int  true  "Bank account ID"
// @Success      200  {object}  handler.syncResultResponse
// @Failure      400,401,403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      409  {object}  map[string]string  "debug mode, or account inactive/has no token"
// @Failure      502  {object}  map[string]string  "Fio API call failed"
// @Router       /account/{id}/sync [post]
func TriggerFioSync(pool *pgxpool.Pool, fioAPIURL, encryptionKey string, debug bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseBankAccountID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		if debug {
			writeError(w, http.StatusConflict, "fio sync is disabled in debug mode (DEBUG=true) — see `make seed` for local fixture data")
			return
		}

		result, err := syncjob.SyncOneAccount(r.Context(), pool, fioAPIURL, encryptionKey, id, debug)
		switch {
		case errors.Is(err, syncjob.ErrAccountNotFound):
			writeError(w, http.StatusNotFound, err.Error())
			return
		case errors.Is(err, syncjob.ErrAccountInactive), errors.Is(err, syncjob.ErrNoToken):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case err != nil:
			writeError(w, http.StatusBadGateway, "fio sync failed: "+err.Error())
			return
		}

		response := syncResultResponse{
			BankAccountID:        result.BankAccountID,
			TransactionsFetched:  result.TransactionsFetched,
			TransactionsInserted: result.TransactionsInserted,
		}
		if processingResult, err := processing.Run(r.Context(), pool); err != nil {
			log.Printf("sync: bank_account_id=%d: transaction processing failed, left for the next scheduled cycle: %v", id, err)
		} else {
			response.TransactionsProcessed = processingResult.TransactionsProcessed
			response.TransactionsFailed = processingResult.TransactionsFailed
		}

		writeJSON(w, http.StatusOK, response)
	}
}

type backfillRequest struct {
	From string `json:"from"` // YYYY-MM-DD, inclusive
	To   string `json:"to"`   // YYYY-MM-DD, inclusive
}

// BackfillAccount handles POST /account/{id}/backfill — a one-off historical
// pull via Fio's /periods/ endpoint (syncjob.BackfillAccount), for
// transactions predating an account's first cursor-based sync. Data older
// than 90 days needs a manual strong-authorization (SCA) unlock done first in
// Fio's own Internet Banking — without it, Fio itself returns an error for
// that range, surfaced here as a 502 same as any other Fio failure.
//
// Unlike TriggerFioSync, this also runs transaction processing/matching
// synchronously afterward: a backfill is meant to be reviewed right away
// (typically right after creating the account, before it starts riding the
// daily cursor-based sync), not left uncategorized until the next 3am cycle.
// If processing fails, the already-committed backfilled rows aren't lost —
// they're just left for the next scheduled cycle to pick up, same as any
// other unprocessed raw_transactions row — so that failure is reported as a
// 200 with zeroed processing counts, not an error, to avoid implying the
// backfill itself failed.
//
// @Summary      Backfill historical transactions for one account
// @Description  Requires the manage-bank-accounts role. Pulls via Fio's /periods/ endpoint for a date range predating the account's cursor-based sync; ranges over 90 days old need SCA unlocked in Fio's own Internet Banking first. Runs transaction processing synchronously afterward.
// @Tags         accounts
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id       path  int                          true  "Bank account ID"
// @Param        request  body  handler.backfillRequest  true  "Date range, both YYYY-MM-DD, inclusive"
// @Success      200  {object}  handler.syncResultResponse
// @Failure      400,401,403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      409  {object}  map[string]string  "debug mode, or account inactive/has no token"
// @Failure      502  {object}  map[string]string  "Fio API call failed"
// @Router       /account/{id}/backfill [post]
func BackfillAccount(pool *pgxpool.Pool, fioAPIURL, encryptionKey string, debug bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseBankAccountID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		if debug {
			writeError(w, http.StatusConflict, "fio sync is disabled in debug mode (DEBUG=true) — see `make seed` for local fixture data")
			return
		}

		var request backfillRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		from, err := time.Parse("2006-01-02", request.From)
		if err != nil {
			writeError(w, http.StatusBadRequest, "from must be YYYY-MM-DD")
			return
		}
		to, err := time.Parse("2006-01-02", request.To)
		if err != nil {
			writeError(w, http.StatusBadRequest, "to must be YYYY-MM-DD")
			return
		}
		if to.Before(from) {
			writeError(w, http.StatusBadRequest, "to must not be before from")
			return
		}
		if to.After(time.Now()) {
			writeError(w, http.StatusBadRequest, "to must not be in the future")
			return
		}

		result, err := syncjob.BackfillAccount(r.Context(), pool, fioAPIURL, encryptionKey, id, from, to, debug)
		switch {
		case errors.Is(err, syncjob.ErrAccountNotFound):
			writeError(w, http.StatusNotFound, err.Error())
			return
		case errors.Is(err, syncjob.ErrAccountInactive), errors.Is(err, syncjob.ErrNoToken):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case err != nil:
			writeError(w, http.StatusBadGateway, "fio backfill failed: "+err.Error())
			return
		}

		response := syncResultResponse{
			BankAccountID:        result.BankAccountID,
			TransactionsFetched:  result.TransactionsFetched,
			TransactionsInserted: result.TransactionsInserted,
		}
		if processingResult, err := processing.Run(r.Context(), pool); err != nil {
			log.Printf("backfill: bank_account_id=%d: transaction processing failed, left for the next scheduled cycle: %v", id, err)
		} else {
			response.TransactionsProcessed = processingResult.TransactionsProcessed
			response.TransactionsFailed = processingResult.TransactionsFailed
		}

		writeJSON(w, http.StatusOK, response)
	}
}

func parseBankAccountID(r *http.Request) (int32, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 32)
	if err != nil || id < 1 {
		return 0, false
	}
	return int32(id), true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
