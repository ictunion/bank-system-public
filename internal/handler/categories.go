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
//
// @Summary      List transaction categories
// @Description  Requires the list-transactions role. Mandatory categories first.
// @Tags         categories
// @Security     BearerAuth
// @Produce      json
// @Success      200  {array}  handler.categoryResponse
// @Failure      401,403  {object}  map[string]string
// @Router       /categories [get]
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
//
// @Summary      Add a custom transaction category
// @Description  Requires the manage-transactions role. Always created with is_mandatory=false.
// @Tags         categories
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        request  body  handler.createCategoryRequest  true  "New category"
// @Success      201  {object}  handler.categoryResponse
// @Failure      400,401,403  {object}  map[string]string
// @Failure      409  {object}  map[string]string  "name already exists"
// @Router       /categories [post]
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
//
// @Summary      Delete a custom transaction category
// @Description  Requires the manage-transactions role. Mandatory categories and categories still in use cannot be deleted.
// @Tags         categories
// @Security     BearerAuth
// @Param        name  path  string  true  "Category name"
// @Success      204  "no content"
// @Failure      400,401,403  {object}  map[string]string  "mandatory category"
// @Failure      404  {object}  map[string]string
// @Failure      409  {object}  map[string]string  "category still referenced by existing transactions"
// @Router       /categories/{name} [delete]
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
