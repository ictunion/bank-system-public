-- +goose Up
-- +goose StatementBegin
-- A member's dues for one month can arrive split across several transactions
-- (e.g. 100 on the 8th, 240 on the 15th), so several transactions may now
-- cover the same member/month. The uniqueness moves to the transaction: one
-- transaction still can't list the same month twice.
ALTER TABLE payment_coverage
    DROP CONSTRAINT payment_coverage_member_number_covers_year_covers_month_key;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE payment_coverage
    ADD CONSTRAINT payment_coverage_transaction_month_key
    UNIQUE (processed_transaction_id, covers_year, covers_month);
-- +goose StatementEnd
-- +goose StatementBegin
-- The dropped constraint's index also served every member/month EXISTS lookup
-- (member_arrears, ListMembersMissingPayment*, PaymentCoverageExists).
CREATE INDEX idx_payment_coverage_member_month
    ON payment_coverage (member_number, covers_year, covers_month);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX idx_payment_coverage_member_month;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE payment_coverage DROP CONSTRAINT payment_coverage_transaction_month_key;
-- +goose StatementEnd
-- +goose StatementBegin
-- Fails if any month is already covered by more than one transaction — those
-- rows have to be resolved by hand first; there's no safe automatic choice of
-- which payment to drop.
ALTER TABLE payment_coverage
    ADD CONSTRAINT payment_coverage_member_number_covers_year_covers_month_key
    UNIQUE (member_number, covers_year, covers_month);
-- +goose StatementEnd
