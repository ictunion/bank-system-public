package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kubik/bank-system/internal/db"
)

const categoryNameMaxLength = 64

type categoryResponse struct {
	Name        string `json:"name"`
	IsMandatory bool   `json:"is_mandatory"`
}

// ListCategories handles GET /categories — every transaction category
// (the four mandatory ones seeded by
// migrations/20260910000001_add_transaction_categories.sql, plus any custom
// ones added since), mandatory ones first. Used by the FE both to populate
// the category picker on manual assignment and to render the manage-
// categories admin view.
func ListCategories(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		categories, err := queries.ListCategories(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list categories")
			return
		}

		out := make([]categoryResponse, len(categories))
		for i, c := range categories {
			out[i] = categoryResponse{Name: c.Name, IsMandatory: c.IsMandatory}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type createCategoryRequest struct {
	Name string `json:"name"`
}

// CreateCategory handles POST /categories — adds a custom category.
// is_mandatory is always false here; only the four seeded categories are
// mandatory (see migrations/20260910000001_add_transaction_categories.sql).
func CreateCategory(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request createCategoryRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		request.Name = strings.TrimSpace(request.Name)
		if request.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		if len(request.Name) > categoryNameMaxLength {
			writeError(w, http.StatusBadRequest, "name is too long")
			return
		}

		category, err := queries.CreateCategory(r.Context(), request.Name)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				writeError(w, http.StatusConflict, "a category with this name already exists")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to create category")
			return
		}

		writeJSON(w, http.StatusCreated, categoryResponse{Name: category.Name, IsMandatory: category.IsMandatory})
	}
}

// DeleteCategory handles DELETE /categories/{name} — refuses to delete any
// of the four mandatory categories (400), a category still referenced by
// processed_transactions.category (409, via the FK's violation on delete),
// or a name that doesn't exist (404).
func DeleteCategory(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")

		category, err := queries.GetCategory(r.Context(), name)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "category not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load category")
			return
		}
		if category.IsMandatory {
			writeError(w, http.StatusBadRequest, "mandatory categories cannot be deleted")
			return
		}

		rows, err := queries.DeleteCategory(r.Context(), name)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				writeError(w, http.StatusConflict, "category is in use by existing transactions")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to delete category")
			return
		}
		if rows == 0 {
			writeError(w, http.StatusNotFound, "category not found")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
