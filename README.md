Main responsibility of this code is payment synchronization and matching. Code does daily pulls from Fio bank (raw transactions from their API) and from Orca (minimal member info -> member number, workplace identification, payment start/stop date).

## API
API is serving multiple routes (see Swagger for detailed info). It's split into few different responsibilities.

Payment history -> We can query this API for payment history of every member, returns matched transactions.

Missing payments -> We can request list of non-paying members per year/month. We have also second version of these routes for Workplaces, so that workplace reps can see their own members who are non-paying.

Budget -> Summary route returns year/month statistics used for Budget-view. Internal for now, can be made public later - accessible to all members.

Payment reconciliation -> Set of routes to define payment categories, match specific payments to categories or members, mark payments as covering multiple months or waiving payment completely for specific year/month. Should be accessible to admins and money people (treasurer, audit committee, etc)

Logging -> Routes that returns logs about every sync, for admins only.

## Frontend
Frontend is located in `/frontend` directory. It provides UI for admins and money people to do operations on top of transactions.

## How to build
We have makefile prepared to assist with running the code. It's important to start by copying `.env.example` to `.env` and setting all mandatory fields. The API can be created by running:
* `make db-start`
* `make migrate`
* `make seed`
* `make sqlc && make start`

Frontend can by created by running (make sure that the database and API has already been started):
* `make frontend-install`
* `make frontend-build && make frontend-dev`

Bank System needs running Keycloak. It can be started by `make up` in Main-system repo. Or update your `.env` and point to your Keycloak instance.

## Roles
Every route need some role from Keycloak. We don't have any public routes, everything is protected. See [internal/keycloak/keycloak.go](internal/keycloak/keycloak.go) for list of roles.

## Swagger
To enable swagger docs, set `ENABLE_SWAGGER_DOCS` to true and open `/api/docs/index.html`. Swagger docs are intended only for development and should be turned off in production.
