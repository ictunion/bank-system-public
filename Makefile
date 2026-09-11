# Run these from inside `nix develop` (postgresql/goose/sqlc binaries come from the flake devShell).

DB_NAME      ?= bank_system
DB_USER      ?= bank_system
DB_PASSWORD  ?= bank_system
DB_PORT      ?= 5433
DB_TEST_NAME ?= bank_system_test
PGDATA       := $(CURDIR)/.pgdata

.PHONY: db-init
db-init:
	@if [ -s "$(PGDATA)/PG_VERSION" ]; then \
		echo "already initialized at $(PGDATA)"; \
	else \
		initdb -D "$(PGDATA)" -U postgres -A trust; \
	fi

.PHONY: db-start
db-start:
	pg_ctl -D "$(PGDATA)" -o "-c listen_addresses=localhost -c unix_socket_directories=$(PGDATA) -p $(DB_PORT)" -l "$(PGDATA)/server.log" start
	@until pg_isready -h localhost -p $(DB_PORT) -q; do sleep 0.2; done
	@psql -h localhost -p $(DB_PORT) -U postgres -tc "SELECT 1 FROM pg_roles WHERE rolname='$(DB_USER)'" | grep -q 1 \
		|| psql -h localhost -p $(DB_PORT) -U postgres -c "CREATE ROLE $(DB_USER) LOGIN PASSWORD '$(DB_PASSWORD)'"
	@psql -h localhost -p $(DB_PORT) -U postgres -tc "SELECT 1 FROM pg_database WHERE datname='$(DB_NAME)'" | grep -q 1 \
		|| psql -h localhost -p $(DB_PORT) -U postgres -c "CREATE DATABASE $(DB_NAME) OWNER $(DB_USER)"

.PHONY: db-stop
db-stop:
	pg_ctl -D "$(PGDATA)" stop -m fast

.PHONY: db-status
db-status:
	pg_ctl -D "$(PGDATA)" status

.PHONY: psql
psql:
	PGPASSWORD=$(DB_PASSWORD) psql -h localhost -p $(DB_PORT) -U $(DB_USER) -d $(DB_NAME)

.PHONY: migrate
migrate:
	goose -dir migrations postgres "postgres://$(DB_USER):$(DB_PASSWORD)@localhost:$(DB_PORT)/$(DB_NAME)?sslmode=disable" up

.PHONY: sqlc
sqlc:
	sqlc generate

# Regenerates internal/swaggerdocs from the @-annotation comments on
# cmd/server/main.go (general API info) and internal/handler/*.go (one
# @Router block per route). Dev-only doc — see docs/stack-overview.md and
# config.EnableSwaggerDocs: the UI only mounts when ENABLE_SWAGGER_DOCS=true,
# never in a production build. --parseInternal so swag can resolve response
# types declared in internal/handler despite dir-scoping to the two package
# paths that actually carry annotations.
.PHONY: swagger
swagger:
	swag init -g main.go -d ./cmd/server,./internal/handler --output internal/swaggerdocs --parseInternal
	go mod tidy

.PHONY: db-test-init
db-test-init:
	@pg_ctl -D "$(PGDATA)" status >/dev/null 2>&1 || $(MAKE) db-start
	@psql -h localhost -p $(DB_PORT) -U postgres -tc "SELECT 1 FROM pg_database WHERE datname='$(DB_TEST_NAME)'" | grep -q 1 \
		|| psql -h localhost -p $(DB_PORT) -U postgres -c "CREATE DATABASE $(DB_TEST_NAME) OWNER $(DB_USER)"

.PHONY: migrate-test
migrate-test:
	goose -dir migrations postgres "postgres://$(DB_USER):$(DB_PASSWORD)@localhost:$(DB_PORT)/$(DB_TEST_NAME)?sslmode=disable" up

# See docs/testing.md — TEST_DATABASE_URL is deliberately separate from
# DATABASE_URL so a test-setup bug can never point at dev data by sharing a
# variable name. -p 1 forces package test binaries to run one at a time:
# `go test ./...` otherwise runs them concurrently, and every package shares
# this one physical test database — dbtest.Tx (per-test rollback) tolerates
# that fine, but dbtest.Pool's TRUNCATE-based cleanup does not (a truncate
# from one package's cleanup can wipe rows a different package's test is
# still using), so all packages sharing this DB must run serially.
.PHONY: test
test: db-test-init migrate-test
	TEST_DATABASE_URL="postgres://$(DB_USER):$(DB_PASSWORD)@localhost:$(DB_PORT)/$(DB_TEST_NAME)?sslmode=disable" go test -p 1 ./...

.PHONY: start
start:
	@pg_ctl -D "$(PGDATA)" status >/dev/null 2>&1 || $(MAKE) db-start
	go run ./cmd/server

.PHONY: build
build:
	go build -o server ./cmd/server

.PHONY: frontend-install
frontend-install:
	cd frontend && npm install

.PHONY: frontend-dev
frontend-dev:
	cd frontend && npm run dev

.PHONY: frontend-build
frontend-build:
	cd frontend && npm run build
