# bank-system frontend

Admin SPA for the bank-system API: Keycloak login, a processed-transactions
table, and admin actions (assign a transaction to a member, mark a payment as
covering multiple months of dues).

Stack: React + TypeScript, built with Vite. See `../docs/stack-overview.md`.

## Develop

Node comes from the repo's `nix develop` shell (see `../flake.nix`).

```sh
npm install
npm run dev      # http://localhost:5173, proxies /api -> http://127.0.0.1:25987
```

Run the backend separately (`make start` in the repo root). The dev proxy
targets `http://127.0.0.1:25987` (`vite.config.ts`) — edit that if the backend
runs elsewhere.

All API calls use same-origin relative URLs (`/api/...`) — the Vite dev proxy and
the production nginx config both route `/api/` to the Go service, so no
per-environment base URL is needed.

## Auth

Keycloak, Authorization Code + PKCE, tokens in memory only. Full setup (the
**required Keycloak audience mapper**, client config, production CSP) is in
`../docs/frontend-auth.md`. Config comes from `VITE_KEYCLOAK_*` env vars —
`.env.development` has working local defaults; override in `.env.local`.

You need a Keycloak user with the `payment-history` client role
on the `bank-system` client to get past sign-in usefully. The `manage-bank-accounts`
role additionally shows the "Bank accounts" admin page.

## Build

```sh
npm run build    # type-check + bundle into dist/
npm run preview  # serve dist/ locally to sanity-check the production build
```

`dist/` is the static bundle nginx serves at `/` in production (packaged as its
own Nix derivation — not yet wired into `flake.nix`).

## Not wired yet

Add when building the corresponding screens:

- **react-router-dom** — routing
- **@tanstack/react-table** — the processed-transactions list
- **react-hook-form** + **zod** — the assign / cover-months forms, and runtime
  validation of API responses
- a component kit — **Mantine** or **shadcn/ui**

Backend also needs new write endpoints first (manual member assignment, lump-sum
`payment_coverage` entry) — see `../docs/db-design.md`.
