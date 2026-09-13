package handler

import (
	"log"
	"net/http"
)

// Recover wraps next so a panicking handler returns a clean 500 JSON
// response and a log line instead of net/http's default: a reset connection
// and a bare stderr stack trace, with no application-level record of what
// broke.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic handling %s %s: %v", r.Method, r.URL.Path, rec)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// MaxRequestBodySize caps every request body read via MaxBodySize below.
// Every POST/PUT/PATCH handler here decodes a small JSON object (account
// details, category names, transaction assignments, waiver reasons, ...) —
// there's no file upload or bulk-data route in this API — so 1 MiB is
// generous headroom over any legitimate body while still bounding a client
// (malicious or just broken) from forcing an unbounded read into memory via
// encoding/json.Decoder, which has no size cap of its own.
const MaxRequestBodySize = 1 << 20 // 1 MiB

// MaxBodySize wraps next so every request body is capped at limit bytes.
// Reading past the limit fails the underlying read with "http: request body
// too large" (http.MaxBytesReader's own error) — every handler here already
// treats a body-read/decode failure as an ordinary 400 invalid-JSON-body
// response (see e.g. handler.CreateBankAccount), so this needs no per-handler
// changes to take effect.
func MaxBodySize(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}
