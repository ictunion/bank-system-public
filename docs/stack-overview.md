# Project Stack Overview

Basic CRUD REST API — Go + PostgreSQL.

## Language / Runtime
- **Go** (1.22+, for native `net/http` method-based routing and path parameters)

## HTTP Layer
- **Standard library `net/http`** as the baseline — Go 1.22+ supports `GET /users/{id}` style routing and method matching without a framework.
- Avoid `Fiber` (built on `fasthttp`, breaks compatibility with standard `net/http` middleware/tooling) unless there's a specific proven performance need.

## Database
- **PostgreSQL**
- **pgx** (native `pgxpool.Pool` interface, not the `database/sql` driver wrapper) — better Postgres type support (JSONB, arrays, UUID), batch queries, `COPY` support, less abstraction overhead.
- **sqlc** — generates type-safe Go code from raw SQL queries. Write SQL, get compile-time-checked Go functions/structs. First-class `pgx` support.
- **golang-migrate** or **goose** — schema migrations.

## Validation
- **go-playground/validator** — standard struct-tag based validation.

## Caching
Caching is not automatic anywhere in this stack — it's implemented explicitly at whichever layer makes sense:

- **HTTP-level (client/CDN caching)**: set `Cache-Control` and `ETag` headers manually in handlers. For static files (e.g. PDFs), `http.ServeFile` / `http.ServeContent` handle ETag generation and `If-Modified-Since` comparison automatically.
- **Reverse proxy / CDN** (nginx, Cloudflare, Fastly): recommended location for HTTP response caching in production — keeps the app stateless.
- **Application-level (DB query caching)**: only add once there's an actual hot-path bottleneck.
  - `go-redis/redis` for Redis-backed caching
  - `patrickmn/go-cache` or a `sync.Map` + TTL for simple single-instance in-memory caching

## HTTP surface
- All application routes are mounted under an **`/api` prefix** (`/api/payments/...`,
  `/api/healthz`, ...). `cmd/server/main.go` builds the route mux unprefixed and mounts
  it via `http.StripPrefix("/api", ...)`. The prefix keeps the API namespace clear of
  the admin frontend's client-side routes and lets the reverse proxy split the two.

## Auth
- **Keycloak** (same realm as the rest of ictunion's stack). Bearer tokens verified in
  `internal/keycloak`; role-gated routes via `handler.RequireRole` (`payment-history`,
  `list-transactions`, `manage-transactions`, `manage-bank-accounts`, `view-event-logs`). Machine-to-machine sync routes use a static shared secret instead
  (see `logic-design.md` "Orca Member Sync").
- Frontend logs in with Authorization Code + PKCE against the same `bank-system` client
  (public), tokens held in memory only. Full setup — including the **required audience
  mapper** without which the backend rejects every SPA token — in `frontend-auth.md`.

## Frontend (planned — admin tool, not built yet)
- Separate SPA: **React + TypeScript, built with Vite**. Scope is small — Keycloak login,
  a processed-transactions table, and two admin actions (assign a transaction to a
  member, mark a payment as covering multiple months).
- Supporting libs: TanStack Query (server state), `react-oidc-context` (Keycloak OIDC —
  wired, see `frontend-auth.md`), and still to add: TanStack Table (the list), React
  Hook Form + Zod (forms + response validation), a component kit (Mantine or shadcn/ui).
- Needs new backend write endpoints first: manual member assignment and lump-sum
  `payment_coverage` entry (see `db-design.md` "processed_transactions is a real table"
  and `payment_coverage` sections).

## Deployment
- **NixOS host.** Go service and the frontend are **separate Nix derivations**, no
  Go-side `embed`.
- **nginx** as the only public listener: TLS via `security.acme` (Let's Encrypt), one
  virtualHost for the admin subdomain. `location /` serves the frontend derivation's
  static files with an SPA fallback (`tryFiles $uri /index.html`); `location /api/`
  `proxy_pass` to the Go service.
- Go service runs as a **systemd unit** bound to `127.0.0.1` only (never exposed
  directly), secrets via `EnvironmentFile`.
- Frontend-only changes rebuild just the frontend derivation; the Go binary is
  untouched.
- nginx also sets the SPA's security headers (CSP, HSTS, `X-Content-Type-Options`,
  `frame-ancestors 'none'`) — exact header block in `frontend-auth.md`. The CSP's
  `connect-src` must list the Keycloak origin.

## Suggested Project Structure (starting point)
```
/cmd/server         — main.go, server startup
/internal/handler   — HTTP handlers
/internal/db        — sqlc-generated code, queries.sql
/internal/model     — domain structs
/migrations         — goose migration SQL files
/frontend           — React + Vite admin SPA (own package.json / node_modules)
/docs               — design notes (shared)
```
Go stays at the module root (idiomatic `cmd/`, `internal/`); the frontend is a
sibling directory, not nested under the backend. `nix develop` covers both
toolchains; `make frontend-*` targets wrap `npm` in `frontend/`.

## Open Decisions / To Revisit
- Whether to introduce Chi (or stay stdlib-only)
- Redis — add only if/when caching becomes necessary
- Frontend framework details (React + Vite direction set; libs above are provisional)
