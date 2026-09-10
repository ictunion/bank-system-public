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
	"context"
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
	// RolePaymentHistory gates the admin payment routes:
	//   GET /payments/{member_number}/history  (see handler.PaymentHistory)
	//   GET /payments/{year}/{month}/missing   (see handler.MissingPayments)
	// for Orca admins looking up any member's payment status. One role covers
	// both: anyone allowed to see an individual member's payments is also
	// allowed to see the cohort-wide "who didn't pay this month" list — the
	// data sensitivity is the same. Not required on the self-service
	// /payments/me/history route, which authorizes by matching the token's sub
	// against members.sub instead of checking this role.
	RolePaymentHistory Role = "payment-history"

	// RoleListTransactions gates GET /transactions and GET /transactions/{id}
	// (see handler.ListTransactions / handler.GetTransaction) — the admin
	// transaction browser. Kept separate from RolePaymentHistory because this
	// view shows every bank transaction with counterparty names, including
	// salary payments, with no redaction; "chase up missing member payments"
	// and "see who gets paid what" are different levels of trust.
	RoleListTransactions Role = "list-transactions"

	// RoleManageTransactions gates the transaction write routes
	// (PUT/DELETE /transactions/{id}/assignment — see handler.AssignTransaction
	// / handler.UnassignTransaction): manual member match and payment_coverage
	// editing. Separate from RoleListTransactions so a read-only auditor can
	// hold the browser role without being able to change matches.
	RoleManageTransactions Role = "manage-transactions"

	// RoleManageBankAccounts gates the bank account admin routes:
	//   GET /account            (see handler.ListBankAccounts)
	//   POST /account           (see handler.CreateBankAccount)
	//   PATCH /account/{id}     (see handler.UpdateBankAccount)
	//   DELETE /account/{id}    (see handler.DeleteBankAccount)
	//   POST /account/{id}/sync (see handler.TriggerFioSync)
	// for admins provisioning/viewing bank_accounts rows — this replaces direct
	// psql access as the way new accounts get added, including each account's
	// own Fio API token. Kept separate from RoleManageTransactions: creating a
	// bank account and configuring its Fio token is a different, higher-trust
	// action than editing a member match.
	RoleManageBankAccounts Role = "manage-bank-accounts"

	// RoleViewEventLogs gates GET /event-logs (see handler.ListEventLogs): the
	// merged sync_fio_runs/sync_orca_runs admin log. Read-only and covers both
	// sync jobs, so it doesn't naturally belong under RoleManageBankAccounts
	// (Fio-specific, write-capable) or RoleListTransactions (unrelated data) —
	// its own role, same as the other distinct admin views.
	RoleViewEventLogs Role = "view-event-logs"

	// RoleViewBudget gates GET /transactions/summary (see
	// handler.TransactionCategorySummary): totals grouped by category and
	// direction, never individual transactions or counterparty names — meant
	// to end up on every member's Keycloak account, not just admins, unlike
	// RoleListTransactions which exposes per-transaction detail. Kept
	// separate from RolePaymentHistory (member-specific payment history) and
	// RoleListTransactions (admin transaction browser) since this is
	// intentionally the widest-held, least-sensitive of the three.
	RoleViewBudget Role = "view-budget"

	// RoleViewWorkplacePaymentHistory gates the workplace-rep payment routes:
	//   GET /payments/workplace/{year}/{month}/missing
	//   GET /payments/workplace/{year}/missing
	// (see handler.WorkplaceMissingPayments / handler.WorkplaceMissingPaymentsInYear)
	// — the workplace-rep counterpart to RolePaymentHistory's admin-wide view.
	// Also accepted (via RequireAnyRole, alongside RolePaymentHistory) on
	// GET /payments/{member_number}/history — see handler.PaymentHistory.
	// Role only grants the
	// *capability* to call these routes; the actual member scoping comes from
	// a live Provider.UserGroupIDs lookup against Keycloak's Account API
	// (matched against members.workplace_executive_committee_sub), same as
	// how /payments/me/history scopes by the token's sub rather than a role.
	// See docs/logic-design.md "Workplace-Scoped Payment History".
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

	response, err := http.Get(issuer + "/protocol/openid-connect/certs")
	if err != nil {
		return nil, fmt.Errorf("fetching keycloak JWKS: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching keycloak JWKS: unexpected status %d", response.StatusCode)
	}

	var jwks jwksResponse
	if err := json.NewDecoder(response.Body).Decode(&jwks); err != nil {
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
// ruled out as non-standard. See docs/logic-design.md "Workplace-Scoped
// Payment History".
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
