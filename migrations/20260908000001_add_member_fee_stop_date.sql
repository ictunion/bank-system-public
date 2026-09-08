-- +goose Up
-- +goose StatementBegin
ALTER TABLE members ADD COLUMN fee_stop_date DATE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE members DROP COLUMN fee_stop_date;
-- +goose StatementEnd
