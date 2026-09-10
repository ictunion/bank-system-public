package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kubik/bank-system/internal/db"
)

const (
	eventLogsDefaultLimit = 100
	eventLogsMaxLimit     = 500
)

type eventLogsResponse struct {
	Total  int64          `json:"total"`
	Limit  int32          `json:"limit"`
	Offset int32          `json:"offset"`
	Events []eventLogItem `json:"events"`
}

type eventLogItem struct {
	EventType    string     `json:"event_type"`
	ID           int64      `json:"id"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	Status       string     `json:"status"`
	Detail       *string    `json:"detail"`
	Fetched      *int32     `json:"fetched"`
	Processed    *int32     `json:"processed"`
	ErrorMessage *string    `json:"error_message"`
}

// ListEventLogs handles GET /event-logs — sync_fio_runs and sync_orca_runs
// merged into one admin-facing feed, newest first. No filters, just
// limit/offset paging (same defaults/cap as ListTransactions).
func ListEventLogs(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		queryParams := r.URL.Query()

		params := db.ListEventLogsParams{
			Lim: eventLogsDefaultLimit,
			Off: 0,
		}

		if s := queryParams.Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				writeError(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			if n > eventLogsMaxLimit {
				n = eventLogsMaxLimit
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

		rows, err := queries.ListEventLogs(r.Context(), params)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list event logs")
			return
		}

		response := eventLogsResponse{
			Limit:  params.Lim,
			Offset: params.Off,
			Events: make([]eventLogItem, 0, len(rows)),
		}
		if len(rows) > 0 {
			response.Total = rows[0].TotalCount
		}
		for _, row := range rows {
			var finishedAt *time.Time
			if row.FinishedAt.Valid {
				finishedAt = &row.FinishedAt.Time
			}
			response.Events = append(response.Events, eventLogItem{
				EventType:    row.EventType,
				ID:           row.ID,
				StartedAt:    row.StartedAt,
				FinishedAt:   finishedAt,
				Status:       row.Status,
				Detail:       row.Detail,
				Fetched:      row.Fetched,
				Processed:    row.Processed,
				ErrorMessage: row.ErrorMessage,
			})
		}

		writeJSON(w, http.StatusOK, response)
	}
}
