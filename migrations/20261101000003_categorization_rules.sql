-- +goose Up
-- +goose StatementBegin
-- The whole categorization pipeline as one ordered list: processing.Run walks
-- the enabled rules by priority (lower first, ties by id) and the first match
-- decides the category. No match falls back to other_income/other_expense by
-- direction — that fallback stays in Go, not a row, so it can't be deleted,
-- disabled or moved above other rules.
--
-- kind:
--   pattern                — plain data: every non-NULL condition must match (AND).
--   own_account            — counter account is one of our non-deleted
--                            bank_accounts (a dynamic list, so not expressible
--                            as a stored value). Always internal_transfer.
--   member_variable_symbol — variable symbol resolves to a member via
--                            member_payment_identifiers on the transaction
--                            date; also sets member_number, matched_by and the
--                            default payment_coverage row. Always membership_fee.
-- The two built-in kinds exist at most once each (partial unique index below)
-- and take no conditions; their row is just their slot in the order.
--
-- enabled = false skips the rule during evaluation without deleting it.
CREATE TABLE categorization_rules (
    id                        BIGSERIAL PRIMARY KEY,
    priority                  INT  NOT NULL,
    kind                      TEXT NOT NULL DEFAULT 'pattern'
                              CHECK (kind IN ('pattern', 'own_account', 'member_variable_symbol')),
    category                  TEXT NOT NULL REFERENCES transaction_categories(name),
    enabled                   BOOLEAN NOT NULL DEFAULT true,

    -- 'pattern' conditions, NULL = not checked
    counter_account_number    TEXT NULL,   -- exact
    variable_symbol           TEXT NULL,   -- exact, leading zeroes ignored
    constant_symbol           TEXT NULL,   -- exact
    text_contains             TEXT NULL,   -- case-insensitive substring of comment/user_identification/message_for_recipient
    transaction_type_contains TEXT NULL,   -- case-insensitive substring of transaction_type (Fio column8)
    direction                 TEXT NULL CHECK (direction IN ('incoming', 'outgoing')),

    note                      TEXT NULL,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A pattern rule needs at least one real condition (direction alone would
    -- swallow half of all traffic); built-in kinds take no conditions and
    -- have a fixed category.
    CHECK (
        (kind = 'pattern' AND COALESCE(counter_account_number, variable_symbol, constant_symbol,
                                       text_contains, transaction_type_contains) IS NOT NULL)
     OR (kind = 'own_account' AND category = 'internal_transfer'
         AND COALESCE(counter_account_number, variable_symbol, constant_symbol,
                      text_contains, transaction_type_contains, direction) IS NULL)
     OR (kind = 'member_variable_symbol' AND category = 'membership_fee'
         AND COALESCE(counter_account_number, variable_symbol, constant_symbol,
                      text_contains, transaction_type_contains, direction) IS NULL)
    )
);

CREATE UNIQUE INDEX categorization_rules_builtin_key
    ON categorization_rules (kind) WHERE kind <> 'pattern';

-- Which rule decided the category. NULL for the fallback, for manual edits,
-- and for rows categorized before rules existed that the backfill below
-- couldn't attribute. ON DELETE SET NULL: deleting a rule doesn't
-- recategorize anything by itself.
ALTER TABLE processed_transactions
    ADD COLUMN categorization_rule_id BIGINT NULL
        REFERENCES categorization_rules(id) ON DELETE SET NULL;

-- Today's hardcoded processing steps, in today's order. Priorities in steps
-- of 100 so new rules fit between without renumbering.
INSERT INTO categorization_rules (priority, kind, category, transaction_type_contains, text_contains, note) VALUES
    (100, 'own_account',            'internal_transfer', NULL,   NULL,   'Transfer between our own bank accounts'),
    (200, 'pattern',                'credited_interest', 'úrok', NULL,   'Interest credited by the bank'),
    (300, 'member_variable_symbol', 'membership_fee',    NULL,   NULL,   'Variable symbol matches a member'),
    (500, 'pattern',                'salary',            NULL,   'mzda', 'Salary heuristic');

-- Attribute existing automatically-categorized rows to the rule that would
-- have produced them, so a rule's usage count is meaningful from day one.
UPDATE processed_transactions pt
SET categorization_rule_id = cr.id
FROM categorization_rules cr
WHERE NOT pt.category_set_manually
  AND pt.category = cr.category
  AND (cr.kind <> 'member_variable_symbol' OR pt.matched_by = 'variable_symbol');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE processed_transactions DROP COLUMN categorization_rule_id;
DROP TABLE categorization_rules;
-- +goose StatementEnd
