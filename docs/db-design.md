# Bank Sync & Payment Processing — DB Design Notes

## Context

Private bank API project. We sync transaction data daily from Fio Bank's API
(`GET /api/cz/v2/accounts/{id}/transactions`, `TransactionV2View` model) into our own
Postgres DB, then run custom processing on top:

- Categorize individual payments (e.g. matching monthly membership fees by variable symbol)
- Detect members who missed a monthly payment
- Expose `/transactions` — filterable admin browser over processed transactions
  (assigned/unassigned, direction, category, date range, ... — see logic-design.md),
  plus `/transactions/{id}/assignment` (PUT/DELETE) for manual member match + coverage
- Expose `/payments/<member_number>/history` — per-member payment history for their member panel
- Expose `/payments/<year>/<month>/missing` and `/payments/<year>/missing` — members who didn't
  pay that specific month, or missed any month in that year (see logic-design.md)
- Internal budgeting view: incoming vs outgoing totals, with privacy redaction
  (no counterparty names on incoming payments; salaries grouped into one number, not
  itemized per employee)

All HTTP routes are served under an `/api` prefix (`GET /api/payments/...`, `GET
/api/transactions`, etc.); paths in these docs are written without it for brevity. The prefix
exists so a reverse proxy can serve the admin frontend at `/` and forward only `/api/`
to this service — see the deployment notes.

**Scale:** ~1,000 transactions/month. No need for replication. No GIS types needed.
Read-heavy; writes happen once/day via sync job.

> ⚠️ Field names below are based on Fio's well-documented, stable transaction data model
> (confirmed via multiple client libraries — Python `fiobank`, `fio-banka`, etc.), but the
> exact JSON key casing for the `TransactionV2View` (AISP v2) endpoint specifically was
> **not verified directly against the live Swagger schema** (JS-rendered page, couldn't be
> fetched). **Pull one real API response before writing Go structs and confirm field names.**

## Core Design Principle

Keep `raw_transactions` dumb and a pure 1:1 mirror of the bank API response — no business
logic, no joins at insert time. Everything else (matching, categorization, privacy
redaction) lives in downstream tables so it can be recomputed/backfilled without ever
touching the sync job. This makes the sync job simple and idempotent, and lets us rebuild
all derived data (categories, matches) without re-hitting the bank API.

## Why Postgres (not a time-series DB)

- Considered TimescaleDB / time-series storage for raw synced data — **not warranted**.
  Time-series DBs earn their keep at high volume with time-bucketed analytical queries
  (metrics/logs, millions of rows). At ~1,000 tx/month, the actual workload is relational
  (join transaction → member, filter by variable symbol, group by category), which a plain
  indexed Postgres table handles better and more simply.
- Postgres's known MVCC/write-amplification weakness (full-row-copy on update, dead tuple
  bloat — see Uber's 2016 migration story) applies to **high-frequency updates on wide,
  heavily-indexed tables**. We have ~33 inserts/day, occasional categorization updates.
  Autovacuum handles this with defaults; no manual `VACUUM FULL` needed (and it would
  lock the table anyway — avoid it for a table backing a live read endpoint).

## Sync Job (Go)

- Plain Go program calling the Fio API, run daily via cron / k8s CronJob / systemd timer
  (or `robfig/cron` if the service runs continuously).
- **Idempotent by design**: upsert on `(bank_account_id, fio_transaction_id)` —
  `INSERT ... ON CONFLICT DO NOTHING`. Safe to rerun after partial failures.
- Prefer Fio's cursor-based "since last download" endpoint over date-range queries where
  possible, to avoid refetching old data.
- Log each run in `sync_fio_runs` (start/end, counts, errors) — needed for debugging bank API
  flakiness/partial pages.
- Wrap each run in a DB transaction.

## Table Design

### 1. `bank_accounts`

Supports multiple bank accounts even though we start with one — avoids a migration later,
and the Fio sync cursor is naturally per-account anyway. Each account carries its own Fio
API token, encrypted at rest via pgcrypto (`pgp_sym_encrypt`/`pgp_sym_decrypt`, keyed by
`BANK_TOKEN_ENCRYPTION_KEY`, never stored in the DB) — added via the admin UI
(`POST /account`, `manage-bank-accounts` role) rather than `psql`.

Deletion (`DELETE /account/{id}`) is a soft delete (`deleted_at`), not a row removal:
`raw_transactions`/`sync_fio_runs` reference `bank_accounts.id` with no `ON DELETE` clause,
so a hard delete would fail once an account has synced history, and admins should still see
that a deleted account used to exist. The sync job (`internal/syncjob`) skips
`deleted_at IS NOT NULL` accounts in Go, not via a query filter — "should this account
sync" is job logic, not storage. `fio_account_id`'s uniqueness is a partial index
(`WHERE deleted_at IS NULL`) rather than a plain column constraint, so a new account can
reuse a deleted one's Fio account number.

```sql
CREATE TABLE bank_accounts (
    id                   SERIAL PRIMARY KEY,
    fio_account_id       TEXT NOT NULL,          -- Fio account number; unique among active rows only
    iban                 TEXT,
    currency             CHAR(3) NOT NULL DEFAULT 'CZK',
    display_name         TEXT NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    fio_token_encrypted  BYTEA,                  -- pgp_sym_encrypt'd Fio API token
    deleted_at           TIMESTAMPTZ             -- soft delete; NULL = active
);
CREATE UNIQUE INDEX bank_accounts_fio_account_id_active_key
    ON bank_accounts (fio_account_id) WHERE deleted_at IS NULL;
```

### 2. `raw_transactions` — 1:1 mirror of Fio API

```sql
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
    executor                 TEXT,
    specification             TEXT,
    comment                    TEXT,
    instruction_id              TEXT,

    -- safety net: full original payload, in case typed columns miss/mis-map a field
    raw_payload             JSONB NOT NULL,

    synced_at               TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (bank_account_id, fio_transaction_id)
);

CREATE INDEX idx_raw_tx_date ON raw_transactions (transaction_date);
CREATE INDEX idx_raw_tx_vs ON raw_transactions (variable_symbol);
CREATE INDEX idx_raw_tx_amount ON raw_transactions (amount);
```

`raw_payload JSONB`: keeps the full original API response alongside typed columns. If Fio
adds/renames a field, or our field mapping is wrong, we can backfill new columns from
stored JSON without re-syncing from the bank.

### 3. `members` + payment identifier mapping

No local copy of member identity (name, email) — that data is owned by **Orca**, the
in-house member API and source of truth for personal data, and fetched from there when
needed. This table holds only payment-domain data that's specific to this service, keyed
by the member's human-readable `member_number` (an `INTEGER`, not Orca's internal
`member_id UUID`). `member_number` is what shows up in the payment variable symbol, so
it's the natural join key against bank transactions.

`sub` (nullable `UUID`) is the one exception — the member's Keycloak account UUID, synced
from Orca's `sub` field. Not Orca's internal `member_id`; it's the Keycloak subject claim,
kept so a logged-in user's JWT (see `internal/keycloak`) can be joined to their
`member_number` row. Nullable and unique: null until the member has a Keycloak account,
unique because one Keycloak account maps to at most one member.

`workplace_executive_committee_sub` (nullable `UUID`, **not** unique) is the Keycloak
group ID of the member's workplace executive committee — synced from Orca the same way
as `sub`, but identifies a Keycloak *group* (many members share one), not an individual
account. Exists so a workplace rep's payment-history view can be scoped to just their
own workplace's members by matching this column against the group IDs on the rep's own
token, entirely within bank-system's own DB — no live call back to Orca per request. See
`logic-design.md` "Workplace-Scoped Payment History".

**Sync from Orca:** daily pull, mirroring the Fio sync job shape (idempotent upsert into
`members` keyed on `member_number`) rather than Orca pushing new/leaving-member events.
Push would need a webhook endpoint on this side (new auth surface) plus a fallback
reconciliation job anyway for missed events, so it doesn't remove the need for a daily
job — only adds a second path for no real latency win at this scale (`fee_start_date`
already carries the true liability date regardless of when the row synced). Revisit if
same-day reaction to Orca changes becomes an actual requirement.

Split identifiers out from `members` because a member's variable symbol can change over
time or they may pay from more than one source.

The Orca member sync owns a default row per member — `variable_symbol = member_number`,
`valid_from = fee_start_date`, `valid_to = fee_stop_date` — the "pays under their own
member number" common case. The row is inserted `ON CONFLICT DO NOTHING` (one-time
seed, skipped while `fee_start_date` is null since `valid_from` is `NOT NULL`), and its
`valid_to` is kept mirrored to `members.fee_stop_date` on every sync: set when Orca
reports fee liability ended, cleared back to null if `fee_stop_date` is unset again.
Only this row (identified by `variable_symbol = member_number`) is sync-managed. To
point a member at a different variable symbol, add a **new** row with that symbol and
its own `valid_from`; it has `variable_symbol != member_number` so the sync leaves it
alone. See `logic-design.md` "Orca Member Sync".

Fee amount is tracked (`raw_transactions.amount`, visible via `processed_transactions`)
but not validated against an expected value — each member sets their own amount per
internal rules we have no way to check, so any amount on a matched transaction is
treated as correct. No `monthly_fee_amount` column here; only presence (did they pay
this month) and the liability window (`fee_start_date` .. `fee_stop_date`) matter for
detection.

```sql
CREATE TABLE members (
    member_number      INTEGER PRIMARY KEY,   -- human-readable ID from the member API; appears in payment variable symbols
    fee_start_date     DATE,          -- when they became liable for the fee; null = not yet liable
    fee_stop_date      DATE,          -- when fee liability ended (left / made exempt); null = still liable
    active             BOOLEAN NOT NULL DEFAULT true,  -- synced from Orca, not used by any logic yet (see logic-design.md)
    sub                UUID UNIQUE,   -- Keycloak account UUID; null until member has a Keycloak account
    workplace_executive_committee_sub UUID,  -- Keycloak group UUID of the member's workplace reps group; null if unassigned
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE member_payment_identifiers (
    id             SERIAL PRIMARY KEY,
    member_number   INTEGER NOT NULL REFERENCES members(member_number),
    variable_symbol TEXT NOT NULL,
    valid_from     DATE NOT NULL DEFAULT CURRENT_DATE,
    valid_to       DATE,              -- null = still active
    UNIQUE (variable_symbol, valid_from)
);
```

### 4. `processed_transactions` — matching / categorization layer

One row per raw transaction, enriched with member match + category. Feeds the budgeting
dashboard directly, and feeds `/payments/<member_number>/history` via `payment_coverage`
(see below — coverage table drives the month list, this table supplies amount/date per
covered transaction).

```sql
CREATE TABLE processed_transactions (
    id                  BIGSERIAL PRIMARY KEY,
    raw_transaction_id  BIGINT NOT NULL UNIQUE REFERENCES raw_transactions(id),

    member_number        INTEGER REFERENCES members(member_number),   -- null = unmatched
    category            TEXT NOT NULL REFERENCES transaction_categories(name),
    direction            TEXT NOT NULL CHECK (direction IN ('incoming','outgoing')),

    matched_by           TEXT,        -- 'variable_symbol' | 'manual' | 'amount_heuristic'
    is_public_visible     BOOLEAN NOT NULL DEFAULT false,     -- controls redaction in budget views

    processed_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_processed_member ON processed_transactions (member_number);
CREATE INDEX idx_processed_category ON processed_transactions (category);
```

`category` values live in their own `transaction_categories` table (added in
`migrations/20260910000001_add_transaction_categories.sql`, `name TEXT PRIMARY KEY,
is_mandatory BOOLEAN`) instead of being a free-standing enum baked into this column — see
logic-design.md "Transaction Categories" for the mandatory-vs-custom split and the
`/categories` API.

Example query — budgeting dashboard (no per-member month semantics needed here):

```sql
SELECT rt.transaction_date, rt.amount, rt.currency, pt.category
FROM processed_transactions pt
JOIN raw_transactions rt ON rt.id = pt.raw_transaction_id
WHERE pt.member_number = $1
ORDER BY rt.transaction_date DESC;
```

Not used for `/payments/<member_number>/history` — see `payment_coverage` example query
below. This query returns one row per transaction, not per covered month, so a lump-sum
payment (e.g. 900 covering 3 months) would show as a single row instead of 3 paid months.

#### Multi-month payments (`payment_coverage`)

Some members pay in lump sums covering several months at once (e.g. 900 once every
3 months instead of 300 monthly) instead of a strict amount every month. No automatic
detection is possible — this needs explicit, per-month, manual marking, and the schema
has to support it.

```sql
CREATE TABLE payment_coverage (
    id                          BIGSERIAL PRIMARY KEY,
    processed_transaction_id   BIGINT NOT NULL REFERENCES processed_transactions(id),
    member_number               INTEGER NOT NULL REFERENCES members(member_number),
    covers_year                 INTEGER NOT NULL,
    covers_month                SMALLINT NOT NULL CHECK (covers_month BETWEEN 1 AND 12),
    UNIQUE (member_number, covers_year, covers_month)
);
```

Default case (one payment = the month *before* the one it landed in — dues are paid a
month in arrears, see logic-design.md "Missed Payment Detection"): the processing step
inserts a single `payment_coverage` row for that month alongside the
`processed_transactions` row — no manual work needed for the common case.

Lump-sum case: manually insert additional `payment_coverage` rows against the same
`processed_transaction_id` for the other months it's meant to cover (e.g. 3 rows for
Jan/Feb/Mar off one March transaction). The `UNIQUE (member_number, covers_year,
covers_month)` constraint stops a month from being double-covered by two different
transactions.

Example query — per-member payment history endpoint
(`/payments/<member_number>/history`, see `logic-design.md` for full endpoint logic):

```sql
SELECT pc.covers_year, pc.covers_month,
       rt.transaction_date, rt.amount, rt.currency,
       pc.processed_transaction_id
FROM payment_coverage pc
JOIN processed_transactions pt ON pt.id = pc.processed_transaction_id
JOIN raw_transactions rt ON rt.id = pt.raw_transaction_id
WHERE pc.member_number = $1
ORDER BY pc.covers_year DESC, pc.covers_month DESC;
```

One row per covered month, not per transaction — a lump-sum payment covering 3 months
returns 3 rows sharing the same `processed_transaction_id`/amount/date. Endpoint layer
groups by `processed_transaction_id` if the panel should show "1 payment, covers
Jan–Mar" instead of 3 separate lines.

"Missed payment this month" detection now checks `payment_coverage`, not
`processed_transactions` directly: generate expected months across the liability window
(`members.fee_start_date` .. `LEAST(COALESCE(members.fee_stop_date, CURRENT_DATE),
CURRENT_DATE - 2 months)`), `LEFT JOIN` against `payment_coverage` grouped by
member/month, flag gaps. A member with a null `fee_start_date` isn't liable yet (no
expected months); one with a `fee_stop_date` isn't expected to pay past it; the
`CURRENT_DATE - 2 months` cap accounts for dues being paid a month in arrears (month M
isn't overdue until M+1 has also fully elapsed) — see logic-design.md "Missed Payment
Detection" for why. Fine to compute this on read at current volume rather than storing
it — the per-member arrears count is a plain (non-materialized) view, `member_arrears`,
that both `/payments/<year>/<month>/missing` and `/payments/<year>/missing` build on
(see logic-design.md). This is unrelated to the "`processed_transactions` is a real
table" decision below — that's about not recomputing manual/heuristic categorization,
whereas an arrears diff has no manual state. A month with a `payment_waivers` row (see
below) is excluded from this diff the same way a covered month is.

#### Waived months (`payment_waivers`)

Some missing months never get a matching transaction and never will — a member who
forgot one payment years ago isn't going to be chased for it indefinitely, but also
shouldn't sit on the missing-payments list forever. `payment_waivers` records an
explicit admin decision to write off one specific month, independently of
`payment_coverage`:

```sql
CREATE TABLE payment_waivers (
    id             BIGSERIAL PRIMARY KEY,
    member_number  INTEGER NOT NULL REFERENCES members(member_number),
    covers_year    INTEGER NOT NULL,
    covers_month   SMALLINT NOT NULL CHECK (covers_month BETWEEN 1 AND 12),
    reason         TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (member_number, covers_year, covers_month)
);
```

Kept as a separate table rather than making `payment_coverage.processed_transaction_id`
nullable: `payment_coverage` stays strictly "a real payment landed for this month" (every
row still has a real amount/date/transaction behind it, so `GetPaymentHistory`'s joins
stay inner joins), and a month can't collide between "paid" and "waived" since each has
its own `UNIQUE (member_number, covers_year, covers_month)` constraint in its own table —
no shared uniqueness scope to reconcile. `reason` is `NOT NULL`: there's no admin-identity
column anywhere in this schema (see logic-design.md "Manual Assignment & Coverage" — no
audit trail exists for manual re-assignment either), so the reason text is the only record
of why a debt was written off.

A waived month is excluded from `member_arrears.total_missed_months` and from all four
`ListMembersMissingPayment*` queries, same treatment as a paid one — but `has_ever_paid`
stays keyed to `payment_coverage` alone, since waiving a month isn't paying it. See
logic-design.md "Payment Waivers" for the `/payments/{member_number}/waive` endpoint.

Budgeting view with privacy redaction: query `processed_transactions` grouped by
`category`, controlling via `is_public_visible` whether individual counterparties are ever
selected — e.g. `WHERE category = 'salary'` → `SUM(amount)` only, never names.

### 5. `sync_fio_runs` — operational tracking

```sql
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
```

### 6. `sync_orca_runs` — operational tracking for the Orca member sync

Same shape as `sync_fio_runs`, no `bank_account_id` (Orca sync isn't per bank account).

```sql
CREATE TABLE sync_orca_runs (
    id                    BIGSERIAL PRIMARY KEY,
    started_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at           TIMESTAMPTZ,
    status                TEXT NOT NULL DEFAULT 'running',  -- running|success|failed
    members_fetched       INTEGER,
    members_upserted      INTEGER,
    error_message         TEXT
);
```

## Design Decision: `processed_transactions` is a real table

Decided (not a view): categorization involves manual/heuristic matching (VS → member,
flagging salaries) that shouldn't be recomputed on every read, and manual override of a
mismatch is needed. A materialized view was considered and rejected for this reason.

## Maintenance Notes

- Autovacuum on defaults is sufficient at this volume — do **not** run `VACUUM FULL`
  after sync (exclusive lock, blocks reads on a table backing a live endpoint). A plain
  `VACUUM ANALYZE` after sync is optional/harmless if desired for fresh planner stats.
- Bank symbols (VS/SS/KS) are the primary matching key for tying payments to members —
  matching logic should live in the sync-adjacent processing step, not in `raw_transactions`.
