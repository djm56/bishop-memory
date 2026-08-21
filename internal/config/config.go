// Package config loads runtime configuration for bishop-memory from
// environment variables (with .env support via godotenv). The values
// are consumed by cmd/memoryd/main.go to wire HTTP listening, the
// SQLite database path, runtime mode, and log verbosity.
package config

import (
	"net"
	"os"

	"github.com/joho/godotenv"
)

// Config holds the runtime configuration for the bishop-memory service.
//
// Fields are exported so callers (cmd/memoryd/main.go,
// internal/api/router.go) can read them directly; MustLoad is the only
// constructor and is the documented entry point.
type Config struct {
	// HTTPHost is the interface bishop-memory's HTTP listener binds
	// to. Defaults to loopback-only ("127.0.0.1") because this service
	// has NO authentication of any kind: every task/event endpoint is
	// open read/write to any caller that can reach it, and
	// POST /v1/documents/sync accepts a caller-supplied filesystem
	// root. README.md and docs/api-contract.md both describe
	// bishop-memory as a locally-bound service reached over an SSH
	// tunnel or by a co-located process — loopback-only is what makes
	// that description true rather than aspirational. Overriding this
	// (HTTP_HOST=0.0.0.0, a LAN address, ...) is a deliberate,
	// operator-chosen exposure; cmd/memoryd/main.go logs a startup
	// warning naming exactly what becomes reachable when it is not
	// loopback (see IsLoopbackHost).
	HTTPHost string

	// HTTPAddr is the gin-form listen address (e.g., "127.0.0.1:8787").
	// Composed by MustLoad as HTTPHost + ":" + PORT.
	HTTPAddr string

	// AppEnv is the runtime mode ("development", "production", ...).
	// Consumed by internal/api/router.go to toggle gin.SetMode.
	AppEnv string

	// DatabasePath is the SQLite file path. Passed to store.Open.
	DatabasePath string

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
func MustLoad() Config {
	// godotenv.Load returns nil when .env is absent (CI / production
	// deployments where env vars are injected directly) and a non-nil
	// error only when .env exists but cannot be parsed. We treat both
	// as non-fatal at this phase and fall back to env vars / defaults.
	_ = godotenv.Load(".env")

	host := envOrDefault("HTTP_HOST", "127.0.0.1")

	return Config{
		HTTPHost:     host,
		HTTPAddr:     host + ":" + envOrDefault("PORT", "8787"),
		AppEnv:       envOrDefault("APP_ENV", "development"),
		DatabasePath: envOrDefault("DB_PATH", "data/memory.db"),
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
