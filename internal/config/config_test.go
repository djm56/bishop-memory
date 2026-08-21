package config

import "testing"

// TestIsLoopbackHost is a table-driven unit test for the Job 2
// (task-20260821-02 step 6) loopback-only default: bishop-memory has no
// authentication, so the startup warning in cmd/memoryd/main.go depends
// on this function correctly distinguishing loopback from "reachable
// from elsewhere."
func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		name string
		host string
		want bool
	}{
		{"IPv4 loopback", "127.0.0.1", true},
		{"IPv4 loopback, non-canonical", "127.0.0.2", true},
		{"IPv6 loopback", "::1", true},
		{"localhost literal", "localhost", true},
		{"all interfaces (empty string)", "", false},
		{"all interfaces (0.0.0.0)", "0.0.0.0", false},
		{"IPv6 all interfaces", "::", false},
		{"LAN address", "192.168.1.10", false},
		{"public address", "203.0.113.5", false},
		{"garbage / unparseable", "not-an-ip", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsLoopbackHost(tc.host); got != tc.want {
				t.Errorf("IsLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

// TestMustLoad_DefaultsToLoopback confirms MustLoad's HTTP_HOST default
// is loopback-only when the env var is unset, matching every
// "locally-bound" claim in README.md and docs/api-contract.md.
func TestMustLoad_DefaultsToLoopback(t *testing.T) {
	t.Setenv("HTTP_HOST", "")
	// envOrDefault treats an unset OR empty-string var as "use the
	// default" (see its own docblock) — t.Setenv("HTTP_HOST", "") and
	// leaving it entirely unset are equivalent for this test's purpose,
	// and t.Setenv guarantees automatic cleanup after the test, unlike
	// os.Unsetenv.

	cfg := MustLoad()

	if cfg.HTTPHost != "127.0.0.1" {
		t.Errorf("HTTPHost = %q, want %q", cfg.HTTPHost, "127.0.0.1")
	}
	if !IsLoopbackHost(cfg.HTTPHost) {
		t.Errorf("default HTTPHost %q is not loopback", cfg.HTTPHost)
	}
	if cfg.HTTPAddr != "127.0.0.1:8787" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, "127.0.0.1:8787")
	}
}

// TestMustLoad_HonoursHTTPHostOverride confirms an operator can still
// bind elsewhere by explicit configuration (Job 2's second requirement)
// — the loopback default must not become a hard-coded floor.
func TestMustLoad_HonoursHTTPHostOverride(t *testing.T) {
	t.Setenv("HTTP_HOST", "0.0.0.0")
	t.Setenv("PORT", "9999")

	cfg := MustLoad()

	if cfg.HTTPHost != "0.0.0.0" {
		t.Errorf("HTTPHost = %q, want %q", cfg.HTTPHost, "0.0.0.0")
	}
	if cfg.HTTPAddr != "0.0.0.0:9999" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, "0.0.0.0:9999")
	}
	if IsLoopbackHost(cfg.HTTPHost) {
		t.Errorf("0.0.0.0 must not be reported as loopback")
	}
}
