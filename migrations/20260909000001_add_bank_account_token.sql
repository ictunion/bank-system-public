-- +goose Up
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS pgcrypto;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE bank_accounts ADD COLUMN fio_token_encrypted BYTEA;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE bank_accounts DROP COLUMN fio_token_encrypted;
-- +goose StatementEnd
-- +goose StatementBegin
DROP EXTENSION IF EXISTS pgcrypto;
-- +goose StatementEnd
