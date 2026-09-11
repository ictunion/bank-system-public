-- +goose Up
-- +goose StatementBegin
-- Dues for month M are expected to be paid during month M+1 (a recurring
-- one-month lag, not just an onboarding grace) — so month M only becomes
-- "missing" once M+1 has also fully elapsed, i.e. once the current month is
-- M+2 or later. Without this cap, member_arrears (and the four
-- missing-payment queries built on the same window) treated a month as
-- overdue the instant it started, which flagged the current — and even the
-- still-within-grace previous — month as "missing" for essentially every
-- liable member. See docs/logic-design.md "Missed Payment Detection".
--
-- The cap only matters while a member is still actively liable (or recently
-- stopped): for someone whose fee_stop_date is already well in the past, the
-- grace period has long since elapsed, and LEAST(...) below resolves to
-- fee_stop_date's month same as before.
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
            date_trunc('month', COALESCE(m.fee_stop_date, CURRENT_DATE)::timestamp),
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
-- +goose StatementEnd
