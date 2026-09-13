// Package config loads server configuration from environment variables.
//
// Env vars are read directly via os.Getenv — no dependency on systemd or any other
// OS-specific mechanism to set them, so this works the same whether the process is
// started by systemd on NixOS, a Windows service, Docker, or a plain shell.
//
// If a .env file exists in the working directory, it's loaded first (without
// overriding any variable already set in the real environment) purely as a local-dev
// convenience. Production deployments are expected to set real env vars and can omit
// the file entirely.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	// DatabaseURL is a standard Postgres connection string, e.g.
	// postgres://user:password@host:5433/dbname?sslmode=disable
	// The Postgres server does not need to run on the same machine — pass any
	// reachable host here.
	DatabaseURL string

	// Addr is the address the HTTP server listens on, e.g. ":8080".
	Addr string

	// BankTokenEncryptionKey is the symmetric passphrase used to encrypt/decrypt
	// each bank_accounts row's Fio API token at rest via pgcrypto
	// (pgp_sym_encrypt/pgp_sym_decrypt — see migrations and internal/db/queries.sql).
	// Never stored in the DB itself. Losing this key makes every already-encrypted
	// token unrecoverable until re-entered per account through the admin UI;
	// rotating it requires decrypting every row with the old key and re-encrypting
	// with the new one (not automated — no bulk key-rotation tooling yet).
	BankTokenEncryptionKey string

	// Debug enables verbose logging of outgoing requests and incoming responses
	// (method, URL, headers, bodies) for external calls, e.g. to Fio's API, and
	// skips the Fio sync job entirely (no startup run, no daily 3am run, and the
	// admin "sync now"/backfill buttons refuse with 409) — local dev has no real
	// Fio account/token, so touching the live API would just fail or overwrite
	// `make seed`'s fixture data. Set via the DEBUG env var (any of "1", "true",
	// "yes", case-insensitive).
	Debug bool

	// EnableSwaggerDocs mounts the generated Swagger UI (see
	// internal/swaggerdocs, produced by `make swagger` from the @-annotations
	// on cmd/server/main.go and internal/handler/*.go) at GET /api/docs/*.
	// Off by default — dev-only, never set in production: every real route
	// here is role-gated, and the docs UI itself carries no auth of its own,
	// so it should never be reachable outside local development. Set via the
	// ENABLE_SWAGGER_DOCS env var (any of "1", "true", "yes", case-insensitive).
	EnableSwaggerDocs bool

	// FioAPIURL is the base URL the Fio sync job builds every request against
	// (see internal/fio.NewClient), e.g. https://fioapi.fio.cz/v1/rest in
	// production. No default baked into the Go code — this env var is the
	// single source of truth, same as OrcaAPIURL below. Tests point it at an
	// httptest.NewServer instead.
	FioAPIURL string

	// OrcaAPIURL is Orca's base URL, e.g. https://api.ictunion.cz or
	// http://127.0.0.1:8000 for local dev. Used by the daily member sync job to
	// call `GET {OrcaAPIURL}/sync/bank/members`.
	OrcaAPIURL string

	// OrcaSyncToken authenticates the daily member sync job against Orca's
	// `/sync/bank/members` route — a static shared secret (not a Keycloak
	// token), sent as `Authorization: Bearer <token>`. Must match `sync_token`
	// in Orca's own config.
	OrcaSyncToken string

	// KeycloakHost/Realm/ClientID configure verification of user-facing bearer
	// tokens (see internal/keycloak), same Keycloak instance/realm as the rest
	// of ictunion's stack. ClientID is bank-system's own client ID in
	// Keycloak — the audience every verified token must carry, and the key
	// under which required roles are looked up (resource_access[ClientID]).
	// KeycloakHost must be https:// unless it's localhost/127.0.0.1/::1 (see
	// requireHTTPSExceptLoopback) — plain HTTP against a real Keycloak host
	// exposes the JWKS fetch and forwarded bearer tokens to interception.
	KeycloakHost     string
	KeycloakRealm    string
	KeycloakClientID string
}

func envBool(name string) bool {
	return map[string]bool{"1": true, "true": true, "yes": true}[strings.ToLower(os.Getenv(name))]
}

// requireHTTPSExceptLoopback rejects rawURL unless it's https://, or plain
// http:// against loopback (localhost/127.0.0.1/::1) — the one case with no
// TLS cert available, since Keycloak normally runs on the same dev machine
// (see .env.example). A real Keycloak host reachable over plain HTTP would
// let a network-position attacker substitute the JWKS response (forging
// tokens this service then accepts as valid — see internal/keycloak) or
// read forwarded bearer tokens off the wire (internal/keycloak.Provider's
// Account API calls forward the caller's own token).
func requireHTTPSExceptLoopback(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	switch parsed.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return nil
	}
	return fmt.Errorf("must be https:// (got %q) — plain http is only allowed for localhost/127.0.0.1", rawURL)
}

func Load() (Config, error) {
	_ = godotenv.Load()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL environment variable is required")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	bankTokenEncryptionKey := os.Getenv("BANK_TOKEN_ENCRYPTION_KEY")
	if bankTokenEncryptionKey == "" {
		return Config{}, fmt.Errorf("BANK_TOKEN_ENCRYPTION_KEY environment variable is required")
	}

	fioAPIURL := os.Getenv("FIO_API_URL")
	if fioAPIURL == "" {
		return Config{}, fmt.Errorf("FIO_API_URL environment variable is required")
	}

	orcaAPIURL := os.Getenv("ORCA_API_URL")
	if orcaAPIURL == "" {
		return Config{}, fmt.Errorf("ORCA_API_URL environment variable is required")
	}

	orcaSyncToken := os.Getenv("ORCA_SYNC_TOKEN")
	if orcaSyncToken == "" {
		return Config{}, fmt.Errorf("ORCA_SYNC_TOKEN environment variable is required")
	}

	keycloakHost := os.Getenv("KEYCLOAK_HOST")
	if keycloakHost == "" {
		return Config{}, fmt.Errorf("KEYCLOAK_HOST environment variable is required")
	}
	if err := requireHTTPSExceptLoopback(keycloakHost); err != nil {
		return Config{}, fmt.Errorf("KEYCLOAK_HOST: %w", err)
	}

	keycloakRealm := os.Getenv("KEYCLOAK_REALM")
	if keycloakRealm == "" {
		return Config{}, fmt.Errorf("KEYCLOAK_REALM environment variable is required")
	}

	keycloakClientID := os.Getenv("KEYCLOAK_CLIENT_ID")
	if keycloakClientID == "" {
		return Config{}, fmt.Errorf("KEYCLOAK_CLIENT_ID environment variable is required")
	}

	return Config{
		DatabaseURL:            databaseURL,
		Addr:                   ":" + port,
		BankTokenEncryptionKey: bankTokenEncryptionKey,
		Debug:                  envBool("DEBUG"),
		EnableSwaggerDocs:      envBool("ENABLE_SWAGGER_DOCS"),
		FioAPIURL:              fioAPIURL,
		OrcaAPIURL:             orcaAPIURL,
		OrcaSyncToken:          orcaSyncToken,
		KeycloakHost:           keycloakHost,
		KeycloakRealm:          keycloakRealm,
		KeycloakClientID:       keycloakClientID,
	}, nil
}
