-- +goose Up
-- +goose StatementBegin
-- Optional per-account color, admin-assigned, purely presentational — lets
-- the transaction browser show at a glance which bank account a transaction
-- belongs to (via a small color swatch), no business logic keyed on it.
-- Nullable: most accounts won't bother setting one. CHECK enforces the only
-- shape the frontend's plain text input sends (#RRGGBB) rather than trusting
-- free-form text into a color swatch's inline style.
ALTER TABLE bank_accounts ADD COLUMN color TEXT
    CHECK (color IS NULL OR color ~ '^#[0-9A-Fa-f]{6}$');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE bank_accounts DROP COLUMN color;
-- +goose StatementEnd
