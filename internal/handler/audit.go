package handler

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/keycloak"
)

// AuditCategory groups related audited actions so the audit log can be
// filtered by area (GET /audit-logs?category=...).
type AuditCategory string

const (
	AuditCategoryBankAccount AuditCategory = "bank_account"
	AuditCategoryProcessing  AuditCategory = "processing"
	AuditCategoryTransaction AuditCategory = "transaction"
	AuditCategoryCategory    AuditCategory = "category"
	AuditCategoryWaiver      AuditCategory = "waiver"
	// AuditCategoryOther is the fallback for a mutating route missing from
	// auditRoutes — the request is still recorded, just uncategorized.
	AuditCategoryOther AuditCategory = "other"
)

// AuditCategories lists every category, used to validate the category filter.
var AuditCategories = []AuditCategory{
	AuditCategoryBankAccount,
	AuditCategoryProcessing,
	AuditCategoryTransaction,
	AuditCategoryCategory,
	AuditCategoryWaiver,
	AuditCategoryOther,
}

type auditAction struct {
	category AuditCategory
	action   string
}

// auditRoutes maps each mutating route pattern (exactly as registered on the
// API mux in cmd/server/main.go) to its category and action name. A new
// POST/PUT/PATCH/DELETE route should get an entry here — without one it's
// still audited, but as AuditCategoryOther with the raw pattern as action.
var auditRoutes = map[string]auditAction{
	"POST /account":                                         {AuditCategoryBankAccount, "create_bank_account"},
	"PATCH /account/{id}":                                   {AuditCategoryBankAccount, "update_bank_account"},
	"DELETE /account/{id}":                                  {AuditCategoryBankAccount, "delete_bank_account"},
	"POST /account/{id}/sync":                               {AuditCategoryBankAccount, "sync_bank_account"},
	"POST /account/{id}/backfill":                           {AuditCategoryBankAccount, "backfill_bank_account"},
	"POST /processing/run":                                  {AuditCategoryProcessing, "run_processing"},
	"PUT /transactions/{id}/assignment":                     {AuditCategoryTransaction, "assign_transaction"},
	"DELETE /transactions/{id}/assignment":                  {AuditCategoryTransaction, "unassign_transaction"},
	"POST /categories":                                      {AuditCategoryCategory, "create_category"},
	"DELETE /categories/{name}":                             {AuditCategoryCategory, "delete_category"},
	"POST /payments/{member_number}/waive":                  {AuditCategoryWaiver, "create_waiver"},
	"DELETE /payments/{member_number}/waive/{year}/{month}": {AuditCategoryWaiver, "delete_waiver"},
}

// auditRecord is placed on the request context by AuditLog and filled in by
// withClaims once an auth wrapper has verified the caller.
type auditRecord struct {
	claims *keycloak.Claims
}

// statusRecorder captures the status code a handler responds with.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(body []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(body)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// AuditLog wraps the API mux so every authenticated POST/PUT/PATCH/DELETE
// request is recorded in audit_logs — who (the token's sub and email),
// what (category, action, route pattern, concrete path, response status),
// when (created_at). GET and other read-only methods pass straight through.
//
// next must be the *http.ServeMux the routes are registered on: ServeMux sets
// Request.Pattern on the request it's handed, which is how the matched route
// is read back here after next returns. The caller's identity comes from the
// auth wrapper inside the route (see withClaims) — a request that never got
// past authentication (401/403 from RequireRole, unmatched route) has no
// caller and is not recorded, so anonymous traffic can't write to this table.
//
// The insert runs after the handler, outside its DB transaction, so a crash
// in between can lose an entry; a failed insert is logged, never surfaced to
// the caller (the response is already written by then).
func AuditLog(queries *db.Queries, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			next.ServeHTTP(w, r)
			return
		}

		record := &auditRecord{}
		request := r.WithContext(context.WithValue(r.Context(), auditRecordContextKey, record))
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, request)

		if record.claims == nil {
			return
		}
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}

		route := request.Pattern
		action, ok := auditRoutes[route]
		if !ok {
			log.Printf("audit log: no category for route %q, recording as %q", route, AuditCategoryOther)
			action = auditAction{AuditCategoryOther, route}
		}

		// Detached from the request context: a client disconnecting right
		// after the change went through must not drop its audit entry.
		insertContext, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		err := queries.CreateAuditLog(insertContext, db.CreateAuditLogParams{
			ActorSub:   record.claims.Subject,
			ActorEmail: nonEmpty(record.claims.Email),
			Category:   string(action.category),
			Action:     action.action,
			Method:     r.Method,
			Route:      route,
			Path:       r.URL.Path,
			StatusCode: int32(status),
		})
		if err != nil {
			log.Printf("audit log: recording %s %s by %s: %v", r.Method, r.URL.Path, record.claims.Subject, err)
		}
	})
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

const (
	auditLogsDefaultLimit = 100
	auditLogsMaxLimit     = 500
)

type auditLogsResponse struct {
	Total  int64          `json:"total"`
	Limit  int32          `json:"limit"`
	Offset int32          `json:"offset"`
	Logs   []auditLogItem `json:"logs"`
}

type auditLogItem struct {
	ID         int64     `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	ActorSub   string    `json:"actor_sub"`
	ActorEmail *string   `json:"actor_email"`
	Category   string    `json:"category"`
	Action     string    `json:"action"`
	Method     string    `json:"method"`
	Route      string    `json:"route"`
	Path       string    `json:"path"`
	StatusCode int32     `json:"status_code"`
}

// ListAuditLogs handles GET /audit-logs — the audit trail of every
// authenticated mutating request, newest first. Optional filters: category,
// actor_sub, from/to (YYYY-MM-DD, inclusive, on created_at); limit/offset
// paging with the same defaults/cap as ListEventLogs.
//
// @Summary      List audit log
// @Description  Requires the view-audit-logs role. One entry per authenticated POST/PUT/PATCH/DELETE request (who, what, when), newest first. status_code is the response status — rejected attempts are included, filter to 2xx client-side for changes that took effect.
// @Tags         audit-logs
// @Security     BearerAuth
// @Produce      json
// @Param        category   query  string  false  "bank_account, processing, transaction, category, waiver or other"
// @Param        actor_sub  query  string  false  "Keycloak subject of the caller"
// @Param        from       query  string  false  "YYYY-MM-DD, inclusive"
// @Param        to         query  string  false  "YYYY-MM-DD, inclusive"
// @Param        limit      query  int     false  "Max results, default 100, capped at 500"
// @Param        offset     query  int     false  "Default 0"
// @Success      200  {object}  handler.auditLogsResponse
// @Failure      400,401,403  {object}  map[string]string
// @Router       /audit-logs [get]
func ListAuditLogs(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		queryParams := r.URL.Query()

		params := db.ListAuditLogsParams{
			Lim: auditLogsDefaultLimit,
			Off: 0,
		}

		if s := queryParams.Get("category"); s != "" {
			known := false
			for _, category := range AuditCategories {
				if string(category) == s {
					known = true
					break
				}
			}
			if !known {
				writeError(w, http.StatusBadRequest, "unknown category")
				return
			}
			params.Category = &s
		}
		if s := queryParams.Get("actor_sub"); s != "" {
			params.ActorSub = &s
		}

		var ok bool
		if params.DateFrom, ok = dateParam(queryParams.Get("from")); !ok {
			writeError(w, http.StatusBadRequest, "from must be YYYY-MM-DD")
			return
		}
		if params.DateTo, ok = dateParam(queryParams.Get("to")); !ok {
			writeError(w, http.StatusBadRequest, "to must be YYYY-MM-DD")
			return
		}

		if s := queryParams.Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				writeError(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			if n > auditLogsMaxLimit {
				n = auditLogsMaxLimit
			}
			params.Lim = int32(n)
		}
		if s := queryParams.Get("offset"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 {
				writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
				return
			}
			params.Off = int32(n)
		}

		rows, err := queries.ListAuditLogs(r.Context(), params)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list audit logs")
			return
		}

		response := auditLogsResponse{
			Limit:  params.Lim,
			Offset: params.Off,
			Logs:   make([]auditLogItem, 0, len(rows)),
		}
		if len(rows) > 0 {
			response.Total = rows[0].TotalCount
		}
		for _, row := range rows {
			response.Logs = append(response.Logs, auditLogItem{
				ID:         row.ID,
				CreatedAt:  row.CreatedAt,
				ActorSub:   row.ActorSub,
				ActorEmail: row.ActorEmail,
				Category:   row.Category,
				Action:     row.Action,
				Method:     row.Method,
				Route:      row.Route,
				Path:       row.Path,
				StatusCode: row.StatusCode,
			})
		}

		writeJSON(w, http.StatusOK, response)
	}
}
