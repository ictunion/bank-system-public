# Plan: standalone budget site for members

## Goal

Serve the budget page (`Budget` and `Categories overview` views) as its own website,
e.g. `https://budget.ictunion.cz`, behind the same Keycloak login. Every member can view
it. Editing stays admin-only (`manage-budget`). The admin app keeps its own Budget page
unchanged.

## Approach

One frontend codebase, two entry points, one build. The new site is a second nginx vhost
that serves the budget entry and proxies `/api/` to the same Go backend, so the API stays
same-origin and no CORS change is needed. It reuses the existing Keycloak client
`bank-system`, so the backend's audience check and role names stay as they are.

The backend needs no code change.

## Steps

### 1. Load categories only in edit mode

`BudgetTable` ([BudgetPlanView.tsx](../frontend/src/budget/BudgetPlanView.tsx)) always
fetches `GET /categories`, which requires `list-transactions`, `view-rules` or
`manage-rules`. A member holding only `view-budget` would get a `403` on every page load.
The category names are only shown in edit mode (under each line's name, and in
`BudgetLineDialog`), so:

- add `enabled: editing` to the `['categories']` query in `BudgetTable`;
- `BudgetLineDialog` only opens in edit mode, so its own query needs no change.

Editing still requires `manage-budget`, and admins hold a categories role as well.

### 2. Second frontend entry point

- `frontend/budget.html`: copy of `index.html` that loads `/src/budgetMain.tsx`.
- `frontend/src/budgetMain.tsx`: same providers as `main.tsx` (`AuthProvider`,
  `QueryClientProvider`, `BrowserRouter`) wrapped in `AuthGate`, rendering only the
  budget routes:
  - `/` and `/budget` redirect to `/budget/plan`
  - `/budget/plan` → `<BudgetPage view="plan" />`
  - `/budget/overview` → `<BudgetPage view="overview" />`
  - a small header with the site name and a sign-out button, no admin navigation
  - a "You don't have access to the budget" message when the user lacks `view-budget`
    (`useHasRole`), instead of failing API calls.
- `vite.config.ts`: `build.rollupOptions.input = { main: 'index.html', budget: 'budget.html' }`.
  One `npm run build` then emits both pages into `dist/` and shares the JS chunks.

Keeping the `/budget/...` paths means `BudgetPage`'s tab buttons and existing links work
unchanged on both sites.

Local dev: Vite's dev server falls back to `index.html` for unknown paths, so the budget
entry is reachable directly only at `/budget.html`. Either accept that (the same page is
already testable inside the admin app), or add a few-line dev-server middleware that
rewrites non-asset requests to `/budget.html` when started with a flag. Decide while
implementing.

### 3. Nix

`nix/bank-frontend.nix` already copies all of `dist/` into `$out/var/www`, so both pages
land in the same output. No new derivation is needed; the new vhost points at the same
directory with a different fallback file.

### 4. nginx

New vhost, mirroring the admin one:

```nginx
server {
  server_name budget.ictunion.cz;
  root <bank-frontend>/var/www;

  location / {
    try_files $uri /budget.html;
  }
  location /api/ {
    proxy_pass http://127.0.0.1:<port>/api/;
  }

  # same security headers as the admin vhost (CSP with the Keycloak origin in
  # connect-src, nosniff, Referrer-Policy, HSTS)
}
```

The auditor report upload (up to 20 MiB) is admin-only, but the `/api/` location still
needs the same `client_max_body_size` as the admin vhost if admins will edit from here.

### 5. Keycloak

On the existing `bank-system` client:

- Valid redirect URIs: add `https://budget.ictunion.cz/*`
- Web origins: `+` already covers it once the redirect URI is added
- Valid post-logout redirect URIs: add the new origin if the list is not `+`

Grant `view-budget` to every member. Use a group or composite role that members already
hold, not `default-roles-<realm>`, which would also grant it to non-member accounts in
the realm.

Login carries over through the Keycloak session: a user already signed in to the admin
app or Orca is signed in to the budget site without a password prompt. Each site keeps
its own stored tokens, because they are on different origins.

### 6. Docs

Update the "Budgets" and "Roles & auth" sections of `CLAUDE.md`: the new entry point and
vhost, `view-budget` now granted to all members, categories loaded only in edit mode.

## What members will see

`view-budget` grants these endpoints, all of which members will now be able to call
directly, not only through the page:

- `GET /budgets`, `GET /budgets/{year}`: the voted budget and actual figures per line,
  plus unbudgeted categories with their totals.
- `GET /transactions/summary`: income and expense totals per category for any date range,
  and the current balance of all accounts.
- `GET /budgets/{year}/auditor-report`: the auditor's PDF.

None of these return member numbers, names or counterparty details.

**Decision needed before rollout:** category totals can reveal individual amounts when a
category has only one payee. With a single employee, `salary` for one month is that
person's pay; `legal` for one month is one lawyer's invoice. The `Categories overview`
view lets anyone pick a single month. Options:

1. Accept it: the voted budget is already public to members.
2. Show only `Budget` (yearly figures) on the member site and keep `Categories overview`
   admin-only. This needs a separate role for `/transactions/summary`, since the API
   cannot tell which site a request came from.
3. Restrict the summary to whole years for non-admins (backend change).

## Effort

About half a day to a day: steps 1 and 2 are small frontend changes; most of the time
goes into nginx, Keycloak and role setup, and testing the login flow on the new origin.
