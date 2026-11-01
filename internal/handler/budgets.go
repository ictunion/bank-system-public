package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/db"
)

const (
	budgetYearMin = 2000
	budgetYearMax = 2100

	budgetTextMaxLength = 128
)

// A non-negative CZK amount with at most two decimals, sent as a string like
// every other amount in this API.
var budgetAmountPattern = regexp.MustCompile(`^\d{1,12}(\.\d{1,2})?$`)

type budgetSummaryResponse struct {
	Year      int32     `json:"year"`
	Note      *string   `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

func toBudgetSummary(budget db.Budget) budgetSummaryResponse {
	return budgetSummaryResponse{Year: budget.Year, Note: budget.Note, CreatedAt: budget.CreatedAt}
}

type budgetLineResponse struct {
	ID        int64   `json:"id"`
	Kind      string  `json:"kind"` // income | expense
	GroupName *string `json:"group_name"`
	Name      string  `json:"name"`
	// Projected is always positive; kind gives the sign.
	Projected  string   `json:"projected"`
	Categories []string `json:"categories"`
	SortOrder  int32    `json:"sort_order"`
}

func toBudgetLineResponse(line db.BudgetLine, categories []string) budgetLineResponse {
	if categories == nil {
		categories = []string{}
	}
	return budgetLineResponse{
		ID:         line.ID,
		Kind:       line.Kind,
		GroupName:  line.GroupName,
		Name:       line.Name,
		Projected:  line.ProjectedAmount,
		Categories: categories,
		SortOrder:  line.SortOrder,
	}
}

type budgetResponse struct {
	Year      int32                `json:"year"`
	Note      *string              `json:"note"`
	CreatedAt time.Time            `json:"created_at"`
	Lines     []budgetLineResponse `json:"lines"`
}

// parseBudgetYear reads the {year} path value.
func parseBudgetYear(r *http.Request) (int32, bool) {
	year, err := strconv.Atoi(r.PathValue("year"))
	return int32(year), err == nil && year >= budgetYearMin && year <= budgetYearMax
}

func parseBudgetLineID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// optionalBudgetText trims value and returns nil for an absent or blank one.
// ok is false when it's longer than budgetTextMaxLength.
func optionalBudgetText(value *string) (*string, bool) {
	if value == nil {
		return nil, true
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, true
	}
	if len(trimmed) > budgetTextMaxLength {
		return nil, false
	}
	return &trimmed, true
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// loadBudget fetches a budget, writing a 404/500 itself when it can't.
func loadBudget(w http.ResponseWriter, r *http.Request, queries *db.Queries, year int32) (db.Budget, bool) {
	budget, err := queries.GetBudget(r.Context(), year)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "budget not found")
		return db.Budget{}, false
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load budget")
		return db.Budget{}, false
	}
	return budget, true
}

// ListBudgets handles GET /budgets — every year that has a budget, newest
// first.
//
// @Summary      List budgets
// @Description  Requires the view-budget role. Newest year first.
// @Tags         budgets
// @Security     BearerAuth
// @Produce      json
// @Success      200  {array}  handler.budgetSummaryResponse
// @Failure      401,403  {object}  map[string]string
// @Router       /budgets [get]
func ListBudgets(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		budgets, err := queries.ListBudgets(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list budgets")
			return
		}
		out := make([]budgetSummaryResponse, len(budgets))
		for i, budget := range budgets {
			out[i] = toBudgetSummary(budget)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// GetBudget handles GET /budgets/{year} — the budget with its lines (display
// order) and each line's mapped categories. Same privacy level as
// /transactions/summary: no per-transaction data, so view-budget.
//
// @Summary      Get one year's budget
// @Description  Requires the view-budget role. Lines in display order (sort_order, then id), each with its mapped categories. Amounts are decimal strings, always CZK.
// @Tags         budgets
// @Security     BearerAuth
// @Produce      json
// @Param        year  path  int  true  "Budget year"
// @Success      200  {object}  handler.budgetResponse
// @Failure      400,401,403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Router       /budgets/{year} [get]
func GetBudget(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, ok := parseBudgetYear(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid year")
			return
		}
		budget, ok := loadBudget(w, r, queries, year)
		if !ok {
			return
		}

		lines, err := queries.ListBudgetLines(r.Context(), year)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list budget lines")
			return
		}
		mappings, err := queries.ListBudgetLineCategories(r.Context(), year)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list budget line categories")
			return
		}
		categoriesByLine := make(map[int64][]string)
		for _, mapping := range mappings {
			categoriesByLine[mapping.BudgetLineID] = append(categoriesByLine[mapping.BudgetLineID], mapping.Category)
		}

		response := budgetResponse{
			Year:      budget.Year,
			Note:      budget.Note,
			CreatedAt: budget.CreatedAt,
			Lines:     make([]budgetLineResponse, len(lines)),
		}
		for i, line := range lines {
			response.Lines[i] = toBudgetLineResponse(line, categoriesByLine[line.ID])
		}
		writeJSON(w, http.StatusOK, response)
	}
}

type createBudgetRequest struct {
	Year int32   `json:"year"`
	Note *string `json:"note"`
}

// CreateBudget handles POST /budgets — creates an empty budget for a year.
//
// @Summary      Create a budget
// @Description  Requires the manage-budget role. Creates an empty budget; add lines with POST /budgets/{year}/lines. 409 if the year already has one.
// @Tags         budgets
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        request  body  handler.createBudgetRequest  true  "Year and optional note"
// @Success      201  {object}  handler.budgetSummaryResponse
// @Failure      400,401,403  {object}  map[string]string
// @Failure      409  {object}  map[string]string  "budget for this year already exists"
// @Router       /budgets [post]
func CreateBudget(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request createBudgetRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if request.Year < budgetYearMin || request.Year > budgetYearMax {
			writeError(w, http.StatusBadRequest, "year must be between 2000 and 2100")
			return
		}
		note, ok := optionalBudgetText(request.Note)
		if !ok {
			writeError(w, http.StatusBadRequest, "note is too long")
			return
		}

		budget, err := queries.CreateBudget(r.Context(), db.CreateBudgetParams{Year: request.Year, Note: note})
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a budget for this year already exists")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create budget")
			return
		}
		writeJSON(w, http.StatusCreated, toBudgetSummary(budget))
	}
}

type updateBudgetRequest struct {
	Note *string `json:"note"` // omit or "" to clear
}

// UpdateBudget handles PATCH /budgets/{year} — the note is the only
// budget-level field.
//
// @Summary      Update a budget's note
// @Description  Requires the manage-budget role. Omit note or send "" to clear it.
// @Tags         budgets
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        year     path  int                          true  "Budget year"
// @Param        request  body  handler.updateBudgetRequest  true  "Note"
// @Success      200  {object}  handler.budgetSummaryResponse
// @Failure      400,401,403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Router       /budgets/{year} [patch]
func UpdateBudget(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, ok := parseBudgetYear(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid year")
			return
		}
		var request updateBudgetRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		note, ok := optionalBudgetText(request.Note)
		if !ok {
			writeError(w, http.StatusBadRequest, "note is too long")
			return
		}

		budget, err := queries.UpdateBudgetNote(r.Context(), db.UpdateBudgetNoteParams{Year: year, Note: note})
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "budget not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update budget")
			return
		}
		writeJSON(w, http.StatusOK, toBudgetSummary(budget))
	}
}

// DeleteBudget handles DELETE /budgets/{year} — removes the budget with all
// its lines and category mappings. Transactions are not affected.
//
// @Summary      Delete a budget
// @Description  Requires the manage-budget role. Deletes the budget and all its lines. Transactions are not affected.
// @Tags         budgets
// @Security     BearerAuth
// @Param        year  path  int  true  "Budget year"
// @Success      204  "no content"
// @Failure      400,401,403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Router       /budgets/{year} [delete]
func DeleteBudget(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, ok := parseBudgetYear(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid year")
			return
		}
		rows, err := queries.DeleteBudget(r.Context(), year)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete budget")
			return
		}
		if rows == 0 {
			writeError(w, http.StatusNotFound, "budget not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// budgetLineRequest is the body of both POST and PUT — PUT is a full
// overwrite, including the category list.
type budgetLineRequest struct {
	Kind       string   `json:"kind"` // income | expense
	GroupName  *string  `json:"group_name"`
	Name       string   `json:"name"`
	Projected  string   `json:"projected"` // decimal string, >= 0, e.g. "60000.00"
	Categories []string `json:"categories"`
	SortOrder  int32    `json:"sort_order"`
}

type normalizedBudgetLine struct {
	kind       string
	groupName  *string
	name       string
	projected  string
	categories []string
	sortOrder  int32
}

// normalizeBudgetLine validates a line request. Returns a client-facing
// error message on failure.
func normalizeBudgetLine(request budgetLineRequest) (normalizedBudgetLine, string) {
	if request.Kind != "income" && request.Kind != "expense" {
		return normalizedBudgetLine{}, "kind must be income or expense"
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return normalizedBudgetLine{}, "name is required"
	}
	if len(name) > budgetTextMaxLength {
		return normalizedBudgetLine{}, "name is too long"
	}
	groupName, ok := optionalBudgetText(request.GroupName)
	if !ok {
		return normalizedBudgetLine{}, "group_name is too long"
	}
	projected := strings.TrimSpace(request.Projected)
	if projected == "" {
		projected = "0"
	}
	if !budgetAmountPattern.MatchString(projected) {
		return normalizedBudgetLine{}, "projected must be a non-negative amount like 60000 or 60000.50"
	}

	// Deduplicated, blanks dropped, order kept.
	seen := make(map[string]bool, len(request.Categories))
	categories := make([]string, 0, len(request.Categories))
	for _, category := range request.Categories {
		category = strings.TrimSpace(category)
		if category == "" || seen[category] {
			continue
		}
		seen[category] = true
		categories = append(categories, category)
	}

	return normalizedBudgetLine{
		kind:       request.Kind,
		groupName:  groupName,
		name:       name,
		projected:  projected,
		categories: categories,
		sortOrder:  request.SortOrder,
	}, ""
}

// categoryConflictResponse is the 409 body when a category is already
// mapped to another line of the same year — names the owning line so the
// editor can show it.
type categoryConflictResponse struct {
	Error    string `json:"error"`
	Category string `json:"category"`
	Line     string `json:"line"`
}

// checkLineCategories verifies every category exists and isn't already
// mapped to a different line of the same year (lineID 0 = a new line).
// Writes the 400/409/500 itself and returns false when the save must stop.
// The UNIQUE (budget_year, category) constraint is the backstop for a
// concurrent save.
func checkLineCategories(w http.ResponseWriter, r *http.Request, queries *db.Queries, year int32, lineID int64, categories []string) bool {
	for _, category := range categories {
		exists, err := queries.CategoryExists(r.Context(), category)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check category")
			return false
		}
		if !exists {
			writeError(w, http.StatusBadRequest, "unknown category: "+category)
			return false
		}
	}

	mappings, err := queries.ListBudgetLineCategories(r.Context(), year)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list budget line categories")
		return false
	}
	owner := make(map[string]string, len(mappings))
	for _, mapping := range mappings {
		if mapping.BudgetLineID != lineID {
			owner[mapping.Category] = mapping.LineName
		}
	}
	for _, category := range categories {
		if line, taken := owner[category]; taken {
			writeJSON(w, http.StatusConflict, categoryConflictResponse{
				Error:    "category " + category + " is already used by budget line " + line,
				Category: category,
				Line:     line,
			})
			return false
		}
	}
	return true
}

// saveBudgetLine writes a line row and replaces its category mappings in one
// DB transaction. lineID 0 creates a new line. found is false when the line
// to update doesn't exist in that year's budget.
func saveBudgetLine(r *http.Request, pool *pgxpool.Pool, year int32, lineID int64, line normalizedBudgetLine) (id int64, found bool, err error) {
	tx, err := pool.Begin(r.Context())
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(r.Context())
	txQueries := db.New(tx)

	if lineID == 0 {
		lineID, err = txQueries.CreateBudgetLine(r.Context(), db.CreateBudgetLineParams{
			BudgetYear:      year,
			Kind:            line.kind,
			GroupName:       line.groupName,
			Name:            line.name,
			ProjectedAmount: line.projected,
			SortOrder:       line.sortOrder,
		})
		if err != nil {
			return 0, false, err
		}
	} else {
		rows, err := txQueries.UpdateBudgetLine(r.Context(), db.UpdateBudgetLineParams{
			ID:              lineID,
			BudgetYear:      year,
			Kind:            line.kind,
			GroupName:       line.groupName,
			Name:            line.name,
			ProjectedAmount: line.projected,
			SortOrder:       line.sortOrder,
		})
		if err != nil {
			return 0, false, err
		}
		if rows == 0 {
			return 0, false, nil
		}
		if err := txQueries.DeleteBudgetLineCategories(r.Context(), lineID); err != nil {
			return 0, false, err
		}
	}

	for _, category := range line.categories {
		if err := txQueries.InsertBudgetLineCategory(r.Context(), db.InsertBudgetLineCategoryParams{
			BudgetLineID: lineID,
			BudgetYear:   year,
			Category:     category,
		}); err != nil {
			return 0, false, err
		}
	}
	return lineID, true, tx.Commit(r.Context())
}

// writeBudgetLine responds with one line as GET /budgets/{year} shows it.
func writeBudgetLine(w http.ResponseWriter, r *http.Request, queries *db.Queries, year int32, lineID int64, status int) {
	lines, err := queries.ListBudgetLines(r.Context(), year)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load budget line")
		return
	}
	mappings, err := queries.ListBudgetLineCategories(r.Context(), year)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load budget line categories")
		return
	}
	for _, line := range lines {
		if line.ID != lineID {
			continue
		}
		var categories []string
		for _, mapping := range mappings {
			if mapping.BudgetLineID == lineID {
				categories = append(categories, mapping.Category)
			}
		}
		writeJSON(w, status, toBudgetLineResponse(line, categories))
		return
	}
	writeError(w, http.StatusInternalServerError, "saved budget line not found")
}

// handleBudgetLineSave is the shared body of CreateBudgetLine (lineID 0) and
// UpdateBudgetLine.
func handleBudgetLineSave(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, year int32, lineID int64) {
	queries := db.New(pool)

	var request budgetLineRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	line, message := normalizeBudgetLine(request)
	if message != "" {
		writeError(w, http.StatusBadRequest, message)
		return
	}
	if _, ok := loadBudget(w, r, queries, year); !ok {
		return
	}
	if !checkLineCategories(w, r, queries, year, lineID, line.categories) {
		return
	}

	id, found, err := saveBudgetLine(r, pool, year, lineID, line)
	if isUniqueViolation(err) {
		// (budget_year, name), or (budget_year, category) if a concurrent
		// save mapped the same category in between.
		writeError(w, http.StatusConflict, "a line with this name already exists in this budget, or a category is already used by another line")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save budget line")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "budget line not found")
		return
	}

	status := http.StatusOK
	if lineID == 0 {
		status = http.StatusCreated
	}
	writeBudgetLine(w, r, queries, year, id, status)
}

// CreateBudgetLine handles POST /budgets/{year}/lines.
//
// @Summary      Add a budget line
// @Description  Requires the manage-budget role. projected is a non-negative decimal string; kind gives the sign. Each category may feed only one line per year (409 names the line that has it). Unknown category → 400.
// @Tags         budgets
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        year     path  int                        true  "Budget year"
// @Param        request  body  handler.budgetLineRequest  true  "New line"
// @Success      201  {object}  handler.budgetLineResponse
// @Failure      400,401,403  {object}  map[string]string
// @Failure      404  {object}  map[string]string  "budget not found"
// @Failure      409  {object}  handler.categoryConflictResponse  "category already used by another line, or duplicate line name"
// @Router       /budgets/{year}/lines [post]
func CreateBudgetLine(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, ok := parseBudgetYear(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid year")
			return
		}
		handleBudgetLineSave(w, r, pool, year, 0)
	}
}

// UpdateBudgetLine handles PUT /budgets/{year}/lines/{id} — full overwrite,
// category list included.
//
// @Summary      Update a budget line
// @Description  Requires the manage-budget role. Full overwrite of the line, including its categories. Same validation as creating one.
// @Tags         budgets
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        year     path  int                        true  "Budget year"
// @Param        id       path  int                        true  "Budget line ID"
// @Param        request  body  handler.budgetLineRequest  true  "Line"
// @Success      200  {object}  handler.budgetLineResponse
// @Failure      400,401,403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      409  {object}  handler.categoryConflictResponse
// @Router       /budgets/{year}/lines/{id} [put]
func UpdateBudgetLine(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, ok := parseBudgetYear(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid year")
			return
		}
		lineID, ok := parseBudgetLineID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		handleBudgetLineSave(w, r, pool, year, lineID)
	}
}

// DeleteBudgetLine handles DELETE /budgets/{year}/lines/{id}.
//
// @Summary      Delete a budget line
// @Description  Requires the manage-budget role. Its categories become unbudgeted for that year.
// @Tags         budgets
// @Security     BearerAuth
// @Param        year  path  int  true  "Budget year"
// @Param        id    path  int  true  "Budget line ID"
// @Success      204  "no content"
// @Failure      400,401,403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Router       /budgets/{year}/lines/{id} [delete]
func DeleteBudgetLine(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, ok := parseBudgetYear(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid year")
			return
		}
		lineID, ok := parseBudgetLineID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		rows, err := queries.DeleteBudgetLine(r.Context(), db.DeleteBudgetLineParams{ID: lineID, BudgetYear: year})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete budget line")
			return
		}
		if rows == 0 {
			writeError(w, http.StatusNotFound, "budget line not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
