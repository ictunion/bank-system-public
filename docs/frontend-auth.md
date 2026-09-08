# Frontend Auth (Keycloak)

How the admin SPA (`frontend/`) authenticates against the same Keycloak realm the
backend validates tokens for. See also `internal/keycloak` (backend verification)
and `logic-design.md` "Orca Member Sync" (the separate machine-to-machine path,
which does *not* use Keycloak).

## Flow

- **Authorization Code + PKCE**, via `react-oidc-context` / `oidc-client-ts`. No
  implicit flow, no client secret (the SPA is a public client).
- On load, `AuthGate` redirects an unauthenticated user to Keycloak. If the
  Keycloak SSO session is alive the round trip is prompt-less.
- Access token is attached as `Authorization: Bearer …` by `src/api/client.ts`.
  On a `401` it tries one silent refresh + retry, then falls back to a full
  redirect.
- Sign-out is RP-initiated (`signoutRedirect`) so the Keycloak session is ended,
  not just the local token.

## Token storage

Tokens (access / refresh / id) are held **in memory only**
(`InMemoryWebStorage`). Consequences:

- Injected script (XSS) cannot read a persisted token — there is no persisted
  token.
- A full page reload drops the session; the app re-runs the (usually prompt-less)
  redirect to restore it.
- The short-lived redirect state (PKCE verifier, nonce) still uses
  `sessionStorage` — it must survive the navigation to Keycloak and back, is not
  a bearer token, and expires in seconds.

**Gold-standard alternative (not implemented): a BFF.** A small backend endpoint
does the code exchange, keeps tokens server-side, sets an `httpOnly`,
`SameSite=Strict` session cookie, and proxies `/api`. The browser then never
holds a token at all. This needs backend session handling and a proxy layer the
Go service doesn't have today. Revisit if the in-memory-token model proves
insufficient.

## Required Keycloak client config

One **public** client, shared by SPA and backend. Backend only fetches JWKS and
verifies signatures — it never talks to the token endpoint — so a public client
is fine for both.

| Setting | Value |
|---|---|
| Client ID | `bank-system` (matches backend `KEYCLOAK_CLIENT_ID`) |
| Client authentication | **Off** (public) |
| Standard flow | **On** |
| Direct access grants | Off |
| PKCE | `S256` (Advanced → Proof Key for Code Exchange Code Challenge Method) |
| Valid redirect URIs | `https://payments.example.com/*`, dev: `http://localhost:5173/*` |
| Valid post-logout redirect URIs | same |
| Web origins | `+` (derive from redirect URIs — needed for CORS on the token endpoint) |

### Audience mapper — required, easy to miss

The backend does `jwt.WithAudience("bank-system")`. Keycloak does **not** put a
client's own ID in the `aud` claim by default, so without this every SPA token is
rejected with an audience error.

Add on the `bank-system` client: **Client scopes → `bank-system-dedicated` → Add
mapper → By configuration → Audience**

- Included Client Audience: `bank-system`
- Add to access token: On

Verify: decode an access token (jwt.io) and confirm `aud` contains
`bank-system`, and `resource_access.bank-system.roles` lists the user's roles.

### Roles

Client roles on `bank-system`: `list-members`, `payment-history`,
`list-transactions` (see `internal/keycloak/keycloak.go`). Assign to the admin
users who should reach those endpoints. The SPA reads
`resource_access["bank-system"].roles` from the token to show/hide admin actions;
the backend independently enforces them.

## Production hardening (nginx)

Set on the SPA vhost (see `stack-overview.md` deployment):

```
add_header Content-Security-Policy "default-src 'self'; connect-src 'self' https://<keycloak-host>; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'" always;
add_header X-Content-Type-Options "nosniff" always;
add_header Referrer-Policy "same-origin" always;
add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
```

- `connect-src` must list the Keycloak origin (token endpoint + JWKS) plus
  `'self'` (the `/api` calls).
- `frame-ancestors 'none'` — the app is never framed; blocks clickjacking.
- `style-src 'unsafe-inline'` is currently needed for React inline `style={}`
  attributes; drop it if styling moves to CSS files/classes.
- No `frame-src` entry: session monitoring is disabled, so Keycloak is never
  loaded in an iframe.

A strict CSP is **not** applied in dev — Vite's HMR needs inline scripts and a
websocket. It's enforced only at the nginx layer in production.

## Known backend limitation

`internal/keycloak` fetches the realm JWKS once at startup, never refreshes, and
picks the first `sig` key without `kid` matching. A realm with two active signing
keys (normal during key rotation) can make valid SPA tokens fail verification.
Fix before this is more than a prototype: `kid`-based lookup + periodic JWKS
refresh.
