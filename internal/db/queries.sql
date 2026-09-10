-- name: ListBankAccounts :many
-- Admin-facing list (GET /account) — deliberately excludes the Fio token.
-- Includes soft-deleted accounts (is_active = false) so admins still see them
-- in the UI, crossed out, as a record that the account used to exist. For the
-- token itself, see ListBankAccountsWithToken (internal use only).
SELECT id, fio_account_id, iban, currency, display_name, created_at,
       (fio_token_encrypted IS NOT NULL)::boolean AS has_token,
       (deleted_at IS NULL)::boolean AS is_active
FROM bank_accounts ORDER BY id;

-- name: ListBankAccountsWithToken :many
-- Internal use only (the Fio sync job) — includes the decrypted Fio token.
-- Includes soft-deleted accounts (is_active = false); the sync job itself is
-- responsible for skipping those rather than filtering here, since "should
-- this account sync" is business logic, not a storage-layer concern. Never
-- expose this query's result over HTTP. encryption_key is bound as a query
-- parameter via pgp_sym_decrypt, never interpolated into SQL text. fio_token
-- is NULL for any account not yet backfilled with a token.
SELECT id, fio_account_id, iban, currency, display_name, created_at,
       pgp_sym_decrypt(fio_token_encrypted, sqlc.arg(encryption_key)::text) AS fio_token,
       (deleted_at IS NULL)::boolean AS is_active
FROM bank_accounts ORDER BY id;

-- name: GetBankAccountWithToken :one
-- Internal use only (the manual "sync now" trigger — see
-- syncjob.SyncOneAccount) — single-row counterpart of
-- ListBankAccountsWithToken, same never-expose-over-HTTP caveat.
SELECT id, fio_account_id, iban, currency, display_name, created_at,
       pgp_sym_decrypt(fio_token_encrypted, sqlc.arg(encryption_key)::text) AS fio_token,
       (deleted_at IS NULL)::boolean AS is_active
FROM bank_accounts WHERE id = sqlc.arg(id);

-- name: CreateBankAccount :one
-- fio_token is encrypted at rest via pgcrypto (pgp_sym_encrypt) using
-- encryption_key (BANK_TOKEN_ENCRYPTION_KEY env var) — never stored in the DB
-- itself. RETURNING list explicitly excludes fio_token_encrypted so the
-- ciphertext (and a fortiori the token) is never echoed back to the caller.
INSERT INTO bank_accounts (fio_account_id, iban, currency, display_name, fio_token_encrypted)
VALUES (
    sqlc.arg(fio_account_id),
    sqlc.narg(iban),
    sqlc.arg(currency),
    sqlc.arg(display_name),
    pgp_sym_encrypt(sqlc.arg(fio_token)::text, sqlc.arg(encryption_key)::text)
)
RETURNING id, fio_account_id, iban, currency, display_name, created_at;

-- name: UpdateBankAccount :one
-- fio_account_id/iban/currency are properties of the real Fio account, not
-- editable metadata — only our own display_name and the sync token can change
-- here. fio_token is sqlc.narg: NULL means "leave the existing token
-- untouched", any non-NULL value re-encrypts and replaces it (see
-- CreateBankAccount for the same pgp_sym_encrypt pattern).
-- Only touches active accounts (deleted_at IS NULL) — a soft-deleted account
-- is a historical record, not something to edit; 0 rows affected reads as
-- "not found" either way (missing id or soft-deleted id).
UPDATE bank_accounts
SET display_name = sqlc.arg(display_name),
    fio_token_encrypted = CASE
        WHEN sqlc.narg(fio_token)::text IS NOT NULL
        THEN pgp_sym_encrypt(sqlc.narg(fio_token)::text, sqlc.arg(encryption_key)::text)
        ELSE fio_token_encrypted
    END
WHERE id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING id, fio_account_id, iban, currency, display_name, created_at,
          (fio_token_encrypted IS NOT NULL)::boolean AS has_token;

-- name: DeleteBankAccount :execrows
-- Soft delete: raw_transactions/sync_fio_runs reference bank_accounts.id with
-- no ON DELETE clause (see initial_schema.sql), so a hard delete would fail
-- once an account has synced history — and the row also needs to stick around
-- for admins to see it used to exist (see ListBankAccounts). deleted_at IS
-- NULL in the WHERE guards against re-timestamping an already-deleted row;
-- :execrows lets handler.DeleteBankAccount tell "already gone" (0 rows) from
-- "deleted just now" (1 row) and return 404 vs 204 accordingly.
UPDATE bank_accounts SET deleted_at = now()
WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: ListEventLogs :many
-- Admin event log (GET /event-logs): sync_fio_runs and sync_orca_runs merged
-- into one feed, newest first. No filters — just a simple paged log, same
-- limit/offset + count(*) OVER () pagination pattern as ListTransactions.
-- detail is the bank account's display name for a fio_sync row, NULL for
-- orca_sync (there's no per-account breakdown for the member sync).
SELECT *, count(*) OVER () AS total_count
FROM (
    SELECT
        'fio_sync'::text AS event_type,
        sfr.id,
        sfr.started_at,
        sfr.finished_at,
        sfr.status,
        ba.display_name AS detail,
        sfr.transactions_fetched AS fetched,
        sfr.transactions_inserted AS processed,
        sfr.error_message
    FROM sync_fio_runs sfr
    LEFT JOIN bank_accounts ba ON ba.id = sfr.bank_account_id
    UNION ALL
    SELECT
        'orca_sync'::text AS event_type,
        sor.id,
        sor.started_at,
        sor.finished_at,
        sor.status,
        NULL::text AS detail,
        sor.members_fetched AS fetched,
        sor.members_upserted AS processed,
        sor.error_message
    FROM sync_orca_runs sor
) events
ORDER BY started_at DESC, id DESC
LIMIT sqlc.arg(lim)::int OFFSET sqlc.arg(off)::int;

-- name: GetMaxFioTransactionID :one
SELECT COALESCE(MAX(fio_transaction_id), 0)::bigint AS max_id
FROM raw_transactions
WHERE bank_account_id = sqlc.arg(bank_account_id);

-- name: CreateSyncFioRun :one
INSERT INTO sync_fio_runs (bank_account_id, status)
VALUES (sqlc.arg(bank_account_id), 'running')
RETURNING *;

-- name: FinishSyncFioRun :exec
UPDATE sync_fio_runs
SET finished_at = now(),
    status = sqlc.arg(status),
    transactions_fetched = sqlc.arg(transactions_fetched),
    transactions_inserted = sqlc.arg(transactions_inserted),
    error_message = sqlc.narg(error_message)
WHERE id = sqlc.arg(id);

-- name: FailStaleSyncFioRuns :execrows
-- Run once at server startup, before the scheduler starts: any row still
-- 'running' predates this process (a single instance drives all syncs, so a
-- 'running' row at boot means the prior process died — crash, panic, kill —
-- between CreateSyncFioRun and FinishSyncFioRun and never got to close it).
UPDATE sync_fio_runs
SET finished_at = now(),
    status = 'failed',
    error_message = 'interrupted: server restarted mid-run'
WHERE status = 'running';

-- name: InsertRawTransaction :execrows
INSERT INTO raw_transactions (
    bank_account_id, fio_transaction_id, transaction_date, amount, currency,
    counter_account_number, counter_account_name, counter_bank_code, counter_bank_name, bic,
    variable_symbol, specific_symbol, constant_symbol,
    user_identification, message_for_recipient, transaction_type, executor, specification, comment, instruction_id,
    raw_payload
) VALUES (
    sqlc.arg(bank_account_id),
    sqlc.arg(fio_transaction_id),
    sqlc.arg(transaction_date),
    sqlc.arg(amount)::numeric,
    sqlc.arg(currency),
    sqlc.narg(counter_account_number),
    sqlc.narg(counter_account_name),
    sqlc.narg(counter_bank_code),
    sqlc.narg(counter_bank_name),
    sqlc.narg(bic),
    sqlc.narg(variable_symbol),
    sqlc.narg(specific_symbol),
    sqlc.narg(constant_symbol),
    sqlc.narg(user_identification),
    sqlc.narg(message_for_recipient),
    sqlc.narg(transaction_type),
    sqlc.narg(executor),
    sqlc.narg(specification),
    sqlc.narg(comment),
    sqlc.narg(instruction_id),
    sqlc.arg(raw_payload)
)
ON CONFLICT (bank_account_id, fio_transaction_id) DO NOTHING;

-- name: UpsertMember :exec
INSERT INTO members (member_number, fee_start_date, fee_stop_date, active, sub, workplace_executive_committee_sub)
VALUES (sqlc.arg(member_number), sqlc.narg(fee_start_date), sqlc.narg(fee_stop_date), sqlc.arg(active), sqlc.narg(sub), sqlc.narg(workplace_executive_committee_sub))
ON CONFLICT (member_number) DO UPDATE
SET fee_start_date = EXCLUDED.fee_start_date,
    fee_stop_date = EXCLUDED.fee_stop_date,
    active = EXCLUDED.active,
    sub = EXCLUDED.sub,
    workplace_executive_committee_sub = EXCLUDED.workplace_executive_committee_sub;

-- name: EnsureDefaultPaymentIdentifier :exec
-- Seeds the auto-generated "default" payment identifier for a member: variable
-- symbol == member_number, valid from their fee_start_date. Runs on every Orca
-- sync. DO NOTHING on conflict so a re-run is a no-op. Skipped by the caller when
-- fee_start_date is null (member not yet liable, and valid_from is NOT NULL).
INSERT INTO member_payment_identifiers (member_number, variable_symbol, valid_from)
VALUES (sqlc.arg(member_number), sqlc.arg(variable_symbol), sqlc.arg(valid_from))
ON CONFLICT (variable_symbol, valid_from) DO NOTHING;

-- name: SyncDefaultPaymentIdentifierValidTo :exec
-- Mirrors members.fee_stop_date onto the default payment identifier's valid_to
-- (variable_symbol == member_number): fee liability ended -> row closed with that
-- date, fee_stop_date cleared in Orca -> row reopened (valid_to = NULL). The sync
-- owns this row's window authoritatively (valid_from = fee_start_date, valid_to =
-- fee_stop_date). To point a member at a different variable symbol, add a *new*
-- member_payment_identifiers row (different variable_symbol, its own valid_from) —
-- that row has variable_symbol != member_number so this UPDATE never touches it.
-- The IS DISTINCT FROM guard makes a steady-state run write nothing.
UPDATE member_payment_identifiers
SET valid_to = sqlc.narg(valid_to)
WHERE member_number = sqlc.arg(member_number)
  AND variable_symbol = sqlc.arg(variable_symbol)
  AND valid_to IS DISTINCT FROM sqlc.narg(valid_to);

-- name: GetMemberNumberBySub :one
SELECT member_number FROM members WHERE sub = sqlc.arg(sub);

-- name: GetMemberWorkplaceSub :one
-- Backs the workplace-rep access path on GET /payments/{member_number}/history
-- (see docs/logic-design.md "Payment History Endpoint"): a caller without the
-- admin payment-history role can still see this member if the returned value
-- is non-null and matches one of the caller's own Keycloak groups.
SELECT workplace_executive_committee_sub FROM members WHERE member_number = sqlc.arg(member_number);

-- name: GetPaymentHistory :many
SELECT pc.covers_year, pc.covers_month,
       rt.transaction_date, rt.amount, rt.currency,
       pc.processed_transaction_id
FROM payment_coverage pc
JOIN processed_transactions pt ON pt.id = pc.processed_transaction_id
JOIN raw_transactions rt ON rt.id = pt.raw_transaction_id
WHERE pc.member_number = sqlc.arg(member_number)
ORDER BY pc.covers_year DESC, pc.covers_month DESC;

-- name: ListMembersMissingPayment :many
-- Members who were liable for the membership fee in the given year/month but
-- have no payment_coverage row for it. "Liable" = fee_start_date is set (the
-- member_arrears view enforces this) and the target month falls within
-- [fee_start_date, COALESCE(fee_stop_date, CURRENT_DATE)] at month granularity.
--
-- total_missed_months comes from the member_arrears view: a total-arrears figure
-- independent of the queried month (every unpaid month across the member's full
-- liability window). Always >= 1 here, since the queried month is one of them.
-- Rows are ordered by it descending ("top offenders first"), member_number
-- breaking ties. See docs/logic-design.md "Missed Payment Detection".
SELECT ma.member_number, ma.total_missed_months
FROM member_arrears ma
JOIN members m ON m.member_number = ma.member_number
WHERE date_trunc('month', m.fee_start_date::timestamp)
        <= make_date(sqlc.arg(year)::int, sqlc.arg(month)::int, 1)::timestamp
  AND make_date(sqlc.arg(year)::int, sqlc.arg(month)::int, 1)::timestamp
        <= date_trunc('month', COALESCE(m.fee_stop_date, CURRENT_DATE)::timestamp)
  AND NOT EXISTS (
      SELECT 1 FROM payment_coverage pc
      WHERE pc.member_number = ma.member_number
        AND pc.covers_year = sqlc.arg(year)::int
        AND pc.covers_month = sqlc.arg(month)::int
  )
ORDER BY ma.total_missed_months DESC, ma.member_number;

-- name: ListMembersMissingPaymentInYear :many
-- Members who missed at least one liable month during the given calendar year —
-- the whole-year counterpart of ListMembersMissingPayment. Same
-- {member_number, total_missed_months} shape and ordering; total_missed_months
-- is still the full-liability-window arrears count (member_arrears view), not
-- scoped to the year. See docs/logic-design.md "Missed Payment Detection".
SELECT ma.member_number, ma.total_missed_months
FROM member_arrears ma
JOIN members m ON m.member_number = ma.member_number
WHERE EXISTS (
    SELECT 1
    FROM generate_series(
        greatest(
            date_trunc('month', m.fee_start_date::timestamp),
            make_date(sqlc.arg(year)::int, 1, 1)::timestamp
        ),
        least(
            date_trunc('month', COALESCE(m.fee_stop_date, CURRENT_DATE)::timestamp),
            make_date(sqlc.arg(year)::int, 12, 1)::timestamp
        ),
        interval '1 month'
    ) AS ym(month)
    WHERE NOT EXISTS (
        SELECT 1 FROM payment_coverage pc
        WHERE pc.member_number = ma.member_number
          AND pc.covers_year = EXTRACT(YEAR FROM ym.month)::int
          AND pc.covers_month = EXTRACT(MONTH FROM ym.month)::int
    )
)
ORDER BY ma.total_missed_months DESC, ma.member_number;

-- name: ListMembersMissingPaymentForWorkplace :many
-- Workplace-rep counterpart to ListMembersMissingPayment — same shape and
-- logic, scoped to members in any of the caller's workplace groups instead of
-- every member. See that query's comment for the liability-window logic.
SELECT ma.member_number, ma.total_missed_months
FROM member_arrears ma
JOIN members m ON m.member_number = ma.member_number
WHERE m.workplace_executive_committee_sub = ANY(sqlc.arg(workplace_subs)::uuid[])
  AND date_trunc('month', m.fee_start_date::timestamp)
        <= make_date(sqlc.arg(year)::int, sqlc.arg(month)::int, 1)::timestamp
  AND make_date(sqlc.arg(year)::int, sqlc.arg(month)::int, 1)::timestamp
        <= date_trunc('month', COALESCE(m.fee_stop_date, CURRENT_DATE)::timestamp)
  AND NOT EXISTS (
      SELECT 1 FROM payment_coverage pc
      WHERE pc.member_number = ma.member_number
        AND pc.covers_year = sqlc.arg(year)::int
        AND pc.covers_month = sqlc.arg(month)::int
  )
ORDER BY ma.total_missed_months DESC, ma.member_number;

-- name: ListMembersMissingPaymentInYearForWorkplace :many
-- Workplace-rep counterpart to ListMembersMissingPaymentInYear — same shape
-- and logic, scoped to members in any of the caller's workplace groups.
SELECT ma.member_number, ma.total_missed_months
FROM member_arrears ma
JOIN members m ON m.member_number = ma.member_number
WHERE m.workplace_executive_committee_sub = ANY(sqlc.arg(workplace_subs)::uuid[])
  AND EXISTS (
    SELECT 1
    FROM generate_series(
        greatest(
            date_trunc('month', m.fee_start_date::timestamp),
            make_date(sqlc.arg(year)::int, 1, 1)::timestamp
        ),
        least(
            date_trunc('month', COALESCE(m.fee_stop_date, CURRENT_DATE)::timestamp),
            make_date(sqlc.arg(year)::int, 12, 1)::timestamp
        ),
        interval '1 month'
    ) AS ym(month)
    WHERE NOT EXISTS (
        SELECT 1 FROM payment_coverage pc
        WHERE pc.member_number = ma.member_number
          AND pc.covers_year = EXTRACT(YEAR FROM ym.month)::int
          AND pc.covers_month = EXTRACT(MONTH FROM ym.month)::int
    )
)
ORDER BY ma.total_missed_months DESC, ma.member_number;

-- name: CreateSyncOrcaRun :one
INSERT INTO sync_orca_runs (status)
VALUES ('running')
RETURNING *;

-- name: FinishSyncOrcaRun :exec
UPDATE sync_orca_runs
SET finished_at = now(),
    status = sqlc.arg(status),
    members_fetched = sqlc.arg(members_fetched),
    members_upserted = sqlc.arg(members_upserted),
    error_message = sqlc.narg(error_message)
WHERE id = sqlc.arg(id);

-- name: FailStaleSyncOrcaRuns :execrows
-- See FailStaleSyncFioRuns — same reasoning, run at startup.
UPDATE sync_orca_runs
SET finished_at = now(),
    status = 'failed',
    error_message = 'interrupted: server restarted mid-run'
WHERE status = 'running';

-- name: ListUnprocessedTransactions :many
SELECT rt.* FROM raw_transactions rt
LEFT JOIN processed_transactions pt ON pt.raw_transaction_id = rt.id
WHERE pt.id IS NULL
ORDER BY rt.id;

-- name: ListTransactions :many
-- Admin transaction browser: processed_transactions enriched with their
-- raw_transactions row, with optional filters. Every filter arg is nullable —
-- NULL / omitted means "don't filter on this". total_count is the full match
-- count ignoring LIMIT/OFFSET (window aggregate) so the caller can paginate. See
-- docs/logic-design.md "Transaction Browser".
SELECT
    pt.id,
    rt.transaction_date,
    rt.amount,
    rt.currency,
    pt.direction,
    pt.category,
    pt.member_number,
    pt.matched_by,
    pt.is_public_visible,
    rt.variable_symbol,
    rt.specific_symbol,
    rt.constant_symbol,
    rt.counter_account_number,
    rt.counter_account_name,
    rt.message_for_recipient,
    rt.user_identification,
    rt.comment,
    count(*) OVER () AS total_count
FROM processed_transactions pt
JOIN raw_transactions rt ON rt.id = pt.raw_transaction_id
WHERE (sqlc.narg(assigned)::boolean IS NULL
        OR (pt.member_number IS NOT NULL) = sqlc.narg(assigned)::boolean)
  AND (sqlc.narg(direction)::text IS NULL OR pt.direction = sqlc.narg(direction)::text)
  AND (sqlc.narg(category)::text IS NULL OR pt.category = sqlc.narg(category)::text)
  AND (sqlc.narg(matched_by)::text IS NULL OR pt.matched_by = sqlc.narg(matched_by)::text)
  AND (sqlc.narg(member_number)::int IS NULL OR pt.member_number = sqlc.narg(member_number)::int)
  AND (sqlc.narg(date_from)::date IS NULL OR rt.transaction_date >= sqlc.narg(date_from)::date)
  AND (sqlc.narg(date_to)::date IS NULL OR rt.transaction_date <= sqlc.narg(date_to)::date)
ORDER BY rt.transaction_date DESC, rt.id DESC
LIMIT sqlc.arg(lim)::int OFFSET sqlc.arg(off)::int;

-- name: GetTransactionCategorySummary :many
-- Budgeting view (see docs/logic-design.md "Transaction Category Summary"):
-- totals grouped by direction/category/currency only — no member_number, no
-- counterparty, no per-transaction rows, so this is safe for the
-- widely-held view-budget role (unlike ListTransactions). SUM(ABS(amount))
-- so an "outgoing" total reads as a positive spend figure rather than the
-- signed value raw_transactions stores it as.
SELECT
    pt.direction,
    pt.category,
    rt.currency,
    COALESCE(SUM(ABS(rt.amount)), 0)::numeric AS total
FROM processed_transactions pt
JOIN raw_transactions rt ON rt.id = pt.raw_transaction_id
WHERE (sqlc.narg(date_from)::date IS NULL OR rt.transaction_date >= sqlc.narg(date_from)::date)
  AND (sqlc.narg(date_to)::date IS NULL OR rt.transaction_date <= sqlc.narg(date_to)::date)
GROUP BY pt.direction, pt.category, rt.currency
ORDER BY pt.direction, total DESC;

-- name: FindMemberByVariableSymbol :one
SELECT member_number FROM member_payment_identifiers
WHERE variable_symbol = sqlc.arg(variable_symbol)
    AND valid_from <= sqlc.arg(transaction_date)
    AND (valid_to IS NULL OR valid_to >= sqlc.arg(transaction_date))
ORDER BY valid_from DESC
LIMIT 1;

-- name: CreateProcessedTransaction :one
INSERT INTO processed_transactions (raw_transaction_id, member_number, category, direction, matched_by)
VALUES (sqlc.arg(raw_transaction_id), sqlc.narg(member_number), sqlc.arg(category), sqlc.arg(direction), sqlc.narg(matched_by))
RETURNING *;

-- name: CreatePaymentCoverage :exec
INSERT INTO payment_coverage (processed_transaction_id, member_number, covers_year, covers_month)
VALUES (sqlc.arg(processed_transaction_id), sqlc.arg(member_number), sqlc.arg(covers_year), sqlc.arg(covers_month))
ON CONFLICT (member_number, covers_year, covers_month) DO NOTHING;

-- name: GetTransactionDetail :one
-- One row for the transaction browser's detail / edit view — same columns as
-- ListTransactions minus the window count. Covered months come from
-- ListCoverageForTransaction.
SELECT
    pt.id,
    rt.transaction_date,
    rt.amount,
    rt.currency,
    pt.direction,
    pt.category,
    pt.member_number,
    pt.matched_by,
    pt.is_public_visible,
    rt.variable_symbol,
    rt.specific_symbol,
    rt.constant_symbol,
    rt.counter_account_number,
    rt.counter_account_name,
    rt.message_for_recipient,
    rt.user_identification,
    rt.comment
FROM processed_transactions pt
JOIN raw_transactions rt ON rt.id = pt.raw_transaction_id
WHERE pt.id = sqlc.arg(id);

-- name: ListCoverageForTransaction :many
SELECT covers_year, covers_month
FROM payment_coverage
WHERE processed_transaction_id = sqlc.arg(processed_transaction_id)
ORDER BY covers_year, covers_month;

-- name: MemberExists :one
SELECT EXISTS (SELECT 1 FROM members WHERE member_number = sqlc.arg(member_number)) AS exists;

-- name: CategoryExists :one
SELECT EXISTS (SELECT 1 FROM transaction_categories WHERE name = sqlc.arg(name)) AS exists;

-- name: ListCategories :many
SELECT * FROM transaction_categories ORDER BY is_mandatory DESC, name;

-- name: CreateCategory :one
-- is_mandatory is never set true here — only the four seeded in
-- migrations/20260910000001_add_transaction_categories.sql are mandatory.
INSERT INTO transaction_categories (name, is_mandatory) VALUES (sqlc.arg(name), false)
RETURNING *;

-- name: DeleteCategory :execrows
-- The is_mandatory=false guard means a mandatory category and a missing one
-- both come back as 0 rows affected — the caller (handler.DeleteCategory)
-- checks GetCategory first to tell those two cases apart. A category still
-- referenced by processed_transactions.category fails this with a foreign
-- key violation instead (see processed_transactions_category_fkey).
DELETE FROM transaction_categories WHERE name = sqlc.arg(name) AND is_mandatory = false;

-- name: GetCategory :one
SELECT * FROM transaction_categories WHERE name = sqlc.arg(name);

-- name: AssignTransactionToMember :one
-- Manual categorization, with or without a member match (see
-- docs/logic-design.md "Manual Assignment & Coverage") — member_number and
-- matched_by are both nullable so a category-only edit (no member) just
-- passes both as NULL. Coverage rows are managed separately by the caller in
-- the same DB transaction.
UPDATE processed_transactions
SET member_number = sqlc.arg(member_number),
    category = sqlc.arg(category),
    matched_by = sqlc.narg(matched_by)
WHERE id = sqlc.arg(id)
RETURNING id;

-- name: UnassignTransaction :one
-- Reverts a manual (or automatic) match: clears the member and matched_by, and
-- resets category to the direction-based default the caller passes in.
UPDATE processed_transactions
SET member_number = NULL,
    matched_by = NULL,
    category = sqlc.arg(category)
WHERE id = sqlc.arg(id)
RETURNING id;

-- name: DeleteCoverageForTransaction :exec
DELETE FROM payment_coverage WHERE processed_transaction_id = sqlc.arg(processed_transaction_id);

-- name: InsertCoverageRow :execrows
-- ON CONFLICT DO NOTHING + :execrows so the caller can tell which requested
-- month was already covered by a *different* transaction (0 rows affected) and
-- report it, rather than silently dropping it.
INSERT INTO payment_coverage (processed_transaction_id, member_number, covers_year, covers_month)
VALUES (sqlc.arg(processed_transaction_id), sqlc.arg(member_number), sqlc.arg(covers_year), sqlc.arg(covers_month))
ON CONFLICT (member_number, covers_year, covers_month) DO NOTHING;
