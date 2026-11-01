-- +goose Up
-- +goose StatementBegin
-- Categories get a UUID id, and everything that referenced a category by name
-- now references the id: processed_transactions, categorization_rules,
-- budget_line_categories and the parent link. The name stays unique, but is
-- now only a label — so a category can be renamed (or, later, archived)
-- without touching the rows that use it.
--
-- The six mandatory categories get fixed, well-known ids, because Go code
-- refers to them directly (internal/categoryid). Every other category gets a
-- random one.
ALTER TABLE transaction_categories ADD COLUMN id UUID;
UPDATE transaction_categories SET id = CASE name
    WHEN 'membership_fee'    THEN '00000000-0000-4000-8000-000000000001'::uuid
    WHEN 'salary'            THEN '00000000-0000-4000-8000-000000000002'::uuid
    WHEN 'other_income'      THEN '00000000-0000-4000-8000-000000000003'::uuid
    WHEN 'other_expense'     THEN '00000000-0000-4000-8000-000000000004'::uuid
    WHEN 'internal_transfer' THEN '00000000-0000-4000-8000-000000000005'::uuid
    WHEN 'credited_interest' THEN '00000000-0000-4000-8000-000000000006'::uuid
    ELSE gen_random_uuid()
END;
ALTER TABLE transaction_categories
    ALTER COLUMN id SET NOT NULL,
    ALTER COLUMN id SET DEFAULT gen_random_uuid();

-- New id columns next to the old name columns, filled from them.
ALTER TABLE transaction_categories ADD COLUMN parent_id UUID;
UPDATE transaction_categories child SET parent_id = parent.id
FROM transaction_categories parent WHERE parent.name = child.parent_name;

ALTER TABLE processed_transactions ADD COLUMN category_id UUID;
UPDATE processed_transactions pt SET category_id = tc.id
FROM transaction_categories tc WHERE tc.name = pt.category;
ALTER TABLE processed_transactions ALTER COLUMN category_id SET NOT NULL;

ALTER TABLE categorization_rules ADD COLUMN category_id UUID;
UPDATE categorization_rules cr SET category_id = tc.id
FROM transaction_categories tc WHERE tc.name = cr.category;
ALTER TABLE categorization_rules ALTER COLUMN category_id SET NOT NULL;

ALTER TABLE budget_line_categories ADD COLUMN category_id UUID;
UPDATE budget_line_categories blc SET category_id = tc.id
FROM transaction_categories tc WHERE tc.name = blc.category;
ALTER TABLE budget_line_categories ALTER COLUMN category_id SET NOT NULL;

-- Drop the name columns. Their foreign keys, indexes and the constraints
-- that mention them (the rules' kind CHECK, budget_line_categories' primary
-- key and per-year UNIQUE) go with them and are recreated on the id below.
ALTER TABLE processed_transactions DROP COLUMN category;
ALTER TABLE categorization_rules DROP COLUMN category;
ALTER TABLE budget_line_categories DROP COLUMN category;
ALTER TABLE transaction_categories DROP COLUMN parent_name;

ALTER TABLE transaction_categories DROP CONSTRAINT transaction_categories_pkey;
ALTER TABLE transaction_categories ADD PRIMARY KEY (id);
ALTER TABLE transaction_categories ADD CONSTRAINT transaction_categories_name_key UNIQUE (name);

-- One level of nesting only; enforced by handler.CreateCategory. No ON
-- DELETE: a parent that still has children can't be deleted.
ALTER TABLE transaction_categories
    ADD CONSTRAINT transaction_categories_parent_id_fkey
    FOREIGN KEY (parent_id) REFERENCES transaction_categories(id);

ALTER TABLE processed_transactions
    ADD CONSTRAINT processed_transactions_category_id_fkey
    FOREIGN KEY (category_id) REFERENCES transaction_categories(id);
CREATE INDEX idx_processed_category_id ON processed_transactions (category_id);

ALTER TABLE categorization_rules
    ADD CONSTRAINT categorization_rules_category_id_fkey
    FOREIGN KEY (category_id) REFERENCES transaction_categories(id);
-- Same rule as before, on the id: a pattern rule needs at least one real
-- condition; built-in kinds take none and have a fixed category
-- (internal_transfer / membership_fee).
ALTER TABLE categorization_rules ADD CONSTRAINT categorization_rules_conditions_check CHECK (
    (kind = 'pattern' AND COALESCE(counter_account_number, variable_symbol, constant_symbol,
                                   text_contains, transaction_type_contains) IS NOT NULL)
 OR (kind = 'own_account' AND category_id = '00000000-0000-4000-8000-000000000005'
     AND COALESCE(counter_account_number, variable_symbol, constant_symbol,
                  text_contains, transaction_type_contains, direction) IS NULL)
 OR (kind = 'member_variable_symbol' AND category_id = '00000000-0000-4000-8000-000000000001'
     AND COALESCE(counter_account_number, variable_symbol, constant_symbol,
                  text_contains, transaction_type_contains, direction) IS NULL)
);

ALTER TABLE budget_line_categories
    ADD CONSTRAINT budget_line_categories_category_id_fkey
    FOREIGN KEY (category_id) REFERENCES transaction_categories(id);
ALTER TABLE budget_line_categories ADD PRIMARY KEY (budget_line_id, category_id);
-- A category feeds at most one line per year.
ALTER TABLE budget_line_categories
    ADD CONSTRAINT budget_line_categories_budget_year_category_id_key UNIQUE (budget_year, category_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Back to name references: add the name columns, fill them from the ids, drop
-- the id columns and recreate the old constraints.
ALTER TABLE transaction_categories ADD COLUMN parent_name TEXT;
UPDATE transaction_categories child SET parent_name = parent.name
FROM transaction_categories parent WHERE parent.id = child.parent_id;

ALTER TABLE processed_transactions ADD COLUMN category TEXT;
UPDATE processed_transactions pt SET category = tc.name
FROM transaction_categories tc WHERE tc.id = pt.category_id;
ALTER TABLE processed_transactions ALTER COLUMN category SET NOT NULL;

ALTER TABLE categorization_rules ADD COLUMN category TEXT;
UPDATE categorization_rules cr SET category = tc.name
FROM transaction_categories tc WHERE tc.id = cr.category_id;
ALTER TABLE categorization_rules ALTER COLUMN category SET NOT NULL;

ALTER TABLE budget_line_categories ADD COLUMN category TEXT;
UPDATE budget_line_categories blc SET category = tc.name
FROM transaction_categories tc WHERE tc.id = blc.category_id;
ALTER TABLE budget_line_categories ALTER COLUMN category SET NOT NULL;

ALTER TABLE processed_transactions DROP COLUMN category_id;
ALTER TABLE categorization_rules DROP COLUMN category_id;
ALTER TABLE budget_line_categories DROP COLUMN category_id;
ALTER TABLE transaction_categories DROP COLUMN parent_id;

ALTER TABLE transaction_categories DROP CONSTRAINT transaction_categories_name_key;
ALTER TABLE transaction_categories DROP CONSTRAINT transaction_categories_pkey;
ALTER TABLE transaction_categories ADD PRIMARY KEY (name);
ALTER TABLE transaction_categories DROP COLUMN id;

ALTER TABLE transaction_categories
    ADD CONSTRAINT transaction_categories_parent_name_fkey
    FOREIGN KEY (parent_name) REFERENCES transaction_categories(name);

ALTER TABLE processed_transactions
    ADD CONSTRAINT processed_transactions_category_fkey
    FOREIGN KEY (category) REFERENCES transaction_categories(name);
CREATE INDEX idx_processed_category ON processed_transactions (category);

ALTER TABLE categorization_rules
    ADD CONSTRAINT categorization_rules_category_fkey
    FOREIGN KEY (category) REFERENCES transaction_categories(name);
ALTER TABLE categorization_rules ADD CONSTRAINT categorization_rules_check CHECK (
    (kind = 'pattern' AND COALESCE(counter_account_number, variable_symbol, constant_symbol,
                                   text_contains, transaction_type_contains) IS NOT NULL)
 OR (kind = 'own_account' AND category = 'internal_transfer'
     AND COALESCE(counter_account_number, variable_symbol, constant_symbol,
                  text_contains, transaction_type_contains, direction) IS NULL)
 OR (kind = 'member_variable_symbol' AND category = 'membership_fee'
     AND COALESCE(counter_account_number, variable_symbol, constant_symbol,
                  text_contains, transaction_type_contains, direction) IS NULL)
);

ALTER TABLE budget_line_categories
    ADD CONSTRAINT budget_line_categories_category_fkey
    FOREIGN KEY (category) REFERENCES transaction_categories(name);
ALTER TABLE budget_line_categories ADD PRIMARY KEY (budget_line_id, category);
ALTER TABLE budget_line_categories
    ADD CONSTRAINT budget_line_categories_budget_year_category_key UNIQUE (budget_year, category);
-- +goose StatementEnd
