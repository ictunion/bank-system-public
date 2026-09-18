-- +goose Up
-- +goose StatementBegin
-- Free-text staff note on a transaction, independent of the Fio-sourced
-- comment column (raw_transactions/processed_transactions.comment, Fio's own
-- column 25 — the bank statement's comment, not admin-authored). One comment
-- per transaction, so it lives directly on processed_transactions rather than
-- a new table, same as every other 1:1 field here. No audit column (who/when)
-- since no admin-identity column exists anywhere in this schema — same
-- reasoning as payment_waivers.reason being the only record of its kind.
ALTER TABLE processed_transactions ADD COLUMN admin_comment TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE processed_transactions DROP COLUMN admin_comment;
-- +goose StatementEnd
