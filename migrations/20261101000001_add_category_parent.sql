-- +goose Up
-- +goose StatementBegin
-- Optional parent link for sub-categories (e.g. legal under salary), so a
-- payment can be counted separately from its parent while the parent still
-- reads sensibly as a group in the admin UI. Single-valued on purpose — a
-- transaction still has exactly one category.
--
-- One level only: a child's parent must itself have parent_name IS NULL.
-- Enforced by handler.CreateCategory, since a CHECK can't look at another
-- row. No ON DELETE: deleting a parent that still has children fails with a
-- foreign key violation, the handler reports it as 409.
ALTER TABLE transaction_categories
    ADD COLUMN parent_name TEXT NULL REFERENCES transaction_categories(name);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE transaction_categories DROP COLUMN parent_name;
-- +goose StatementEnd
