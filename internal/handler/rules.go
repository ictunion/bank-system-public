package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kubik/bank-system/internal/db"
)

const (
	// The only kind that can be created or deleted; the built-in kinds
	// (own_account, member_variable_symbol) are seeded by migration.
	ruleKindPattern = "pattern"

	ruleFieldMaxLength = 255
)

type categorizationRuleResponse struct {
	ID                      int64     `json:"id"`
	Priority                int32     `json:"priority"`
	Kind                    string    `json:"kind"`
	Category                string    `json:"category"`
	Enabled                 bool      `json:"enabled"`
	CounterAccountNumber    *string   `json:"counter_account_number"`
	VariableSymbol          *string   `json:"variable_symbol"`
	ConstantSymbol          *string   `json:"constant_symbol"`
	TextContains            *string   `json:"text_contains"`
	TransactionTypeContains *string   `json:"transaction_type_contains"`
	Direction               *string   `json:"direction"`
	Note                    *string   `json:"note"`
	CreatedAt               time.Time `json:"created_at"`
}

func toCategorizationRuleResponse(rule db.CategorizationRule) categorizationRuleResponse {
	return categorizationRuleResponse{
		ID:                      rule.ID,
		Priority:                rule.Priority,
		Kind:                    rule.Kind,
		Category:                rule.Category,
		Enabled:                 rule.Enabled,
		CounterAccountNumber:    rule.CounterAccountNumber,
		VariableSymbol:          rule.VariableSymbol,
		ConstantSymbol:          rule.ConstantSymbol,
		TextContains:            rule.TextContains,
		TransactionTypeContains: rule.TransactionTypeContains,
		Direction:               rule.Direction,
		Note:                    rule.Note,
		CreatedAt:               rule.CreatedAt,
	}
}

// categorizationRuleRequest is the body of both POST and PUT — PUT is a full
// overwrite, so every omitted optional field is cleared. For a built-in rule
// (own_account, member_variable_symbol) only priority, enabled and note can
// change; category must be omitted or equal its fixed category, and no
// condition may be set.
type categorizationRuleRequest struct {
	Priority *int32  `json:"priority"`
	Category string  `json:"category"`
	Enabled  *bool   `json:"enabled"` // optional, defaults to true
	Note     *string `json:"note"`

	CounterAccountNumber    *string `json:"counter_account_number"`
	VariableSymbol          *string `json:"variable_symbol"`
	ConstantSymbol          *string `json:"constant_symbol"`
	TextContains            *string `json:"text_contains"`
	TransactionTypeContains *string `json:"transaction_type_contains"`
	Direction               *string `json:"direction"` // incoming | outgoing
}

// normalizedRule is a validated categorizationRuleRequest: optional strings
// trimmed, blank ones turned into nil.
type normalizedRule struct {
	priority int32
	category string
	enabled  bool
	note     *string

	counterAccountNumber    *string
	variableSymbol          *string
	constantSymbol          *string
	textContains            *string
	transactionTypeContains *string
	direction               *string
}

func (rule normalizedRule) hasCondition() bool {
	return rule.counterAccountNumber != nil || rule.variableSymbol != nil || rule.constantSymbol != nil ||
		rule.textContains != nil || rule.transactionTypeContains != nil
}

// optionalRuleField trims value and returns nil for an absent or blank one.
// ok is false when the value is longer than ruleFieldMaxLength.
func optionalRuleField(value *string) (*string, bool) {
	if value == nil {
		return nil, true
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, true
	}
	if len(trimmed) > ruleFieldMaxLength {
		return nil, false
	}
	return &trimmed, true
}

// normalizeRuleRequest validates the parts of a request that don't depend on
// the rule's kind. Returns a client-facing error message on failure.
func normalizeRuleRequest(request categorizationRuleRequest) (normalizedRule, string) {
	if request.Priority == nil {
		return normalizedRule{}, "priority is required"
	}
	rule := normalizedRule{
		priority: *request.Priority,
		category: strings.TrimSpace(request.Category),
		enabled:  request.Enabled == nil || *request.Enabled,
	}

	fields := []struct {
		name   string
		source *string
		target **string
	}{
		{"note", request.Note, &rule.note},
		{"counter_account_number", request.CounterAccountNumber, &rule.counterAccountNumber},
		{"variable_symbol", request.VariableSymbol, &rule.variableSymbol},
		{"constant_symbol", request.ConstantSymbol, &rule.constantSymbol},
		{"text_contains", request.TextContains, &rule.textContains},
		{"transaction_type_contains", request.TransactionTypeContains, &rule.transactionTypeContains},
		{"direction", request.Direction, &rule.direction},
	}
	for _, field := range fields {
		value, ok := optionalRuleField(field.source)
		if !ok {
			return normalizedRule{}, field.name + " is too long"
		}
		*field.target = value
	}

	if rule.direction != nil && *rule.direction != "incoming" && *rule.direction != "outgoing" {
		return normalizedRule{}, "direction must be incoming or outgoing"
	}
	return rule, ""
}

// validatePatternRule checks what only a pattern rule needs: an existing
// category and at least one real condition (direction alone would match half
// of all traffic). Returns status 0 when valid.
func validatePatternRule(r *http.Request, queries *db.Queries, rule normalizedRule) (int, string) {
	if rule.category == "" {
		return http.StatusBadRequest, "category is required"
	}
	if !rule.hasCondition() {
		return http.StatusBadRequest, "at least one condition besides direction is required"
	}
	exists, err := queries.CategoryExists(r.Context(), rule.category)
	if err != nil {
		return http.StatusInternalServerError, "failed to check category"
	}
	if !exists {
		return http.StatusBadRequest, "invalid category"
	}
	return 0, ""
}

func parseRuleID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// writeRule loads a rule and writes it out.
func writeRule(w http.ResponseWriter, r *http.Request, queries *db.Queries, id int64, status int) {
	rule, err := queries.GetCategorizationRule(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load categorization rule")
		return
	}
	writeJSON(w, status, toCategorizationRuleResponse(rule))
}

// ListCategorizationRules handles GET /categorization-rules — every rule,
// disabled ones included, in evaluation order (priority, then id). The
// direction fallback (other_income/other_expense) isn't a rule and isn't
// listed; it always applies last.
//
// @Summary      List categorization rules
// @Description  Requires the list-transactions role. Evaluation order; disabled rules included.
// @Tags         categorization-rules
// @Security     BearerAuth
// @Produce      json
// @Success      200  {array}  handler.categorizationRuleResponse
// @Failure      401,403  {object}  map[string]string
// @Router       /categorization-rules [get]
func ListCategorizationRules(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rules, err := queries.ListCategorizationRules(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list categorization rules")
			return
		}

		out := make([]categorizationRuleResponse, len(rules))
		for i, rule := range rules {
			out[i] = toCategorizationRuleResponse(rule)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// CreateCategorizationRule handles POST /categorization-rules — adds a
// pattern rule. Built-in kinds can't be created; they're seeded by migration.
// New rules only apply to transactions processed from now on.
//
// @Summary      Add a categorization rule
// @Description  Requires the manage-transactions role. Always kind=pattern. Every set condition must match (AND); at least one besides direction is required. Blank strings count as unset.
// @Tags         categorization-rules
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        request  body  handler.categorizationRuleRequest  true  "New rule"
// @Success      201  {object}  handler.categorizationRuleResponse
// @Failure      400,401,403  {object}  map[string]string
// @Router       /categorization-rules [post]
func CreateCategorizationRule(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request categorizationRuleRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		rule, message := normalizeRuleRequest(request)
		if message != "" {
			writeError(w, http.StatusBadRequest, message)
			return
		}
		if status, message := validatePatternRule(r, queries, rule); status != 0 {
			writeError(w, status, message)
			return
		}

		id, err := queries.CreateCategorizationRule(r.Context(), db.CreateCategorizationRuleParams{
			Priority:                rule.priority,
			Category:                rule.category,
			Enabled:                 rule.enabled,
			CounterAccountNumber:    rule.counterAccountNumber,
			VariableSymbol:          rule.variableSymbol,
			ConstantSymbol:          rule.constantSymbol,
			TextContains:            rule.textContains,
			TransactionTypeContains: rule.transactionTypeContains,
			Direction:               rule.direction,
			Note:                    rule.note,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create categorization rule")
			return
		}

		writeRule(w, r, queries, id, http.StatusCreated)
	}
}

// UpdateCategorizationRule handles PUT /categorization-rules/{id} — full
// overwrite of a rule. Built-in rules accept only priority, enabled and note
// (anything else is a 400, since they have a fixed category and no
// conditions). Doesn't recategorize already-processed transactions.
//
// @Summary      Update a categorization rule
// @Description  Requires the manage-transactions role. Full overwrite: omitted optional fields are cleared. Built-in rules (own_account, member_variable_symbol): only priority, enabled and note may change.
// @Tags         categorization-rules
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id       path  int                                true  "Rule ID"
// @Param        request  body  handler.categorizationRuleRequest  true  "Rule"
// @Success      200  {object}  handler.categorizationRuleResponse
// @Failure      400,401,403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Router       /categorization-rules/{id} [put]
func UpdateCategorizationRule(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseRuleID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		var request categorizationRuleRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		rule, message := normalizeRuleRequest(request)
		if message != "" {
			writeError(w, http.StatusBadRequest, message)
			return
		}

		existing, err := queries.GetCategorizationRule(r.Context(), id)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "categorization rule not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load categorization rule")
			return
		}

		if existing.Kind == ruleKindPattern {
			if status, message := validatePatternRule(r, queries, rule); status != 0 {
				writeError(w, status, message)
				return
			}
		} else {
			if rule.category != "" && rule.category != existing.Category {
				writeError(w, http.StatusBadRequest, "the category of a built-in rule cannot be changed")
				return
			}
			if rule.hasCondition() || rule.direction != nil {
				writeError(w, http.StatusBadRequest, "a built-in rule takes no conditions")
				return
			}
			rule.category = existing.Category
		}

		if _, err := queries.UpdateCategorizationRule(r.Context(), db.UpdateCategorizationRuleParams{
			ID:                      id,
			Priority:                rule.priority,
			Category:                rule.category,
			Enabled:                 rule.enabled,
			CounterAccountNumber:    rule.counterAccountNumber,
			VariableSymbol:          rule.variableSymbol,
			ConstantSymbol:          rule.constantSymbol,
			TextContains:            rule.textContains,
			TransactionTypeContains: rule.transactionTypeContains,
			Direction:               rule.direction,
			Note:                    rule.note,
		}); errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "categorization rule not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update categorization rule")
			return
		}

		writeRule(w, r, queries, id, http.StatusOK)
	}
}

// DeleteCategorizationRule handles DELETE /categorization-rules/{id} — pattern
// rules only; a built-in rule is a 400 (disable it instead). Transactions the
// rule decided keep their category.
//
// @Summary      Delete a categorization rule
// @Description  Requires the manage-transactions role. Built-in rules cannot be deleted, only disabled. Already-categorized transactions keep their category.
// @Tags         categorization-rules
// @Security     BearerAuth
// @Param        id  path  int  true  "Rule ID"
// @Success      204  "no content"
// @Failure      400,401,403  {object}  map[string]string  "built-in rule"
// @Failure      404  {object}  map[string]string
// @Router       /categorization-rules/{id} [delete]
func DeleteCategorizationRule(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseRuleID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}

		existing, err := queries.GetCategorizationRule(r.Context(), id)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "categorization rule not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load categorization rule")
			return
		}
		if existing.Kind != ruleKindPattern {
			writeError(w, http.StatusBadRequest, "built-in rules cannot be deleted, only disabled")
			return
		}

		rows, err := queries.DeleteCategorizationRule(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete categorization rule")
			return
		}
		if rows == 0 {
			writeError(w, http.StatusNotFound, "categorization rule not found")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
