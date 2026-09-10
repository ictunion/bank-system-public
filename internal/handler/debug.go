package handler

import (
	"net/http"
	"strings"

	"github.com/kubik/bank-system/internal/keycloak"
)

// WhoAmI handles GET /debug/whoami — dumps every claim on the caller's own
// bearer token as-is, unfiltered by internal/keycloak.Claims (which only
// declares the fields bank-system currently reads, so it would silently
// hide anything else Keycloak is actually sending). Only ever echoes the
// caller's own token back to them — no cross-user data — but still only
// registered when appConfig.Debug is set (see cmd/server/main.go): it's a
// setup/troubleshooting aid for wiring up Keycloak mappers (see
// docs/frontend-auth.md "Group membership mapper"), not something meant to
// stay reachable in production.
func WhoAmI(provider *keycloak.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(authHeader, "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}

		claims, err := provider.VerifyRaw(token)
		if err != nil {
			writeError(w, http.StatusForbidden, "not authorized")
			return
		}

		writeJSON(w, http.StatusOK, claims)
	}
}
