// Package api — tests for searchMemoryHandler.
//
// TestSearchMemoryHandler_FTSSyntaxErrorDoesNotEchoQuery is the
// regression test for task-20260821-02 step 7a's CRITICAL 1 fix: it
// reproduces, against a REAL FTS5 table via the production
// modernc.org/sqlite driver (not a hand-built staticErr literal), the
// exact leak the reviewer found empirically — a malformed FTS5 query
// causes the driver's own Error() text to embed a fragment of the
// caller's query (e.g. `fts5: syntax error near "OR"`). Before the
// step 7a fix, internal/api/search.go wrapped that raw driver error in
// fmt.Errorf and passed it straight to validationError, which — after
// step 6 correctly stopped collapsing hand-authored messages into one
// generic string — let the driver's query fragment reach the HTTP
// response verbatim (CONV-033). This test asserts the response details
// is now the fixed, static "invalid search query syntax" string with
// no trace of the submitted query's tokens.
package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"

	"bishop-memory/internal/store"
)

// openSearchTestDB opens a fresh temp-file SQLite database, applies the
// production db/schema.sql (via store.ApplySchema, the same production
// bootstrap path internal/importer/importer_test.go's openTestDB uses),
// and returns it. The database is empty (no rows), which is all this
// test needs: an FTS5 syntax error occurs during MATCH parsing, before
// any row would be scanned.
func openSearchTestDB(t *testing.T) *sql.DB {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite",
		"file:"+dbPath+
			"?_pragma=busy_timeout(5000)"+
			"&_pragma=foreign_keys(1)"+
			"&_pragma=journal_mode(WAL)",
	)
	if err != nil {
		t.Fatalf("open test sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	schemaPath := filepath.Join("..", "..", "db", "schema.sql")
	if err := store.ApplySchema(db, schemaPath); err != nil {
		t.Fatalf("apply schema %s: %v", schemaPath, err)
	}
	return db
}

// TestSearchMemoryHandler_FTSSyntaxErrorDoesNotEchoQuery is described
// in the package docblock above.
func TestSearchMemoryHandler_FTSSyntaxErrorDoesNotEchoQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := openSearchTestDB(t)

	router := gin.New()
	router.GET("/v1/memory/search", searchMemoryHandler(db))

	// Reproduces the reviewer's disposable-program probe: "hello AND OR"
	// is syntactically invalid FTS5 (a dangling boolean operator), and
	// the real modernc.org/sqlite driver's error text names the specific
	// offending token — "OR" in this case.
	cases := []struct {
		name  string
		query string
	}{
		{"dangling OR", "hello AND OR"},
		{"dangling AND chain", "AND AND AND"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := "/v1/memory/search?q=" + url.QueryEscape(tc.query)
			req := httptest.NewRequest(http.MethodGet, target, nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}

			var decoded map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("response body is not valid JSON: %v (raw: %s)", err, rec.Body.String())
			}

			details, ok := decoded["details"].(string)
			if !ok {
				t.Fatalf(`decoded["details"] = %#v, want a string`, decoded["details"])
			}

			const want = "invalid search query syntax"
			if details != want {
				t.Fatalf("details = %q, want %q", details, want)
			}

			// The empirical part of the probe: confirm none of the raw
			// driver vocabulary that caused this specific failure (the
			// query's own operators, or the driver's "fts5:"/"syntax
			// error" prefix) survives into the response. This is what
			// would have failed against the pre-fix code, which
			// produced e.g. `invalid search query: fts5: syntax error
			// near "OR"` for the first case.
			lower := strings.ToLower(details)
			for _, leaked := range []string{"fts5", "syntax error", "near"} {
				if strings.Contains(lower, leaked) {
					t.Fatalf("details %q leaks driver vocabulary %q", details, leaked)
				}
			}
		})
	}
}

// TestSearchMemoryHandler_MissingQueryVsSyntaxError is the CONV-048
// distinguishability check the step 7a brief requires alongside the
// leak fix: "q missing" and "q syntactically invalid" MUST remain two
// different, tellable-apart outcomes, not collapsed back into one
// generic message while closing the echo.
func TestSearchMemoryHandler_MissingQueryVsSyntaxError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := openSearchTestDB(t)

	router := gin.New()
	router.GET("/v1/memory/search", searchMemoryHandler(db))

	doRequest := func(t *testing.T, target string) string {
		t.Helper()

		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
		}

		var decoded map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("response body is not valid JSON: %v (raw: %s)", err, rec.Body.String())
		}

		details, ok := decoded["details"].(string)
		if !ok {
			t.Fatalf(`decoded["details"] = %#v, want a string`, decoded["details"])
		}
		return details
	}

	missing := doRequest(t, "/v1/memory/search")
	syntaxErr := doRequest(t, "/v1/memory/search?q="+url.QueryEscape("AND AND AND"))

	if missing != "q is required" {
		t.Fatalf("missing-q details = %q, want %q", missing, "q is required")
	}
	if syntaxErr != "invalid search query syntax" {
		t.Fatalf("syntax-error details = %q, want %q", syntaxErr, "invalid search query syntax")
	}
	if missing == syntaxErr {
		t.Fatalf("missing-q and syntax-error produced the same details %q; the two cases must remain distinguishable (CONV-048)", missing)
	}
}
