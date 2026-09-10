# Orca `/sync/bank/members` — API contract

Spec for the endpoint Orca implements, so bank-system's daily member sync job can pull
payment-relevant member data. See [logic-design.md](logic-design.md) "Orca Member Sync"
and [db-design.md](db-design.md) `members` section for the design decisions behind this
(pull not push, why only these fields, why no `variable_symbol` here).

## Request

```
GET /sync/bank/members
Authorization: Bearer <ORCA_SYNC_TOKEN>
```

- Static shared-secret bearer token (CSPRNG-generated, 32+ bytes), **not** a
  Keycloak token — this route is called machine-to-machine with no logged-in
  user behind it. Same value stored as `ORCA_SYNC_TOKEN` in bank-system's env
  and `sync_token` in Orca's config.
- No query params — this is a full pull every time, not incremental. At
  member-list scale (~hundreds), a full pull each day is cheap; no `since`/
  cursor param needed unless that changes.

## Response — `200 OK`

```json
{
  "members": [
    {
      "member_number": 42,
      "fee_start_date": "2024-01-15",
      "fee_stop_date": null,
      "active": true,
      "sub": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
      "workplace_executive_committee_sub": "9c858901-8a57-4791-81fe-4c455b099bc9"
    },
    {
      "member_number": 43,
      "fee_start_date": null,
      "fee_stop_date": null,
      "active": true,
      "sub": null,
      "workplace_executive_committee_sub": null
    },
    {
      "member_number": 12,
      "fee_start_date": "2021-03-01",
      "fee_stop_date": "2024-11-30",
      "active": false,
      "sub": null,
      "workplace_executive_committee_sub": null
    }
  ]
}
```

Top-level object (not a bare array) so pagination/metadata can be added
later without a breaking shape change.

| Field | Type | Nullable | Meaning |
|---|---|---|---|
| `member_number` | integer | no | Human-readable member ID (Orca's internal `member_id` UUID is **not** exposed here — `member_number` is what appears in payment variable symbols and is bank-system's join key). Must be unique per member. |
| `fee_start_date` | string, `YYYY-MM-DD` | yes | Date the member became liable for the membership fee. `null` if not yet determined/liable — bank-system's missed-payment detection treats a null `fee_start_date` as "not yet liable," not as "liable since forever." |
| `fee_stop_date` | string, `YYYY-MM-DD` | yes | Date the member's fee liability *ended* (they left, or were made fee-exempt). `null` while liability is still open. bank-system uses it as the upper bound of the liability window: missed-payment detection stops expecting payments after this date, and it becomes `valid_to` on the member's default payment identifier so post-departure transactions no longer match as membership fees. Orca must send the real historical date, not the day the member's status changed in Orca — otherwise a member who left years ago looks liable up to today. |
| `active` | boolean | no | Whether the member is currently active. bank-system does not delete rows on sync — an inactive/departed member stays in `members` with `active = false` so payment history stays intact. Mirrored into `members.active` but **not** currently used by any bank-system logic (fee-window logic keys on `fee_start_date`/`fee_stop_date`); kept for FE display and future use. |
| `sub` | string, UUID | yes | The member's Keycloak `sub` claim (their Keycloak account's UUID) — lets bank-system join a logged-in user's JWT to their `member_number` row. `null` if the member has no Keycloak account yet. Distinct from Orca's internal `member_id` — not exposed here, see below. |
| `workplace_executive_committee_sub` | string, UUID | yes | The Keycloak **group** ID of the member's workplace executive committee (the "Workplace N reps" group in Keycloak) — not a user `sub` despite the name, same ID-space idea as `sub` above but for a group rather than an individual account. Lets bank-system scope a rep's payment-history view to just their own workplace's members, by looking up the rep's Keycloak groups live (via Keycloak's Account API, not a token claim — see `logic-design.md` "Workplace-Scoped Payment History") and matching against this column, with no live call back to Orca. `null` if the member isn't currently assigned to a tracked workplace. |

Deliberately excluded: name, email, Orca's internal `member_id` (UUID), or
any other identity field — Orca stays the sole source of truth for member
identity; bank-system only mirrors the fields it actually needs for payment
matching, liability tracking, and (via `sub`) tying a logged-in request to a
member row. Also excluded: `variable_symbol` — that's tracked in
bank-system's own `member_payment_identifiers` table, not synced from Orca
(a member's VS is a bank-system-side payment concern, can change
independently of Orca's member record, and isn't something Orca has anyway).
bank-system does, however, *derive* a default identifier from this response:
on each sync it seeds one `member_payment_identifiers` row per member with
`variable_symbol = member_number`, `valid_from = fee_start_date` (skipped
while `fee_start_date` is null) and `valid_to = fee_stop_date`. See
`logic-design.md` "Orca Member Sync".

## Sync semantics (bank-system side, for context)

- Idempotent upsert into `members` keyed on `member_number` — same shape as
  the Fio sync job. Re-running the pull with unchanged data is a no-op.
- A member no longer present in the response is **not** deleted locally — Orca is expected
  to always include every member (past and present) with `active` reflecting
  current status. Orca always returns the full set,
  `active` is authoritative, so bank-system's upsert logic stays a dumb
  merge with no delete/absence-inference edge cases.
- Run tracking: bank-system logs each pull in `sync_orca_runs`
  (`members_fetched`, `members_upserted`, `error_message`) — no action
  needed on Orca's side beyond returning a correct response.

## Error responses

| Status | When |
|---|---|
| `401 Unauthorized` | Missing/incorrect bearer token |
| `500` | Orca-side failure — bank-system will log and retry on the next scheduled run, no special handling needed |
