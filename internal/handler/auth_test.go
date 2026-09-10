package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubik/bank-system/internal/keycloak"
	"github.com/kubik/bank-system/internal/keycloaktest"
)

// recordingNext returns an http.HandlerFunc that writes 200 and records
// whatever ClaimsFromContext/TokenFromContext see, so tests can confirm the
// auth wrappers actually populate the context next reads from — not just
// that they let the request through.
func recordingNext() (next http.HandlerFunc, called *bool, gotClaims **keycloak.Claims, gotToken *string) {
	called = new(bool)
	gotClaims = new(*keycloak.Claims)
	gotToken = new(string)
	next = func(w http.ResponseWriter, r *http.Request) {
		*called = true
		*gotClaims, _ = ClaimsFromContext(r.Context())
		*gotToken, _ = TokenFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}
	return
}

func TestRequireRole_Success(t *testing.T) {
	provider := keycloaktest.New(t)
	token := provider.SignToken(t, keycloak.Claims{
		ResourceAccess: map[string]keycloak.RolesClaim{
			keycloaktest.ClientID: {Roles: []string{string(keycloak.RolePaymentHistory)}},
		},
	})
	next, called, gotClaims, gotToken := recordingNext()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	RequireRole(provider.Provider, keycloak.RolePaymentHistory, next)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if !*called {
		t.Fatal("next was not called")
	}
	if *gotClaims == nil {
		t.Error("ClaimsFromContext returned nil inside next")
	}
	if *gotToken != token {
		t.Errorf("TokenFromContext = %q, want the original token", *gotToken)
	}
}

func TestRequireRole_MissingBearerToken(t *testing.T) {
	provider := keycloaktest.New(t)
	next, called, _, _ := recordingNext()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil) // no Authorization header
	RequireRole(provider.Provider, keycloak.RolePaymentHistory, next)(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if *called {
		t.Error("next was called despite no bearer token")
	}
}

func TestRequireRole_MissingRole(t *testing.T) {
	provider := keycloaktest.New(t)
	token := provider.SignToken(t, keycloak.Claims{}) // no resource_access at all
	next, called, _, _ := recordingNext()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	RequireRole(provider.Provider, keycloak.RolePaymentHistory, next)(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if *called {
		t.Error("next was called despite the token missing the required role")
	}
}

func TestRequireRole_InvalidToken(t *testing.T) {
	provider := keycloaktest.New(t)
	next, called, _, _ := recordingNext()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer not-a-real-token")
	RequireRole(provider.Provider, keycloak.RolePaymentHistory, next)(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if *called {
		t.Error("next was called despite an unparseable token")
	}
}

func TestRequireAnyRole(t *testing.T) {
	provider := keycloaktest.New(t)
	roles := []keycloak.Role{keycloak.RolePaymentHistory, keycloak.RoleViewWorkplacePaymentHistory}

	t.Run("holds one of the roles", func(t *testing.T) {
		token := provider.SignToken(t, keycloak.Claims{
			ResourceAccess: map[string]keycloak.RolesClaim{
				keycloaktest.ClientID: {Roles: []string{string(keycloak.RoleViewWorkplacePaymentHistory)}},
			},
		})
		next, called, _, _ := recordingNext()

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		RequireAnyRole(provider.Provider, roles, next)(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
		}
		if !*called {
			t.Error("next was not called despite holding one of the roles")
		}
	})

	t.Run("holds none of the roles", func(t *testing.T) {
		token := provider.SignToken(t, keycloak.Claims{
			ResourceAccess: map[string]keycloak.RolesClaim{
				keycloaktest.ClientID: {Roles: []string{"manage-transactions"}},
			},
		})
		next, called, _, _ := recordingNext()

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		RequireAnyRole(provider.Provider, roles, next)(recorder, request)

		if recorder.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
		if *called {
			t.Error("next was called despite holding none of the required roles")
		}
	})
}

func TestRequireAuth(t *testing.T) {
	provider := keycloaktest.New(t)

	t.Run("valid token, no role needed", func(t *testing.T) {
		token := provider.SignToken(t, keycloak.Claims{}) // no roles at all — RequireAuth doesn't check any
		next, called, gotClaims, _ := recordingNext()

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		RequireAuth(provider.Provider, next)(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
		}
		if !*called || *gotClaims == nil {
			t.Error("next was not called with claims populated")
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		next, called, _, _ := recordingNext()

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Authorization", "Bearer garbage")
		RequireAuth(provider.Provider, next)(recorder, request)

		if recorder.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
		if *called {
			t.Error("next was called despite an invalid token")
		}
	})
}

func TestClaimsFromContext_Absent(t *testing.T) {
	if claims, ok := ClaimsFromContext(context.Background()); ok || claims != nil {
		t.Errorf("ClaimsFromContext on a bare context = (%v, %v), want (nil, false)", claims, ok)
	}
}

func TestTokenFromContext_Absent(t *testing.T) {
	if token, ok := TokenFromContext(context.Background()); ok || token != "" {
		t.Errorf("TokenFromContext on a bare context = (%q, %v), want (\"\", false)", token, ok)
	}
}
