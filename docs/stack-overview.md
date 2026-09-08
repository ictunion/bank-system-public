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

## Suggested Project Structure (starting point)
```
/cmd/server        — main.go, server startup
/internal/handler   — HTTP handlers
/internal/db        — sqlc-generated code, queries.sql
/internal/model      — domain structs
/migrations          — goose to migrate SQL files
```

## Open Decisions / To Revisit
- Whether to introduce Chi (or stay stdlib-only)
- Redis — add only if/when caching becomes necessary
- Auth strategy (not yet discussed)
- Deployment target (not yet discussed)
