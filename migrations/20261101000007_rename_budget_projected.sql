-- +goose Up
-- +goose StatementBegin
-- "Budgeted" is the standard term (budgeted vs. actual), and the UI and API
-- use it; the column follows so the name is the same everywhere. A plain
-- rename — the CHECK (>= 0) and default carry over.
ALTER TABLE budget_lines RENAME COLUMN projected_amount TO budgeted_amount;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE budget_lines RENAME COLUMN budgeted_amount TO projected_amount;
-- +goose StatementEnd
