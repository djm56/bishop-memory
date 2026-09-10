package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"bishop-memory/internal/store"

	"github.com/gin-gonic/gin"
)

// newMissionStepsTestDB opens a database through store.Open — the SAME
// constructor cmd/memoryd uses — and applies the schema necessary for
// mission step upsert testing: missions, mission_steps, and flight_recorder.
//
// The unique index on mission_steps(mission_id, step) is created by the
// test setup, simulating the EnsureMissionStepsIndex migration.
func newMissionStepsTestDB(t *testing.T) *sql.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "steps.db")
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
		`CREATE TABLE mission_steps (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			mission_id TEXT    NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
			step       TEXT,
			phase      TEXT,
			agent      TEXT,
			status     TEXT,
			notes      TEXT,
			started_at TEXT,
			ended_at   TEXT,
			summary    TEXT,
			created_at TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),
			CHECK (status IS NULL OR status IN ('pending', 'in-progress', 'done', 'failed'))
		)`,
		`CREATE TABLE flight_recorder (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			mission_id TEXT,
			step       TEXT,
			agent      TEXT,
			event      TEXT    NOT NULL,
			note       TEXT    NOT NULL,
			occurred_at TEXT,
			created_at TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),
			FOREIGN KEY (mission_id) REFERENCES missions(id) ON DELETE CASCADE
		)`,
		// Create the unique index as the migration does
		`CREATE UNIQUE INDEX idx_mission_steps_unique_key
		 ON mission_steps(mission_id, step)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("apply test schema: %v", err)
		}
	}
	return db
}

// newMissionStepsRouter creates a test Gin router with the mission steps handler.
func newMissionStepsRouter(db *sql.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/v1/missions/:missionID/steps", createMissionStepHandler(db))
	return router
}

// postMissionStep makes a POST request to record a mission step.
func postMissionStep(t *testing.T, router *gin.Engine, missionID string, payload map[string]any) (int, map[string]any) {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/missions/%s/steps", missionID), bytes.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Logf("warning: could not unmarshal response body: %v", err)
	}

	return w.Code, result
}

// countRows queries the database for rows matching a condition.
func countRows(t *testing.T, db *sql.DB, table, where string, args ...any) int {
	t.Helper()
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
	if where != "" {
		query += " WHERE " + where
	}
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

// getStep queries a single mission step by mission_id and step.
func getStep(t *testing.T, db *sql.DB, missionID, step string) (status *string, phase *string, agent *string, notes *string) {
	t.Helper()
	var statusVal sql.NullString
	var phaseVal sql.NullString
	var agentVal sql.NullString
	var notesVal sql.NullString

	var stepVal any
	if step == "" {
		stepVal = nil
	} else {
		stepVal = step
	}

	err := db.QueryRow(`
		SELECT status, phase, agent, notes
		FROM mission_steps
		WHERE mission_id = ? AND step IS ?`,
		missionID,
		stepVal,
	).Scan(&statusVal, &phaseVal, &agentVal, &notesVal)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, nil
	}
	if err != nil {
		t.Fatalf("query step: %v", err)
	}

	if statusVal.Valid {
		status = &statusVal.String
	}
	if phaseVal.Valid {
		phase = &phaseVal.String
	}
	if agentVal.Valid {
		agent = &agentVal.String
	}
	if notesVal.Valid {
		notes = &notesVal.String
	}

	return
}

// countFlightRecorderRows counts flight_recorder entries for a mission.
func countFlightRecorderRows(t *testing.T, db *sql.DB, missionID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM flight_recorder WHERE mission_id = ?", missionID).Scan(&count); err != nil {
		t.Fatalf("count flight recorder rows: %v", err)
	}
	return count
}

// getFlightRecorderNotes returns the notes from flight_recorder rows for a mission, in order.
func getFlightRecorderNotes(t *testing.T, db *sql.DB, missionID string) []string {
	t.Helper()
	rows, err := db.Query("SELECT note FROM flight_recorder WHERE mission_id = ? ORDER BY id ASC", missionID)
	if err != nil {
		t.Fatalf("query flight recorder: %v", err)
	}
	defer rows.Close()

	var notes []string
	for rows.Next() {
		var note string
		if err := rows.Scan(&note); err != nil {
			t.Fatalf("scan note: %v", err)
		}
		notes = append(notes, note)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate flight recorder: %v", err)
	}
	return notes
}

// FlightRecorderRow represents a single row from flight_recorder.
type FlightRecorderRow struct {
	Step  *string
	Agent *string
	Event string
	Note  string
}

// getFlightRecorderRows returns full flight_recorder rows for a mission, in order.
func getFlightRecorderRows(t *testing.T, db *sql.DB, missionID string) []FlightRecorderRow {
	t.Helper()
	rows, err := db.Query("SELECT step, agent, event, note FROM flight_recorder WHERE mission_id = ? ORDER BY id ASC", missionID)
	if err != nil {
		t.Fatalf("query flight recorder: %v", err)
	}
	defer rows.Close()

	var result []FlightRecorderRow
	for rows.Next() {
		var stepVal sql.NullString
		var agentVal sql.NullString
		var event string
		var note string
		if err := rows.Scan(&stepVal, &agentVal, &event, &note); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		row := FlightRecorderRow{
			Event: event,
			Note:  note,
		}
		if stepVal.Valid {
			row.Step = &stepVal.String
		}
		if agentVal.Valid {
			row.Agent = &agentVal.String
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate flight recorder: %v", err)
	}
	return result
}

// Test 1: First POST creates — 201, created: true, row present.
func TestCreateMissionStepFirst(t *testing.T) {
	db := newMissionStepsTestDB(t)
	router := newMissionStepsRouter(db)

	// Create the mission first
	if _, err := db.Exec(`
		INSERT INTO missions (id, title, status)
		VALUES (?, ?, ?)`,
		"mission-1", "Test Mission", "in-progress",
	); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// Post a step
	status, result := postMissionStep(t, router, "mission-1", map[string]any{
		"step":   "1",
		"phase":  "Core",
		"agent":  "hicks",
		"status": "pending",
		"notes":  "Initial step",
	})

	if status != http.StatusCreated {
		t.Errorf("expected 201, got %d", status)
	}
	if created, ok := result["created"].(bool); !ok || !created {
		t.Errorf("expected created=true, got %v", result["created"])
	}

	// Verify row exists
	stepStatus, stepPhase, stepAgent, stepNotes := getStep(t, db, "mission-1", "1")
	if stepStatus == nil || *stepStatus != "pending" {
		t.Errorf("expected status=pending, got %v", stepStatus)
	}
	if stepPhase == nil || *stepPhase != "Core" {
		t.Errorf("expected phase=Core, got %v", stepPhase)
	}
	if stepAgent == nil || *stepAgent != "hicks" {
		t.Errorf("expected agent=hicks, got %v", stepAgent)
	}
	if stepNotes == nil || *stepNotes != "Initial step" {
		t.Errorf("expected notes='Initial step', got %v", stepNotes)
	}
}

// Test 2: Second POST for the same (mission_id, step) updates — 200, created: false.
func TestCreateMissionStepUpdate(t *testing.T) {
	db := newMissionStepsTestDB(t)
	router := newMissionStepsRouter(db)

	// Create mission
	if _, err := db.Exec(`
		INSERT INTO missions (id, title, status)
		VALUES (?, ?, ?)`,
		"mission-2", "Test Mission", "in-progress",
	); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// First POST — create
	postMissionStep(t, router, "mission-2", map[string]any{
		"step":   "2",
		"status": "pending",
	})

	// Second POST for same step — update
	status, result := postMissionStep(t, router, "mission-2", map[string]any{
		"step":   "2",
		"status": "in-progress",
	})

	if status != http.StatusOK {
		t.Errorf("expected 200, got %d", status)
	}
	if created, ok := result["created"].(bool); !ok || created {
		t.Errorf("expected created=false, got %v", result["created"])
	}

	// Verify exactly one row still exists
	count := countRows(t, db, "mission_steps", "mission_id = ? AND step IS ?", "mission-2", "2")
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}

	// Verify status was updated
	stepStatus, _, _, _ := getStep(t, db, "mission-2", "2")
	if stepStatus == nil || *stepStatus != "in-progress" {
		t.Errorf("expected status=in-progress after update, got %v", stepStatus)
	}
}

// Test 3: Full status transition pending → in-progress → done.
func TestCreateMissionStepTransition(t *testing.T) {
	db := newMissionStepsTestDB(t)
	router := newMissionStepsRouter(db)

	// Create mission
	if _, err := db.Exec(`
		INSERT INTO missions (id, title, status)
		VALUES (?, ?, ?)`,
		"mission-3", "Test Mission", "in-progress",
	); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// Transition 1: pending
	postMissionStep(t, router, "mission-3", map[string]any{
		"step":   "3",
		"status": "pending",
	})
	status, _, _, _ := getStep(t, db, "mission-3", "3")
	if status == nil || *status != "pending" {
		t.Errorf("after create: expected status=pending, got %v", status)
	}

	// Transition 2: in-progress
	postMissionStep(t, router, "mission-3", map[string]any{
		"step":   "3",
		"status": "in-progress",
	})
	status, _, _, _ = getStep(t, db, "mission-3", "3")
	if status == nil || *status != "in-progress" {
		t.Errorf("after 1st update: expected status=in-progress, got %v", status)
	}

	// Transition 3: done
	postMissionStep(t, router, "mission-3", map[string]any{
		"step":   "3",
		"status": "done",
	})
	status, _, _, _ = getStep(t, db, "mission-3", "3")
	if status == nil || *status != "done" {
		t.Errorf("after 2nd update: expected status=done, got %v", status)
	}

	// Verify exactly one row
	count := countRows(t, db, "mission_steps", "mission_id = ? AND step IS ?", "mission-3", "3")
	if count != 1 {
		t.Errorf("expected 1 row after transitions, got %d", count)
	}
}

// Test 4: Update omitting phase and notes preserves stored values.
func TestCreateMissionStepPreserveFields(t *testing.T) {
	db := newMissionStepsTestDB(t)
	router := newMissionStepsRouter(db)

	// Create mission
	if _, err := db.Exec(`
		INSERT INTO missions (id, title, status)
		VALUES (?, ?, ?)`,
		"mission-4", "Test Mission", "in-progress",
	); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// First POST — set phase and notes
	postMissionStep(t, router, "mission-4", map[string]any{
		"step":   "4",
		"phase":  "Phase A",
		"notes":  "Important notes",
		"status": "pending",
	})

	// Second POST — omit phase and notes, update only status
	postMissionStep(t, router, "mission-4", map[string]any{
		"step":   "4",
		"status": "in-progress",
	})

	// Verify phase and notes are preserved
	_, stepPhase, _, stepNotes := getStep(t, db, "mission-4", "4")
	if stepPhase == nil || *stepPhase != "Phase A" {
		t.Errorf("expected phase='Phase A' preserved, got %v", stepPhase)
	}
	if stepNotes == nil || *stepNotes != "Important notes" {
		t.Errorf("expected notes='Important notes' preserved, got %v", stepNotes)
	}
}

// Test 5: Update appends a second flight_recorder row with different note.
func TestCreateMissionStepAuditTrail(t *testing.T) {
	db := newMissionStepsTestDB(t)
	router := newMissionStepsRouter(db)

	// Create mission
	if _, err := db.Exec(`
		INSERT INTO missions (id, title, status)
		VALUES (?, ?, ?)`,
		"mission-5", "Test Mission", "in-progress",
	); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// First POST — create
	postMissionStep(t, router, "mission-5", map[string]any{
		"step":   "5",
		"agent":  "hicks",
		"status": "pending",
	})

	rows := getFlightRecorderRows(t, db, "mission-5")
	if len(rows) != 1 {
		t.Fatalf("after create: expected 1 row, got %d", len(rows))
	}
	if rows[0].Step == nil || *rows[0].Step != "5" {
		t.Errorf("after create: expected step=5, got %v", rows[0].Step)
	}
	if rows[0].Agent == nil || *rows[0].Agent != "hicks" {
		t.Errorf("after create: expected agent=hicks, got %v", rows[0].Agent)
	}
	if rows[0].Event != "mission.step" {
		t.Errorf("after create: expected event=mission.step, got %q", rows[0].Event)
	}
	if rows[0].Note != "Mission step 5 created (status: pending)" {
		t.Errorf("after create: expected note 'Mission step 5 created (status: pending)', got %q", rows[0].Note)
	}

	// Second POST — update with different status
	postMissionStep(t, router, "mission-5", map[string]any{
		"step":   "5",
		"agent":  "apone",
		"status": "in-progress",
	})

	rows = getFlightRecorderRows(t, db, "mission-5")
	if len(rows) != 2 {
		t.Fatalf("after update: expected 2 rows, got %d", len(rows))
	}
	if rows[1].Step == nil || *rows[1].Step != "5" {
		t.Errorf("after update: expected step=5, got %v", rows[1].Step)
	}
	if rows[1].Agent == nil || *rows[1].Agent != "apone" {
		t.Errorf("after update: expected agent=apone, got %v", rows[1].Agent)
	}
	if rows[1].Event != "mission.step" {
		t.Errorf("after update: expected event=mission.step, got %q", rows[1].Event)
	}
	if rows[1].Note != "Mission step 5 updated (status: in-progress)" {
		t.Errorf("after update: expected note 'Mission step 5 updated (status: in-progress)', got %q", rows[1].Note)
	}
}

// Test 5b: Audit row without status has no parenthetical.
func TestCreateMissionStepAuditTrailNoStatus(t *testing.T) {
	db := newMissionStepsTestDB(t)
	router := newMissionStepsRouter(db)

	// Create mission
	if _, err := db.Exec(`
		INSERT INTO missions (id, title, status)
		VALUES (?, ?, ?)`,
		"mission-5b", "Test Mission", "in-progress",
	); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// POST without status
	postMissionStep(t, router, "mission-5b", map[string]any{
		"step":  "5b",
		"agent": "hicks",
	})

	rows := getFlightRecorderRows(t, db, "mission-5b")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Note != "Mission step 5b created" {
		t.Errorf("expected note without parens 'Mission step 5b created', got %q", rows[0].Note)
	}
	if rows[0].Agent == nil || *rows[0].Agent != "hicks" {
		t.Errorf("expected agent=hicks, got %v", rows[0].Agent)
	}
}

// Test 5c: Audit row with missing agent stores NULL, not empty string.
func TestCreateMissionStepAuditTrailNoAgent(t *testing.T) {
	db := newMissionStepsTestDB(t)
	router := newMissionStepsRouter(db)

	// Create mission
	if _, err := db.Exec(`
		INSERT INTO missions (id, title, status)
		VALUES (?, ?, ?)`,
		"mission-5c", "Test Mission", "in-progress",
	); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// POST without agent
	postMissionStep(t, router, "mission-5c", map[string]any{
		"step":   "5c",
		"status": "pending",
	})

	rows := getFlightRecorderRows(t, db, "mission-5c")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Agent != nil {
		t.Errorf("expected agent=NULL, got %v", rows[0].Agent)
	}
	if rows[0].Note != "Mission step 5c created (status: pending)" {
		t.Errorf("expected note 'Mission step 5c created (status: pending)', got %q", rows[0].Note)
	}
}

// Test 5d: An update that omits agent and status, while prior values exist,
// preserves both in mission_steps (COALESCE) AND the audit row records those
// preserved (resulting) values rather than NULL/absent — the audit note
// reflects the row's post-upsert state, not the fields this particular
// request happened to supply.
func TestCreateMissionStepAuditTrailPreservedOnPartialUpdate(t *testing.T) {
	db := newMissionStepsTestDB(t)
	router := newMissionStepsRouter(db)

	// Create mission
	if _, err := db.Exec(`
		INSERT INTO missions (id, title, status)
		VALUES (?, ?, ?)`,
		"mission-5d", "Test Mission", "in-progress",
	); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// First POST — establish agent and status.
	postMissionStep(t, router, "mission-5d", map[string]any{
		"step":   "5d",
		"agent":  "hicks",
		"status": "pending",
	})

	// Second POST — omit both agent and status; only notes changes.
	postMissionStep(t, router, "mission-5d", map[string]any{
		"step":  "5d",
		"notes": "follow-up note",
	})

	// mission_steps: agent and status preserved by COALESCE.
	stepStatus, _, stepAgent, stepNotes := getStep(t, db, "mission-5d", "5d")
	if stepAgent == nil || *stepAgent != "hicks" {
		t.Errorf("expected agent='hicks' preserved, got %v", stepAgent)
	}
	if stepStatus == nil || *stepStatus != "pending" {
		t.Errorf("expected status='pending' preserved, got %v", stepStatus)
	}
	if stepNotes == nil || *stepNotes != "follow-up note" {
		t.Errorf("expected notes='follow-up note', got %v", stepNotes)
	}

	// flight_recorder: the second (update) row must reflect the preserved
	// resulting state, not NULL agent / absent status, even though this
	// request's body carried neither field.
	rows := getFlightRecorderRows(t, db, "mission-5d")
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[1].Agent == nil || *rows[1].Agent != "hicks" {
		t.Errorf("expected audit row agent='hicks' (resulting state), got %v", rows[1].Agent)
	}
	if rows[1].Note != "Mission step 5d updated (status: pending)" {
		t.Errorf("expected note 'Mission step 5d updated (status: pending)' (resulting status), got %q", rows[1].Note)
	}
}

// Test 6: Index migration is idempotent — calling twice is a no-op.
func TestEnsureMissionStepsIndexIdempotent(t *testing.T) {
	db := newMissionStepsTestDB(t)

	// The index was already created by the test setup. Call the migration twice
	// to verify it's a no-op.
	if err := store.EnsureMissionStepsIndex(db); err != nil {
		t.Errorf("first call to EnsureMissionStepsIndex: %v", err)
	}
	if err := store.EnsureMissionStepsIndex(db); err != nil {
		t.Errorf("second call to EnsureMissionStepsIndex: %v", err)
	}

	// Verify the index still exists
	var indexName string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master
		 WHERE type = 'index' AND name = 'idx_mission_steps_unique_key'`,
	).Scan(&indexName)
	if err != nil {
		t.Errorf("index missing after idempotent calls: %v", err)
	}
}

// Test 7: Duplicate pre-check returns descriptive error.
func TestEnsureMissionStepsIndexDuplicateDetection(t *testing.T) {
	// Create a test database WITHOUT the unique index.
	path := filepath.Join(t.TempDir(), "dup.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Create schema without the unique index
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
		`CREATE TABLE mission_steps (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			mission_id TEXT    NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
			step       TEXT,
			phase      TEXT,
			agent      TEXT,
			status     TEXT,
			notes      TEXT,
			started_at TEXT,
			ended_at   TEXT,
			summary    TEXT,
			created_at TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),
			CHECK (status IS NULL OR status IN ('pending', 'in-progress', 'done', 'failed'))
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("apply test schema: %v", err)
		}
	}

	// Create a mission and two duplicate steps
	if _, err := db.Exec(`
		INSERT INTO missions (id, title, status)
		VALUES (?, ?, ?)`,
		"mission-dup", "Test", "in-progress",
	); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO mission_steps (mission_id, step, status)
		VALUES (?, ?, ?), (?, ?, ?)`,
		"mission-dup", "1", "pending",
		"mission-dup", "1", "pending",
	); err != nil {
		t.Fatalf("insert duplicate steps: %v", err)
	}

	// Run the migration — should return an error about duplicates
	err = store.EnsureMissionStepsIndex(db)
	if err == nil {
		t.Errorf("expected error on duplicate detection, got nil")
	}
	if err != nil {
		errStr := err.Error()
		if errStr == "" {
			t.Errorf("error message is empty")
		}
		// Verify the error contains helpful information
		if !contains(errStr, "duplicate") {
			t.Errorf("error should mention 'duplicate', got: %s", errStr)
		}
		if !contains(errStr, "mission-dup") {
			t.Errorf("error should show the mission ID, got: %s", errStr)
		}
	}
}

// Test 8: POST with no step field returns 400 (validation error).
func TestCreateMissionStepMissingStep(t *testing.T) {
	db := newMissionStepsTestDB(t)
	router := newMissionStepsRouter(db)

	// Create mission
	if _, err := db.Exec(`
		INSERT INTO missions (id, title, status)
		VALUES (?, ?, ?)`,
		"mission-6", "Test Mission", "in-progress",
	); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// POST with no step field — should return 400
	status, _ := postMissionStep(t, router, "mission-6", map[string]any{
		"status": "pending",
		"notes":  "No step field provided",
	})

	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", status)
	}

	// Verify no row was inserted
	count := countRows(t, db, "mission_steps", "mission_id = ?", "mission-6")
	if count != 0 {
		t.Errorf("expected 0 rows (validation error), got %d", count)
	}
}

// Test 8b: POST with empty step field returns 400 (validation error).
func TestCreateMissionStepEmptyStep(t *testing.T) {
	db := newMissionStepsTestDB(t)
	router := newMissionStepsRouter(db)

	// Create mission
	if _, err := db.Exec(`
		INSERT INTO missions (id, title, status)
		VALUES (?, ?, ?)`,
		"mission-6b", "Test Mission", "in-progress",
	); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// POST with empty step — should return 400
	status, _ := postMissionStep(t, router, "mission-6b", map[string]any{
		"step":   "",
		"status": "pending",
	})

	if status != http.StatusBadRequest {
		t.Errorf("expected 400 for empty step, got %d", status)
	}

	// Verify no row was inserted
	count := countRows(t, db, "mission_steps", "mission_id = ?", "mission-6b")
	if count != 0 {
		t.Errorf("expected 0 rows (validation error), got %d", count)
	}
}

// Test 8c: POST with whitespace-only step field returns 400 (validation error).
func TestCreateMissionStepWhitespaceStep(t *testing.T) {
	db := newMissionStepsTestDB(t)
	router := newMissionStepsRouter(db)

	// Create mission
	if _, err := db.Exec(`
		INSERT INTO missions (id, title, status)
		VALUES (?, ?, ?)`,
		"mission-6c", "Test Mission", "in-progress",
	); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// POST with whitespace-only step — should return 400
	status, _ := postMissionStep(t, router, "mission-6c", map[string]any{
		"step":   "   ",
		"status": "pending",
	})

	if status != http.StatusBadRequest {
		t.Errorf("expected 400 for whitespace-only step, got %d", status)
	}

	// Verify no row was inserted
	count := countRows(t, db, "mission_steps", "mission_id = ?", "mission-6c")
	if count != 0 {
		t.Errorf("expected 0 rows (validation error), got %d", count)
	}
}

// Test 9: id and created_at remain unchanged after an update (immutability check).
func TestCreateMissionStepIdAndCreatedAtImmutable(t *testing.T) {
	db := newMissionStepsTestDB(t)
	router := newMissionStepsRouter(db)

	// Create mission
	if _, err := db.Exec(`
		INSERT INTO missions (id, title, status)
		VALUES (?, ?, ?)`,
		"mission-7", "Test Mission", "in-progress",
	); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// First POST — create
	postMissionStep(t, router, "mission-7", map[string]any{
		"step":   "7",
		"status": "pending",
	})

	// Read id and created_at after creation
	var idBefore int64
	var createdAtBefore string
	err := db.QueryRow(`
		SELECT id, created_at FROM mission_steps
		WHERE mission_id = ? AND step = ?`,
		"mission-7", "7",
	).Scan(&idBefore, &createdAtBefore)
	if err != nil {
		t.Fatalf("query after create: %v", err)
	}

	// Second POST — update
	postMissionStep(t, router, "mission-7", map[string]any{
		"step":   "7",
		"status": "in-progress",
		"notes":  "Updated notes",
	})

	// Read id and created_at after update
	var idAfter int64
	var createdAtAfter string
	err = db.QueryRow(`
		SELECT id, created_at FROM mission_steps
		WHERE mission_id = ? AND step = ?`,
		"mission-7", "7",
	).Scan(&idAfter, &createdAtAfter)
	if err != nil {
		t.Fatalf("query after update: %v", err)
	}

	// Verify id is unchanged
	if idBefore != idAfter {
		t.Errorf("id changed after update: %d → %d (should be immutable)", idBefore, idAfter)
	}

	// Verify created_at is unchanged
	if createdAtBefore != createdAtAfter {
		t.Errorf("created_at changed after update: %q → %q (should be immutable)", createdAtBefore, createdAtAfter)
	}
}

// Test 10: Legacy NULL-step rows don't block index creation (migration regression test).
func TestEnsureMissionStepsIndexWithLegacyNullSteps(t *testing.T) {
	// Create a test database WITHOUT the unique index.
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Create schema without the unique index
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
		`CREATE TABLE mission_steps (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			mission_id TEXT    NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
			step       TEXT,
			phase      TEXT,
			agent      TEXT,
			status     TEXT,
			notes      TEXT,
			started_at TEXT,
			ended_at   TEXT,
			summary    TEXT,
			created_at TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),
			CHECK (status IS NULL OR status IN ('pending', 'in-progress', 'done', 'failed'))
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("apply test schema: %v", err)
		}
	}

	// Create a mission
	if _, err := db.Exec(`
		INSERT INTO missions (id, title, status)
		VALUES (?, ?, ?)`,
		"mission-legacy", "Test", "in-progress",
	); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// Insert two legacy NULL-step rows for the same mission
	// (this would be impossible now, but represents pre-fix data)
	if _, err := db.Exec(`
		INSERT INTO mission_steps (mission_id, step, status)
		VALUES (?, NULL, ?), (?, NULL, ?)`,
		"mission-legacy", "pending",
		"mission-legacy", "in-progress",
	); err != nil {
		t.Fatalf("insert legacy NULL-step rows: %v", err)
	}

	// Verify two NULL-step rows exist
	count := countRows(t, db, "mission_steps", "mission_id = ? AND step IS NULL", "mission-legacy")
	if count != 2 {
		t.Errorf("expected 2 legacy NULL-step rows, got %d", count)
	}

	// Run the migration — should succeed despite NULL-step rows
	// (because NULL steps are excluded from the duplicate check)
	err = store.EnsureMissionStepsIndex(db)
	if err != nil {
		t.Errorf("EnsureMissionStepsIndex failed with legacy NULL-step rows: %v", err)
	}

	// Verify the index was created
	var indexName string
	err = db.QueryRow(
		`SELECT name FROM sqlite_master
		 WHERE type = 'index' AND name = 'idx_mission_steps_unique_key'`,
	).Scan(&indexName)
	if err != nil {
		t.Errorf("index not created: %v", err)
	}

	// Verify the NULL-step rows still exist (migration didn't delete them)
	count = countRows(t, db, "mission_steps", "mission_id = ? AND step IS NULL", "mission-legacy")
	if count != 2 {
		t.Errorf("legacy NULL-step rows were modified: expected 2, got %d", count)
	}
}

// contains is a helper to check if a string contains a substring.
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
