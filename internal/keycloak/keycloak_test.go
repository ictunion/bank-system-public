package keycloak

import (
	"context"
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
)

const (
	testIssuer   = "https://kc.example.test/realms/test"
	testClientID = "bank-system"
	testKid      = "test-key"
)

// newTestProvider builds a Provider with a locally generated RSA keypair —
// no real Keycloak, no JWKS fetch. Returns the private key too, so the test can sign tokens against
// it via signToken. Keyed by testKid, same as signToken sets on the token
// header — Provider now looks keys up by kid (see keyFor).
func newTestProvider(t *testing.T) (*Provider, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}
	return &Provider{
		issuer:   testIssuer,
		clientID: testClientID,
		keys:     map[string]*rsa.PublicKey{testKid: &key.PublicKey},
	}, key
}

func signToken(t *testing.T, key *rsa.PrivateKey, claims jwt.Claims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = testKid
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	return signed
}

// writeTestJWKS serves a one-key JWKS response under kid — a local
// equivalent of keycloaktest's writeJWKS that this package can't import
// (keycloaktest imports keycloak, and this file is package keycloak itself
// — importing it back would cycle).
func writeTestJWKS(w http.ResponseWriter, kid string, key *rsa.PublicKey) {
	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"keys": []map[string]string{
			{"kty": "RSA", "use": "sig", "kid": kid, "n": n, "e": e},
		},
	})
}

func validClaims() Claims {
	return Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    testIssuer,
			Subject:   "11111111-1111-1111-1111-111111111111",
			Audience:  jwt.ClaimStrings{testClientID},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
}

func TestVerify_Success(t *testing.T) {
	provider, key := newTestProvider(t)
	claims := validClaims()
	claims.Email = "member@example.test"

	got, err := provider.Verify(signToken(t, key, claims))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Subject != claims.Subject {
		t.Errorf("Subject = %q, want %q", got.Subject, claims.Subject)
	}
	if got.Email != "member@example.test" {
		t.Errorf("Email = %q, want member@example.test", got.Email)
	}
}

func TestVerify_Failures(t *testing.T) {
	provider, key := newTestProvider(t)
	_, otherKey := newTestProvider(t) // a different keypair — wrong signer

	tests := []struct {
		name   string
		claims Claims
		key    *rsa.PrivateKey
	}{
		{
			name: "wrong issuer",
			claims: func() Claims {
				c := validClaims()
				c.Issuer = "https://not-the-right-issuer.test/realms/other"
				return c
			}(),
			key: key,
		},
		{
			name: "wrong audience",
			claims: func() Claims {
				c := validClaims()
				c.Audience = jwt.ClaimStrings{"some-other-client"}
				return c
			}(),
			key: key,
		},
		{
			name: "expired",
			claims: func() Claims {
				c := validClaims()
				c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
				return c
			}(),
			key: key,
		},
		{
			name:   "signed by a different key",
			claims: validClaims(),
			key:    otherKey,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := provider.Verify(signToken(t, tt.key, tt.claims)); err == nil {
				t.Error("Verify returned nil error, want a validation failure")
			}
		})
	}
}

func TestVerify_MalformedToken(t *testing.T) {
	provider, _ := newTestProvider(t)
	if _, err := provider.Verify("not.a.jwt"); err == nil {
		t.Error("Verify returned nil error, want a parse failure")
	}
}

func TestClaims_HasRole(t *testing.T) {
	claims := Claims{
		ResourceAccess: map[string]RolesClaim{
			testClientID: {Roles: []string{"payment-history", "view-budget"}},
			"other-client": {Roles: []string{"payment-history"}},
		},
	}

	tests := []struct {
		name     string
		clientID string
		role     Role
		want     bool
	}{
		{"held role", testClientID, RolePaymentHistory, true},
		{"another held role", testClientID, RoleViewBudget, true},
		{"role not held", testClientID, RoleManageTransactions, false},
		{"role held, but for a different client", "other-client", RoleViewBudget, false},
		{"client not present in resource_access at all", "nonexistent-client", RolePaymentHistory, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := claims.HasRole(tt.clientID, tt.role); got != tt.want {
				t.Errorf("HasRole(%q, %q) = %v, want %v", tt.clientID, tt.role, got, tt.want)
			}
		})
	}
}

func TestProvider_Authorize(t *testing.T) {
	provider, key := newTestProvider(t)

	withRole := validClaims()
	withRole.ResourceAccess = map[string]RolesClaim{testClientID: {Roles: []string{"manage-bank-accounts"}}}

	withoutRole := validClaims()

	if _, err := provider.Authorize(signToken(t, key, withRole), RoleManageBankAccounts); err != nil {
		t.Errorf("Authorize with the role: %v", err)
	}
	if _, err := provider.Authorize(signToken(t, key, withoutRole), RoleManageBankAccounts); err == nil {
		t.Error("Authorize without the role: got nil error, want a missing-role failure")
	}
}

func TestProvider_HasRole(t *testing.T) {
	provider, _ := newTestProvider(t)
	claims := &Claims{ResourceAccess: map[string]RolesClaim{testClientID: {Roles: []string{"view-event-logs"}}}}

	if !provider.HasRole(claims, RoleViewEventLogs) {
		t.Error("HasRole = false, want true")
	}
	if provider.HasRole(claims, RoleManageTransactions) {
		t.Error("HasRole = true, want false")
	}
}

func TestUserGroupIDs(t *testing.T) {
	const token = "the-callers-own-bearer-token"
	var gotAuthHeader, gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
			{"id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},
		})
	}))
	defer server.Close()

	provider := &Provider{issuer: server.URL, clientID: testClientID}

	ids, err := provider.UserGroupIDs(context.Background(), token)
	if err != nil {
		t.Fatalf("UserGroupIDs: %v", err)
	}

	if gotPath != "/account/groups" {
		t.Errorf("request path = %q, want /account/groups", gotPath)
	}
	if gotAuthHeader != "Bearer "+token {
		t.Errorf("Authorization header = %q, want %q — the caller's own token must be forwarded, not a service-account one", gotAuthHeader, "Bearer "+token)
	}
	want := []string{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

func TestUserGroupIDs_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	provider := &Provider{issuer: server.URL, clientID: testClientID}

	if _, err := provider.UserGroupIDs(context.Background(), "token"); err == nil {
		t.Error("UserGroupIDs returned nil error on a 403 response, want an error")
	}
}

// TestProvider_KeyRotation pins down the actual bug the kid/refresh rework
// fixes: a token signed with a key that didn't exist yet when this process
// last fetched the JWKS must still verify, by refetching on the cache miss
// instead of requiring a restart.
func TestProvider_KeyRotation(t *testing.T) {
	oldKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}
	newKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}

	activeKid := "old-kid"
	activeKey := oldKey
	mux := http.NewServeMux()
	mux.HandleFunc("GET /realms/rotation-test/protocol/openid-connect/certs", func(w http.ResponseWriter, r *http.Request) {
		writeTestJWKS(w, activeKid, &activeKey.PublicKey)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	provider, err := NewProvider(server.URL, "rotation-test", testClientID)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// Rotate: Keycloak now serves a new key under a new kid. This process
	// hasn't refetched yet — its cache still only has "old-kid".
	activeKid = "new-kid"
	activeKey = newKey

	claims := validClaims()
	claims.Issuer = fmt.Sprintf("%s/realms/rotation-test", server.URL)
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "new-kid"
	signed, err := token.SignedString(newKey)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}

	if _, err := provider.Verify(signed); err != nil {
		t.Errorf("Verify after rotation: %v, want success via keyFor's on-demand refresh", err)
	}
}

// TestProvider_UnknownKidNeverResolves covers the other side: a kid that
// isn't in the JWKS even after a refresh (forged, or a stale/unrelated
// value) must still fail, not succeed against some fallback key.
func TestProvider_UnknownKidNeverResolves(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /realms/unknown-kid-test/protocol/openid-connect/certs", func(w http.ResponseWriter, r *http.Request) {
		writeTestJWKS(w, "known-kid", &key.PublicKey)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	provider, err := NewProvider(server.URL, "unknown-kid-test", testClientID)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	claims := validClaims()
	claims.Issuer = fmt.Sprintf("%s/realms/unknown-kid-test", server.URL)
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "totally-different-kid"
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}

	if _, err := provider.Verify(signed); err == nil {
		t.Error("Verify with an unresolvable kid: got nil error, want a failure")
	}
}

// TestVerify_MissingKidHeader covers a token with no kid at all — keyFor is
// never reached, Verify must reject it outright rather than falling back to
// some default key.
func TestVerify_MissingKidHeader(t *testing.T) {
	provider, key := newTestProvider(t)
	claims := validClaims()

	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}

	if _, err := provider.Verify(signed); err == nil {
		t.Error("Verify with no kid header: got nil error, want a failure")
	}
}
