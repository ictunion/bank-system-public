-- +goose Up
-- +goose StatementBegin
ALTER TABLE members ADD COLUMN sub UUID UNIQUE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE members DROP COLUMN sub;
-- +goose StatementEnd
