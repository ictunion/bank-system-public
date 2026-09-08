-- name: ListBankAccounts :many
SELECT * FROM bank_accounts ORDER BY id;

-- name: CreateBankAccount :one
INSERT INTO bank_accounts (fio_account_id, iban, currency, display_name)
VALUES (sqlc.arg(fio_account_id), sqlc.narg(iban), sqlc.arg(currency), sqlc.arg(display_name))
RETURNING *;

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

-- name: ListMembers :many
SELECT * FROM members ORDER BY member_number;

-- name: UpsertMember :exec
INSERT INTO members (member_number, fee_start_date, fee_stop_date, active, sub)
VALUES (sqlc.arg(member_number), sqlc.narg(fee_start_date), sqlc.narg(fee_stop_date), sqlc.arg(active), sqlc.narg(sub))
ON CONFLICT (member_number) DO UPDATE
SET fee_start_date = EXCLUDED.fee_start_date,
    fee_stop_date = EXCLUDED.fee_stop_date,
    active = EXCLUDED.active,
    sub = EXCLUDED.sub;

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
