// Package config loads runtime configuration for bishop-memory from
// environment variables (with .env support via godotenv). The values
// are consumed by cmd/memoryd/main.go to wire HTTP listening, the
// SQLite database path, runtime mode, and log verbosity.
package config

import (
	"net"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

// Config holds the runtime configuration for the bishop-memory service.
//
// Fields are exported so callers (cmd/memoryd/main.go,
// internal/api/router.go) can read them directly; MustLoad is the only
// constructor and is the documented entry point.
type Config struct {
	// HTTPHost is the interface bishop-memory's HTTP listener binds
	// to. Defaults to loopback-only ("127.0.0.1"): with no API key
	// configured, /v1 is open to any caller that can reach it, which is
	// only safe on loopback. Binding anything else (a Tailscale or LAN
	// address, 0.0.0.0) requires an API key — cmd/memoryd refuses to
	// start without one unless AllowNoAuth is set, and the router then
	// refuses every /v1 request without a valid key
	// (docs/plans/NETWORK-DEPLOYMENT-PLAN.md §3; see IsLoopbackHost).
	HTTPHost string

	// HTTPAddr is the gin-form listen address (e.g., "127.0.0.1:8787").
	// Composed by MustLoad as HTTPHost + ":" + PORT.
	HTTPAddr string

	// AppEnv is the runtime mode ("development", "production", ...).
	// Consumed by internal/api/router.go to toggle gin.SetMode.
	AppEnv string

	// DatabasePath is the SQLite file path. Passed to store.Open.
	DatabasePath string

	// APIKeysFile is the API keys file (internal/auth). Defaults to
	// "api-keys" beside the database file.
	APIKeysFile string

	// APIKey is one extra key from BISHOP_API_KEY, named "env".
	APIKey string

	// AllowNoAuth (BISHOP_ALLOW_NO_AUTH=1) lets memoryd start on a
	// non-loopback address with no API key configured. Off by default.
	AllowNoAuth bool

	// TLSCertFile and TLSKeyFile, both set, make memoryd serve HTTPS.
	TLSCertFile string
	TLSKeyFile  string

	// LogLevel is the verbosity for structured logging. The service
	// does not yet filter on this — it is plumbed through so Phase 3
	// can wire slog without a config-shape change.
	LogLevel string
}

// MustLoad reads runtime configuration from environment variables,
// optionally sourcing from a .env file in the working directory. A
// missing or unreadable .env file is not fatal — values fall back to
// the documented defaults.
//
// MustLoad currently has no fatal error path; the name is preserved
// from the Phase 1 stub so callers (cmd/memoryd/main.go) can treat
// boot failure as fatal in the future without an API change.
//
// Defaults:
//
//	HTTP_HOST    -> "127.0.0.1" (loopback-only; see Config.HTTPHost)
//	PORT         -> "8787"   (matches Makefile `health` target)
//	DB_PATH      -> "data/memory.db" (matches Makefile `DB` variable)
//	APP_ENV      -> "development"
//	LOG_LEVEL    -> "info"
//	BISHOP_API_KEYS_FILE -> "<dir of DB_PATH>/api-keys"
//	BISHOP_API_KEY, BISHOP_ALLOW_NO_AUTH, TLS_CERT_FILE, TLS_KEY_FILE -> unset
func MustLoad() Config {
	// godotenv.Load returns nil when .env is absent (CI / production
	// deployments where env vars are injected directly) and a non-nil
	// error only when .env exists but cannot be parsed. We treat both
	// as non-fatal at this phase and fall back to env vars / defaults.
	// MEMORYD_ENV_FILE names the settings file (the server installer writes
	// one on macOS); without it, .env in the working directory as before.
	// Variables already in the environment win over the file.
	_ = godotenv.Load(envOrDefault("MEMORYD_ENV_FILE", ".env"))

	host := envOrDefault("HTTP_HOST", "127.0.0.1")
	dbPath := envOrDefault("DB_PATH", "data/memory.db")

	return Config{
		HTTPHost:     host,
		HTTPAddr:     net.JoinHostPort(host, envOrDefault("PORT", "8787")),
		AppEnv:       envOrDefault("APP_ENV", "development"),
		DatabasePath: dbPath,
		APIKeysFile:  envOrDefault("BISHOP_API_KEYS_FILE", filepath.Join(filepath.Dir(dbPath), "api-keys")),
		APIKey:       os.Getenv("BISHOP_API_KEY"),
		AllowNoAuth:  os.Getenv("BISHOP_ALLOW_NO_AUTH") == "1",
		TLSCertFile:  os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:   os.Getenv("TLS_KEY_FILE"),
		LogLevel:     envOrDefault("LOG_LEVEL", "info"),
	}
}

// IsLoopbackHost reports whether host — the value composed into
// Config.HTTPAddr — is loopback-only: "127.0.0.1", "::1", or
// "localhost" (net.Listen resolves "localhost" via the system resolver,
// which on every supported platform maps it to a loopback address, but
// we treat the literal string as loopback directly rather than
// resolving it here, since resolution can hit the network and this
// check must stay a pure, offline function). An empty host string is
// deliberately NOT loopback: net.Listen treats "" as "all interfaces",
// the exact opposite of loopback-only, so an empty HTTP_HOST must warn,
// not pass silently.
func IsLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// envOrDefault returns the value of name, or fallback when unset or empty.
// An empty-string env var (e.g. LOG_LEVEL=) is treated as unset so callers
// can blank a default without an empty string propagating.
func envOrDefault(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		return value
	}
	return fallback
}
