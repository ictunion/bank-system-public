# Run these from inside `nix develop` (postgresql/goose/sqlc binaries come from the flake devShell).

DB_NAME     ?= bank_system
DB_USER     ?= bank_system
DB_PASSWORD ?= bank_system
DB_PORT     ?= 5433
PGDATA      := $(CURDIR)/.pgdata

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

.PHONY: start
start:
	@pg_ctl -D "$(PGDATA)" status >/dev/null 2>&1 || $(MAKE) db-start
	go run ./cmd/server

.PHONY: build
build:
	go build -o server ./cmd/server
