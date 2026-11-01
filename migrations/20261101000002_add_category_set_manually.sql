-- +goose Up
-- +goose StatementBegin
-- True once an admin has set this transaction's category by hand
-- (PUT /transactions/{id}/assignment, with or without a member), reset by
-- DELETE /transactions/{id}/assignment. Every automatic re-categorization
-- (reclassify/rematch actions) skips these rows. Needed because matched_by
-- is only 'manual' when a member was given — a category-only edit (e.g.
-- moving a lawyer payment from salary to legal) left matched_by NULL and
-- could be silently overwritten. matched_by keeps meaning only "how the
-- member was found".
ALTER TABLE processed_transactions 
    ADD COLUMN category_set_manually BOOLEAN NOT NULL DEFAULT false;

-- Backfill: every manual member match is a manual edit.
UPDATE processed_transactions SET category_set_manually = true WHERE matched_by = 'manual';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE processed_transactions DROP COLUMN category_set_manually;
-- +goose StatementEnd
