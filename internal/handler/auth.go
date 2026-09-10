package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/kubik/bank-system/internal/keycloak"
)

// RequireRole wraps next so it only runs if the request carries a Keycloak
// bearer token valid for provider's realm/client and holding role — mirrors
// Orca's `require_role` guard (see internal/keycloak package doc). Verified
// claims (and the raw token string itself) are attached to the request
// context same as RequireAuth, so a handler behind RequireRole can still do
// its own finer-grained data scoping (see handler.WorkplaceMissingPayments,
// which forwards the raw token to keycloak.Provider.UserGroupIDs) — role
// grants the capability to call the route, the handler's own check narrows
// which rows it returns.
func RequireRole(provider *keycloak.Provider, role keycloak.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(authHeader, "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}

		claims, err := provider.Authorize(token, role)
		if err != nil {
			writeError(w, http.StatusForbidden, "not authorized")
			return
		}

		next(w, r.WithContext(withClaims(r.Context(), claims, token)))
	}
}

// RequireAnyRole wraps next so it only runs if the request carries a valid
// Keycloak bearer token holding at least one of roles — for routes where
// different roles grant different scopes of the *same* data rather than
// gating access to different routes entirely (see handler.PaymentHistory:
// RolePaymentHistory admins see any member, RoleViewWorkplacePaymentHistory reps
// see only their own workplace's members). RequireAnyRole only establishes
// that the caller holds *some* legitimate access — same as RequireRole, it
// attaches verified claims to the request context, and it's on the handler
// to check provider.HasRole itself and apply whatever narrower scoping the
// weaker role implies.
func RequireAnyRole(provider *keycloak.Provider, roles []keycloak.Role, next http.HandlerFunc) http.HandlerFunc {
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

		allowed := false
		for _, role := range roles {
			if provider.HasRole(claims, role) {
				allowed = true
				break
			}
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "not authorized")
			return
		}

		next(w, r.WithContext(withClaims(r.Context(), claims, token)))
	}
}

type contextKey string

const (
	claimsContextKey contextKey = "keycloak-claims"
	tokenContextKey  contextKey = "keycloak-token"
)

// withClaims attaches both the verified Claims and the raw token string to
// ctx — the raw string is what keycloak.Provider.UserGroupIDs needs to call
// Keycloak's self-scoped Account API as the caller themselves (see
// handler.WorkplaceMissingPayments), which decoded Claims alone can't provide
// (there's no re-encoding a JWT from its claims).
func withClaims(ctx context.Context, claims *keycloak.Claims, token string) context.Context {
	ctx = context.WithValue(ctx, claimsContextKey, claims)
	ctx = context.WithValue(ctx, tokenContextKey, token)
	return ctx
}

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

		next(w, r.WithContext(withClaims(r.Context(), claims, token)))
	}
}

// ClaimsFromContext returns the Keycloak claims attached by RequireAuth,
// RequireRole, or RequireAnyRole.
func ClaimsFromContext(requestContext context.Context) (*keycloak.Claims, bool) {
	claims, ok := requestContext.Value(claimsContextKey).(*keycloak.Claims)
	return claims, ok
}

// TokenFromContext returns the raw bearer token string attached by
// RequireAuth, RequireRole, or RequireAnyRole — needed for
// keycloak.Provider.UserGroupIDs, which must forward the caller's own token
// to Keycloak's self-scoped Account API (decoded Claims alone can't be
// turned back into a valid token).
func TokenFromContext(requestContext context.Context) (string, bool) {
	token, ok := requestContext.Value(tokenContextKey).(string)
	return token, ok
}
