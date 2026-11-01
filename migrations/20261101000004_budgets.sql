-- +goose Up
-- +goose StatementBegin
-- One voted budget per year, always CZK. Board + Treasurer prepare it, members
-- vote on it, an admin enters the voted version here for transparency. Lines
-- stay freely editable (the tool is also used during preparation); "don't
-- edit after the vote" is a process rule, not enforced here — no revision
-- history.
CREATE TABLE budgets (
    year       INT PRIMARY KEY CHECK (year BETWEEN 2000 AND 2100),
    note       TEXT NULL,               -- e.g. "approved at members' meeting 2026-03-14"
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row of the voted sheet ("Legal", projected 60 000). projected_amount is
-- always positive; kind gives the sign. group_name is plain text (e.g.
-- "Events expenses") rather than a table of its own — groups carry no data
-- besides a name; their order is their lines' lowest sort_order, subtotals are
-- computed by the reader.
CREATE TABLE budget_lines (
    id               BIGSERIAL PRIMARY KEY,
    budget_year      INT NOT NULL REFERENCES budgets(year) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('income', 'expense')),
    group_name       TEXT NULL,
    name             TEXT NOT NULL,
    projected_amount NUMERIC(14,2) NOT NULL DEFAULT 0 CHECK (projected_amount >= 0),
    sort_order       INT NOT NULL DEFAULT 0,
    UNIQUE (budget_year, name),
    UNIQUE (id, budget_year)             -- target for the composite FK below
);

-- Which transaction categories feed a line's actual figure. budget_year is
-- denormalized only so the UNIQUE below can be enforced: a category feeds at
-- most one line per year — the no-double-counting guarantee, so every
-- transaction lands in exactly one line (or the report's "unbudgeted" rows).
-- A parent category does not pull in its sub-categories; each is mapped on
-- its own.
CREATE TABLE budget_line_categories (
    budget_line_id BIGINT NOT NULL,
    budget_year    INT NOT NULL,
    category       TEXT NOT NULL REFERENCES transaction_categories(name),
    PRIMARY KEY (budget_line_id, category),
    FOREIGN KEY (budget_line_id, budget_year)
        REFERENCES budget_lines(id, budget_year) ON DELETE CASCADE,
    UNIQUE (budget_year, category)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE budget_line_categories;
DROP TABLE budget_lines;
DROP TABLE budgets;
-- +goose StatementEnd
