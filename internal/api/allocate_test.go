package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"bishop-memory/internal/store"

	"github.com/gin-gonic/gin"
)

// newAllocateTestDB opens a database through store.Open — the SAME
// constructor cmd/memoryd uses — and applies just enough schema for
// allocation: missions (with the harness column) and flight_recorder
// (which allocation writes its audit row into).
//
// Going through store.Open rather than a bare sql.Open matters. It is
// what supplies busy_timeout, WAL, foreign_keys, and above all
// SetMaxOpenConns(1) — the single-writer pattern the service actually
// runs under. A test that opens its own pool with different settings
// measures a database configuration that does not exist in production:
// the first draft of this file did exactly that, and the concurrency
// test below failed with SQLITE_BUSY and "cannot start a transaction
// within a transaction" against code that is fine as deployed.
//
// It is also file-backed rather than ":memory:" on purpose — an
// in-memory SQLite database is per-connection unless shared-cache is
// configured, so pooled connections would each see their own empty
// missions table and every allocation would compute sequence 1.
func newAllocateTestDB(t *testing.T) *sql.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "alloc.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	stmts := []string{
		`CREATE TABLE missions (
			id          TEXT PRIMARY KEY,
			title       TEXT NOT NULL,
			status      TEXT NOT NULL DEFAULT 'not-started',
			outcome     TEXT,
			owner       TEXT,
			priority    TEXT NOT NULL DEFAULT 'normal',
			next_action TEXT,
			blockers    TEXT,
			harness     TEXT,
			opened_at   TEXT NOT NULL DEFAULT (CURRENT_TIMESTAMP),
			closed_at   TEXT,
			created_at  TEXT NOT NULL DEFAULT (CURRENT_TIMESTAMP),
			updated_at  TEXT NOT NULL DEFAULT (CURRENT_TIMESTAMP),
			CHECK (status IN ('not-started','in-progress','blocked','complete')),
			CHECK (outcome IS NULL OR outcome IN ('done','failed')),
			CHECK (priority IN ('low','normal','high','urgent'))
		)`,
		`CREATE TABLE flight_recorder (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			mission_id  TEXT,
			step        TEXT,
			agent       TEXT,
			event       TEXT NOT NULL,
			note        TEXT NOT NULL,
			occurred_at TEXT,
			created_at  TEXT NOT NULL DEFAULT (CURRENT_TIMESTAMP)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("apply test schema: %v", err)
		}
	}
	return db
}

func newAllocateRouter(db *sql.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/v1/missions/allocate", allocateMissionHandler(db))
	return router
}

// allocate posts one allocation request and returns the decoded response.
func allocate(t *testing.T, router *gin.Engine, payload map[string]any) (int, map[string]any) {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/missions/allocate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var decoded map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("response not JSON (status %d): %s", rec.Code, rec.Body.String())
		}
	}
	return rec.Code, decoded
}

// TestAllocate_SequenceIsGlobalAcrossHarnesses is the core multi-harness
// guarantee: two DIFFERENT harnesses allocating on the same UTC day must
// receive different ids. This is the exact case that local, per-harness
// derivation gets wrong — both harnesses would compute 01 from their own
// mission folders and collide.
func TestAllocate_SequenceIsGlobalAcrossHarnesses(t *testing.T) {
	db := newAllocateTestDB(t)
	router := newAllocateRouter(db)

	status, first := allocate(t, router, map[string]any{
		"harness": "anom", "title": "First mission", "date": "20260907",
	})
	if status != http.StatusCreated {
		t.Fatalf("first allocate status = %d, want 201 (%v)", status, first)
	}
	if first["id"] != "mission-20260907-01" {
		t.Fatalf("first id = %v, want mission-20260907-01", first["id"])
	}

	status, second := allocate(t, router, map[string]any{
		"harness": "other-harness", "title": "Second mission", "date": "20260907",
	})
	if status != http.StatusCreated {
		t.Fatalf("second allocate status = %d, want 201 (%v)", status, second)
	}
	if second["id"] != "mission-20260907-02" {
		t.Fatalf("second id = %v, want mission-20260907-02 — the counter must be "+
			"global across harnesses, not per-harness", second["id"])
	}

	// The harness that opened each mission must be recorded, otherwise a
	// central registry cannot say who owns what.
	var harness string
	if err := db.QueryRow(`SELECT harness FROM missions WHERE id = ?`,
		"mission-20260907-02").Scan(&harness); err != nil {
		t.Fatalf("read harness: %v", err)
	}
	if harness != "other-harness" {
		t.Fatalf("harness = %q, want %q", harness, "other-harness")
	}
}

// TestAllocate_ResetsPerDay confirms NN is scoped to the UTC day and
// restarts at 01 when the date rolls over.
func TestAllocate_ResetsPerDay(t *testing.T) {
	db := newAllocateTestDB(t)
	router := newAllocateRouter(db)

	_, _ = allocate(t, router, map[string]any{"harness": "anom", "title": "day one", "date": "20260907"})
	_, second := allocate(t, router, map[string]any{"harness": "anom", "title": "day two", "date": "20260908"})

	if second["id"] != "mission-20260908-01" {
		t.Fatalf("id = %v, want mission-20260908-01 (counter must reset per UTC day)", second["id"])
	}
}

// TestAllocate_IgnoresNonSequenceIDs guards the regex anchoring. A mission
// id that is not shaped mission-YYYYMMDD-NN must not contribute to the
// maximum — otherwise a harness-qualified or hand-written id could push
// the counter somewhere it does not belong, or be misread as a sequence.
func TestAllocate_IgnoresNonSequenceIDs(t *testing.T) {
	db := newAllocateTestDB(t)
	router := newAllocateRouter(db)

	// Both of these match the LIKE prefix but neither is a global sequence id.
	for _, id := range []string{"mission-20260907-anom-07", "mission-20260907-xx"} {
		if _, err := db.Exec(
			`INSERT INTO missions (id, title) VALUES (?, 'pre-existing')`, id); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	_, decoded := allocate(t, router, map[string]any{
		"harness": "anom", "title": "next", "date": "20260907",
	})
	if decoded["id"] != "mission-20260907-01" {
		t.Fatalf("id = %v, want mission-20260907-01 — non-sequence ids must be ignored", decoded["id"])
	}
}

// TestAllocate_WidensPastNinetyNine confirms a day that runs long keeps
// counting rather than wrapping or zero-padding into a collision, matching
// the harness doctrine's "carry on with three digits".
func TestAllocate_WidensPastNinetyNine(t *testing.T) {
	db := newAllocateTestDB(t)
	router := newAllocateRouter(db)

	if _, err := db.Exec(
		`INSERT INTO missions (id, title) VALUES ('mission-20260907-99', 'ninety-nine')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, decoded := allocate(t, router, map[string]any{
		"harness": "anom", "title": "one hundred", "date": "20260907",
	})
	if decoded["id"] != "mission-20260907-100" {
		t.Fatalf("id = %v, want mission-20260907-100", decoded["id"])
	}
}

// TestAllocate_RequiresHarness confirms an unattributed allocation is
// refused. A centrally-allocated mission with no harness recorded cannot
// be attributed later, which defeats the reason for allocating centrally.
func TestAllocate_RequiresHarness(t *testing.T) {
	db := newAllocateTestDB(t)
	router := newAllocateRouter(db)

	status, _ := allocate(t, router, map[string]any{"title": "no harness", "date": "20260907"})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when harness is absent", status)
	}

	status, _ = allocate(t, router, map[string]any{"harness": "   ", "title": "blank", "date": "20260907"})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when harness is whitespace only", status)
	}
}

// TestAllocate_ConcurrentAllocationsAreUnique is the test the endpoint
// exists for.
//
// Several harnesses allocate simultaneously against one database. Every
// caller must receive a DISTINCT id, and the set must be exactly the
// contiguous range 01..N with no gaps and no repeats. A naive
// read-max-then-insert without the retry loop fails here: two goroutines
// read the same maximum, compute the same next id, and one of them either
// errors or — worse, in a design that reserved without inserting —
// silently returns a duplicate.
func TestAllocate_ConcurrentAllocationsAreUnique(t *testing.T) {
	db := newAllocateTestDB(t)
	router := newAllocateRouter(db)

	// Deliberately NOT touching the connection pool: store.Open's
	// SetMaxOpenConns(1) is the configuration under test.
	const callers = 12

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		ids      = make(map[string]int)
		failures []string
	)

	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(n int) {
			defer wg.Done()

			status, decoded := func() (int, map[string]any) {
				body, _ := json.Marshal(map[string]any{
					"harness": fmt.Sprintf("harness-%d", n),
					"title":   fmt.Sprintf("concurrent mission %d", n),
					"date":    "20260907",
				})
				req := httptest.NewRequest(http.MethodPost, "/v1/missions/allocate", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				var out map[string]any
				_ = json.Unmarshal(rec.Body.Bytes(), &out)
				return rec.Code, out
			}()

			mu.Lock()
			defer mu.Unlock()
			if status != http.StatusCreated {
				failures = append(failures, fmt.Sprintf("caller %d: status %d (%v)", n, status, decoded))
				return
			}
			id, _ := decoded["id"].(string)
			ids[id]++
		}(i)
	}
	wg.Wait()

	for _, failure := range failures {
		t.Errorf("allocation failed: %s", failure)
	}

	if len(ids) != callers {
		t.Fatalf("distinct ids = %d, want %d — ids issued: %v", len(ids), callers, ids)
	}
	for id, count := range ids {
		if count != 1 {
			t.Errorf("id %s issued %d times, want exactly 1", id, count)
		}
	}

	// The issued set must be exactly 01..callers: contiguous, no gaps.
	for n := 1; n <= callers; n++ {
		want := fmt.Sprintf("mission-20260907-%02d", n)
		if _, ok := ids[want]; !ok {
			t.Errorf("expected id %s was never issued (got %v)", want, ids)
		}
	}

	// And the database must agree with what was handed out.
	var stored int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM missions WHERE id LIKE 'mission-20260907-%'`).Scan(&stored); err != nil {
		t.Fatalf("count missions: %v", err)
	}
	if stored != callers {
		t.Fatalf("missions stored = %d, want %d", stored, callers)
	}
}
