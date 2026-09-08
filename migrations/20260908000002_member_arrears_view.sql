-- +goose Up
-- +goose StatementBegin
-- Per-member arrears: how many months across a member's full fee-liability
-- window [fee_start_date .. COALESCE(fee_stop_date, CURRENT_DATE)] have no
-- payment_coverage row. Shared by the /payments/{year}/{month}/missing and
-- /payments/{year}/missing endpoints so the "missed months" definition lives in
-- one place. Members with no fee_start_date aren't liable and are excluded.
CREATE VIEW member_arrears AS
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

-- +goose Down
-- +goose StatementBegin
DROP VIEW member_arrears;
-- +goose StatementEnd
