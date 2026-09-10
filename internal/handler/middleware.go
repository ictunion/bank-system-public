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
