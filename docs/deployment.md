# Deployment (NixOS)

How `bank-system` ships to production, following the same pattern the rest of
ictunion's stack already uses (see the `infra` repo — `orca.nix`, `web.nix`,
`gray-whale.nix`). This doc lives here because it's about *this* project's build
outputs and runtime config; the actual NixOS module lives in `infra`, a separate
private repo. Nothing here is implemented yet — this is the plan.

## Shape of the deployment

Two independent Nix derivations, same split `stack-overview.md`'s "Deployment"
sketch already calls for:

1. **`bank-system-api`** — the Go binary (`cmd/server`), one process that serves
   `/api/*` *and* runs the daily Fio/Orca sync + processing job internally (a
   goroutine started in `main.go` — no separate timer/cron unit needed).
2. **`bank-system-frontend`** — the Vite build (`frontend/`), static files served
   by nginx.

Both run on ictunion's existing NixOS host (`ictunion.cz`, deployed via
`deploy-rs`/`nixosConfigurations.vpsfree` in `infra/flake.nix`) — no new host.
`nginx` is the only public listener, same as every other service there
(`orca.nix`, `keycloak.nix`, `web.nix`): `location /` → frontend static files,
`location /api/` → `proxy_pass http://127.0.0.1:<port>/api/`. The Go service
binds `127.0.0.1` only, matching orca's `ROCKET_HOST`/nginx split
(`orca.nix`).

## What's missing before any of this can be wired up

`flake.nix` here currently only exposes a `devShells.default` — no `packages`
output. `infra/flake.nix` builds every other ictunion service by pulling its
source repo in as a flake input and reading `outputs.packages."${system}"` off
it (see `injectModule` in `infra/flake.nix`: `ictunion-system =
main-system-src.outputs.packages."${system}"`). This repo needs the matching
output before `infra` can reference it as `ictunion-bank-system` (or similar)
the same way.

Add to this repo's `flake.nix`:

```nix
packages.${system} = {
  api = pkgs.buildGoModule {
    pname = "bank-system-api";
    version = "0.1.0";
    src = ./.;
    subPackages = [ "./cmd/server" ];
    vendorHash = null; # or the real hash once `go mod vendor` is pinned — see buildGoModule docs
  };

  frontend = pkgs.buildNpmPackage {
    pname = "bank-system-frontend";
    version = "0.1.0";
    src = ./frontend;
    npmDepsHash = ""; # nix will print the correct value on first build failure
    # VITE_* vars are baked into the bundle at build time (see frontend/.env.example) —
    # production values must be set here, not left to a runtime .env.
    env = {
      VITE_KEYCLOAK_URL = "https://keycloak.ictunion.cz";
      VITE_KEYCLOAK_REALM = "members";
      VITE_KEYCLOAK_CLIENT_ID = "bank-system";
    };
    installPhase = ''
      mkdir -p $out/var/www
      cp -r dist/* $out/var/www
    '';
  };

  default = self.packages.${system}.api;
};
```

`api.vendorHash`/`frontend.npmDepsHash` get filled in for real on the first
build attempt (Nix reports the correct hash in the error when it's wrong/null)
— not guessed here. The frontend's `installPhase` output shape (`$out/var/www`)
matches every other static-site derivation in `infra` (`melon-head`,
`members-panel`, `paytransparency`) so it drops straight into
`pkgs.ictunion-lib.nginx.with-static-site-caching`.

## Database

New role + database on the existing Postgres cluster (`infra/nixos/postgres.nix`
— already `trust`-authenticated for `localhost`, no password needed, not
reachable over the network at all; see `infra/secrets/README.md` for why that's
the deliberate model). Matches local dev's `bank_system`/`bank_system` naming
(`Makefile`):

```nix
# infra/nixos/postgres.nix
services.postgresql = {
  ensureDatabases = [ "bank_system" ];
  ensureUsers = [{
    name = "bank_system";
    ensureDBOwnership = true;
  }];
};
```

(`gray-whale.nix`'s `ensureDatabases` for `ictunion` has no matching
`ensureUsers` — its role predates that NixOS option and was created by hand
once. Declaring both here avoids that manual step for a fresh deploy.)

`DATABASE_URL=postgres://bank_system@localhost/bank_system?sslmode=disable` —
no password, same as every other `trust`-authenticated local connection on this
host.

**Migrations** run as a one-shot systemd unit before the service starts, same
pattern as `gray-whale-migrate.service`:

```nix
# infra/nixos/bank-system.nix
systemd.services.bank-system-migrate = {
  wantedBy = [ "multi-user.target" ];
  after = [ "postgresql.service" ];
  serviceConfig = {
    Type = "oneshot";
    ExecStart = "${pkgs.goose}/bin/goose -dir ${bank-system-src}/migrations postgres \"postgres://bank_system@localhost/bank_system?sslmode=disable\" up";
  };
};
```

Also add `"bank_system"` to `services.postgresqlBackup.databases` in
`postgres.nix` (currently `[ "ictunion" "listmonk" "roundcube" "keycloak" ]`) —
easy to forget, and this is the one DB in the deployment holding data nothing
else can regenerate (raw Fio transaction history, manual category
assignments).

## Secrets

Two real secrets, neither derivable from anything else: `BANK_TOKEN_ENCRYPTION_KEY`
(symmetric key protecting every stored Fio API token — losing it makes existing
tokens unrecoverable) and `ORCA_SYNC_TOKEN` (shared secret with Orca's
`/sync/bank/members` route — must match Orca's own config value exactly).

`infra`'s existing secrets model (`infra/secrets/README.md`) is deliberately
low-tech: plaintext files committed to that private repo, protected by the fact
that the host has no exposed DB port and only trusted admins get SSH access —
`keycloak_psql_pass` (`infra/nixos/keycloak.nix`, read via `passwordFile`) is
the existing precedent for a *recoverable* plaintext secret (the mail
passwords in the same directory are bcrypt-*hashed*, which doesn't apply here —
this app needs the raw value at runtime, not a comparison).

Follow that same shape, generalized to `EnvironmentFile` since there are two
values, not one:

1. Add `infra/secrets/bank-system.env` (git-committed to the private `infra`
   repo, **not** this repo):
   ```
   BANK_TOKEN_ENCRYPTION_KEY=<openssl rand -base64 32>
   ORCA_SYNC_TOKEN=<matches Orca's own sync_token config>
   ```
2. Reference it from the service unit via `serviceConfig.EnvironmentFile`, not
   the plain `environment = { ... }` attrset `orca.nix` uses for its
   (non-secret) config — an `environment` attrset gets embedded straight into
   the generated unit file under `/nix/store` and shows up in `systemctl show`/
   `/proc/<pid>/environ` to any local user; `EnvironmentFile` keeps the two
   secret values out of both.

Everything else (`FIO_API_URL`, `ORCA_API_URL`, `KEYCLOAK_HOST`,
`KEYCLOAK_REALM`, `KEYCLOAK_CLIENT_ID`, `PORT`, `DEBUG`, `DISABLE_FIO_SYNC`) is
non-secret and can sit directly in the unit's `environment = { ... }` attrset,
same as `orca.nix`'s `ROCKET_*` vars.

## NixOS service module

`infra/nixos/bank-system.nix`, modeled on `orca.nix`:

```nix
{ config, pkgs, ... }:
let
  bank-system = pkgs.ictunion-bank-system.api;
  bank-system-frontend = pkgs.ictunion-bank-system.frontend;
in
{
  users = {
    groups.bank-system = { };
    users.bank-system = {
      group = "bank-system";
      isSystemUser = true;
    };
  };

  systemd.services.bank-system-migrate = { /* see "Database" above */ };

  systemd.services.bank-system = {
    description = "bank-system API";
    after = [ "bank-system-migrate.service" "postgresql.service" "keycloak.service" ];
    wantedBy = [ "multi-user.target" ];
    environment = {
      PORT = "25987";
      DATABASE_URL = "postgres://bank_system@localhost/bank_system?sslmode=disable";
      FIO_API_URL = "https://fioapi.fio.cz/v1/rest";
      ORCA_API_URL = "https://api.ictunion.cz";
      KEYCLOAK_HOST = "https://keycloak.ictunion.cz";
      KEYCLOAK_REALM = "members";
      KEYCLOAK_CLIENT_ID = "bank-system";
      DEBUG = "false";
      DISABLE_FIO_SYNC = "false";
    };
    serviceConfig = {
      EnvironmentFile = "${pkgs.ictunion-secrets}/var/secrets/bank-system.env";
      ExecStart = "${bank-system}/bin/server";
      Restart = "on-failure";
      User = "bank-system";
      NoNewPrivileges = true;
      PrivateTmp = true;
      ProtectSystem = "full";
      PrivateDevices = true;
    };
  };

  services.nginx.virtualHosts."bank.ictunion.cz" = pkgs.ictunion-lib.nginx.with-static-site-caching {
    root = "${bank-system-frontend}/var/www";
    locations."/" = {
      tryFiles = "$uri /index.html"; # SPA fallback — React Router client-side routes
      extraConfig = ''
        add_header Content-Security-Policy "default-src 'self'; connect-src 'self' https://keycloak.ictunion.cz; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'" always;
        add_header X-Content-Type-Options "nosniff" always;
        add_header Referrer-Policy "same-origin" always;
        add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
      '';
    };
    locations."/api/" = {
      proxyPass = "http://127.0.0.1:25987/api/";
    };
  };
}
```

The CSP/security headers block is copied verbatim from `frontend-auth.md`
("Production hardening (nginx)") — kept in sync there, not duplicated as a
second source of truth here.

`bank.ictunion.cz` above is a placeholder — pick the real subdomain (matches
the Keycloak client's redirect URI in `frontend-auth.md`, currently written
there as the placeholder `payments.example.com`; both need to agree on
whatever's chosen) and:

- add it to `infra/nixos/default.nix`'s `imports` (`./bank-system.nix`)
- add a matching `enableACME = true; forceSSL = true;` entry to
  `infra/security/default.nix`'s `services.nginx.virtualHosts`, same as every
  other public vhost there
- update the Keycloak `bank-system` client's redirect URIs and this doc's
  `VITE_KEYCLOAK_*`/CSP `connect-src` accordingly if it changes

## Keycloak

The `bank-system` client (public, PKCE, audience mapper, roles) must exist in
the `members` realm before first deploy — full checklist already in
`frontend-auth.md` ("Required Keycloak client config", "Audience mapper",
"Roles"). Not repeated here. One thing specific to *production* rather than
local dev: the client's **Valid redirect URIs** must list the real
`https://<subdomain>.ictunion.cz/*`, not just the `localhost:5173` dev entry.

## Orca coordination

`ORCA_SYNC_TOKEN` is a shared secret — generating it on the bank-system side
only (`infra/secrets/bank-system.env`) does nothing until Orca's own config
(`infra/nixos/orca.nix`'s `ROCKET_*` env, or wherever Orca ends up reading its
`sync_token` from) is updated to the same value. Coordinate the two changes in
the same deploy; a mismatch fails every daily member sync with an auth error,
not a crash, so it's easy to miss for a day.

## Deploy checklist

1. Add `packages.${system}` to this repo's `flake.nix` (api + frontend), fill
   in real `vendorHash`/`npmDepsHash`.
2. In `infra/flake.nix`: add `bank-system-src` as a flake input, wire it into
   `injectModule`'s overlay as `ictunion-bank-system`, same shape as
   `ictunion-system`.
3. `infra/nixos/postgres.nix`: `ensureDatabases`/`ensureUsers` for
   `bank_system`, add it to `postgresqlBackup.databases`.
4. `infra/secrets/bank-system.env`: generate `BANK_TOKEN_ENCRYPTION_KEY`
   (`openssl rand -base64 32`), agree `ORCA_SYNC_TOKEN` with Orca's config.
5. Create the `bank-system` Keycloak client in the `members` realm (see
   `frontend-auth.md`), redirect URIs pointed at the real production subdomain.
6. Add `infra/nixos/bank-system.nix` (service + migrate unit + nginx vhost),
   import it from `infra/nixos/default.nix`.
7. Add the ACME vhost entry to `infra/security/default.nix`.
8. `deploy-rs` to `vpsfree` (`infra`'s own `deploy` flake app).
9. Post-deploy: see "Post-deploy runbook" below.

## Post-deploy runbook

What actually needs to happen, in order, once both BE and FE are running for the
first time:

1. **Orca sync runs automatically on startup** — no action needed. The
   scheduler fires immediately on boot as well as daily at 3am
   (`scheduler.RunImmediatellyAndThenDaily`, [main.go](../cmd/server/main.go)),
   so `members`/`member_payment_identifiers` are populated from Orca right away.
2. **That same first cycle's Fio sync does nothing, and that's expected** —
   `RunFioSync` errors immediately with "no bank_accounts row exists yet"
   ([syncjob/fio.go](../internal/syncjob/fio.go)) since no bank account exists
   yet. That failure also skips `processing.Run` for this cycle (`main.go`:
   `if !orcaOK || !fioOK { ...; return }`) — harmless, there's no raw
   transaction data yet to process either.
3. **Create the bank account** via the FE (Bank accounts → Add bank account):
   `fio_account_id`, `display_name`, and the real Fio API token from Fio's IB
   (Nastavení → API).
4. **Unlock historical access in Fio's own Internet Banking** (Nastavení → API
   → lock icon next to the token → SCA via SMS/push) — needed for any account
   with data older than 90 days, which an existing account almost certainly
   has. The unlock is valid for **10 minutes**, so do this immediately before
   step 5, not in advance.
5. **Run Backfill** (Bank accounts → Backfill…) for the account's full
   history — see "How far back to backfill" below for the date range, and
   do it within that 10-minute window.
6. **That's it for data flow.** `POST /account/{id}/backfill` runs
   `processing.Run` synchronously (`internal/handler/account.go`), so matched
   transactions and `payment_coverage` rows exist as soon as the backfill call
   returns — every read endpoint (transaction browser, payment history,
   missing payments, budget) is serving real data at that point. There's no
   separate "process now" step to run.
7. **Manually re-match legacy payments the backfill couldn't match** — see
   "The unmatched-legacy-payments problem" below. Expect to need this; there's
   no fully automatic fix for it today.

### How far back to backfill

Use a date safely before the account was ever opened (e.g. `2015-01-01`) — **you don't
need to know the real first-transaction date.** Fio's `/periods/` endpoint just returns
whatever it actually has in `[from, to]`; asking for a range that starts before the
account existed isn't an error, it's just an empty result for the part with no data.

Two real constraints on the range, though:

- Anything older than 90 days needs the SCA unlock (step 4) done first — without it the
  whole call fails with a `422` for that entire range, not just the pre-90-day part (see
  [fio-api.md](fio-api.md), "The 90-day strong-authorization (SCA) rule").
- Fio caps a single response at 50,000 movements (`413`). A long-lived, high-volume
  account may need the backfill split into a few narrower calls (e.g. one per year)
  instead of a single all-time one — the endpoint takes an arbitrary range, so that's
  just calling it again with different `from`/`to`, no code change needed.

### The unmatched-legacy-payments problem

Matching (`internal/processing.processOne`) only has one *reliable* signal today: an
exact variable-symbol match against `member_payment_identifiers`. Anything without a
matching VS — wrong digit, blank, a member paying under a spouse's/company's transfer,
years-old payments predating a consistent VS convention — falls into `other_income`,
unmatched to any member. On a large historical backfill this can plausibly be hundreds
of real membership payments landing as "unmatched," which in turn makes the
missing-payments views show members as non-paying when they actually paid, just under
the wrong identifier.

**There's no fully automatic fix that's also safe.** The strongest available signal is
`counter_account_number` — most people pay dues from the same personal bank account
every time — but auto-applying a match on that basis risks real harm if wrong (a shared
household account, one person paying for two members, a company account): it would
mark someone as paid when they didn't pay, or attribute a payment to the wrong member's
record. That risk is exactly why processOne doesn't do this today.

The realistic middle ground, not built yet: **assisted matching, not automatic
matching** — extend the transaction browser/detail view to *suggest* a member for an
unmatched transaction based on `counter_account_number` matches on that member's
previously-VS-confirmed payments, surfaced as a one-click-to-confirm suggestion rather
than an auto-applied match. This turns the backlog from "type in a member number
hundreds of times" into "review and confirm a pre-filled suggestion hundreds of times" —
meaningfully faster, without silently trusting a heuristic with real payment-status
consequences. Scoped as its own feature; not implemented as part of this deploy.

## Not covered here

- **CI/CD for this repo's own tests** — `.github/workflows/test.yml`, separate
  from production deployment (see `testing.md`).
- **Zero-downtime rollout** — a plain `systemd` restart on deploy is a few
  seconds of `/api` downtime; not addressed here since nothing in this stack
  needs stricter uptime than orca/keycloak already get from the same
  deploy-rs flow.
- **Log aggregation/monitoring** — this service `log.Printf`s to stdout
  (journald captures it, same as every other systemd service here); no
  dashboards or alerting wired up yet.
