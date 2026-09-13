// Package keycloaktest builds a real *keycloak.Provider backed by a local
// RSA keypair and a fake Keycloak HTTP server (JWKS + Account API), for
// tests that need one without a real Keycloak instance. A sibling to
// internal/dbtest: same idea (a real dependency, faked at the network edge)
// applied to Keycloak instead of Postgres.
package keycloaktest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/kubik/bank-system/internal/keycloak"
)

// Realm/ClientID are fixed — nothing in these tests needs them to vary. Kid
// is the fake JWKS's one signature key's ID — keycloak.Provider now looks
// up keys by kid (see internal/keycloak), so the fake JWKS entry and every
// token SignToken issues must agree on it.
const (
	Realm    = "test-realm"
	ClientID = "bank-system"
	Kid      = "test-key"
)

// Provider wraps a real *keycloak.Provider — every exported method
// (Verify, Authorize, HasRole, UserGroupIDs) behaves exactly as production
// code sees it, just pointed at New's fake server instead of real Keycloak.
type Provider struct {
	*keycloak.Provider
	PrivateKey *rsa.PrivateKey
	Issuer     string

	groups *[]string
}

// New starts a fake Keycloak server (JWKS + Account API) and returns a real
// *keycloak.Provider pointed at it. The server is closed on test cleanup.
func New(t *testing.T) *Provider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}
	groups := new([]string)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /realms/"+Realm+"/protocol/openid-connect/certs", func(w http.ResponseWriter, r *http.Request) {
		writeJWKS(w, &key.PublicKey)
	})
	// UserGroupIDs is self-scoped in production (see internal/keycloak
	// docs) — this fake doesn't bother inspecting the bearer token to
	// decide which groups to return, since no test here needs more than
	// one caller's worth of groups at a time. SetGroups controls the
	// response for every subsequent call.
	mux.HandleFunc("GET /realms/"+Realm+"/account/groups", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		out := make([]map[string]string, len(*groups))
		for i, g := range *groups {
			out[i] = map[string]string{"id": g}
		}
		_ = json.NewEncoder(w).Encode(out)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	provider, err := keycloak.NewProvider(server.URL, Realm, ClientID)
	if err != nil {
		t.Fatalf("keycloak.NewProvider against fake server: %v", err)
	}

	return &Provider{
		Provider:   provider,
		PrivateKey: key,
		Issuer:     fmt.Sprintf("%s/realms/%s", server.URL, Realm),
		groups:     groups,
	}
}

// SetGroups controls what UserGroupIDs returns for every subsequent call.
// Sequential-test-only — not safe to mutate from more than one goroutine at
// a time.
func (p *Provider) SetGroups(groups []string) {
	*p.groups = groups
}

// SignToken signs claims as this provider's fake Keycloak would. Issuer,
// Audience, and ExpiresAt default to valid values when left zero — set them
// explicitly to test a specific failure (wrong issuer, expired, ...).
func (p *Provider) SignToken(t *testing.T, claims keycloak.Claims) string {
	t.Helper()

	if claims.Issuer == "" {
		claims.Issuer = p.Issuer
	}
	if len(claims.Audience) == 0 {
		claims.Audience = jwt.ClaimStrings{ClientID}
	}
	if claims.ExpiresAt == nil {
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = Kid
	signed, err := token.SignedString(p.PrivateKey)
	if err != nil {
		t.Fatalf("signing test token: %v", err)
	}
	return signed
}

func writeJWKS(w http.ResponseWriter, key *rsa.PublicKey) {
	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"keys": []map[string]string{
			{"kty": "RSA", "use": "sig", "kid": Kid, "n": n, "e": e},
		},
	})
}
