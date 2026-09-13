// Package keycloak verifies Keycloak-issued JWTs for user-facing endpoints,
// loosely mirroring the pattern used by Orca (see
// orca/src/server/oid/keycloak.rs in https://github.com/ictunion/main-system-public):
// fetch the realm's JWKS, validate RS256 + issuer + audience (our own
// Keycloak client ID) using the specific signature key named by the token's
// own `kid` header, then check the caller's roles under
// `resource_access[client_id].roles`.
//
// Unlike Orca's simplification, Provider keys its cached signature keys by
// `kid` (not "just take the first sig key") and re-fetches the JWKS on
// demand whenever a token names a kid it doesn't recognize — so a live
// Keycloak key rotation (old + new key briefly coexisting in the JWKS, or
// the old key retired outright) doesn't require restarting this process.
package keycloak

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"

	"github.com/golang-jwt/jwt/v5"
)

// Role is one of bank-system's own Keycloak client roles — the roles this
// service's endpoints require, looked up under `resource_access[client_id]`
// on the token. Named to match Orca's own `Role` enum where the concept
// overlaps (see orca/src/server/oid.rs in ictunion/main-system-public), but
// this is bank-system's independent role list, not Orca's.
type Role string

const (
	// RolePaymentHistory protects:
	//   GET /payments/{member_number}/history  (also accepts RoleViewWorkplacePaymentHistory)
	//   GET /payments/{year}/{month}/missing
	//   GET /payments/{year}/missing
	RolePaymentHistory Role = "payment-history"

	// RoleListTransactions protects:
	//   GET /transactions
	//   GET /transactions/{id}
	//   GET /categories
	RoleListTransactions Role = "list-transactions"

	// RoleManageTransactions protects:
	//   PUT /transactions/{id}/assignment
	//   DELETE /transactions/{id}/assignment
	//   POST /categories
	//   DELETE /categories/{name}
	//   GET /payments/waivers
	//   POST /payments/{member_number}/waive
	//   DELETE /payments/{member_number}/waive/{year}/{month}
	RoleManageTransactions Role = "manage-transactions"

	// RoleManageBankAccounts protects:
	//   GET /account
	//   POST /account
	//   PATCH /account/{id}
	//   DELETE /account/{id}
	//   POST /account/{id}/sync
	//   POST /account/{id}/backfill
	RoleManageBankAccounts Role = "manage-bank-accounts"

	// RoleViewEventLogs protects:
	//   GET /event-logs
	RoleViewEventLogs Role = "view-event-logs"

	// RoleViewBudget protects:
	//   GET /transactions/summary
	RoleViewBudget Role = "view-budget"

	// RoleViewWorkplacePaymentHistory protects:
	//   GET /payments/{member_number}/history  (also accepts RolePaymentHistory)
	//   GET /payments/workplace/{year}/{month}/missing
	//   GET /payments/workplace/{year}/missing
	RoleViewWorkplacePaymentHistory Role = "view-workplace-payment-history"
)

// Claims is the subset of a Keycloak access token we care about.
type Claims struct {
	jwt.RegisteredClaims
	Email          string                `json:"email"`
	ResourceAccess map[string]RolesClaim `json:"resource_access"`
}

type RolesClaim struct {
	Roles []string `json:"roles"`
}

// HasRole reports whether the token carries the given role for the given
// Keycloak client — i.e. `resource_access[clientID].roles` contains role.
func (c *Claims) HasRole(clientID string, role Role) bool {
	ra, ok := c.ResourceAccess[clientID]
	if !ok {
		return false
	}
	for _, r := range ra.Roles {
		if Role(r) == role {
			return true
		}
	}
	return false
}

// Provider verifies tokens issued by one Keycloak realm for one client
// (audience). One bank-system instance talks to one realm/client, so one
// Provider covers the whole service.
type Provider struct {
	issuer   string
	clientID string
	jwksURL  string

	mutex   sync.Mutex
	keys map[string]*rsa.PublicKey // Keycloak key ID (kid) -> RSA public key
}

type jwksResponse struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// NewProvider fetches the realm's JWKS from Keycloak and returns a Provider
// scoped to that realm and clientID (the audience every verified token must
// carry).
func NewProvider(host, realm, clientID string) (*Provider, error) {
	issuer := fmt.Sprintf("%s/realms/%s", strings.TrimRight(host, "/"), realm)
	provider := &Provider{
		issuer:   issuer,
		clientID: clientID,
		jwksURL:  issuer + "/protocol/openid-connect/certs",
	}
	if err := provider.refreshKeys(); err != nil {
		return nil, err
	}
	return provider, nil
}

// refreshKeys re-fetches the realm's JWKS and replaces the cached kid ->
// public key map wholesale. Called once at startup (NewProvider) and again,
// on demand, by keyFor whenever a token names a kid not in the current
// cache — the latter is what lets a live Keycloak key rotation take effect
// without restarting this process.
func (p *Provider) refreshKeys() error {
	response, err := http.Get(p.jwksURL)
	if err != nil {
		return fmt.Errorf("fetching keycloak JWKS: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching keycloak JWKS: unexpected status %d", response.StatusCode)
	}

	var jwks jwksResponse
	if err := json.NewDecoder(response.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("decoding keycloak JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Use != "sig" || k.Kid == "" {
			continue
		}
		key, err := rsaPublicKeyFromJWK(k.N, k.E)
		if err != nil {
			return fmt.Errorf("parsing keycloak signature key %q: %w", k.Kid, err)
		}
		keys[k.Kid] = key
	}
	if len(keys) == 0 {
		return fmt.Errorf("keycloak JWKS: no signature keys found")
	}

	p.mutex.Lock()
	p.keys = keys
	p.mutex.Unlock()
	return nil
}

// keyFor returns the cached public key for kid, refreshing the JWKS once
// and retrying on a cache miss before giving up — see refreshKeys.
func (p *Provider) keyFor(kid string) (*rsa.PublicKey, error) {
	p.mutex.Lock()
	key, ok := p.keys[kid]
	p.mutex.Unlock()
	if ok {
		return key, nil
	}

	if err := p.refreshKeys(); err != nil {
		return nil, err
	}

	p.mutex.Lock()
	key, ok = p.keys[kid]
	p.mutex.Unlock()
	if !ok {
		return nil, fmt.Errorf("no signature key found for kid %q", kid)
	}
	return key, nil
}

func rsaPublicKeyFromJWK(nEncoded, eEncoded string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nEncoded)
	if err != nil {
		return nil, fmt.Errorf("decoding modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eEncoded)
	if err != nil {
		return nil, fmt.Errorf("decoding exponent: %w", err)
	}

	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}

	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

// Verify validates a bearer token's signature, issuer, and audience, and
// returns its claims. It does not check any role — see Authorize.
func (p *Provider) Verify(tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		kid, ok := t.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, fmt.Errorf("token has no kid header")
		}
		return p.keyFor(kid)
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(p.issuer),
		jwt.WithAudience(p.clientID),
	)
	if err != nil {
		return nil, fmt.Errorf("verifying token: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

// Authorize verifies the token and additionally requires it to carry the
// given role for this Provider's client — the Go equivalent of Orca's
// `OidProvider::require_role`.
func (p *Provider) Authorize(tokenString string, role Role) (*Claims, error) {
	claims, err := p.Verify(tokenString)
	if err != nil {
		return nil, err
	}
	if !claims.HasRole(p.clientID, role) {
		return nil, fmt.Errorf("token is missing role %q", role)
	}
	return claims, nil
}

// HasRole reports whether claims carries role for this Provider's own
// client. A thin wrapper around Claims.HasRole so callers outside this
// package — which don't have access to the unexported clientID — can do
// their own finer-grained role checks after RequireAnyRole has already
// established the caller holds at least one acceptable role (see
// handler.PaymentHistory, which branches on this to decide whether the
// caller gets the admin-wide view or must be scoped to their own
// workplace).
func (p *Provider) HasRole(claims *Claims, role Role) bool {
	return claims.HasRole(p.clientID, role)
}

// UserGroupIDs looks up the Keycloak group IDs (UUIDs) the bearer of
// tokenString currently belongs to, via Keycloak's **Account** REST API
// (`GET {issuer}/account/groups`) — mirrors Orca's own
// `KeycloakProvider::get_own_groups` (orca/src/server/oid/keycloak.rs in
// ictunion/main-system-public). Deliberately not the Admin API: the Account
// API is self-scoped (it answers only for whoever's token this is, so it
// just needs the caller's own already-verified bearer token forwarded — no
// service account, no separate confidential client, no client_credentials
// setup) and needs only the `view-groups` role, which is on by default via
// `default-roles-<realm>` in a stock Keycloak realm. This is also why it's
// a live call rather than a token claim: Keycloak's stock Group Membership
// *protocol mapper* only ever exposes a group's name/path, never its ID,
// and getting the ID onto the token any other way needs a script mapper —
// ruled out as non-standard.
func (p *Provider) UserGroupIDs(requestContext context.Context, tokenString string) ([]string, error) {
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, p.issuer+"/account/groups", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+tokenString)
	request.Header.Set("Accept", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetching keycloak account groups: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching keycloak account groups: unexpected status %d", response.StatusCode)
	}

	var groups []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&groups); err != nil {
		return nil, fmt.Errorf("decoding keycloak account groups: %w", err)
	}

	ids := make([]string, len(groups))
	for i, g := range groups {
		ids[i] = g.ID
	}
	return ids, nil
}
