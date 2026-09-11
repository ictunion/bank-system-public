-- +goose Up
-- +goose StatementBegin
-- A member who genuinely missed a month years ago (e.g. one forgotten
-- payment in June 2022) shouldn't sit on the missing-payments list forever,
-- but there's no bank transaction to match against — nobody is going to pay
-- it retroactively, and nobody should be asked to. payment_waivers records
-- an explicit admin decision to write a specific month off, separately from
-- payment_coverage (which stays strictly "a real payment landed for this
-- month" — see docs/db-design.md `payment_coverage`). Keeping it a separate
-- table rather than a nullable processed_transaction_id on payment_coverage
-- means every payment_coverage row still has real amount/date/transaction
-- backing it (GetPaymentHistory's joins don't need to become outer joins),
-- and a month can't collide between "paid" and "waived" since they live in
-- different tables with their own UNIQUE constraints.
--
-- reason is NOT NULL: with no admin-identity column anywhere in this schema
-- (see docs/logic-design.md — no audit trail exists for manual assignment
-- either), the reason text is the only record of why a debt was written off.
-- Forcing it non-empty keeps that record meaningful.
CREATE TABLE payment_waivers (
    id             BIGSERIAL PRIMARY KEY,
    member_number  INTEGER NOT NULL REFERENCES members(member_number),
    covers_year    INTEGER NOT NULL,
    covers_month   SMALLINT NOT NULL CHECK (covers_month BETWEEN 1 AND 12),
    reason         TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (member_number, covers_year, covers_month)
);

-- member_arrears (see migrations/20260911000002_missing_payment_grace_period.sql)
-- now excludes waived months from total_missed_months, same as it already
-- excludes paid ones — a waived month is no longer "arrears" in any sense
-- the UI should surface. has_ever_paid is untouched: waiving a month isn't
-- paying it.
CREATE OR REPLACE VIEW member_arrears AS
SELECT
    m.member_number,
    (
        SELECT count(*)::int
        FROM generate_series(
            date_trunc('month', m.fee_start_date::timestamp),
            LEAST(
                date_trunc('month', COALESCE(m.fee_stop_date, CURRENT_DATE)::timestamp),
                date_trunc('month', CURRENT_DATE::timestamp) - interval '2 months'
            ),
            interval '1 month'
        ) AS expected(month)
        WHERE NOT EXISTS (
            SELECT 1 FROM payment_coverage pc
            WHERE pc.member_number = m.member_number
              AND pc.covers_year = EXTRACT(YEAR FROM expected.month)::int
              AND pc.covers_month = EXTRACT(MONTH FROM expected.month)::int
        )
        AND NOT EXISTS (
            SELECT 1 FROM payment_waivers pw
            WHERE pw.member_number = m.member_number
              AND pw.covers_year = EXTRACT(YEAR FROM expected.month)::int
              AND pw.covers_month = EXTRACT(MONTH FROM expected.month)::int
        )
    ) AS total_missed_months,
    EXISTS (
        SELECT 1 FROM payment_coverage pc WHERE pc.member_number = m.member_number
    ) AS has_ever_paid
FROM members m
WHERE m.fee_start_date IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE VIEW member_arrears AS
SELECT
    m.member_number,
    (
        SELECT count(*)::int
        FROM generate_series(
            date_trunc('month', m.fee_start_date::timestamp),
            LEAST(
                date_trunc('month', COALESCE(m.fee_stop_date, CURRENT_DATE)::timestamp),
                date_trunc('month', CURRENT_DATE::timestamp) - interval '2 months'
            ),
            interval '1 month'
        ) AS expected(month)
        WHERE NOT EXISTS (
            SELECT 1 FROM payment_coverage pc
            WHERE pc.member_number = m.member_number
              AND pc.covers_year = EXTRACT(YEAR FROM expected.month)::int
              AND pc.covers_month = EXTRACT(MONTH FROM expected.month)::int
        )
    ) AS total_missed_months,
    EXISTS (
        SELECT 1 FROM payment_coverage pc WHERE pc.member_number = m.member_number
    ) AS has_ever_paid
FROM members m
WHERE m.fee_start_date IS NOT NULL;

DROP TABLE payment_waivers;
-- +goose StatementEnd
