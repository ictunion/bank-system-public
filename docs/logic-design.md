# Business Logic Design Notes

Companion to [`db-design.md`](./db-design.md) — decisions about how derived/business
logic is computed, as opposed to how data is stored.

## Orca Member Sync

Daily pull, same idempotent shape as the Fio sync job (see `db-design.md` `members`
section for why pull over push, and `sync_orca_runs` for the tracking table). Calls
Orca's `/sync/bank/members` — a dedicated endpoint scoped to payment-relevant fields only
(`member_number`, `fee_start_date`, `fee_stop_date`, `active`, `sub`,
`workplace_executive_committee_sub`), not full member identity. `active` is mirrored
into `members.active` but no bank-system logic reads it — the fee-liability window is
`fee_start_date` .. `fee_stop_date`, both real dates from Orca.

## Workplace-Scoped Payment History

`workplace_executive_committee_sub` (see `db-design.md` `members` section) lets a
workplace rep see payment history/missed-payments for only their own workplace's
members, without being an admin (`list-transactions`/`payment-history`) and without
bank-system calling back into Orca per request. Orca sends this field on
`/sync/bank/members`, and the daily sync job upserts it into
`members.workplace_executive_committee_sub` same as every other member field.

Three routes carry the **`view-workplace-payment-history`** role — a *capability* check
only, deliberately as weak as that sounds (see below). Two are workplace-only routes
gated by `RequireRole`; the third, `GET /payments/{member_number}/history`, additionally
accepts `payment-history` via `RequireAnyRole` and does its own extra check when only
the weaker role matched — see "Payment History Endpoint" above for that one, it's not
repeated here:

- **`GET /payments/workplace/{year}/{month}/missing`** / **`GET
  /payments/workplace/{year}/missing`** — same `{member_number,
  total_missed_months, has_ever_paid}[]` shape as
  `MissingPayments`/`MissingPaymentsInYear`, filtered to the caller's workplace(s).

(A bulk `GET /payments/workplace/history` — every payment for every member in a rep's
workplace, not just who's missing — was built alongside these but removed: missed
payments is the only workplace-rep view actually needed for now. Its
`GetPaymentHistoryForWorkplace` query and `WorkplacePaymentHistory` handler are gone;
re-add both if that view comes back into scope.)

**Scoping mechanism:** live, not a token claim. `handler.RequireRole`/`RequireAnyRole`
attach both the verified `Claims` *and* the raw bearer token string to the request
context (same as `RequireAuth` does for `/payments/me/history`), and every workplace
handler calls `keycloak.Provider.UserGroupIDs(ctx, token)` — forwarding the caller's own
token to Keycloak's **Account REST API** (`GET {issuer}/account/groups`), which is
self-scoped: it answers only for whoever's token it is, so no elevated privilege is
needed on bank-system's side at all. This mirrors Orca's own
`KeycloakProvider::get_own_groups` (`orca/src/server/oid/keycloak.rs`,
ictunion/main-system-public) exactly. The resulting group IDs pass straight through to
SQL as `WHERE members.workplace_executive_committee_sub =
ANY(sqlc.arg(workplace_subs)::uuid[])` (`ListMembersMissingPaymentForWorkplace`,
`ListMembersMissingPaymentInYearForWorkplace` in `queries.sql`). No lookup of "which of
my groups is a workplace" needed — a rep's other, unrelated Keycloak groups just match
zero members and are silently harmless. This is the same RBAC-for-capability /
claim-for-scope split `/payments/me/history` already uses with `sub`: the role says *you
may call this route type*, the live lookup
says *which rows come back*.

**Why live instead of a token claim:** Keycloak's stock Group Membership mapper only
emits a group's *path*/*name*, never its internal UUID — getting the UUID onto the token
any other way needs a script mapper or custom SPI, ruled out as non-standard. Calling
the Account API instead keeps `workplace_executive_committee_sub` as a plain Keycloak
group UUID with zero extra Keycloak configuration — no protocol mapper, no second
client, no secret; just the caller's own token, forwarded. **The one thing to confirm,
Keycloak-side:** the `view-groups` role on the `account` client, on by default via
`default-roles-<realm>` in a stock realm (see `frontend-auth.md` "Workplace-scoped
payments"). Without it, `UserGroupIDs` errors rather than silently returning nothing.

Alongside each `members` upsert, the sync manages a **default payment identifier** in
`member_payment_identifiers`: `variable_symbol = member_number`, `valid_from =
fee_start_date`, `valid_to = fee_stop_date`. This is the "member pays under their own
member number" common case — the variable symbol Fio matching keys on. It's not synced
from Orca (Orca has no notion of variable symbols — see `orca-sync-members.md`); the
symbol is derived locally from `member_number`, the window from the two fee dates. The
row is inserted `ON CONFLICT DO NOTHING` (so a re-run is a no-op), and skipped entirely
while `fee_start_date` is null — the member isn't liable yet and `valid_from` is `NOT
NULL`; the next sync after Orca sets `fee_start_date` seeds it.

On every sync the row's `valid_to` is re-mirrored to the member's current
`fee_stop_date`: set when Orca reports fee liability ended, cleared back to `null` if
`fee_stop_date` is unset again. Because Orca sends the *real* end date (not the day the
status changed in Orca), a member who left a year ago is closed as of a year ago, not
as of deploy day. Only the default row (`variable_symbol = member_number`) is
sync-managed; to point a member at a different variable symbol, add a new row with that
symbol and its own `valid_from` — the sync never touches it. Effect on matching: a
payment landing after `fee_stop_date` falls outside the `valid_from`/`valid_to` window,
so it doesn't match as a membership fee and falls through to `other_income` — correct,
since the member owes no fee past that date.

### Auth

Orca's normal endpoints authenticate via a Keycloak user token, scoped to whichever
logged-in user/admin is making the request. This sync job has no logged-in user behind
it — it's a machine calling on its own behalf — so `/sync/bank/members` uses a different
scheme: a static shared-secret bearer token instead of Keycloak.

- Token generated with a CSPRNG (32+ random bytes, hex/base64), not a hand-picked string.
- Stored identically on both sides: `ORCA_SYNC_TOKEN` in this service's env, `sync_token`
  in Orca's own config. No Keycloak involved on either end for this route.
- Sent as `Authorization: Bearer <token>` — same header shape Orca already parses for
  Keycloak tokens, so Orca's middleware just branches on route: this one path checks
  against the static secret (constant-time comparison — `subtle.ConstantTimeCompare` in
  Go, not `==`, since this path skips Keycloak's own signature-based validation and a
  hand-rolled `==` check would leak timing information), everything else keeps validating
  Keycloak as normal.
- Scoped to this one route only — not a general Orca credential, and separate from
  `FIO_TOKEN` (different service, different trust boundary, independent rotation).

Considered and deferred: Keycloak client-credentials grant (service account) instead of a
hand-rolled secret — would get expiry/rotation/revocation through Keycloak's existing
admin tooling for free. Worth it if Orca already has a service-account pattern for other
internal callers; not worth standing up Keycloak client infra for a single caller if this
is the first one. Revisit if a second machine-to-machine caller shows up.

## Transaction Processing

Runs after both daily syncs (Orca, then Fio — see `cmd/server/main.go`), turning new
`raw_transactions` rows into `processed_transactions` (+ `payment_coverage` for the
default case). Idempotent: only processes rows with no `processed_transactions` row yet,
so a normal daily run only touches that day's new transactions, and a failed run is safe
to retry — it just picks up whatever's still unprocessed.

Per transaction:

1. **Member match** — `variable_symbol` looked up against `member_payment_identifiers`,
   restricted to the identifier valid on the transaction's date (`valid_from` /
   `valid_to`). Matched → `category = 'membership_fee'`, `matched_by = 'variable_symbol'`,
   and a `payment_coverage` row inserted for the transaction's own year/month (the
   default single-month case — see `db-design.md` `payment_coverage`). Lump-sum
   multi-month coverage stays a manual follow-up step, not handled here.
2. **Salary detection** — only checked if not matched above. Substring match on `"mzda"`
   (case-insensitive) against `comment` or `user_identification`. Not error-proof (no
   structured payroll signal exists yet), but good enough for a first version — revisit
   if it starts mis-tagging. Matched → `category = 'salary'`, no member/coverage.
3. **Fallback** — anything left over is categorized by direction alone:
   `other_income` (amount positive) or `other_expense` (amount negative/zero).

`is_public_visible` is left at its schema default (`false`) for every row — nothing is
auto-marked visible in budget views; that's a manual admin edit, same as recategorizing
a mismatched row (see `db-design.md` "Design Decision: `processed_transactions` is a
real table").

## Transaction Browser (`GET /transactions`)

Backs the admin frontend's transaction list: `processed_transactions` joined to its
`raw_transactions` row, newest first (`transaction_date DESC, id DESC`), with optional
filters. Handler `ListTransactions`, query `ListTransactions`, gated by the
**`list-transactions`** Keycloak role — kept separate from `payment-history` because
this view carries every transaction's counterparty name including salary payments, with
no redaction; chasing missing member dues and seeing who gets paid what are different
trust levels.

Filters, all optional query params, all combinable (each is a nullable SQL arg —
omitted means "don't filter on it", via `(sqlc.narg(x)::T IS NULL OR col = x)`):

| param | values | effect |
|---|---|---|
| `assigned` | `true` / `false` | `member_number IS [NOT] NULL` — `false` is the unassigned worklist |
| `direction` | `incoming` / `outgoing` | |
| `category` | any `transaction_categories.name` | not validated against the table — an unknown value just matches nothing |
| `matched_by` | `variable_symbol` / `manual` / `amount_heuristic` | audit auto- vs hand-matched |
| `member_number` | int | one member's transactions |
| `from`, `to` | `YYYY-MM-DD` | inclusive range on `transaction_date` |
| `limit`, `offset` | int | `limit` default 100, capped 500; `offset` default 0 |

Bad enum / date / int values are `400`. Response is
`{total, limit, offset, transactions[]}` where `total` is the full match count ignoring
pagination (`count(*) OVER ()` in the same query, read off the first row). Each item
carries `id` (the `processed_transactions.id` — the handle for the upcoming assign /
add-coverage actions), amount/date/currency/direction, category/member_number/matched_by,
the payment symbols, and counterparty + free-text fields for identification.

The January-missing-payments workflow: `/payments/2026/1/missing` says *who* is short,
then `GET /transactions?direction=incoming&assigned=false&from=2026-01-01&to=2026-01-31`
lists the unassigned January credits to match against them.

Deferred filters: free-text search (`ILIKE` on counterparty name / VS / message),
amount range, currency, `bank_account_id`, "covers month X" (join `payment_coverage`).

### Detail view

`GET /transactions/{id}` (same `list-transactions` role) returns one transaction in the
list-item shape plus `covered_months: [{year, month}]` from `payment_coverage`. Backs the
edit dialog.

### Transaction Category Summary (`GET /transactions/summary`)

The budgeting pie charts: total spent/received per category, e.g. "total spent on
salaries this year", never individual transactions. Gated by the **`view-budget`**
role, deliberately not `list-transactions` — this is meant to end up on every member's
Keycloak account eventually (a members-facing budgeting page is planned as a thin
wrapper around the same FE component and this same endpoint), whereas
`list-transactions` exposes counterparty names and stays admin-only. The response never
carries `member_number`, counterparty, or any other per-transaction field — only
`category`/`currency`/`total` — so widening who can call it later is safe by
construction, not by convention.

Optional `from`/`to` (`YYYY-MM-DD`, inclusive), same convention as the transaction
browser above; the FE derives these from Year/Month selectors rather than exposing raw
date inputs. Response:

```json
{
  "incoming": [ {"category": "membership_fee", "currency": "CZK", "total": "48200.00"}, ... ],
  "outgoing": [ {"category": "salary", "currency": "CZK", "total": "112000.00"}, ... ]
}
```

One query, one round trip for both pie charts (`GROUP BY direction, category, currency`
in `GetTransactionCategorySummary`, split into the two arrays in the handler) rather than
two separate calls. `total` is `SUM(ABS(amount))` — always a positive spend/receipt
figure regardless of how `raw_transactions.amount` signs incoming vs outgoing. Grouped by
`currency` too (not just category) since `bank_accounts`/`raw_transactions` don't
constrain every account to the same currency — summing across currencies would be
silently wrong; in practice this repo currently has a single CZK account so every row
comes back with the same currency, but the query doesn't assume that.

## Manual Assignment & Coverage

The admin edits an auto-processed transaction: attach it to the right member, and (for a
membership fee) say which months it pays for. Both live on one endpoint so the write is
atomic and matches how the admin thinks ("this payment is member 42's, covering
Jan–Mar"). Gated by the **`manage-transactions`** role — separate from
`list-transactions` so a read-only auditor can browse without being able to re-match.

**`PUT /transactions/{id}/assignment`**

```json
{ "member_number": 42, "category": "membership_fee", "covers": [ {"year":2026,"month":1}, {"year":2026,"month":2} ] }
```

- `member_number` is **optional** — plenty of transactions (other_income/other_expense,
  even some salary rows) aren't tied to any member. Omit it to make this a category-only
  edit: `{"category": "other_expense"}` alone is a valid request. When given, it's
  validated (`400` if not a real member) and `matched_by` is set to `'manual'`; omitted,
  `member_number` and `matched_by` are both cleared to `NULL`.
- `category` optional, default `membership_fee`; must name an existing `transaction_categories`
  row (`400` otherwise — see "Transaction Categories" below).
- One DB transaction: set `member_number` + `category` + `matched_by`; delete this
  transaction's `payment_coverage` rows; re-insert one per `covers` entry.
- `covers`/coverage rows only apply for `category = membership_fee` **with** a
  `member_number` (`payment_coverage.member_number` is `NOT NULL`, so there's nothing to
  attach a coverage row to otherwise) — `400` if `covers` is given for a different
  category, or for `membership_fee` with no `member_number`. When omitted/empty for a
  covered membership fee, it defaults to the transaction's own year/month — same as the
  automatic single-month case.
- A requested month already covered by a **different** transaction (the
  `payment_coverage` `UNIQUE (member_number, covers_year, covers_month)` constraint) →
  `409` with `{conflicts: [{year, month}]}`, whole write rolled back. The admin clears
  the other transaction's coverage first.
- `404` if the transaction doesn't exist.
- Success returns the updated detail (`GET /transactions/{id}` shape).

**`DELETE /transactions/{id}/assignment`**

Reverts a match (manual or automatic): clears `member_number` and `matched_by`, deletes
the transaction's `payment_coverage` rows, resets `category` to the direction default
(`other_income` / `other_expense`). Salary re-detection is *not* re-run — an edge case;
re-processing would be a separate action. `204` on success. One DB transaction.

Liability-window checks are deliberately **not** enforced on `covers` — manual override
(including back-dating a lump sum) is the whole point of this endpoint.

## Transaction Categories

`transaction_categories(name TEXT PRIMARY KEY, is_mandatory BOOLEAN, created_at)` backs the
`category` column (`processed_transactions.category` has an FK to it, added in
`migrations/20260910000001_add_transaction_categories.sql`). `membership_fee`, `salary`,
`other_income`, `other_expense` are seeded by that migration with `is_mandatory = true` —
they're hardcoded elsewhere too (the default/fallback categories in
`internal/processing/processing.go` and `internal/handler/transactions.go`), so deleting
them isn't just a data question, it'd break processing. Everything else an admin adds is
`is_mandatory = false` and freely deletable.

- **`GET /categories`** (`list-transactions` role) — every category, mandatory ones first.
- **`POST /categories`** (`manage-transactions` role) — `{name}`, always creates
  `is_mandatory = false`. `409` on a duplicate name.
- **`DELETE /categories/{name}`** (`manage-transactions` role) — `404` if the name doesn't
  exist, `400` if `is_mandatory`, `409` if any `processed_transactions` row still
  references it (the FK), else `204`.

## Payment History Endpoint (`/payments/<member_number>/history`)

**Driven by `payment_coverage`, not `processed_transactions` directly** — coverage table
holds one row per month actually paid, which is what the panel needs to show ("paid
Jan, Feb, Mar"), not one row per bank transaction. A single lump-sum transaction can
cover several months, so transaction count and paid-month count diverge.

Query (see `db-design.md` `payment_coverage` section for full example):

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

Endpoint logic:

1. Run query above — one row per covered month.
2. Group rows by `processed_transaction_id` before returning, so the panel can render
   either per-month ("Jan: paid, Feb: paid") or per-payment ("900 CZK, covers Jan–Mar")
   depending on what the frontend wants. Grouping happens at the endpoint/service layer,
   not in SQL — no aggregation function fits both display needs cleanly at this scale.
3. No amount validation against an expected fee — `members` has no
   `monthly_fee_amount` (see `db-design.md`), so any amount on a matched transaction is
   shown as-is, not checked.
4. Coverage rows come from the processing step for the default case (payment covers its
   own transaction month) and from manual entry for lump-sum cases — this endpoint is
   read-only against `payment_coverage`, it does not create coverage rows itself.

Same underlying table (`payment_coverage`) also backs missed-payment detection below —
one is "which months have a row" (history), the other is "which expected months don't"
(missing).

**Two routes, three access paths, one render function.** `GET
/payments/{member_number}/history` is gated by `RequireAnyRole(payment-history,
view-workplace-payment-history)`, then branches inside `handler.PaymentHistory` itself:

- Holding **`payment-history`** (Orca admins) — any member, no further check.
- Holding only **`view-workplace-payment-history`** (a workplace rep) — only if that member's
  `workplace_executive_committee_sub` matches one of the caller's own Keycloak groups
  (`memberInCallerWorkplace`, same scoping the `/payments/workplace/.../missing` routes
  use — see "Workplace-Scoped Payment History" below), else `403`.

`GET /payments/me/history` is the third path — self-service, requires only a valid
token (any Keycloak client bank-system accepts, see `docs/db-design.md` `members`
`sub` column), no role check at all. It resolves `member_number` from the token's
`sub` via `members.sub`, then renders the same way `PaymentHistory` does
(`writePaymentHistory`) — bypassing `PaymentHistory`'s own role/workplace branch
entirely, since a sub match is its own, stronger authorization regardless of which
roles (if any) that token happens to carry. All three paths funnel into the same
render function so the response shape can't drift between them.

## Missed Payment Detection

**Computed on read, not prefilled/stored.**

`payment_coverage` (see `db-design.md`) only holds rows for months a member actually
paid — one row per `(member_number, covers_year, covers_month)`. A "missing" month is
just the absence of a row, found by diffing against the months a member was liable to
pay.

Two cohort endpoints, both gated by the same `payment-history` Keycloak role as the
per-member history route (anyone trusted with an individual member's payments is trusted
with the cohort list — same data sensitivity), both returning
`{member_number, total_missed_months, has_ever_paid}` sorted by `total_missed_months`
descending (top offenders first), `member_number` breaking ties:

- `GET /payments/{year}/{month}/missing` (handler `MissingPayments`, query
  `ListMembersMissingPayment`) — members liable that specific month with no coverage
  row for it.
- `GET /payments/{year}/missing` (handler `MissingPaymentsInYear`, query
  `ListMembersMissingPaymentInYear`) — members who missed at least one liable month
  anywhere in that calendar year.

`total_missed_months` is the same figure for both: a total-arrears count — *every*
unpaid month across the member's full liability window, not scoped to the queried
month/year — so it's stable regardless of what you ask about and serves as the
contact-priority sort key. It's always `>= 1` for a member in either list.

`has_ever_paid` is `false` when the member has zero `payment_coverage` rows at all,
across their whole history — not just for the queried month/year. Distinguishes a member
who never started paying at all (a real, common case in practice — e.g. missed the
onboarding email with bank transfer details and never noticed) from one who's usually
current but missed a month; `total_missed_months` alone can't tell those apart, but they
warrant different follow-up (send onboarding info again vs. a one-off reminder). Nothing
else is returned: the frontend resolves names/contact details from Orca by
`member_number`.

Both `total_missed_months` and `has_ever_paid` come from the `member_arrears` view
(`migrations/…_member_arrears_view.sql`), which is the full-window `generate_series` +
`NOT EXISTS` diff below wrapped as a per-member `count(*)` (plus a plain `EXISTS` for
`has_ever_paid`) — defined once so the two
endpoints (and any future `/payments/{member_number}/missing`) can't drift. The
month/year endpoints add only their own "was a payment missed in this window" filter on
top. `year` is validated `1 <= year <= current year + 1`, `month` `1 <= month <= 12`,
before the query runs.

Per-member query shape (the diff the `member_arrears` view is built from; also the
basis for a future `/payments/{member_number}/missing` route):

```sql
SELECT expected.month
FROM generate_series(
    date_trunc('month', m.fee_start_date),
    date_trunc('month', COALESCE(m.fee_stop_date, CURRENT_DATE)),
    interval '1 month'
) AS expected(month)
LEFT JOIN payment_coverage pc
    ON pc.member_number = m.member_number
    AND pc.covers_year = EXTRACT(YEAR FROM expected.month)
    AND pc.covers_month = EXTRACT(MONTH FROM expected.month)
WHERE m.member_number = $1
    AND m.fee_start_date IS NOT NULL
    AND pc.id IS NULL;
```

The window is `fee_start_date` .. `COALESCE(fee_stop_date, CURRENT_DATE)`: a member with
no `fee_start_date` yet isn't liable (the `IS NOT NULL` guard drops them — `generate_series`
on a null start would error anyway), and a member with a `fee_stop_date` isn't expected
to pay past it, so an ex-member stops accruing "missed" months the day their liability
ended rather than forever. (Restrict further with
`member_payment_identifiers.valid_from`/`valid_to` if a member's identifier window is
narrower still.)

### Why not prefill `payment_coverage` with placeholder/missing rows

Considered and rejected:

- Would need a background job to keep placeholder rows in sync every time
  `fee_start_date`, `member_payment_identifiers.valid_from/valid_to`, or just the
  passage of time (a new month starting) changes what's "expected" — extra moving part
  for no benefit at this scale.
- `payment_coverage` currently has one clean meaning: a row = an actual payment applied
  to that month. Mixing in placeholder "missing" rows would overload that meaning and
  complicate the manual lump-sum-payment workflow (inserting/removing coverage rows) it
  was built for.
- At ~1,000 transactions/month, `generate_series` + `LEFT JOIN` per member is cheap
  enough to compute on every request — no measured need to precompute.

Revisit only if this becomes a proven hot path (e.g. a dashboard computing "missing"
across all members frequently) — at that point, a plain view or scheduled cache refresh
would be the first thing to try before touching the storage model.
