# API Testing Plan

Scope: `internal/`, `cmd/`, everything reachable from the Go API. Frontend tests are
explicitly out of scope for now. This is the plan — see the end of the doc for what's
not built yet.

## Philosophy

**Stdlib only.** `testing` package, table-driven tests (`[]struct{name, input, want}`
looped with `t.Run`), no assertion library (no testify, no go-cmp). Matches how the rest
of this codebase already avoids dependencies it doesn't strictly need (hand-rolled
frontend, no ORM, no HTTP framework) — tests should read the same way.

**Real Postgres, not a mocked `*db.Queries`.** A large share of this app's actual logic
lives in the SQL itself, not in Go: `member_arrears` (a view), `generate_series`-driven
missed-payment detection, `ANY(uuid[])` workplace scoping, real joins across
`processed_transactions`/`raw_transactions`/`payment_coverage`. Mocking `*db.Queries`
would only prove a handler calls the right Go method with the right arguments — it
can't catch a wrong `JOIN`, an off-by-one in the liability-window math, or a query that
silently returns the wrong rows. Given how much of this project's actual bugs (see this
session's history) turned out to live in SQL edge cases, not Go logic, that trade-off
isn't worth it here. So: handler and query-layer tests run against a real, migrated
Postgres database, not a fake.

**`net/http/httptest`, no real server.** Handlers are tested by calling the
`http.HandlerFunc` directly with `httptest.NewRecorder()` / `httptest.NewRequest()` —
no `httptest.NewServer`, no real network socket, for anything below the outermost
`handler.Recover` wrapper.

## Test database

One extra database on the *same* Postgres cluster `make db-start` already runs (same
`$PGDATA`, same port) — not a second `pg_ctl` instance. Proposed Makefile additions
(not yet added — see "What's not built yet"):

```make
DB_TEST_NAME ?= bank_system_test

.PHONY: db-test-init
db-test-init: db-start
	@psql -h localhost -p $(DB_PORT) -U postgres -tc "SELECT 1 FROM pg_database WHERE datname='$(DB_TEST_NAME)'" | grep -q 1 \
		|| psql -h localhost -p $(DB_PORT) -U postgres -c "CREATE DATABASE $(DB_TEST_NAME) OWNER $(DB_USER)"

.PHONY: migrate-test
migrate-test:
	goose -dir migrations postgres "postgres://$(DB_USER):$(DB_PASSWORD)@localhost:$(DB_PORT)/$(DB_TEST_NAME)?sslmode=disable" up

.PHONY: test
test: db-test-init migrate-test
	TEST_DATABASE_URL="postgres://$(DB_USER):$(DB_PASSWORD)@localhost:$(DB_PORT)/$(DB_TEST_NAME)?sslmode=disable" go test ./...
```

`TEST_DATABASE_URL` is deliberately a separate env var from `DATABASE_URL` — a bug in a
test's setup pointing at the wrong database should never be able to touch dev data by
sharing a variable name.

**Isolation: one transaction per test, rolled back, not truncate-between-tests.** This
project already has the right primitive for this — `db.DBTX` (see
`internal/db/db.go`, sqlc-generated) is satisfied by both `*pgxpool.Pool` and `pgx.Tx`,
which is exactly how `AssignTransaction`/`UnassignTransaction` already build a
tx-scoped `*db.Queries` today. Tests do the same: `pool.Begin(ctx)`, `db.New(tx)`,
register `t.Cleanup(func() { tx.Rollback(ctx) })`. Every test starts from a clean,
migrated-but-empty schema and its writes vanish on rollback — no truncation step, no
test-ordering dependency, safe to run `-parallel`.

**Skip gracefully, don't fail, when `TEST_DATABASE_URL` is unset.** A shared test helper
(new package, `internal/dbtest`) exposes `dbtest.Tx(t *testing.T) *db.Queries`, calling
`t.Skip("TEST_DATABASE_URL not set")` if the env var is missing. That way `go test ./...`
still runs the DB-free packages (`internal/config`, pure-function parts of
`internal/processing`) for a contributor who hasn't set up a test DB, and CI (which
does) gets full coverage. Consistent with how this repo already treats missing
optional config (e.g. `KeycloakServiceClientID` before it was removed) as "degrade,
don't crash," not with silently skipping something that should be a hard failure —
worth a second look if it ever hides a real CI misconfiguration.

## Package-by-package

| Package | Approach |
|---|---|
| `internal/config` | Pure. Table-driven, stdlib only, no DB. |
| `internal/keycloak` | See "Testing the auth layer" below. |
| `internal/handler` | The bulk of "everything in the API." One `_test.go` per handler file, same `package handler` (not `handler_test`) so tests can use unexported helpers directly — `withClaims` to inject fake auth context, `workplaceGroupUUIDs` etc. `httptest.NewRecorder`/`NewRequest` calling handler funcs directly against a `dbtest.Tx(t)`-backed `*db.Queries`. |
| `internal/syncjob` | `httptest.NewServer` standing in for Fio/Orca, real DB tx underneath (these already write through `insertTransactions`/`UpsertMember` etc.). Both `orca.NewClient` and `fio.NewClient` take `baseURL` as a param, so both are trivial to point at a test server. |
| `internal/processing` | Real DB tx — `processOne`'s categorization branches (VS match / `mzda` substring / directional fallback) are exactly the kind of logic worth table-driving over fixture `raw_transactions` rows. |
| `internal/orca`, `internal/fio` | Client-level parsing tests (`Transactions()`, `toTransaction()`) are pure — no DB, no network, just feed fixture JSON payloads in. |
| `internal/db` | Not tested directly — it's sqlc-generated, and its correctness is exactly what the `handler`/`processing` integration tests exercise indirectly by calling it against real Postgres. Hand-testing generated boilerplate would be testing sqlc, not this codebase. |
| `cmd/server` | Not unit tested — `main.go` is wiring, no branching logic worth a table. Covered implicitly by whichever handler tests exist. |

## Testing the auth layer

`RequireRole`/`RequireAnyRole`/`RequireAuth` all depend on a live `*keycloak.Provider`,
which itself depends on Keycloak's JWKS endpoint at construction time. Two different
things need testing, deliberately kept separate rather than routing every handler test
through a fake Keycloak:

1. **`internal/keycloak` itself** (same-package tests, so unexported fields are
   reachable): generate one RSA keypair locally (`rsa.GenerateKey`), hand-craft JWTs
   against it with `jwt/v5` (already a dependency), build a `Provider{issuer, clientID,
   key}` struct literal directly (no real Keycloak, no JWKS fetch) to test
   `Verify`/`Authorize`/`HasRole`. For `UserGroupIDs`, `httptest.NewServer` stands in
   for Keycloak's Account API (`GET {issuer}/account/groups`) — point `Provider.issuer`
   at the test server's URL and assert on the request it receives (right path, right
   `Authorization: Bearer <token>` forwarded) and the response parsing.
2. **`internal/handler` tests** don't re-verify auth at all — they call
   `withClaims(req.Context(), claims, token)` directly to simulate "the middleware
   already ran and put this caller's claims/token in context," then call the handler
   function itself. This is the standard way to keep handler tests about handler logic,
   not about re-proving JWT verification works (that's `internal/keycloak`'s job) —
   same reasoning as unit-testing a function versus its caller.

`RequireRole`/`RequireAnyRole`/`RequireAuth` themselves get their own small test in
`internal/handler` (same package) using the fake `Provider` from (1), confirming the
context actually gets `withClaims`-populated correctly and role rejection produces the
right status/body — this is the one place both layers meet.

## What's not built yet

Still just the plan for actual test coverage — nothing under `_test.go` exists yet. One
prerequisite from the original version of this doc is already done: `fio.NewClient`
now takes `baseURL` as a param, sourced from the required `FIO_API_URL` env var (see
`internal/config`) — no default baked into Go code, same single-source-of-truth
treatment as `ORCA_API_URL`. Matches `orca.NewClient`'s existing shape, so both Fio and
Orca sync tests can point at an `httptest.NewServer` with no further client changes;
tests just pass the test server's URL as `baseURL` directly, same as production passes
the real one.

What's left, in the order I'd actually build it:

1. Makefile targets (`db-test-init`, `migrate-test`, `test`) and `internal/dbtest`
   helper package — nothing runs without these.
2. Test coverage itself, package by package per the table above. `internal/handler` is
   both the largest and the actual point of "test everything in the API," so it's the
   one worth starting with.

Both DB setup (`db-test-init`, `migrate-test`) and running the suite (`make test`) need
`nix develop` — same restriction as every other `goose`/`psql`/`go` command in this
repo (see root `CLAUDE.md`). I can write every test file; running them is on you.
