-- +goose Up
-- +goose StatementBegin
-- The Orca sync used to insert a member's default payment identifier
-- (variable_symbol = member_number) with ON CONFLICT (variable_symbol,
-- valid_from) DO NOTHING and never update valid_from afterwards, so a
-- corrected fee_start_date in Orca added a second default row instead of
-- moving the first one. Keep one default row per member — the earliest
-- valid_from, the widest window, until the next sync rewrites it from Orca's
-- current fee_start_date anyway.
DELETE FROM member_payment_identifiers mpi
USING member_payment_identifiers keeper
WHERE mpi.variable_symbol = mpi.member_number::text
  AND keeper.variable_symbol = keeper.member_number::text
  AND keeper.member_number = mpi.member_number
  AND (keeper.valid_from, keeper.id) < (mpi.valid_from, mpi.id);
-- +goose StatementEnd
-- +goose StatementBegin
-- At most one default row per member, so the sync can upsert its whole
-- window. Rows with a different variable_symbol (admin-added alternatives)
-- are outside the predicate and unaffected.
CREATE UNIQUE INDEX member_payment_identifiers_default_key
    ON member_payment_identifiers (member_number)
    WHERE variable_symbol = member_number::text;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Deleted duplicate rows are not restored — the kept row covers the widest window.
DROP INDEX member_payment_identifiers_default_key;
-- +goose StatementEnd
