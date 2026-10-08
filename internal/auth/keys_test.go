package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAddVerifyRevoke covers a key's life: added, verified by name, other
// keys refused, the file mode, and revoked without a restart.
func TestAddVerifyRevoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "api-keys")
	store, err := NewStore(path, "")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if !store.Empty() {
		t.Fatal("a missing keys file should be an empty store")
	}

	key, err := Add(path, "laptop")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !strings.HasPrefix(key, KeyPrefix) || len(key) < 40 {
		t.Fatalf("key %q does not look generated", key)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("keys file mode %v, want 0600", info.Mode().Perm())
	}
	if content, _ := os.ReadFile(path); strings.Contains(string(content), key) {
		t.Fatal("the keys file holds the key in clear")
	}
	if name, ok := store.Verify(key); !ok || name != "laptop" {
		t.Fatalf("Verify(new key) = %q, %v", name, ok)
	}
	for _, bad := range []string{"", "bm_wrong", key + "x"} {
		if _, ok := store.Verify(bad); ok {
			t.Errorf("Verify(%q) passed", bad)
		}
	}
	if _, err := Add(path, "laptop"); err == nil {
		t.Error("a duplicate name was accepted")
	}
	if _, err := Add(path, "bad name"); err == nil {
		t.Error("a name with a space was accepted")
	}

	// Revoke rewrites the file; make its mtime differ even on coarse clocks.
	time.Sleep(10 * time.Millisecond)
	if err := Revoke(path, "laptop"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := store.Verify(key); ok {
		t.Fatal("a revoked key still verifies")
	}
	if err := Revoke(path, "laptop"); err == nil {
		t.Error("revoking a missing key succeeded")
	}
}

// TestEnvKeyAndMalformedFile covers BISHOP_API_KEY and a broken file.
func TestEnvKeyAndMalformedFile(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "none"), "  secret  ")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if store.Empty() {
		t.Fatal("an env key should make the store non-empty")
	}
	if name, ok := store.Verify("secret"); !ok || name != "env" {
		t.Fatalf("Verify(env key) = %q, %v", name, ok)
	}

	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("laptop not-a-hash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(bad, ""); err == nil {
		t.Fatal("a malformed keys file was accepted")
	}
}
