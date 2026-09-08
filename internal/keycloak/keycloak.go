// Package keycloak verifies Keycloak-issued JWTs for user-facing endpoints,
// mirroring the pattern used by Orca (see orca/src/server/oid/keycloak.rs in
// https://github.com/ictunion/main-system-public): fetch the realm's JWKS
// once at startup, pick the signature ("sig") key, validate RS256 + issuer +
// audience (our own Keycloak client ID), then check the caller's roles under
// `resource_access[client_id].roles`.
//
// Prototype-level: the signing key is fetched once at Provider construction
// and never refreshed/rotated, and there's no `kid`-based key lookup (same
// simplification Orca's implementation makes). Revisit before relying on
// this for anything beyond debug endpoints.
package keycloak

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// Role is one of bank-system's own Keycloak client roles — the roles this
// service's endpoints require, looked up under `resource_access[client_id]`
// on the token. Named to match Orca's own `Role` enum where the concept
// overlaps (see orca/src/server/oid.rs in ictunion/main-system-public), but
// this is bank-system's independent role list, not Orca's.
type Role string

const (
	// RoleListMembers gates GET /members (see handler.ListMembers) — debug
	// prototype only, not meant to survive to production as-is.
	RoleListMembers Role = "list-members"

	// RolePaymentHistory gates GET /payments/{member_number}/history (see
	// handler.PaymentHistory) — the by-member_number admin route, for Orca
	// admins looking up any member's history. Not required on the future
	// self-service /payments/me/history route, which authorizes by matching
	// the token's sub against members.sub instead of checking this role.
	RolePaymentHistory Role = "payment-history"
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
	key      *rsa.PublicKey
}

type jwksResponse struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// NewProvider fetches the realm's JWKS from Keycloak and returns a Provider
// scoped to that realm and clientID (the audience every verified token must
// carry).
func NewProvider(host, realm, clientID string) (*Provider, error) {
	issuer := fmt.Sprintf("%s/realms/%s", strings.TrimRight(host, "/"), realm)

	resp, err := http.Get(issuer + "/protocol/openid-connect/certs")
	if err != nil {
		return nil, fmt.Errorf("fetching keycloak JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching keycloak JWKS: unexpected status %d", resp.StatusCode)
	}

	var jwks jwksResponse
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, fmt.Errorf("decoding keycloak JWKS: %w", err)
	}

	var sigKey *jwk
	for i, k := range jwks.Keys {
		if k.Use == "sig" {
			sigKey = &jwks.Keys[i]
			break
		}
	}
	if sigKey == nil {
		return nil, fmt.Errorf("keycloak JWKS: no signature key found")
	}

	key, err := rsaPublicKeyFromJWK(sigKey.N, sigKey.E)
	if err != nil {
		return nil, fmt.Errorf("parsing keycloak signature key: %w", err)
	}

	return &Provider{issuer: issuer, clientID: clientID, key: key}, nil
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
		return p.key, nil
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
