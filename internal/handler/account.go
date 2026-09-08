package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kubik/bank-system/internal/db"
)

type createBankAccountRequest struct {
	FioAccountID string  `json:"fio_account_id"`
	IBAN         *string `json:"iban"`
	Currency     string  `json:"currency"`
	DisplayName  string  `json:"display_name"`
}

type bankAccountResponse struct {
	ID           int32     `json:"id"`
	FioAccountID string    `json:"fio_account_id"`
	IBAN         *string   `json:"iban"`
	Currency     string    `json:"currency"`
	DisplayName  string    `json:"display_name"`
	CreatedAt    time.Time `json:"created_at"`
}

// CreateBankAccount handles POST /account — registers a bank_accounts row so
// the Fio sync job (internal/syncjob) has something to sync against. This is a
// one-time-per-account setup call, not part of the daily sync flow.
func CreateBankAccount(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createBankAccountRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		req.FioAccountID = strings.TrimSpace(req.FioAccountID)
		req.DisplayName = strings.TrimSpace(req.DisplayName)
		req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
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

		account, err := queries.CreateBankAccount(r.Context(), db.CreateBankAccountParams{
			FioAccountID: req.FioAccountID,
			Iban:         req.IBAN,
			Currency:     req.Currency,
			DisplayName:  req.DisplayName,
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
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
