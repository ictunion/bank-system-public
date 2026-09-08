-- +goose Up
-- +goose StatementBegin
CREATE TABLE bank_accounts (
    id              SERIAL PRIMARY KEY,
    fio_account_id  TEXT NOT NULL UNIQUE,   -- Fio account number
    iban            TEXT,
    currency        CHAR(3) NOT NULL DEFAULT 'CZK',
    display_name    TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE raw_transactions (
    id                    BIGSERIAL PRIMARY KEY,
    bank_account_id       INTEGER NOT NULL REFERENCES bank_accounts(id),

    -- Fio's own transaction ID ("ID pohybu") — idempotency key
    fio_transaction_id    BIGINT NOT NULL,

    transaction_date      DATE NOT NULL,
    amount                NUMERIC(15,2) NOT NULL,
    currency              CHAR(3) NOT NULL,

    -- counterparty info
    counter_account_number TEXT,
    counter_account_name   TEXT,
    counter_bank_code      TEXT,
    counter_bank_name      TEXT,
    bic                    TEXT,

    -- CZ banking payment symbols — primary matching key for user identification
    variable_symbol        TEXT,
    specific_symbol        TEXT,
    constant_symbol        TEXT,

    user_identification     TEXT,   -- free-text sender identification
    message_for_recipient   TEXT,   -- payer's message
    transaction_type        TEXT,   -- e.g. "Bezhotovostní příjem", "Platba kartou"
    executor                TEXT,
    specification           TEXT,
    comment                 TEXT,
    instruction_id          TEXT,

    -- safety net: full original payload, in case typed columns miss/mis-map a field
    raw_payload             JSONB NOT NULL,

    synced_at               TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (bank_account_id, fio_transaction_id)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_raw_tx_date ON raw_transactions (transaction_date);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX idx_raw_tx_vs ON raw_transactions (variable_symbol);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX idx_raw_tx_amount ON raw_transactions (amount);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE members (
    member_number      INTEGER PRIMARY KEY,   -- human-readable ID from the member API; appears in payment variable symbols
    fee_start_date     DATE,          -- when they became liable for the fee
    active             BOOLEAN NOT NULL DEFAULT true,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE member_payment_identifiers (
    id             SERIAL PRIMARY KEY,
    member_number   INTEGER NOT NULL REFERENCES members(member_number),
    variable_symbol TEXT NOT NULL,
    valid_from     DATE NOT NULL DEFAULT CURRENT_DATE,
    valid_to       DATE,              -- null = still active
    UNIQUE (variable_symbol, valid_from)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_member_payment_identifiers_member ON member_payment_identifiers (member_number);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE processed_transactions (
    id                  BIGSERIAL PRIMARY KEY,
    raw_transaction_id  BIGINT NOT NULL UNIQUE REFERENCES raw_transactions(id),

    member_number        INTEGER REFERENCES members(member_number),   -- null = unmatched
    category            TEXT NOT NULL,                       -- 'membership_fee','salary','other_income','other_expense', etc.
    direction            TEXT NOT NULL CHECK (direction IN ('incoming','outgoing')),

    matched_by           TEXT,        -- 'variable_symbol' | 'manual' | 'amount_heuristic'
    is_public_visible     BOOLEAN NOT NULL DEFAULT false,     -- controls redaction in budget views

    processed_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_processed_member ON processed_transactions (member_number);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX idx_processed_category ON processed_transactions (category);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE payment_coverage (
    id                          BIGSERIAL PRIMARY KEY,
    processed_transaction_id   BIGINT NOT NULL REFERENCES processed_transactions(id),
    member_number               INTEGER NOT NULL REFERENCES members(member_number),
    covers_year                 INTEGER NOT NULL,
    covers_month                SMALLINT NOT NULL CHECK (covers_month BETWEEN 1 AND 12),
    UNIQUE (member_number, covers_year, covers_month)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_payment_coverage_processed_tx ON payment_coverage (processed_transaction_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE sync_fio_runs (
    id                    BIGSERIAL PRIMARY KEY,
    bank_account_id       INTEGER NOT NULL REFERENCES bank_accounts(id),
    started_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at           TIMESTAMPTZ,
    status                TEXT NOT NULL DEFAULT 'running',  -- running|success|failed
    transactions_fetched  INTEGER,
    transactions_inserted INTEGER,
    error_message         TEXT
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE sync_orca_runs (
    id                    BIGSERIAL PRIMARY KEY,
    started_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at           TIMESTAMPTZ,
    status                TEXT NOT NULL DEFAULT 'running',  -- running|success|failed
    members_fetched       INTEGER,
    members_upserted      INTEGER,
    error_message         TEXT
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE sync_orca_runs;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE sync_fio_runs;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE payment_coverage;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE processed_transactions;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE member_payment_identifiers;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE members;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE raw_transactions;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE bank_accounts;
-- +goose StatementEnd
