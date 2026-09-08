# Fio Bank REST API — reference

Summary of Fio's "Bankovnictví" export/import API (classic token-in-URL API,
**not** the AISP v2/PSD2 API). Source: official PDF `API_Bankovnictvi.pdf`,
verze 1.9 (16.10.2025), obtained from Fio. Ask the developer for the current
PDF from Fio's docs if this ever needs re-checking against a newer version.

This file exists so Claude (and future devs) can check the API's actual
behavior without re-reading the PDF every time — keep it in sync whenever the
Fio integration changes. Code that implements this: [internal/fio/client.go](../internal/fio/client.go),
[internal/fio/types.go](../internal/fio/types.go), [internal/syncjob/fio.go](../internal/syncjob/fio.go).

## Base URL & auth

```
https://fioapi.fio.cz/v1/rest/
```

(Old base `https://www.fio.cz/ib_api/rest/` was retired — it 301-redirects to
the Fio homepage now. See git history / commit that fixed this.)

- Token: 64-char string, generated per-account in Fio's Internet Banking
  under **Nastavení → API**. Scoped to exactly one bank account.
- Token usable **5 minutes** after creation.
- Two token permission types: "Sledování účtu" (read-only, what we use) vs.
  "Sledování účtu a zadávání platebních příkazů" (read+write). Our `FIO_TOKEN`
  is read-only.
- Token validity max 180 days, can be set to auto-renew on each IB login.

## Rate limit

**Minimum 30 seconds between requests on the same token**, regardless of
format or read vs. write. Violating this returns `409 Conflict`. Not
currently a practical concern (we sync once daily / on-demand in dev), but
matters if retry logic or manual testing hits the API in a tight loop.

## Endpoints we use

All support multiple output formats via the `.{format}` suffix — we always
use `.json` (see [types.go:1-5](../internal/fio/types.go#L1-L5) for why: flat
per-transaction columns, easy to map to `raw_transactions`).

### `GET /last/{token}/transactions.json` — cursor-based sync

```
https://fioapi.fio.cz/v1/rest/last/{token}/transactions.json
```

Returns transactions since the last successful call **on this token**. Fio
stores the cursor server-side (not per our DB) — this is why we don't track
a "last synced" watermark ourselves. Used by [`Client.FetchNew`](../internal/fio/client.go).

- If there's nothing new, response is empty transaction list + just the
  account header — cursor is **not** advanced in that case.
- If there are new transactions, Fio advances the cursor **as a side effect
  of serving the response** (i.e. before/as the client reads it, not after
  the client acknowledges anything). This is why a crash between a
  successful `FetchNew` and our DB commit can lose a batch — see
  `RewindTo` below.
- Subject to the 90-day strong-auth rule (see below) if the cursor is more
  than 90 days stale.

### `GET /periods/{token}/{from}/{to}/transactions.json` — date range

```
https://fioapi.fio.cz/v1/rest/periods/{token}/{from}/{to}/transactions.json
```

`{from}`/`{to}` are `YYYY-MM-DD`. **Does not touch the `/last/` cursor at
all.** Used by [`Client.FetchPeriod`](../internal/fio/client.go) — our
`DEBUG=true` dev-mode sync path, to pull the last 90 days without needing
strong authorization or disturbing the real cursor. See [syncjob/fio.go](../internal/syncjob/fio.go)
`fioSCAWindow`.

### `GET /set-last-id/{token}/{id}/` — rewind cursor to a transaction ID

```
https://fioapi.fio.cz/v1/rest/set-last-id/{token}/{id}/
```

Sets the server-side cursor to just after the given `fio_transaction_id`
("ID pohybu"), so the next `/last/` call re-returns everything after it.
Used by [`Client.RewindTo`](../internal/fio/client.go) as best-effort
recovery when `FetchNew` succeeded (cursor already advanced) but our DB
insert then failed — without this, that batch is permanently skipped, since
`raw_transactions`'s unique constraint only guards against re-inserting rows
we already have, not rows we never received.

### `GET /set-last-date/{token}/{date}/` — rewind cursor to a date

```
https://fioapi.fio.cz/v1/rest/set-last-date/{token}/{rrrr-mm-dd}/
```

Alternative watermark reset, by date instead of transaction ID. **Not
currently implemented in our client** — could be added if we ever want to
prime the cursor to "start syncing from N days ago" without a full backfill
(e.g. bootstrapping a new bank account without triggering the 90-day SCA
requirement). Doesn't fetch data itself, so likely doesn't require strong
auth either, but this is unconfirmed — verify before relying on it.

## Endpoints that exist but we don't use

- `GET /by-id/{token}/{year}/{id}/transactions.{format}` — official numbered
  statements ("oficiální výpisy"), not raw transaction pulls.
- `GET /merchant/{token}/{from}/{to}/transactions.xml` — card/POS terminal
  transactions, XML only. Only relevant for merchant accounts.
- `GET /lastStatement/{token}/statement` — returns the number of the most
  recently generated official statement.
- `POST /import/` — uploads payment orders (ABO, Fio XML, SEPA pain.001/008).
  Requires a read+write token; ours is read-only, so this is a non-starter
  unless the token type changes.

## Response JSON shape

```json
{
  "accountStatement": {
    "info": { "accountId": "...", "currency": "...", "iban": "...", "...": "..." },
    "transactionList": {
      "transaction": [
        { "column22": {"id":22,"name":"ID pohybu","value":123}, "column0": {...}, "...": null }
      ]
    }
  }
}
```

Each transaction is a map of `column{N}` → `{id, name, value}` or JSON
`null` if that field is empty for that row (not merely omitted). Parsed in
[`rawTransaction.toTransaction`](../internal/fio/types.go).

### Column ID → field mapping (JSON `transactionList`)

| Column | Fio name (CZ)             | Our field                | Notes |
|--------|----------------------------|--------------------------|-------|
| 0      | Datum                       | `transaction_date`       | `rrrr-mm-dd+HHMM` e.g. `2026-05-28+0200`; we keep only the calendar date |
| 1      | Objem                       | `amount`                 | signed decimal |
| 2      | Protiúčet                   | `counter_account_number` | |
| 3      | Kód banky                   | `counter_bank_code`      | |
| 4      | KS                           | `constant_symbol`        | |
| 5      | VS                           | `variable_symbol`        | **primary member-payment matching key** — see [db-design.md](db-design.md) |
| 6      | SS                           | `specific_symbol`        | |
| 7      | Uživatelská identifikace     | `user_identification`    | free-text sender identification |
| 8      | Typ                          | `transaction_type`       | e.g. "Bezhotovostní příjem" — see type list in the PDF §5.1 (45 types) |
| 9      | Provedl                      | `executor`                | who executed the order (staff name, for outgoing payments) |
| 10     | Název protiúčtu              | `counter_account_name`   | |
| 12     | Název banky                  | `counter_bank_name`      | |
| 14     | Měna                          | `currency`                | ISO 4217 |
| 16     | Zpráva pro příjemce           | `message_for_recipient`  | |
| 17     | ID pokynu                     | `instruction_id`         | groups related "pohyby" (e.g. a payment + its fee) |
| 18     | Upřesnění                     | `specification`          | usually an FX rate/amount |
| 22     | ID pohybu                     | `fio_transaction_id`     | **unique per movement** — our idempotency key, `UNIQUE(bank_account_id, fio_transaction_id)` |
| 25     | Komentář                      | `comment`                 | |
| 26     | BIC                            | `bic`                     | |
| 27     | Reference plátce               | *(not mapped)*            | added in doc v1.6.28; not currently a DB column |

**ID pohybu vs. ID pokynu**: one instruction ("pokyn") can produce multiple
movements ("pohyby") — e.g. a foreign payment creates one pohyb for the
transfer and one for the fee, both sharing the same `instruction_id` but with
distinct `fio_transaction_id`s. Storno (reversal) similarly shares
`instruction_id` with the original but gets its own `fio_transaction_id` and
an opposite-signed amount.

## Errors we've actually hit or should handle

| Status / condition | Meaning | What to do |
|---|---|---|
| `404 Not Found` | Malformed URL/params | Check the path shape matches this doc |
| `409 Conflict` | <30s since last call on this token | Back off; don't retry-loop |
| `422` | Requesting data older than 90 days without strong auth | See below |
| `500 Internal Server Error` | Token doesn't exist / isn't active | Check token in Fio's IB, re-copy into `.env` |
| `413` | Response would exceed 50,000 movements | Narrow the date range, or advance the watermark first |
| **301 redirect** | Not a documented Fio error — happens if the base URL is wrong (e.g. the old `www.fio.cz/ib_api` domain) or (per Fio's own troubleshooting notes) sometimes for a malformed token. Our `http.Client` treats any redirect from this API as an error rather than following it silently into HTML — see `CheckRedirect` in [client.go](../internal/fio/client.go). | Verify base URL / token format |

## The 90-day strong-authorization (SCA) rule

Data younger than 90 days: no extra auth needed, `/last/` and `/periods/`
work with just the token.

Data 90+ days old: Fio returns `422` unless the account holder has done a
**strong authorization** for historical access, done manually in Fio's own
Internet Banking (Nastavení → API → click the lock icon next to the token →
authorize via SMS/push). That unlock is valid for **10 minutes**, during
which the full history becomes fetchable.

This is why our sync job has a `DISABLE_FIO_SYNC` / `DEBUG` split (see
[.env.example](../.env.example)): local dev has no way to do this
in-app authorization, so `DEBUG=true` makes the sync use `/periods/` capped
to the last 90 days instead of the full-history `/last/`, avoiding `422`
entirely. Production, with `DEBUG=false`, uses the real cursor-based
`/last/` and would need the manual 90-day unlock done once for any initial
backfill older than that window.

## Transaction type list

Full ordinal type list ("Typy pohybů na účtu", 45 entries, e.g. "Bezhotovostní
příjem", "Okamžitá odchozí platba") is in PDF §5.1 — not reproduced here
since `transaction_type` is stored as free text (`column8`/"Typ"), not an
enum, in `raw_transactions`. Check the PDF directly if a type ever needs
special-casing in the sync/matching logic.
