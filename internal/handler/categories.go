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
	Name        string  `json:"name"`
	IsMandatory bool    `json:"is_mandatory"`
	ParentName  *string `json:"parent_name"`
}

func toCategoryResponse(category db.TransactionCategory) categoryResponse {
	return categoryResponse{Name: category.Name, IsMandatory: category.IsMandatory, ParentName: category.ParentName}
}

// ListCategories handles GET /categories — every transaction category
// (the four mandatory ones seeded by
// migrations/20260910000001_add_transaction_categories.sql, plus any custom
// ones added since), mandatory ones first. Used by the FE both to populate
// the category picker on manual assignment and to render the manage-
// categories admin view. parent_name is null for a top-level category; the
// FE groups sub-categories under their parent itself.
//
// @Summary      List transaction categories
// @Description  Requires the list-transactions or manage-rules role. Mandatory categories first. parent_name is null for top-level categories.
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
			out[i] = toCategoryResponse(c)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type createCategoryRequest struct {
	Name       string  `json:"name"`
	ParentName *string `json:"parent_name,omitempty"`
}

// CreateCategory handles POST /categories — adds a custom category.
// is_mandatory is always false here; only the four seeded categories are
// mandatory (see migrations/20260910000001_add_transaction_categories.sql).
// An optional parent_name makes it a sub-category; the parent must exist
// and be top-level itself (one level of nesting only), else 400.
//
// @Summary      Add a custom transaction category
// @Description  Requires the manage-transactions role. Always created with is_mandatory=false. Optional parent_name must name an existing top-level category.
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

		if request.ParentName != nil {
			parentName := strings.TrimSpace(*request.ParentName)
			if parentName == "" {
				request.ParentName = nil
			} else {
				parent, err := queries.GetCategory(r.Context(), parentName)
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, http.StatusBadRequest, "parent category does not exist")
					return
				} else if err != nil {
					writeError(w, http.StatusInternalServerError, "failed to load parent category")
					return
				}
				if parent.ParentName != nil {
					writeError(w, http.StatusBadRequest, "parent category is itself a sub-category; only one level of nesting is allowed")
					return
				}
				request.ParentName = &parentName
			}
		}

		category, err := queries.CreateCategory(r.Context(), db.CreateCategoryParams{
			Name:       request.Name,
			ParentName: request.ParentName,
		})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				writeError(w, http.StatusConflict, "a category with this name already exists")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to create category")
			return
		}

		writeJSON(w, http.StatusCreated, toCategoryResponse(category))
	}
}

// DeleteCategory handles DELETE /categories/{name} — refuses to delete any
// of the four mandatory categories (400), a category that still has
// sub-categories (409), a category still referenced by
// processed_transactions, categorization_rules or budget_line_categories
// (409, via the FK's violation on delete),
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
// @Failure      409  {object}  map[string]string  "category still has sub-categories or is referenced by existing transactions"
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

		// Checked up front only for a clearer error message — the
		// parent_name FK would reject the delete anyway, but with the same
		// 23503 code as a reference from processed_transactions.
		hasChildren, err := queries.CategoryHasChildren(r.Context(), name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check sub-categories")
			return
		}
		if hasChildren {
			writeError(w, http.StatusConflict, "category still has sub-categories")
			return
		}

		rows, err := queries.DeleteCategory(r.Context(), name)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				writeError(w, http.StatusConflict, "category is in use by existing transactions, categorization rules or budget lines")
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
