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
//
// TestSearchMemoryHandler_HyphenatedQueriesReturnResults and
// TestSearchMemoryHandler_HostileInputsNeverReturn500 are
// task-20260821-03's regression tests for the hyphen defect described
// in search.go's package docblock: every hyphenated identifier the
// task's bug report reproduced as an HTTP 500 (step-sync,
// task-20260821-01, EVENT-LOG) must now return 200, and no input —
// adversarial or otherwise — may reach a 500. TestSanitizeFTS5Query is
// the accompanying pure-function proof that the rewrite these two
// integration tests exercise end-to-end is a byte-for-byte no-op for
// every input shape required not to regress.
//
// Step 2a adds four groups of tests for the CRITICAL and two WARNINGs
// a review found in the above (task-20260821-03 step 2a):
//
//   - TestIsCallerQueryError_SchemaFaultIsNotCallerFault and
//     TestSearchMemoryHandler_MissingTableIsServerFault are the
//     CRITICAL fix's regression tests: a missing/mismatched
//     documents_fts table must classify as a server fault (500), not
//     a caller fault (400), while a genuine caller MATCH-syntax fault
//     (a dangling boolean operator) must still classify as a caller
//     fault. Both are exercised against a real modernc.org/sqlite
//     driver and a real (or deliberately absent/mismatched) FTS5
//     index — never a synthetic error literal.
//   - TestSanitizeFTS5Query's new "embedded quote" cases and
//     TestSearchMemoryHandler_EmbeddedQuoteWordMatchesLiteralPhraseOnly
//     are WARNING 1's fix: a literal `"` embedded in one
//     whitespace-delimited word (no surrounding whitespace) must
//     become one escaped literal phrase, not several implicit-AND
//     fragments — confirmed against a real index by precision, not
//     merely by absence of a driver error (a document containing the
//     three fragment-words scattered apart, but never the caller's
//     literal punctuated text, must NOT match).
//   - TestSanitizeFTS5Query's new "composed vs decomposed" cases are
//     WARNING 2's fix: a precomposed and an NFD-decomposed encoding of
//     the same visible word must now be classified identically by
//     isFTS5WordRune (both pass through as a bareword).
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

// seedDocument inserts one row into documents and its matching row
// into documents_fts (rowid = documents.id), mirroring the importer's
// same-transaction contract documented on documents_fts in
// db/schema.sql, so a search test exercises the exact join shape
// production traffic does rather than an empty index.
func seedDocument(t *testing.T, db *sql.DB, sourcePath, title, kind, body string) int64 {
	t.Helper()

	res, err := db.Exec(
		`INSERT INTO documents (source_path, title, kind, body, sha256) VALUES (?, ?, ?, ?, ?)`,
		sourcePath, title, kind, body, "test-sha",
	)
	if err != nil {
		t.Fatalf("insert documents row: %v", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("get inserted document id: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO documents_fts (rowid, title, body) VALUES (?, ?, ?)`,
		id, title, body,
	); err != nil {
		t.Fatalf("insert documents_fts row: %v", err)
	}

	return id
}

// TestSearchMemoryHandler_HyphenatedQueriesReturnResults is the direct
// regression test for task-20260821-03's reported defect: it
// reproduces, against a real FTS5 index built from db/schema.sql (not
// a synthetic error value), the exact three queries the task's bug
// report captured returning HTTP 500 with driver errors of the shape
// `no such column: <token after the hyphen>` — "step-sync" ("no such
// column: sync"), "task-20260821-01" ("no such column: 20260821"), and
// "EVENT-LOG" ("no such column: LOG"). Each must now return 200 with
// at least one hit, matching what the already-working quoted form
// `"step-sync"` returns today. It also re-asserts, against the same
// seeded corpus, that the three behaviours the task required not to
// regress — a single plain word, an implicit-AND space-separated
// query, and an explicit quoted phrase — still return 200 with hits.
func TestSearchMemoryHandler_HyphenatedQueriesReturnResults(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := openSearchTestDB(t)
	seedDocument(t, db,
		"/fake/EVENT-LOG.md", "Event Log", "state",
		"Row: task-20260821-01 | step-sync | @documentation-writer confirmed delegation and all three sync targets.",
	)

	router := gin.New()
	router.GET("/v1/memory/search", searchMemoryHandler(db))

	doSearch := func(t *testing.T, q string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()

		target := "/v1/memory/search?q=" + url.QueryEscape(q)
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		var decoded map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("response body is not valid JSON: %v (raw: %s)", err, rec.Body.String())
		}
		return rec, decoded
	}

	resultCount := func(t *testing.T, decoded map[string]any) int {
		t.Helper()

		results, ok := decoded["results"].([]any)
		if !ok {
			t.Fatalf(`decoded["results"] = %#v, want an array`, decoded["results"])
		}
		return len(results)
	}

	cases := []struct {
		name string
		q    string
	}{
		// The three literal reproduction cases from the bug report.
		{"hyphenated identifier: step-sync", "step-sync"},
		{"hyphenated task id: task-20260821-01", "task-20260821-01"},
		{"shouty hyphenated identifier: EVENT-LOG", "EVENT-LOG"},
		// Behaviours that must not regress.
		{"single plain word", "delegation"},
		{"space-separated implicit AND", "step sync"},
		{"already-quoted phrase", `"step-sync"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, decoded := doSearch(t, tc.q)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			if n := resultCount(t, decoded); n == 0 {
				t.Fatalf("q=%q returned 0 results, want at least 1", tc.q)
			}
		})
	}
}

// TestSearchMemoryHandler_HostileInputsNeverReturn500 is the
// requirement-3 acceptance test: every input in this table — the
// hyphen defect's own reproduction shapes plus a set of adversarial
// inputs (unbalanced quotes, a lone "*", a lone "-", a lone "NOT", an
// empty query, a 5000-byte token, non-Latin Unicode, and an attempted
// MATCH-argument injection) — must be executed against a real FTS5
// index without ever producing HTTP 500, and any 4xx it does produce
// must carry only the static, authored details string (never the
// modernc.org/sqlite driver's own Error() text, which embeds a
// fragment of the submitted query — CONV-033).
func TestSearchMemoryHandler_HostileInputsNeverReturn500(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := openSearchTestDB(t)
	router := gin.New()
	router.GET("/v1/memory/search", searchMemoryHandler(db))

	cases := []struct {
		name       string
		target     string
		wantStatus int
	}{
		{"unbalanced quote", "/v1/memory/search?q=" + url.QueryEscape(`unbalanced "quote`), http.StatusBadRequest},
		{"lone asterisk", "/v1/memory/search?q=" + url.QueryEscape("*"), http.StatusOK},
		{"lone hyphen", "/v1/memory/search?q=" + url.QueryEscape("-"), http.StatusOK},
		{"lone NOT", "/v1/memory/search?q=" + url.QueryEscape("NOT"), http.StatusBadRequest},
		{"very long string (5000 bytes)", "/v1/memory/search?q=" + url.QueryEscape(strings.Repeat("a", 5000)), http.StatusOK},
		{"unicode", "/v1/memory/search?q=" + url.QueryEscape("日本語 テスト"), http.StatusOK},
		{"MATCH-argument injection attempt", "/v1/memory/search?q=" + url.QueryEscape(`x' OR MATCH 'y`), http.StatusOK},
		{"missing q", "/v1/memory/search", http.StatusBadRequest},
		{"dangling boolean operator (pre-existing behaviour, unchanged)", "/v1/memory/search?q=" + url.QueryEscape("hello AND OR"), http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			var decoded map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("response body is not valid JSON: %v (raw: %s)", err, rec.Body.String())
			}

			if rec.Code < http.StatusBadRequest {
				return
			}

			details, ok := decoded["details"].(string)
			if !ok {
				t.Fatalf(`decoded["details"] = %#v, want a string`, decoded["details"])
			}

			lower := strings.ToLower(details)
			for _, leaked := range []string{"fts5", "syntax error", "sqlite", "no such column", "unterminated", "special query"} {
				if strings.Contains(lower, leaked) {
					t.Fatalf("details %q leaks driver vocabulary %q", details, leaked)
				}
			}
		})
	}
}

// TestSanitizeFTS5Query is a pure-function table test for the rewrite
// described in search.go's package docblock. It is the direct proof
// that the rewrite is a no-op — byte-for-byte identical output — for
// every input shape the task required not to regress (a plain word,
// space-separated words, an already-quoted phrase), and that it
// quotes exactly the punctuated-bareword shapes the hyphen defect
// reported, while leaving a pure-letter FTS5 keyword (which looks
// identical to an ordinary word at this layer) untouched.
func TestSanitizeFTS5Query(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"plain word is unchanged", "delegation", "delegation", true},
		{"space-separated words unchanged", "step sync", "step sync", true},
		{"already-quoted phrase unchanged", `"step-sync"`, `"step-sync"`, true},
		{"hyphenated bareword is quoted", "step-sync", `"step-sync"`, true},
		{"hyphenated task id is quoted", "task-20260821-01", `"task-20260821-01"`, true},
		{"shouty hyphenated identifier is quoted", "EVENT-LOG", `"EVENT-LOG"`, true},
		{"plain prefix wildcard is preserved", "wor*", "wor*", true},
		{"lone asterisk is quoted, not passed through", "*", `"*"`, true},
		{"lone hyphen is quoted", "-", `"-"`, true},
		{"pure-letter operator keyword is untouched", "NOT", "NOT", true},
		{"unbalanced quote is rejected", `unbalanced "quote`, "", false},
		{"unicode word runes pass through", "日本語 テスト", "日本語 テスト", true},

		// Step 2a — WARNING 1 fix: a '"' embedded inside one
		// whitespace-delimited word, with no whitespace on either
		// side of it, is not a phrase boundary the caller could have
		// intended. It is now escaped (doubled) and folded into that
		// one word's own literal phrase, rather than splitting the
		// word into several implicit-AND fragments. This holds
		// regardless of how many '"' the word contains — even two (as
		// the review's own example had) or one (odd, previously
		// rejected outright as "unbalanced" by the old scan, since
		// the lone embedded quote was misread as opening a phrase
		// that was never closed within this token).
		{"even embedded quotes become one escaped phrase", `abc"def"ghi`, `"abc""def""ghi"`, true},
		{"odd embedded quotes become one escaped phrase (previously rejected)", `abc"def`, `"abc""def"`, true},
		{"single embedded quote plus trailing star, all one phrase", `it"s*`, `"it""s*"`, true},
		{"a genuine leading quote at a token boundary still opens an explicit phrase", `"a" b"c`, `"a" "b""c"`, true},

		// Step 2a — WARNING 2 fix: a precomposed and an NFD-decomposed
		// encoding of the same visible word are now classified
		// identically by isFTS5WordRune (both are an unchanged
		// bareword) instead of the decomposed form being quoted, the
		// composed form not, purely because a bare combining accent
		// (category Mn) failed unicode.IsLetter on its own.
		{"precomposed café passes through unchanged", "caf\u00e9", "caf\u00e9", true},
		{"decomposed café (e + combining acute) now also passes through unchanged", "cafe\u0301", "cafe\u0301", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sanitizeFTS5Query(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got=%q)", ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Fatalf("sanitizeFTS5Query(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// openRawTestDB opens a fresh temp-file SQLite database WITHOUT applying
// db/schema.sql, so the caller controls exactly what (if anything) exists.
// Used only by the step 2a schema-fault tests below, which need to put the
// database into a state openSearchTestDB deliberately never produces.
func openRawTestDB(t *testing.T) *sql.DB {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "raw.db")

	db, err := sql.Open("sqlite",
		"file:"+dbPath+
			"?_pragma=busy_timeout(5000)"+
			"&_pragma=foreign_keys(1)"+
			"&_pragma=journal_mode(WAL)",
	)
	if err != nil {
		t.Fatalf("open raw test sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db
}

// searchQuerySQL is the exact statement searchMemoryHandler executes,
// duplicated here (rather than exported from search.go, which is out of
// this task's file scope) so these tests exercise the identical statement
// shape isCallerQueryError's docblock reasons about.
const searchQuerySQL = `
	SELECT d.id, d.source_path, d.title, d.kind,
	       snippet(documents_fts, 1, '<mark>', '</mark>', '…', 24) AS snippet,
	       rank
	FROM documents_fts
	JOIN documents d ON d.id = documents_fts.rowid
	WHERE documents_fts MATCH ?
	ORDER BY rank
	LIMIT 20
`

// TestIsCallerQueryError_SchemaFaultIsNotCallerFault is the direct,
// function-level regression test for the CRITICAL a review found in step
// 2's isCallerQueryError: a genuine schema/deployment fault (a missing
// documents_fts table, or one that exists but is not the FTS5 virtual
// table the query expects — e.g. after a botched manual ALTER TABLE)
// raises the identical *sqlite.Error.Code() == SQLITE_ERROR a genuine
// caller MATCH-syntax fault does, so code alone cannot tell them apart.
// Every case here reproduces a real error from the real driver — no
// *sqlite.Error is hand-constructed — and each is exercised through the
// exact statement shape searchMemoryHandler runs.
func TestIsCallerQueryError_SchemaFaultIsNotCallerFault(t *testing.T) {
	runQuery := func(t *testing.T, db *sql.DB, match string) error {
		t.Helper()
		rows, err := db.Query(searchQuerySQL, match)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
		}
		return rows.Err()
	}

	t.Run("missing documents_fts table is not a caller fault", func(t *testing.T) {
		db := openRawTestDB(t) // no schema applied at all
		err := runQuery(t, db, "anything")
		if err == nil {
			t.Fatalf("expected an error querying a database with no schema, got none")
		}
		if isCallerQueryError(err) {
			t.Fatalf("isCallerQueryError(%v) = true, want false — a missing table is a server fault, not a caller fault", err)
		}
	})

	t.Run("documents_fts existing as a plain (non-FTS5) table is not a caller fault", func(t *testing.T) {
		db := openRawTestDB(t)
		if _, err := db.Exec(`CREATE TABLE documents (id INTEGER PRIMARY KEY, source_path TEXT, title TEXT, kind TEXT, body TEXT, sha256 TEXT)`); err != nil {
			t.Fatalf("create documents: %v", err)
		}
		// A plain table, not "CREATE VIRTUAL TABLE ... USING fts5(...)" —
		// simulates schema drift (e.g. a migration that didn't run, or an
		// admin action that replaced the FTS5 table with an ordinary one).
		if _, err := db.Exec(`CREATE TABLE documents_fts (title TEXT, body TEXT)`); err != nil {
			t.Fatalf("create plain documents_fts: %v", err)
		}
		err := runQuery(t, db, "anything")
		if err == nil {
			t.Fatalf("expected an error querying a MATCH clause against a non-FTS5 table, got none")
		}
		if isCallerQueryError(err) {
			t.Fatalf("isCallerQueryError(%v) = true, want false — a schema-mismatched table is a server fault, not a caller fault", err)
		}
	})

	t.Run("a genuine caller MATCH-syntax fault is still a caller fault", func(t *testing.T) {
		db := openSearchTestDB(t) // real, correctly-shaped FTS5 index
		// A dangling boolean operator: the one caller-fault shape that can
		// still reach the database after sanitizeFTS5Query (every
		// punctuation-based shape is quoted or rejected before this point).
		err := runQuery(t, db, "hello AND OR")
		if err == nil {
			t.Fatalf("expected a dangling-operator FTS5 syntax error, got none")
		}
		if !isCallerQueryError(err) {
			t.Fatalf("isCallerQueryError(%v) = false, want true — a dangling boolean operator is a caller fault", err)
		}
	})
}

// TestSearchMemoryHandler_MissingTableIsServerFault is the end-to-end
// acceptance test for the CRITICAL fix: hitting the real HTTP handler
// against a database with no documents_fts table (or a mismatched one)
// must return 500 "internal server error", never the 400 "invalid search
// query syntax" this defect previously returned. internalError's own
// contract (see errors.go) is that it never includes driver text in the
// response body, so this also re-confirms CONV-033 holds on this path.
func TestSearchMemoryHandler_MissingTableIsServerFault(t *testing.T) {
	gin.SetMode(gin.TestMode)

	doRequest := func(t *testing.T, db *sql.DB) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()

		router := gin.New()
		router.GET("/v1/memory/search", searchMemoryHandler(db))

		target := "/v1/memory/search?q=" + url.QueryEscape("step-sync")
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		var decoded map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("response body is not valid JSON: %v (raw: %s)", err, rec.Body.String())
		}
		return rec, decoded
	}

	assertServerFault := func(t *testing.T, rec *httptest.ResponseRecorder, decoded map[string]any) {
		t.Helper()

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusInternalServerError, rec.Body.String())
		}
		errVal, _ := decoded["error"].(string)
		if errVal != "internal server error" {
			t.Fatalf(`decoded["error"] = %q, want %q`, errVal, "internal server error")
		}
		if _, hasDetails := decoded["details"]; hasDetails {
			t.Fatalf("response leaked a details field on a 500: %#v", decoded)
		}
		// Belt-and-braces CONV-033 check on the raw body too.
		raw := rec.Body.String()
		lower := strings.ToLower(raw)
		for _, leaked := range []string{"sqlite", "no such table", "no such column", "documents_fts"} {
			if strings.Contains(lower, leaked) {
				t.Fatalf("response body %q leaks driver/schema vocabulary %q", raw, leaked)
			}
		}
	}

	t.Run("missing documents_fts table", func(t *testing.T) {
		db := openRawTestDB(t)
		rec, decoded := doRequest(t, db)
		assertServerFault(t, rec, decoded)
	})

	t.Run("documents_fts exists but is not an FTS5 table", func(t *testing.T) {
		db := openRawTestDB(t)
		if _, err := db.Exec(`CREATE TABLE documents (id INTEGER PRIMARY KEY, source_path TEXT, title TEXT, kind TEXT, body TEXT, sha256 TEXT)`); err != nil {
			t.Fatalf("create documents: %v", err)
		}
		if _, err := db.Exec(`CREATE TABLE documents_fts (title TEXT, body TEXT)`); err != nil {
			t.Fatalf("create plain documents_fts: %v", err)
		}
		rec, decoded := doRequest(t, db)
		assertServerFault(t, rec, decoded)
	})
}

// TestSearchMemoryHandler_EmbeddedQuoteWordMatchesLiteralPhraseOnly is the
// end-to-end precision test for WARNING 1's fix. It is not enough to show
// the embedded-quote query merely avoids a 500 or a syntax error — the OLD
// (pre-step-2a) behaviour also avoided both, while silently returning the
// WRONG result set. This test seeds two documents: one containing the
// caller's literal punctuated text as one adjacent phrase, and one
// containing the same three fragment-words scattered far apart with no
// punctuation at all. Only the first must match.
func TestSearchMemoryHandler_EmbeddedQuoteWordMatchesLiteralPhraseOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := openSearchTestDB(t)

	seedDocument(t, db,
		"/fake/literal.md", "Literal", "reference",
		`literal abc"def"ghi here`,
	)
	seedDocument(t, db,
		"/fake/scattered.md", "Scattered", "reference",
		"abc appears here, much later ghi shows up, and somewhere else def too",
	)

	router := gin.New()
	router.GET("/v1/memory/search", searchMemoryHandler(db))

	target := "/v1/memory/search?q=" + url.QueryEscape(`abc"def"ghi`)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("response body is not valid JSON: %v (raw: %s)", err, rec.Body.String())
	}
	results, ok := decoded["results"].([]any)
	if !ok {
		t.Fatalf(`decoded["results"] = %#v, want an array`, decoded["results"])
	}
	if len(results) != 1 {
		t.Fatalf("q=%q matched %d documents, want exactly 1 (the literal-adjacent-phrase doc); results=%#v",
			`abc"def"ghi`, len(results), results)
	}
	hit, ok := results[0].(map[string]any)
	if !ok {
		t.Fatalf("results[0] = %#v, want an object", results[0])
	}
	if sourcePath, _ := hit["source_path"].(string); sourcePath != "/fake/literal.md" {
		t.Fatalf("matched document source_path = %q, want %q (the scattered-words doc must NOT match)",
			sourcePath, "/fake/literal.md")
	}
}
