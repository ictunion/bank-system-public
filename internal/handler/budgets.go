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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/categoryid"
	"github.com/kubik/bank-system/internal/db"
)

const (
	budgetYearMin = 2000
	budgetYearMax = 2100

	budgetTextMaxLength = 128
	budgetNoteMaxLength = 500
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
	ID          int64         `json:"id"`
	Kind        string        `json:"kind"` // income | expense
	GroupName   *string       `json:"group_name"`
	Name        string        `json:"name"`
	Budgeted    string        `json:"budgeted"` // always positive; kind gives the sign
	CategoryIDs []pgtype.UUID `json:"category_ids"`
	SortOrder   int32         `json:"sort_order"`
	Note        *string       `json:"note"`
}

func toBudgetLineResponse(line db.BudgetLine, categoryIDs []pgtype.UUID) budgetLineResponse {
	if categoryIDs == nil {
		categoryIDs = []pgtype.UUID{}
	}
	return budgetLineResponse{
		ID:          line.ID,
		Kind:        line.Kind,
		GroupName:   line.GroupName,
		Name:        line.Name,
		Budgeted:    line.BudgetedAmount,
		CategoryIDs: categoryIDs,
		SortOrder:   line.SortOrder,
		Note:        line.Note,
	}
}

// budgetReportLine is a line as GET /budgets/{year} reports it: the line
// plus its actual figure. Line create/update responses don't carry it — the
// editor reloads the report after a save.
type budgetReportLine struct {
	budgetLineResponse
	// Actual is the year's actual CZK amount for the line's categories,
	// signed so it reads positive in the normal case (spend for an expense
	// line, receipts for an income line); a refund lowers it.
	Actual string `json:"actual"`
}

// unbudgetedTotal is one category's actual amount in a year where no line of
// the budget maps that category.
type unbudgetedTotal struct {
	CategoryID pgtype.UUID `json:"category_id"`
	Category   string      `json:"category"`  // name, for display
	Direction  string      `json:"direction"` // incoming | outgoing
	Actual     string      `json:"actual"`    // positive; direction gives the sign
}

type budgetResponse struct {
	Year      int32              `json:"year"`
	Note      *string            `json:"note"`
	CreatedAt time.Time          `json:"created_at"`
	AsOf      string             `json:"as_of"` // YYYY-MM-DD the actual figures were computed on
	Lines     []budgetReportLine `json:"lines"`
	// Unbudgeted lists the year's transactions in categories no line maps
	// (internal transfers excluded), so incomes − expenses reconcile.
	Unbudgeted []unbudgetedTotal `json:"unbudgeted"`
	// OtherCurrencyCount is how many of the year's transactions weren't in
	// CZK and so aren't in any figure here.
	OtherCurrencyCount int32 `json:"other_currency_count"`
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

// GetBudget handles GET /budgets/{year} — the budget vs. actual report: the
// budget's lines (display order), each with its mapped categories, budgeted
// and actual amount, plus the year's unbudgeted category totals. Same privacy
// level as /transactions/summary: aggregates only, no per-transaction data,
// so view-budget.
//
// @Summary      Get one year's budget vs. actual
// @Description  Requires the view-budget role. Lines in display order (sort_order, then id), each with its mapped category_ids, budgeted and actual (that year's CZK transactions in those categories; signed, a refund lowers it). unbudgeted: categories with transactions that year but no line (internal transfers excluded). other_currency_count: non-CZK transactions left out. Amounts are decimal strings, always CZK.
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
		categoriesByLine := make(map[int64][]pgtype.UUID)
		for _, mapping := range mappings {
			categoriesByLine[mapping.BudgetLineID] = append(categoriesByLine[mapping.BudgetLineID], mapping.CategoryID)
		}

		actuals, err := queries.GetBudgetLineActuals(r.Context(), year)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to compute budget actuals")
			return
		}
		actualByLine := make(map[int64]string, len(actuals))
		for _, actual := range actuals {
			actualByLine[actual.BudgetLineID] = actual.ActualTotal
		}
		unbudgetedRows, err := queries.ListUnbudgetedCategoryTotals(r.Context(), db.ListUnbudgetedCategoryTotalsParams{
			BudgetYear:                 year,
			InternalTransferCategoryID: categoryid.InternalTransfer,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to compute unbudgeted totals")
			return
		}
		otherCurrencyCount, err := queries.CountOtherCurrencyTransactions(r.Context(), year)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to count other-currency transactions")
			return
		}

		response := budgetResponse{
			Year:               budget.Year,
			Note:               budget.Note,
			CreatedAt:          budget.CreatedAt,
			AsOf:               time.Now().Format("2006-01-02"),
			Lines:              make([]budgetReportLine, len(lines)),
			Unbudgeted:         make([]unbudgetedTotal, len(unbudgetedRows)),
			OtherCurrencyCount: otherCurrencyCount,
		}
		for i, line := range lines {
			actualTotal, ok := actualByLine[line.ID]
			if !ok {
				actualTotal = "0.00"
			}
			response.Lines[i] = budgetReportLine{
				budgetLineResponse: toBudgetLineResponse(line, categoriesByLine[line.ID]),
				Actual:             actualTotal,
			}
		}
		for i, row := range unbudgetedRows {
			response.Unbudgeted[i] = unbudgetedTotal{
				CategoryID: row.CategoryID,
				Category:   row.Category,
				Direction:  row.Direction,
				Actual:     row.Total,
			}
		}
		writeJSON(w, http.StatusOK, response)
	}
}

type createBudgetRequest struct {
	Year int32   `json:"year"`
	Note *string `json:"note"`
	// CopyFromYear, if set, copies that year's lines — names, groups, order,
	// budgeted amounts and category mappings — into the new budget, so a
	// new year starts from last year's sheet.
	CopyFromYear *int32 `json:"copy_from_year"`
}

// CreateBudget handles POST /budgets — creates a budget for a year, empty or
// as a copy of another year's lines (one DB transaction).
//
// @Summary      Create a budget
// @Description  Requires the manage-budget role. Creates an empty budget, or with copy_from_year a copy of that year's lines (budgeted amounts and category mappings included). 409 if the year already has one; 400 if copy_from_year has no budget.
// @Tags         budgets
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        request  body  handler.createBudgetRequest  true  "Year and optional note"
// @Success      201  {object}  handler.budgetSummaryResponse
// @Failure      400,401,403  {object}  map[string]string
// @Failure      409  {object}  map[string]string  "budget for this year already exists"
// @Router       /budgets [post]
func CreateBudget(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		queries := db.New(pool)

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

		if request.CopyFromYear != nil {
			if _, err := queries.GetBudget(r.Context(), *request.CopyFromYear); errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "copy_from_year has no budget")
				return
			} else if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to load budget to copy")
				return
			}
		}

		tx, err := pool.Begin(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create budget")
			return
		}
		defer tx.Rollback(r.Context())
		txQueries := db.New(tx)

		budget, err := txQueries.CreateBudget(r.Context(), db.CreateBudgetParams{Year: request.Year, Note: note})
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a budget for this year already exists")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create budget")
			return
		}
		if request.CopyFromYear != nil {
			if err := txQueries.CopyBudgetLines(r.Context(), db.CopyBudgetLinesParams{
				FromYear: *request.CopyFromYear,
				ToYear:   request.Year,
			}); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to copy budget lines")
				return
			}
			if err := txQueries.CopyBudgetLineCategories(r.Context(), db.CopyBudgetLineCategoriesParams{
				FromYear: *request.CopyFromYear,
				ToYear:   request.Year,
			}); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to copy budget line categories")
				return
			}
		}
		if err := tx.Commit(r.Context()); err != nil {
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
	Kind        string        `json:"kind"` // income | expense
	GroupName   *string       `json:"group_name"`
	Name        string        `json:"name"`
	Budgeted    string        `json:"budgeted"` // decimal string, >= 0, e.g. "60000.00"
	CategoryIDs []pgtype.UUID `json:"category_ids"`
	SortOrder   int32         `json:"sort_order"`
	Note        *string       `json:"note"` // optional; omit or "" to clear
}

type normalizedBudgetLine struct {
	kind        string
	groupName   *string
	name        string
	budgeted    string
	categoryIDs []pgtype.UUID
	sortOrder   int32
	note        *string
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
	var note *string
	if request.Note != nil {
		if trimmed := strings.TrimSpace(*request.Note); trimmed != "" {
			if len(trimmed) > budgetNoteMaxLength {
				return normalizedBudgetLine{}, "note is too long"
			}
			note = &trimmed
		}
	}
	budgeted := strings.TrimSpace(request.Budgeted)
	if budgeted == "" {
		budgeted = "0"
	}
	if !budgetAmountPattern.MatchString(budgeted) {
		return normalizedBudgetLine{}, "budgeted must be a non-negative amount like 60000 or 60000.50"
	}

	// Deduplicated, order kept.
	seen := make(map[pgtype.UUID]bool, len(request.CategoryIDs))
	categoryIDs := make([]pgtype.UUID, 0, len(request.CategoryIDs))
	for _, categoryID := range request.CategoryIDs {
		if !categoryID.Valid {
			return normalizedBudgetLine{}, "category_ids must not contain null"
		}
		if seen[categoryID] {
			continue
		}
		seen[categoryID] = true
		categoryIDs = append(categoryIDs, categoryID)
	}

	return normalizedBudgetLine{
		kind:        request.Kind,
		groupName:   groupName,
		name:        name,
		budgeted:    budgeted,
		categoryIDs: categoryIDs,
		sortOrder:   request.SortOrder,
		note:        note,
	}, ""
}

// categoryConflictResponse is the 409 body when a category is already
// mapped to another line of the same year — names the owning line so the
// editor can show it.
type categoryConflictResponse struct {
	Error      string      `json:"error"`
	CategoryID pgtype.UUID `json:"category_id"`
	Line       string      `json:"line"`
}

// checkLineCategories verifies every category exists and isn't already
// mapped to a different line of the same year (lineID 0 = a new line).
// Writes the 400/409/500 itself and returns false when the save must stop.
// The UNIQUE (budget_year, category_id) constraint is the backstop for a
// concurrent save.
func checkLineCategories(w http.ResponseWriter, r *http.Request, queries *db.Queries, year int32, lineID int64, categoryIDs []pgtype.UUID) bool {
	names := make(map[pgtype.UUID]string, len(categoryIDs))
	for _, categoryID := range categoryIDs {
		category, err := queries.GetCategory(r.Context(), categoryID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "unknown category in category_ids")
			return false
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check category")
			return false
		}
		names[categoryID] = category.Name
	}

	mappings, err := queries.ListBudgetLineCategories(r.Context(), year)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list budget line categories")
		return false
	}
	owner := make(map[pgtype.UUID]string, len(mappings))
	for _, mapping := range mappings {
		if mapping.BudgetLineID != lineID {
			owner[mapping.CategoryID] = mapping.LineName
		}
	}
	for _, categoryID := range categoryIDs {
		if line, taken := owner[categoryID]; taken {
			writeJSON(w, http.StatusConflict, categoryConflictResponse{
				Error:      "category " + names[categoryID] + " is already used by budget line " + line,
				CategoryID: categoryID,
				Line:       line,
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
			BudgetedAmount:  line.budgeted,
			SortOrder:       line.sortOrder,
			Note:            line.note,
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
			BudgetedAmount:  line.budgeted,
			SortOrder:       line.sortOrder,
			Note:            line.note,
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

	for _, categoryID := range line.categoryIDs {
		if err := txQueries.InsertBudgetLineCategory(r.Context(), db.InsertBudgetLineCategoryParams{
			BudgetLineID: lineID,
			BudgetYear:   year,
			CategoryID:   categoryID,
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
		var categoryIDs []pgtype.UUID
		for _, mapping := range mappings {
			if mapping.BudgetLineID == lineID {
				categoryIDs = append(categoryIDs, mapping.CategoryID)
			}
		}
		writeJSON(w, status, toBudgetLineResponse(line, categoryIDs))
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
	if !checkLineCategories(w, r, queries, year, lineID, line.categoryIDs) {
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
// @Description  Requires the manage-budget role. budgeted is a non-negative decimal string; kind gives the sign. Each category (by id in category_ids) may feed only one line per year (409 names the line that has it). Unknown category → 400.
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
