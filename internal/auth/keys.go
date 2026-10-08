// Package auth holds memoryd's API keys (docs/plans/NETWORK-DEPLOYMENT-PLAN.md §3).
//
// Keys live in a plain text file, one per line:
//
//	<name> <hex sha256 of the key>
//
// Blank lines and lines starting with "#" are ignored. memoryd never stores a
// key in clear: `memoryd keys add <name>` generates one, appends its hash and
// prints the key once. The file is re-read whenever its modification time or
// size changes, so adding or revoking a key needs no restart.
//
// One extra key may come from the environment (BISHOP_API_KEY), for a quick
// single-key setup; it is named "env".
package auth

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// KeyPrefix starts every generated key, so a key is recognisable in a config
// file or a leaked log line.
const KeyPrefix = "bm_"

// validName is what a key name may contain; it becomes a log field.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// entry is one key: its name and the SHA-256 of the key.
type entry struct {
	name string
	hash [sha256.Size]byte
}

// Store answers whether a presented key is valid, and which key it is.
// The zero value is not usable; call NewStore.
type Store struct {
	path   string
	envKey *entry

	mu      sync.Mutex
	entries []entry
	modTime time.Time
	size    int64
	loaded  bool
}

// NewStore returns a store over the keys file at path (which need not exist
// yet) plus envKey when it is non-empty. It reads the file once to surface a
// malformed file at start-up.
func NewStore(path, envKey string) (*Store, error) {
	s := &Store{path: path}
	if envKey = strings.TrimSpace(envKey); envKey != "" {
		s.envKey = &entry{name: "env", hash: sha256.Sum256([]byte(envKey))}
	}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path is the keys file this store reads.
func (s *Store) Path() string { return s.path }

// Empty reports whether no key is configured at all, in the file or the
// environment. An empty store means authentication is off.
func (s *Store) Empty() bool {
	if s.envKey != nil {
		return false
	}
	_ = s.reload()
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries) == 0
}

// Verify returns the name of the key presented and true, or "" and false.
// Every configured key is compared in constant time.
func (s *Store) Verify(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	_ = s.reload()
	sum := sha256.Sum256([]byte(key))

	s.mu.Lock()
	candidates := append([]entry(nil), s.entries...)
	s.mu.Unlock()
	if s.envKey != nil {
		candidates = append(candidates, *s.envKey)
	}

	name, ok := "", false
	for _, e := range candidates {
		if subtle.ConstantTimeCompare(sum[:], e.hash[:]) == 1 && !ok {
			name, ok = e.name, true
		}
	}
	return name, ok
}

// Names lists the key names in the file, in file order.
func (s *Store) Names() ([]string, error) {
	if err := s.reload(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.entries))
	for _, e := range s.entries {
		names = append(names, e.name)
	}
	return names, nil
}

// reload re-reads the keys file when it has changed since the last read. A
// missing file is an empty key list, not an error.
func (s *Store) reload() error {
	info, err := os.Stat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		s.mu.Lock()
		s.entries, s.loaded, s.modTime, s.size = nil, true, time.Time{}, 0
		s.mu.Unlock()
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat keys file %s: %w", s.path, err)
	}

	s.mu.Lock()
	unchanged := s.loaded && info.ModTime().Equal(s.modTime) && info.Size() == s.size
	s.mu.Unlock()
	if unchanged {
		return nil
	}

	entries, err := readKeysFile(s.path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.entries, s.loaded, s.modTime, s.size = entries, true, info.ModTime(), info.Size()
	s.mu.Unlock()
	return nil
}

// readKeysFile parses the keys file.
func readKeysFile(path string) ([]entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open keys file %s: %w", path, err)
	}
	defer f.Close()

	var entries []entry
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 2 || !validName.MatchString(fields[0]) {
			return nil, fmt.Errorf("keys file %s line %d: want \"<name> <sha256>\"", path, line)
		}
		raw, err := hex.DecodeString(fields[1])
		if err != nil || len(raw) != sha256.Size {
			return nil, fmt.Errorf("keys file %s line %d: the hash is not a hex SHA-256", path, line)
		}
		var e entry
		e.name = fields[0]
		copy(e.hash[:], raw)
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}

// Add generates a key named name, appends its hash to the keys file at path
// (creating it, mode 0600, and its directory), and returns the key. The name
// must be new.
func Add(path, name string) (string, error) {
	if !validName.MatchString(name) {
		return "", fmt.Errorf("key name %q: use letters, digits, '.', '_' or '-', up to 64 characters", name)
	}
	existing, err := readIfExists(path)
	if err != nil {
		return "", err
	}
	for _, e := range existing {
		if e.name == name {
			return "", fmt.Errorf("a key named %q already exists; revoke it first", name)
		}
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	key := KeyPrefix + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(key))

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create keys directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("open keys file %s: %w", path, err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s %s\n", name, hex.EncodeToString(sum[:])); err != nil {
		return "", fmt.Errorf("write keys file %s: %w", path, err)
	}
	return key, f.Close()
}

// Revoke removes the key named name from the keys file at path.
func Revoke(path, name string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read keys file %s: %w", path, err)
	}
	var kept []string
	found := false
	for _, line := range strings.Split(strings.TrimRight(string(content), "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == name {
			found = true
			continue
		}
		kept = append(kept, line)
	}
	if !found {
		return fmt.Errorf("no key named %q", name)
	}
	out := strings.Join(kept, "\n")
	if out != "" {
		out += "\n"
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o600); err != nil {
		return fmt.Errorf("write keys file: %w", err)
	}
	return os.Rename(tmp, path)
}

func readIfExists(path string) ([]entry, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return readKeysFile(path)
}
