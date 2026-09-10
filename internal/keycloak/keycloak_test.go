package keycloak

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuer   = "https://kc.example.test/realms/test"
	testClientID = "bank-system"
)

// newTestProvider builds a Provider with a locally generated RSA keypair —
// no real Keycloak, no JWKS fetch (see docs/testing.md "Testing the auth
// layer"). Returns the private key too, so the test can sign tokens against
// it via signToken.
func newTestProvider(t *testing.T) (*Provider, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}
	return &Provider{issuer: testIssuer, clientID: testClientID, key: &key.PublicKey}, key
}

func signToken(t *testing.T, key *rsa.PrivateKey, claims jwt.Claims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	return signed
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
