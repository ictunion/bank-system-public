-- +goose Up
-- +goose StatementBegin
-- A transaction between two of our own bank_accounts (e.g. moving funds
-- between accounts before this app supported multiple) isn't real income or
-- expense — auto-detected in processing.go by matching counter_account_number
-- against the other configured bank_accounts.fio_account_id, before the
-- variable_symbol/salary checks. Mandatory (undeletable) since both
-- processing.go and GetTransactionCategorySummary hardcode this literal name
-- — same reasoning as the other four mandatory categories.
INSERT INTO transaction_categories (name, is_mandatory) VALUES ('internal_transfer', true);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM transaction_categories WHERE name = 'internal_transfer';
-- +goose StatementEnd
