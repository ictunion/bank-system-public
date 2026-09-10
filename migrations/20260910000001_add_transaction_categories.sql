-- +goose Up
-- +goose StatementBegin
CREATE TABLE transaction_categories (
    name         TEXT PRIMARY KEY,
    is_mandatory BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
-- These four names are hardcoded elsewhere in the Go code (default/fallback
-- categories in internal/processing/processing.go and
-- internal/handler/transactions.go) — seeded here so they exist from the
-- very first migration run, before any admin has touched the categories
-- API. is_mandatory=true blocks deletion (see DeleteCategory in
-- internal/db/queries.sql) — renaming or removing any of these four requires
-- a matching code change, not just a DB edit.
INSERT INTO transaction_categories (name, is_mandatory) VALUES
    ('membership_fee', true),
    ('salary', true),
    ('other_income', true),
    ('other_expense', true);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE processed_transactions
    ADD CONSTRAINT processed_transactions_category_fkey
    FOREIGN KEY (category) REFERENCES transaction_categories(name);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE processed_transactions DROP CONSTRAINT processed_transactions_category_fkey;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE transaction_categories;
-- +goose StatementEnd
