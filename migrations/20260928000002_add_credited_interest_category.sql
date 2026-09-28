-- +goose Up
-- +goose StatementBegin
-- Interest Fio credits on a savings account — auto-detected in processing.go
-- by a substring match on raw_transactions.transaction_type (Fio's own
-- column8, e.g. "Připsaný úrok"), not free text like the "mzda" salary
-- heuristic, so this is a reliable system-generated signal, not a guess.
-- Mandatory (undeletable) since processing.go hardcodes this literal name —
-- same reasoning as the other five mandatory categories.
INSERT INTO transaction_categories (name, is_mandatory) VALUES ('credited_interest', true);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM transaction_categories WHERE name = 'credited_interest';
-- +goose StatementEnd
