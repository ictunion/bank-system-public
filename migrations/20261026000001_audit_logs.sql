-- +goose Up
-- +goose StatementBegin
-- audit_logs records who changed what and when: one row per authenticated
-- mutating API request (POST/PUT/PATCH/DELETE), written by the
-- handler.AuditLog middleware after the handler returns. Read-only (GET)
-- requests are never recorded.
--
-- actor_sub is the caller's Keycloak subject — the stable identity.
-- actor_email is a snapshot from the same token (email doubles as username
-- in this realm), kept so the log stays readable without a Keycloak lookup
-- and still shows who it was even if the account is later renamed or
-- removed. Not guaranteed to be present on a token, hence nullable.
--
-- category groups related actions (bank_account, transaction, waiver, ...)
-- for filtering; action names the specific operation
-- (create_bank_account, assign_transaction, ...). Both are owned by Go
-- constants (internal/handler/audit.go), not CHECK-constrained here, so
-- adding a route doesn't need a migration. route is the matched ServeMux pattern
-- ("PUT /transactions/{id}/assignment"), path the concrete request path
-- ("/transactions/42/assignment") — which carries the affected record's id.
--
-- status_code is the response status: a rejected request (400, 403 from a
-- handler-level role check, 409, ...) is still recorded as an attempt, so
-- filter on 2xx for changes that actually took effect.
CREATE TABLE audit_logs (
    id              BIGSERIAL PRIMARY KEY,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_sub       TEXT NOT NULL,
    actor_email     TEXT,
    category        TEXT NOT NULL,
    action          TEXT NOT NULL,
    method          TEXT NOT NULL,
    route           TEXT NOT NULL,
    path            TEXT NOT NULL,
    status_code     INTEGER NOT NULL
);

CREATE INDEX audit_logs_created_at_idx ON audit_logs (created_at DESC, id DESC);
CREATE INDEX audit_logs_category_created_at_idx ON audit_logs (category, created_at DESC);
CREATE INDEX audit_logs_actor_sub_created_at_idx ON audit_logs (actor_sub, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE audit_logs;
-- +goose StatementEnd
