-- +goose Up
-- +goose StatementBegin
-- The Keycloak group ID of the member's workplace executive committee (the
-- "Workplace N reps" group) — lets bank-system scope a rep's payment-history
-- view to just their own workplace's members by matching this against the
-- group IDs on the rep's own token, with no live call back to Orca. Not
-- unique like members.sub: many members share the same committee.
ALTER TABLE members ADD COLUMN workplace_executive_committee_sub UUID;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE members DROP COLUMN workplace_executive_committee_sub;
-- +goose StatementEnd
