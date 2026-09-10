-- +goose Up
-- +goose StatementBegin
-- Adds has_ever_paid to member_arrears: whether the member has *any*
-- payment_coverage row at all, regardless of which months. Distinguishes
-- "missed one month here and there" from "never started paying at all" (e.g.
-- missed the onboarding email with bank details) — the same total_missed_months
-- figure looks identical for both today, but they need very different
-- follow-up. See docs/logic-design.md "Missed Payment Detection".
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
    ) AS total_missed_months
FROM members m
WHERE m.fee_start_date IS NOT NULL;
-- +goose StatementEnd
