package main

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// clientEnvPath is the per-user client settings file written by
// `scripts/install.sh client` (docs/plans/NETWORK-DEPLOYMENT-PLAN.md §5.3):
// BISHOP_MEMORY_CLIENT_ENV, else ~/.config/bishop-memory/client.env.
func clientEnvPath() string {
	if p := os.Getenv("BISHOP_MEMORY_CLIENT_ENV"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "bishop-memory", "client.env")
}

// loadClientEnv sets each KEY=value from the client settings file that the
// environment does not already set, so an explicit env block (a harness's
// .mcp.json) always wins. A missing file is fine.
func loadClientEnv() {
	path := clientEnvPath()
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.Trim(strings.TrimSpace(value), `"'`)
		if _, set := os.LookupEnv(key); !set {
			_ = os.Setenv(key, value)
		}
	}
}

// apiKeyTransport adds the bishop-memory API key to every request.
type apiKeyTransport struct {
	key  string
	base http.RoundTripper
}

func (t apiKeyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.key)
	return t.base.RoundTrip(r)
}

// transport is http.DefaultTransport, wrapped to send BISHOP_MEMORY_API_KEY
// when one is set.
func transport() http.RoundTripper {
	if key := strings.TrimSpace(os.Getenv("BISHOP_MEMORY_API_KEY")); key != "" {
		return apiKeyTransport{key: key, base: http.DefaultTransport}
	}
	return http.DefaultTransport
}
