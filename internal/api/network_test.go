package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"bishop-memory/internal/auth"
	"bishop-memory/internal/config"
)

// TestAPIKeyGuard checks which requests the key guard lets through: open
// with no keys on loopback, keys required once one exists, always required
// off loopback even with none, and the pages and /healthz never guarded.
func TestAPIKeyGuard(t *testing.T) {
	_, db := newTriageTestRouter(t)
	path := filepath.Join(t.TempDir(), "api-keys")
	keys, err := auth.NewStore(path, "")
	if err != nil {
		t.Fatal(err)
	}
	get := func(router http.Handler, url, key string) int {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	loopback := NewRouterWithKeys(config.Config{AppEnv: "test", HTTPHost: "127.0.0.1"}, db, keys)
	network := NewRouterWithKeys(config.Config{AppEnv: "test", HTTPHost: "0.0.0.0"}, db, keys)
	open := NewRouterWithKeys(config.Config{AppEnv: "test", HTTPHost: "0.0.0.0", AllowNoAuth: true}, db, keys)

	if code := get(loopback, "/v1/missions", ""); code != http.StatusOK {
		t.Errorf("loopback, no keys: %d, want 200", code)
	}
	if code := get(network, "/v1/missions", ""); code != http.StatusUnauthorized {
		t.Errorf("network, no keys: %d, want 401", code)
	}
	if code := get(open, "/v1/missions", ""); code != http.StatusOK {
		t.Errorf("network, no keys, BISHOP_ALLOW_NO_AUTH: %d, want 200", code)
	}

	key, err := auth.Add(path, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	for _, router := range []http.Handler{loopback, network} {
		if code := get(router, "/v1/missions", ""); code != http.StatusUnauthorized {
			t.Errorf("keys exist, none sent: %d, want 401", code)
		}
		if code := get(router, "/v1/missions", "bm_wrong"); code != http.StatusUnauthorized {
			t.Errorf("wrong key: %d, want 401", code)
		}
		if code := get(router, "/v1/missions", key); code != http.StatusOK {
			t.Errorf("right key: %d, want 200", code)
		}
		for _, page := range []string{"/healthz", "/triage", "/missions", "/favicon.svg"} {
			if code := get(router, page, ""); code != http.StatusOK {
				t.Errorf("%s without a key: %d, want 200", page, code)
			}
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/missions", nil)
	req.Header.Set("X-API-Key", key)
	rec := httptest.NewRecorder()
	network.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("X-API-Key: %d, want 200", rec.Code)
	}
}

// TestDocumentPush checks push, hashes and delete: a pushed brief is filed
// under its mission and harness, an agent becomes crew, bad paths and
// non-Markdown are refused, and delete removes the document and its search
// row.
func TestDocumentPush(t *testing.T) {
	router, db := newTriageTestRouter(t)
	post := func(url string, body any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	brief := "/home/me/h/.claude/memory/missions/m-1/BRIEF.md"
	code, out := post("/v1/documents/push", map[string]any{
		"harness": "kirsch",
		"files":   []map[string]string{{"path": brief, "rel_path": "missions/m-1/BRIEF.md", "content": "# Brief\n\nZebra goal\n"}},
		"agents":  []map[string]string{{"path": "/home/me/h/.claude/agents/hicks.md", "content": "---\ndescription: Junior implementer (hicks). Builds.\n---\n"}},
	})
	if code != http.StatusOK || out["files"] != float64(1) || out["agents"] != float64(1) {
		t.Fatalf("push: %d %v", code, out)
	}
	var kind, mission, harness, role string
	if err := db.QueryRow(`SELECT kind, mission_id, harness FROM documents WHERE source_path = ?`, brief).Scan(&kind, &mission, &harness); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if kind != "brief" || mission != "m-1" || harness != "kirsch" {
		t.Errorf("pushed brief = (%s, %s, %s)", kind, mission, harness)
	}
	if err := db.QueryRow(`SELECT role FROM crew WHERE name = 'hicks'`).Scan(&role); err != nil || role != "Junior implementer" {
		t.Errorf("crew hicks = %q, %v", role, err)
	}

	for name, file := range map[string]map[string]string{
		"parent segment": {"path": "/x/a.md", "rel_path": "../a.md", "content": "x"},
		"hidden segment": {"path": "/x/a.md", "rel_path": ".git/a.md", "content": "x"},
		"absolute":       {"path": "/x/a.md", "rel_path": "/a.md", "content": "x"},
		"not markdown":   {"path": "/x/a.txt", "rel_path": "a.txt", "content": "x"},
	} {
		if code, _ := post("/v1/documents/push", map[string]any{"harness": "kirsch", "files": []map[string]string{file}}); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}

	_, hashes := do(t, router, http.MethodGet, "/v1/documents/hashes?harness=kirsch", nil)
	if h := hashes["hashes"].(map[string]any); len(h) != 1 || h[brief] == nil {
		t.Errorf("hashes = %v", h)
	}

	if code, out := post("/v1/documents/delete", map[string]any{"paths": []string{brief}}); code != http.StatusOK || out["deleted"] != float64(1) {
		t.Fatalf("delete: %d %v", code, out)
	}
	var n int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM documents) + (SELECT COUNT(*) FROM documents_fts)`).Scan(&n); err != nil || n != 0 {
		t.Errorf("after delete: %d rows, %v", n, err)
	}
}
