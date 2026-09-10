-- +goose Up
-- +goose StatementBegin
ALTER TABLE bank_accounts ADD COLUMN deleted_at TIMESTAMPTZ;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE bank_accounts DROP CONSTRAINT bank_accounts_fio_account_id_key;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE UNIQUE INDEX bank_accounts_fio_account_id_active_key ON bank_accounts (fio_account_id) WHERE deleted_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX bank_accounts_fio_account_id_active_key;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE bank_accounts ADD CONSTRAINT bank_accounts_fio_account_id_key UNIQUE (fio_account_id);
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE bank_accounts DROP COLUMN deleted_at;
-- +goose StatementEnd
