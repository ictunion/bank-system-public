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
	// (method, URL, headers, bodies) for external calls, e.g. to Fio's API.
	// Set via the DEBUG env var (any of "1", "true", "yes", case-insensitive).
	Debug bool

	// DisableFioSync skips starting the Fio sync job entirely (no startup run, no
	// daily 3am run). For local dev when you don't want the live Fio API touched
	// at all — e.g. after seeding fake raw_transactions data, so a server restart
	// doesn't re-pull and re-insert real transactions over it. Set via the
	// DISABLE_FIO_SYNC env var (any of "1", "true", "yes", case-insensitive).
	DisableFioSync bool

	// OrcaAPIURL is Orca's base URL, e.g. https://api.ictunion.cz or
	// http://127.0.0.1:8000 for local dev. Used by the daily member sync job to
	// call `GET {OrcaAPIURL}/sync/bank/members` (see docs/orca-sync-members.md).
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
	KeycloakHost     string
	KeycloakRealm    string
	KeycloakClientID string
}

func envBool(name string) bool {
	return map[string]bool{"1": true, "true": true, "yes": true}[strings.ToLower(os.Getenv(name))]
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
		DisableFioSync:   envBool("DISABLE_FIO_SYNC"),
		OrcaAPIURL:       orcaAPIURL,
		OrcaSyncToken:    orcaSyncToken,
		KeycloakHost:     keycloakHost,
		KeycloakRealm:    keycloakRealm,
		KeycloakClientID: keycloakClientID,
	}, nil
}
