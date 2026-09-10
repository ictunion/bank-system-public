package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/syncjob"
)

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
}

// ListBankAccounts handles GET /account — lists registered bank accounts for
// the admin UI, including soft-deleted ones (IsActive false) so admins still
// see the historical record. Never includes the Fio token itself, only
// whether one is set.
func ListBankAccounts(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accounts, err := queries.ListBankAccounts(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list bank accounts")
			return
		}

		resp := make([]bankAccountResponse, len(accounts))
		for i, account := range accounts {
			resp[i] = bankAccountResponse{
				ID:           account.ID,
				FioAccountID: account.FioAccountID,
				IBAN:         account.Iban,
				Currency:     account.Currency,
				DisplayName:  account.DisplayName,
				CreatedAt:    account.CreatedAt,
				HasToken:     account.HasToken,
				IsActive:     account.IsActive,
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// CreateBankAccount handles POST /account — registers a bank_accounts row so
// the Fio sync job (internal/syncjob) has something to sync against, including
// the Fio API token it should use (encrypted at rest, see queries.sql). This is
// a one-time-per-account setup call, not part of the daily sync flow.
func CreateBankAccount(queries *db.Queries, encryptionKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createBankAccountRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		req.FioAccountID = strings.TrimSpace(req.FioAccountID)
		req.DisplayName = strings.TrimSpace(req.DisplayName)
		req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
		req.FioToken = strings.TrimSpace(req.FioToken)
		if req.Currency == "" {
			req.Currency = "CZK"
		}

		if req.FioAccountID == "" {
			writeError(w, http.StatusBadRequest, "fio_account_id is required")
			return
		}
		if req.DisplayName == "" {
			writeError(w, http.StatusBadRequest, "display_name is required")
			return
		}
		if len(req.Currency) != 3 {
			writeError(w, http.StatusBadRequest, "currency must be a 3-letter code")
			return
		}
		if req.FioToken == "" {
			writeError(w, http.StatusBadRequest, "fio_token is required")
			return
		}

		account, err := queries.CreateBankAccount(r.Context(), db.CreateBankAccountParams{
			FioAccountID:  req.FioAccountID,
			Iban:          req.IBAN,
			Currency:      req.Currency,
			DisplayName:   req.DisplayName,
			FioToken:      req.FioToken,
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
func UpdateBankAccount(queries *db.Queries, encryptionKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseBankAccountID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}

		var req updateBankAccountRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		req.DisplayName = strings.TrimSpace(req.DisplayName)
		if req.DisplayName == "" {
			writeError(w, http.StatusBadRequest, "display_name is required")
			return
		}

		var fioToken *string
		if req.FioToken != nil {
			trimmed := strings.TrimSpace(*req.FioToken)
			if trimmed == "" {
				writeError(w, http.StatusBadRequest, "fio_token cannot be blank")
				return
			}
			fioToken = &trimmed
		}

		account, err := queries.UpdateBankAccount(r.Context(), db.UpdateBankAccountParams{
			ID:            id,
			DisplayName:   req.DisplayName,
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
	BankAccountID        int32 `json:"bank_account_id"`
	TransactionsFetched  int   `json:"transactions_fetched"`
	TransactionsInserted int   `json:"transactions_inserted"`
}

// TriggerFioSync handles POST /account/{id}/sync — an admin "sync now" button
// for one bank account, on top of the scheduled daily run
// (internal/scheduler + cmd/server/main.go). Refuses to run at all when
// DISABLE_FIO_SYNC is set, same local-dev safety net as the scheduled job
// (see config.DisableFioSync) — nothing here bypasses it, so flipping that env
// var back off later re-enables this button with no code change.
func TriggerFioSync(pool *pgxpool.Pool, encryptionKey string, disableFioSync, debug bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseBankAccountID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		if disableFioSync {
			writeError(w, http.StatusConflict, "fio sync is disabled (DISABLE_FIO_SYNC)")
			return
		}

		result, err := syncjob.SyncOneAccount(r.Context(), pool, encryptionKey, id, debug)
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

		writeJSON(w, http.StatusOK, syncResultResponse{
			BankAccountID:        result.BankAccountID,
			TransactionsFetched:  result.TransactionsFetched,
			TransactionsInserted: result.TransactionsInserted,
		})
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

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
