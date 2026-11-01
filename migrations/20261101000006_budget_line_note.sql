-- +goose Up
-- +goose StatementBegin
-- Free-text note per budget line, shown in the budget table's Note column
-- (e.g. "includes the autumn seminar" or "cut from 50k at the members'
-- meeting"). Optional, admin-edited like the rest of the line.
ALTER TABLE budget_lines ADD COLUMN note TEXT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE budget_lines DROP COLUMN note;
-- +goose StatementEnd
