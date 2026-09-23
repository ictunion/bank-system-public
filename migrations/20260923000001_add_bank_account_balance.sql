-- +goose Up
-- +goose StatementBegin
-- Fio's own closing balance, captured for free from the `info` block every
-- /last/ (cursor-based sync) response already carries — no extra API call,
-- and authoritative (Fio does the math, so it can't drift the way summing
-- our own raw_transactions could if a batch was ever missed — see the
-- cursor-crash-recovery caveat in internal/syncjob). Both nullable: unknown
-- until the account's first successful sync. Only the regular cursor-based
-- sync path ever writes these — never backfill (its date range is often in
-- the past, so its own closingBalance isn't "current").
ALTER TABLE bank_accounts ADD COLUMN balance NUMERIC(15,2);
ALTER TABLE bank_accounts ADD COLUMN balance_as_of TIMESTAMPTZ;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE bank_accounts DROP COLUMN balance_as_of;
ALTER TABLE bank_accounts DROP COLUMN balance;
-- +goose StatementEnd
