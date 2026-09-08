package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/kubik/bank-system/internal/keycloak"
)

// RequireRole wraps next so it only runs if the request carries a Keycloak
// bearer token valid for provider's realm/client and holding role — mirrors
// Orca's `require_role` guard (see internal/keycloak package doc).
func RequireRole(provider *keycloak.Provider, role keycloak.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(authHeader, "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}

		if _, err := provider.Authorize(token, role); err != nil {
			writeError(w, http.StatusForbidden, "not authorized")
			return
		}

		next(w, r)
	}
}

type contextKey string

const claimsContextKey contextKey = "keycloak-claims"

// RequireAuth wraps next so it only runs if the request carries a valid
// Keycloak bearer token, with no role requirement — for self-service routes
// (e.g. /payments/me/history) that authorize by matching the token's own
// sub rather than a role. Verified claims are attached to the request
// context; read them back with ClaimsFromContext.
func RequireAuth(provider *keycloak.Provider, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(authHeader, "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}

		claims, err := provider.Verify(token)
		if err != nil {
			writeError(w, http.StatusForbidden, "not authorized")
			return
		}

		next(w, r.WithContext(context.WithValue(r.Context(), claimsContextKey, claims)))
	}
}

// ClaimsFromContext returns the Keycloak claims attached by RequireAuth.
func ClaimsFromContext(ctx context.Context) (*keycloak.Claims, bool) {
	claims, ok := ctx.Value(claimsContextKey).(*keycloak.Claims)
	return claims, ok
}
