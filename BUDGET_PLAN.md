# Plan: Budget vs. actual, budget editor, sub-categories

Status: proposal, nothing implemented yet. Three features, meant to ship in this order
since each one builds on the previous:

1. **Sub-categories + categorization rules** (so "Legal" can exist separately from "Employee wages")
2. **Budget data model + editor** (admin defines yearly budget lines and maps categories to them)
3. **Budget vs. actual view** (Projected / Real / Difference table on the Budget page)

---

## 1. Tags vs. sub-categories — recommendation: sub-categories (single-valued)

### The core constraint

A budget line's "Real" figure is a sum over transactions. For the table to add up
(`Incomes sum − Expenses sum = Difference`, and matching the bank balance change), **every
transaction must land in exactly one budget line, or in an explicit "Unbudgeted" row.**

### Why not tags

Tags are many-to-many. A lawyer payment would carry category `salary` *and* tag `lawyer`.
If "Employee wages" maps to `salary` and "Legal" maps to tag `lawyer`, the payment matches
both lines and is counted twice. Fixing that needs precedence rules ("tag lines beat
category lines", "what if two tags match two lines?") — complexity in every query and hard
to explain to the treasurer. Tags only make sense for a cross-cutting dimension (e.g.
"Project X" spanning several categories), which nothing currently asks for. Skip for now;
can be added later without conflicting with this plan.

### Why sub-categories

`processed_transactions.category` is already single-valued and already editable per
transaction (`PUT /transactions/{id}/assignment`, category-only edit). Admins can already
create new categories (`POST /categories`). So in the simplest form **"Legal" is just
another category** — no schema change at all is needed to make the budget work.

The parent link adds only grouping/rollup, so the existing pie chart and transaction
browser still read sensibly:

```sql
ALTER TABLE transaction_categories
    ADD COLUMN parent_name TEXT NULL REFERENCES transaction_categories(name);
-- one level only: a child's parent must itself have parent_name IS NULL
-- (enforced in the handler; a CHECK can't look at another row)
```

- `legal` with parent `salary`, `accountant` with parent `other_expense`, etc.
- `GET /categories` returns `parent_name`; Categories page shows children indented under
  the parent; create form gets an optional "Parent" select.
- Transaction browser `category=salary` filter: add `include_children=true` (or make it
  the default) so filtering by a parent shows its children too.
- `GET /transactions/summary` (pie chart): optional `rollup=true` groups children into
  the parent. Not required for the budget view itself.
- Deleting a parent that still has children: `409`, same as "still referenced".

The parent column is **nice-to-have** — if it feels like overhead, ship flat categories
first and add `parent_name` later; the budget design below doesn't depend on it.

### The real pain point: re-tagging lawyer payments every month by hand

Today processing auto-tags salaries via the `"mzda"` heuristic, so a lawyer's payment lands
in `salary` and an admin must re-categorize it manually every month. Fix with
**categorization rules** — and move *every* existing processing step into the same table,
so the whole pipeline is one ordered list an admin can read (and mostly edit) in the UI
instead of being split between hardcoded Go steps and a rules table.

```sql
CREATE TABLE categorization_rules (
    id                        BIGSERIAL PRIMARY KEY,
    priority                  INT  NOT NULL,          -- lower runs first; ties broken by id
    kind                      TEXT NOT NULL DEFAULT 'pattern'
                              CHECK (kind IN ('pattern', 'own_account', 'member_variable_symbol')),
    category                  TEXT NOT NULL REFERENCES transaction_categories(name),
    enabled                   BOOLEAN NOT NULL DEFAULT true,   -- false = skipped by evaluation, row kept

    -- 'pattern' conditions: every non-NULL one must match (AND)
    counter_account_number    TEXT NULL,   -- exact
    variable_symbol           TEXT NULL,   -- exact
    constant_symbol           TEXT NULL,   -- exact
    text_contains             TEXT NULL,   -- case-insensitive substring of comment/user_identification/message_for_recipient
    transaction_type_contains TEXT NULL,   -- case-insensitive substring of transaction_type (Fio column8)
    direction                 TEXT NULL CHECK (direction IN ('incoming', 'outgoing')),

    note                      TEXT NULL,   -- "Advokátní kancelář XY"
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- a pattern rule needs at least one real condition (direction alone would swallow
    -- half of all traffic); built-in kinds take no conditions and have a fixed category
    CHECK (
        (kind = 'pattern' AND COALESCE(counter_account_number, variable_symbol, constant_symbol,
                                       text_contains, transaction_type_contains) IS NOT NULL)
     OR (kind = 'own_account'            AND category = 'internal_transfer'
         AND COALESCE(counter_account_number, variable_symbol, constant_symbol,
                      text_contains, transaction_type_contains, direction) IS NULL)
     OR (kind = 'member_variable_symbol' AND category = 'membership_fee'
         AND COALESCE(counter_account_number, variable_symbol, constant_symbol,
                      text_contains, transaction_type_contains, direction) IS NULL)
    )
);
CREATE UNIQUE INDEX categorization_rules_builtin_key
    ON categorization_rules (kind) WHERE kind <> 'pattern';   -- at most one of each built-in

ALTER TABLE processed_transactions
    ADD COLUMN categorization_rule_id BIGINT NULL
        REFERENCES categorization_rules(id) ON DELETE SET NULL;
```

**`kind`** — two of today's steps can't be expressed as static conditions, so they stay
as Go code but get a row (a *slot* in the order) instead of a hardcoded position:

- `own_account` — counter account is one of our non-deleted `bank_accounts`
  (`ListActiveBankAccountNumbers`, still fetched once per run). Dynamic list, not a value.
- `member_variable_symbol` — VS resolves to a member via `member_payment_identifiers` on
  the transaction date. Also has side effects no other rule has: sets `member_number`,
  `matched_by = 'variable_symbol'`, and writes the default `payment_coverage` row.
- `pattern` — everything else, all plain data.

Built-in rows: can't be deleted or have their category changed (`400`), but priority and
`enabled` are editable. Pattern rows: full CRUD.

**`enabled`** — turns a rule off without deleting it (keeps its conditions, note and
`categorization_rule_id` history, e.g. to test whether a rule is misfiring, or to park a
seasonal one). A disabled rule is simply skipped during evaluation, as if it weren't in
the list. Toggling doesn't recategorize anything by itself — already-processed rows keep
their category and `categorization_rule_id` until `reapply_rules` (below) runs, same as
editing or deleting a rule. Applies to built-ins too: disabling `own_account` or
`member_variable_symbol` is allowed (it's the only way to take them out of the chain,
since they can't be deleted), but the UI asks for confirmation on `member_variable_symbol`
since with it off no dues get matched and every member starts showing missed payments.
Toggled via the same `PATCH` as other rule edits.

**`categorization_rule_id`** on `processed_transactions` records which rule decided the
category — shown in the transaction detail ("categorized by rule #12: Advokátní kancelář
XY"), and lets a rule's delete dialog say "used by 37 transactions". NULL for fallback and
for manual edits. `ON DELETE SET NULL`: deleting a rule doesn't recategorize anything by
itself, that's what `reapply_rules` (below) is for.

#### Seeded order (migration)

Priorities in steps of 100 so new rules fit between without renumbering:

| priority | kind | category | conditions | replaces |
|---:|---|---|---|---|
| 100 | `own_account` | `internal_transfer` | — | step 1, internal transfer |
| 200 | `pattern` | `credited_interest` | `transaction_type_contains = 'úrok'` | step 2, `containsUrok` |
| 300 | `member_variable_symbol` | `membership_fee` | — | step 3, member match |
| 400 | `pattern` | `legal` | `counter_account_number = '<lawyer>'`, `direction = 'outgoing'` | new — **created by admin in UI**, not the migration (account number not known yet; see open questions) |
| 410 | `pattern` | `social_media` | `counter_account_number = '<social media person>'`, `direction = 'outgoing'` | new — same as lawyer, **created by admin in UI** |
| 500 | `pattern` | `salary` | `text_contains = 'mzda'` | step 4, salary heuristic |
| — | *(Go, not a row)* | `other_income` / `other_expense` | direction | step 5, fallback |

Lawyer at 400 and social media at 410 sit *after* member match and *before* `mzda` (so
they win over the generic salary heuristic). Matching by counter account number alone is
enough; if either account changes, the admin edits the rule.

**Pattern rules may sit before member match** (priority < 300) — intended for targeting a
specific `variable_symbol`/`constant_symbol` that collides with a member number, e.g. a
supplier invoice paid with VS `42` while member 42 exists. Such a rule beats the member
lookup and the transaction never becomes `membership_fee`. No restriction in DB or
handler; the Rules table just shows the member-match row prominently so it's clear which
rules run before it. Text-only rules placed there are allowed too, but they can steal
real dues — the add/edit form shows a hint when priority < the member-match row's.

`text_contains` searches `comment`, `user_identification` **and** `message_for_recipient`
for every rule, including the seeded `mzda` one — deliberate widening vs. today's salary
heuristic (which ignores `message_for_recipient`), so a wage payment noted only in the
recipient message gets tagged too.

#### Fallback: keep in Go, not a priority-9999 rule

Recommendation: **keep it hardcoded**, show it in the UI as a read-only last row
("everything else → other income / other expense by direction").

- It's two categories chosen by direction, so as rules it'd be two rows, both with no
  real condition — exactly what the `CHECK` above forbids for pattern rules. Loosening the
  check just for these opens the door to an accidental catch-all at priority 50.
- It must always exist: every transaction needs *some* category. As rows, deleting one or
  moving it above other rules silently breaks processing; as code it can't.
- `UnassignTransaction` already resets to the same direction default — that stays one Go
  function used by both, rather than processing reading a rule and unassign hardcoding.

#### Evaluation

`processing.Run` loads all enabled rules once (`ListEnabledCategorizationRules WHERE enabled
ORDER BY priority, id`; the admin UI uses an unfiltered list query),
alongside the own-account list, then per transaction walks them in order and takes the
first match; no match → fallback. Pure Go over already-loaded rows, no per-transaction
query except the existing member lookup when the `member_variable_symbol` slot is reached.
`internal/processing` loses its hardcoded step order and the `containsMzda`/`containsUrok`
helpers; `salary`/`credited_interest` stop being detection literals (still mandatory
categories — the seeded rules reference them).

#### Re-applying to already-processed rows

Rules only run on unprocessed rows, so a new/edited rule doesn't touch history. Replace
the three existing re-check actions (`reclassify_internal_transfers`, `rematch_unmatched`,
`reclassify_interest`) with one `POST /processing/run` action **`reapply_rules`**
(`manage-transactions`): re-evaluate the full rule chain for every row with
`NOT category_set_manually` (see prerequisite below) and `matched_by IS DISTINCT FROM
'manual'`; where the outcome differs, update `category`/`member_number`/`matched_by`/
`categorization_rule_id` and rewrite its auto `payment_coverage` row (safe — non-manual
rows only ever have the one default coverage row). Same idempotence as today: a second
call changes nothing. Old action names can stay as aliases for one release, then go;
the three Bank Accounts buttons collapse into one "Re-apply rules".

Admin UI: a "Rules" section on the Categories page — ordered table (enabled toggle, priority,
kind, conditions, category, note, # transactions; disabled rows greyed out but kept in
their position), add / edit / delete for pattern rows, priority + enabled edit for built-ins, fallback shown as the fixed last row. Bonus shortcut in
`AssignDialog`: when changing a transaction's category, checkbox "Always categorize
payments to/from this account as X" that creates a pattern rule at priority 400.

### Prerequisite bug to fix first: category-only manual edits aren't protected

`AssignTransaction` sets `matched_by = 'manual'` only when a member is given; a
category-only edit leaves `matched_by = NULL`. All reclassify actions skip only
`matched_by = 'manual'`, so **an admin moving a payment to `legal` by hand can be silently
overwritten** by `reclassify_interest`/`reclassify_internal_transfers` today, and by
`apply_rules` later. With budgets relying on manual categories, this becomes real.

Fix: new column `processed_transactions.category_set_manually BOOLEAN NOT NULL DEFAULT false`,
set true by `AssignTransaction` (any call), reset by `UnassignTransaction`. Every reclassify
candidate query adds `AND NOT pt.category_set_manually`. Keeps `matched_by` meaning only
"how was the member found". (Alternative: set `matched_by = 'manual'` on category-only
edits too — fewer changes, but muddies `matched_by`'s meaning and the "assigned" filter.)

---

## 2. Budget data model

Budget is voted once per year, always CZK, ~20 lines, optional groups with
subtotals — mirrors the Excel sheet.

Lifecycle: Board + Treasurer prepare it, members vote on it, then an admin enters the voted
version into this tool for transparency. The tool may also be used during preparation, so
lines, categories and amounts are freely editable (e.g. 50k proposed for public events,
members cut it to 40k at the vote — admin just edits the line). No revision history, no
"approved" lock: once voted, the budget isn't amended during the year, and that's a
process rule, not something the code enforces.

```sql
CREATE TABLE budgets (
    year       INT PRIMARY KEY CHECK (year BETWEEN 2000 AND 2100),
    note       TEXT NULL,               -- e.g. "approved at members' meeting 2026-03-14"
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE budget_lines (
    id               BIGSERIAL PRIMARY KEY,
    budget_year      INT NOT NULL REFERENCES budgets(year) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('income', 'expense')),
    group_name       TEXT NULL,          -- "Events expenses", "Employment expenses", ...
    name             TEXT NOT NULL,      -- "Legal"
    projected_amount NUMERIC(14,2) NOT NULL DEFAULT 0 CHECK (projected_amount >= 0),
    sort_order       INT NOT NULL DEFAULT 0,
    UNIQUE (budget_year, name),
    UNIQUE (id, budget_year)             -- target for the composite FK below
);

CREATE TABLE budget_line_categories (
    budget_line_id BIGINT NOT NULL,
    budget_year    INT NOT NULL,         -- denormalized only to enforce the UNIQUE below
    category       TEXT NOT NULL REFERENCES transaction_categories(name),
    PRIMARY KEY (budget_line_id, category),
    FOREIGN KEY (budget_line_id, budget_year)
        REFERENCES budget_lines(id, budget_year) ON DELETE CASCADE,
    UNIQUE (budget_year, category)       -- a category feeds at most one line per year
);
```

Design notes:

- **`UNIQUE (budget_year, category)` is the no-double-counting guarantee** from section 1,
  enforced by the DB, not just the UI. Confirmed requirement: no category feeds more than
  one line, so "Employee wages" never includes lawyer or social-media pay — each has its
  own category and its own line.
- No `currency` column: budgets are always CZK. Actuals only sum CZK transactions; any
  other currency is skipped and counted (see queries) as a guard.
- `group_name` as plain text instead of a `budget_groups` table: groups have no data of
  their own except a name and order; group order = min `sort_order` of its lines. Subtotals
  computed in the frontend. Upgrade to a table only if groups need their own fields.
- `projected_amount` always positive; `kind` gives the sign. Matches how the Excel reads.
- Budget per year, not a reusable template: categories mapped to lines can change year to
  year, and last year's voted budget must stay exactly as voted. "Copy from previous year"
  makes yearly setup a one-click job (see API).
- `transaction_categories` deletion: `DeleteCategory` already returns `409` on FK
  violation — now also fires when a budget line references the category. Update the error
  message ("still used by transactions or a budget line").
- Mandatory-category seed: none needed. Optional: `make seed` creates a sample budget for
  the current year so local dev shows the view.

### Initial mapping for the current sheet

New categories to create (admin, via UI) are marked *new*. Parents in brackets if
sub-categories are used.

| Budget line | Group | Kind | Categories |
|---|---|---|---|
| Membership fees | — | income | `membership_fee` |
| Interest | — | income | `credited_interest` |
| Other incomes | — | income | `other_income` |
| Members' Meeting proceedings | Events expenses | expense | `members_meeting` *new* |
| Board and active members intensive planning | Events expenses | expense | `board_planning` *new* |
| Public events and seminars | Events expenses | expense | `public_events` *new* |
| Letní odborová škola | Events expenses | expense | `summer_school` *new* |
| Employee wages | Employment expenses | expense | `salary` |
| Employee expenses | Employment expenses | expense | `employee_expenses` *new* |
| Legal | Employment expenses | expense | `legal` *new* [salary] |
| Accountant | Employment expenses | expense | `accountant` *new* |
| Social media | Employment expenses | expense | `social_media` *new* [salary] |
| Administrative | — | expense | `administrative` *new* |
| Workplaces expenses | — | expense | `workplace_expenses` *new* |
| OSPPP - membership fees | — | expense | `osppp_fee` *new* |
| IT infrastructure and domains | — | expense | `it_infrastructure` *new* |
| Other expenses | — | expense | `other_expense` |

"Incomes sum", "Expenses sum", "Difference" and the group subtotals are computed rows,
not stored lines. Note the sheet lists "Administrative … Other expenses" without a group
header — rendered ungrouped (`group_name` NULL) unless the treasurer wants one.

Categories have a slug `name` and `categoryLabel()` prettifies it in the UI. Czech/long
display names ("Letní odborová škola") live on the budget line `name`, not the category —
fine. If categories need nicer labels too, add `transaction_categories.label TEXT NULL`
later.

---

## 3. API

New role **`manage-budget`** (write), read stays on existing **`view-budget`**. Projected
amounts and per-line real totals are aggregate data, same privacy level as the existing
`/transactions/summary`, so `view-budget` is the right gate. `manage-budget` is granted to
**admins** — Board + Treasurer decide the budget, but entering it into the tool is a
technical task. Still a separate role rather than reusing `manage-transactions`, so budget
editing and transaction matching can be granted independently.

| Method | Route | Role | Purpose |
|---|---|---|---|
| GET | `/budgets` | view-budget | List years that have a budget `[{year, note}]` |
| GET | `/budgets/{year}` | view-budget | Full report: lines + categories + projected + real, plus unbudgeted rows |
| POST | `/budgets` | manage-budget | `{year, note?, copy_from_year?}` — create; copies lines + mappings + projected amounts when `copy_from_year` given. `409` if year exists |
| PATCH | `/budgets/{year}` | manage-budget | `{note}` |
| DELETE | `/budgets/{year}` | manage-budget | Cascades lines. Frontend confirms |
| POST | `/budgets/{year}/lines` | manage-budget | `{kind, group_name?, name, projected_amount, categories[], sort_order?}` |
| PUT | `/budgets/{year}/lines/{id}` | manage-budget | Full overwrite of the same fields (backs the Edit modal) |
| DELETE | `/budgets/{year}/lines/{id}` | manage-budget | `204` |
| PUT | `/budgets/{year}/order` | manage-budget | `{line_ids: []}` — reorder (optional; up/down buttons can use PUT line instead) |

Line create/update runs in one DB transaction: update line row, delete its
`budget_line_categories`, insert the new set. A category already used by another line of
the same year → `409` with `{"error": "...", "category": "salary", "line": "Employee wages"}`
(caught from the unique violation) so the modal can show which line owns it. Unknown
category → `400`.

`GET /budgets/{year}` response:

```json
{
  "year": 2026,
  "note": "approved 2026-03-14",
  "as_of": "2026-10-26",
  "lines": [
    {"id": 7, "kind": "expense", "group_name": "Employment expenses", "name": "Legal",
     "projected": "60000.00", "real": "41250.00", "categories": ["legal"], "sort_order": 10}
  ],
  "unbudgeted": [
    {"category": "some_new_category", "kind": "expense", "real": "1200.00"}
  ],
  "other_currency_count": 0
}
```

Every amount is a decimal string, consistent with the existing summary endpoint.

Add all mutating routes to `auditRoutes` in `internal/handler/audit.go` (category
`budget`). Add the `@`-annotations, then `make swagger`. New role constant in
`internal/keycloak/keycloak.go`, role created in Keycloak and granted to admins.

### Queries (`internal/db/queries.sql`)

"Real" per line:

```sql
-- name: GetBudgetLineActuals :many
SELECT
    bl.id,
    COALESCE(SUM(
        CASE WHEN bl.kind = 'income' THEN rt.amount ELSE -rt.amount END
    ), 0)::numeric AS real_total
FROM budget_lines bl
LEFT JOIN budget_line_categories blc ON blc.budget_line_id = bl.id
LEFT JOIN processed_transactions pt ON pt.category = blc.category
LEFT JOIN raw_transactions rt
       ON rt.id = pt.raw_transaction_id
      AND rt.currency = 'CZK'
      AND rt.transaction_date >= make_date(bl.budget_year, 1, 1)
      AND rt.transaction_date <  make_date(bl.budget_year + 1, 1, 1)
WHERE bl.budget_year = sqlc.arg(year)
GROUP BY bl.id;
```

**Signed sum, not `SUM(ABS(amount))`** like the existing summary: a refund (incoming
money on an expense category, e.g. a cancelled venue deposit) should *reduce* that line's
spend, not increase it. An expense line's real is `−SUM(amount)`, an income line's is
`SUM(amount)`. This intentionally differs from `/transactions/summary`; document it in the
query comment.

Plus `ListUnbudgetedCategoryTotals(year)` — categories with transactions in that year
(excluding `internal_transfer`) that aren't in any `budget_line_categories` row for the
year, grouped by category + direction. This is what makes the table reconcile: without
it, a payment categorized as something unmapped silently disappears from both sums.

Plus `ListBudgetLines(year)`, `ListBudgetLineCategories(year)`, CRUD queries, and
`CopyBudgetLines(from_year, to_year)` (`INSERT … SELECT` twice inside the handler's
transaction). Then `make sqlc`.

Edge: non-CZK transactions are skipped; report the count (`other_currency_count`) so it
isn't silent. Today everything is CZK, so this is just a guard.

---

## 4. Frontend

### Budget page gets two views

`BudgetPage.tsx` gets a toggle at the top: **Overview** (existing pie charts, month/year
filter, unchanged) | **Budget vs. actual** (new). Persist choice in the URL query
(`?view=plan`) so links work.

### "Budget vs. actual" view (new `budget/BudgetPlanView.tsx`)

- Year select (from `GET /budgets`). If the selected year has no budget: empty state,
  and for `manage-budget` a "Create budget for 2027" button with "Copy lines from 2026"
  checkbox (on by default when the previous year exists).
- Table mirroring the Excel layout:

  | | Projected | Real | Difference | % |
  |---|---:|---:|---:|---:|
  | **Incomes** | | | | |
  | Membership fees | 600 000 | 482 300 | −117 700 | 80 % |
  | … | | | | |
  | **Incomes sum** | | | | |
  | **Expenses** | | | | |
  | *Events expenses* (group subtotal row) | | | | |
  |   Members' Meeting proceedings | | | | |
  | … | | | | |
  | **Expenses sum** | | | | |
  | **Difference** (incomes − expenses) | | | | |
  | Unbudgeted (if any, warning color, with category names) | | | | |

- Difference column = Real − Projected. Color: for expenses, over budget = red; for
  incomes, under projection = red. Optional thin progress bar in the % cell.
- % = Real / Projected, against the full-year voted amount. No pro-rating, no linear
  projection, no "% of year elapsed" hint — plain numbers identical to the voted sheet.
  For the current year only a "Real as of 26 Oct 2026" date label.
- Clicking a line's Real amount → opens the transaction browser filtered to those
  categories and the year (needs `list-transactions`; hide the link otherwise). The
  browser's `category` filter takes one value today — either allow repeated
  `category=` params or link one category at a time from a small popover.
- "Export CSV" button (projected/real per line) — easy win since the board is used to
  Excel. Optional.

### Edit mode (role `manage-budget`)

- "Edit" toggle on the view. In edit mode each line gets a pencil icon, each section an
  "Add line" button, and up/down arrows for order.
- **Edit line modal** (reuse `Overlay.tsx`, same pattern as `AssignDialog`):
  - Name (text, required)
  - Kind (income / expense — preset from the section where "Add line" was clicked)
  - Group (combobox: existing group names of this year + free text, empty = no group)
  - Projected amount (number)
  - Categories: checkbox list of all categories (children indented under parents if
    sub-categories ship). Categories already mapped to *another* line this year are shown
    disabled with "used by: Employee wages". Only categories matching the kind's
    direction are suggested first, but all are allowed (some categories are mixed).
  - Delete button (confirm).
- Budget-level: edit note, delete budget (confirm with year typed in).

### Other frontend touches

- `api/budget.ts`: new types/functions for the endpoints above.
- `roles.ts`: add `manage-budget`.
- Categories page: parent column + parent select on create (if sub-categories ship);
  Rules section (if rules ship).
- `AssignDialog`: category select shows children grouped under parents; optional "create
  rule" checkbox.

---

## 5. Implementation order / milestones

1. **Fix `category_set_manually`** (migration + `AssignTransaction`/`UnassignTransaction`
   + reclassify candidate queries + tests). Small, independent, protects everything after.
2. **Budget backend**: migration, queries, handlers, role, audit routes, swagger, tests
   (real Postgres per repo convention — table-driven: real totals per line, refund
   reduces expense, unbudgeted rows, other-currency skip, unique-category `409`, copy from
   previous year).
3. **Budget frontend**: read-only plan view first, then edit mode + modal.
   → At this point the feature is usable: admin creates `legal` etc. as flat categories
   and re-categorizes lawyer payments by hand.
4. **Categorization rules**: migration (table + seeded built-in/interest/`mzda` rows +
   `categorization_rule_id`), rewrite `processing.Run` to walk the rule list, replace the
   three reclassify actions with `reapply_rules`, CRUD endpoints + audit routes, UI.
   Tests must show the seeded rules reproduce today's categorization (same fixtures, same
   output — except the intended `mzda`-in-`message_for_recipient` widening, which gets its
   own case) before the lawyer rule is added, plus a case where a VS pattern rule at
   priority < 300 beats a colliding member number. Removes the monthly manual
   re-tagging.
5. **Sub-category parent link** (optional polish): `parent_name`, Categories page
   hierarchy, browser filter/summary rollup.

## 6. Decisions (answered by treasurer/board)

- **Currency**: always CZK. No `currency` column; non-CZK transactions skipped and counted
  as a guard.
- **Projection**: none. Real vs. full-year projected amount + plain percentage; no linear
  or pro-rated projection.
- **Amendments**: a voted budget isn't changed during the year — no revisions table, no
  approved/current pair. Lines stay freely editable (`PUT`/`PATCH`) because the tool is
  also used during preparation; "don't edit after the vote" is process, not code.
- **`manage-budget`**: admins. Board + Treasurer prepare, members vote, admin enters it.
- **Lawyer matching**: by counter account number (outgoing) is enough. If the lawyer's
  account changes, the admin updates the rule.
- **One category per line**: confirmed. "Employee wages" excludes lawyer and social-media
  pay; both get their own sub-category and own budget line.
