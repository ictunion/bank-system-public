package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/bank-system/internal/categoryid"
	"github.com/kubik/bank-system/internal/db"
)

const categoryNameMaxLength = 64

type categoryResponse struct {
	ID          pgtype.UUID `json:"id"`
	Name        string      `json:"name"`
	IsMandatory bool        `json:"is_mandatory"`
	ParentID    pgtype.UUID `json:"parent_id"` // null for a top-level category
}

func toCategoryResponse(category db.TransactionCategory) categoryResponse {
	return categoryResponse{ID: category.ID, Name: category.Name, IsMandatory: category.IsMandatory, ParentID: category.ParentID}
}

// parseCategoryID reads the {id} path value.
func parseCategoryID(r *http.Request) (pgtype.UUID, bool) {
	return categoryid.Parse(r.PathValue("id"))
}

// ListCategories handles GET /categories — every transaction category (the
// six mandatory ones with fixed ids, see internal/categoryid, plus any custom
// ones added since), mandatory ones first. Used by the FE both to populate
// the category picker on manual assignment and to render the manage-
// categories admin view. Everything else in the API refers to a category by
// its id; the name is a label. parent_id is null for a top-level category;
// the FE groups sub-categories under their parent itself.
//
// @Summary      List transaction categories
// @Description  Requires the list-transactions or manage-rules role. Mandatory categories first. parent_id is null for top-level categories.
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
	Name     string      `json:"name"`
	ParentID pgtype.UUID `json:"parent_id"` // optional
}

// CreateCategory handles POST /categories — adds a custom category with a
// generated id. is_mandatory is always false here; only the six fixed-id
// categories are mandatory. An optional parent_id makes it a sub-category;
// the parent must exist and be top-level itself (one level of nesting
// only), else 400.
//
// @Summary      Add a custom transaction category
// @Description  Requires the manage-transactions role. Always created with is_mandatory=false. Optional parent_id must be an existing top-level category.
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

		if request.ParentID.Valid {
			parent, err := queries.GetCategory(r.Context(), request.ParentID)
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "parent category does not exist")
				return
			} else if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to load parent category")
				return
			}
			if parent.ParentID.Valid {
				writeError(w, http.StatusBadRequest, "parent category is itself a sub-category; only one level of nesting is allowed")
				return
			}
		}

		category, err := queries.CreateCategory(r.Context(), db.CreateCategoryParams{
			Name:     request.Name,
			ParentID: request.ParentID,
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

// DeleteCategory handles DELETE /categories/{id} — refuses to delete any
// of the six mandatory categories (400), a category that still has
// sub-categories (409), a category still referenced by
// processed_transactions, categorization_rules or budget_line_categories
// (409, via the FK's violation on delete),
// or an id that doesn't exist (404).
//
// @Summary      Delete a custom transaction category
// @Description  Requires the manage-transactions role. Mandatory categories and categories still in use cannot be deleted.
// @Tags         categories
// @Security     BearerAuth
// @Param        id  path  string  true  "Category UUID"
// @Success      204  "no content"
// @Failure      400,401,403  {object}  map[string]string  "mandatory category"
// @Failure      404  {object}  map[string]string
// @Failure      409  {object}  map[string]string  "category still has sub-categories or is referenced by transactions, rules or budget lines"
// @Router       /categories/{id} [delete]
func DeleteCategory(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseCategoryID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}

		category, err := queries.GetCategory(r.Context(), id)
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
		// parent_id FK would reject the delete anyway, but with the same
		// 23503 code as a reference from processed_transactions.
		hasChildren, err := queries.CategoryHasChildren(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check sub-categories")
			return
		}
		if hasChildren {
			writeError(w, http.StatusConflict, "category still has sub-categories")
			return
		}

		rows, err := queries.DeleteCategory(r.Context(), id)
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
