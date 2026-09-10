// Package api — HTTP handlers for /v1/flight-recorder and /v1/documents/sync.
//
// appendFlightRecorderHandler appends a durable agent/system event.
// listFlightRecorderHandler reads recent events, optionally filtered by mission.
// syncDocumentsHandler triggers an explicit Markdown/JSONL import of the
// agent's memory tree into the documents + FTS5 tables via the importer.
package api

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/importer"
	"bishop-memory/internal/model"
)

// appendFlightRecorderHandler handles POST /v1/flight-recorder.
//
// It binds an AppendFlightRecorderRequest, inserts one row into the flight_recorder table
// (mission_id and agent are nullable for system/agent-scoped events), and
// returns the new event id. Validation is driven by the binding tags on
// model.AppendFlightRecorderRequest (event + note required).
//
// Agent identity (Phase 3): the mcpd HTTP proxy composes
// Agent = "<harness>:<sub-agent>" (e.g. "anomalous:hicks") and
// forwards it here so the audit trail records who emitted each event.
// Agent is optional; system / non-agent callers (and the mission-lifecycle
// event INSERTs in createMissionHandler / updateMissionHandler /
// createMissionStepHandler) leave it NULL.
func appendFlightRecorderHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request model.AppendFlightRecorderRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		request.Event = strings.TrimSpace(request.Event)
		request.Note = strings.TrimSpace(request.Note)
		// Trim agent for parity with event / note so a stray " "
		// in the composed "<harness>:<agent>" string does not leak
		// through; the flight_recorder.agent column is a free-form TEXT.
		request.Agent = strings.TrimSpace(request.Agent)
		request.Step = strings.TrimSpace(request.Step)
		request.OccurredAt = strings.TrimSpace(request.OccurredAt)

		// Post-trim non-empty guard: a JSON body of {"event":"   "}
		// (or {"note":""}) passes binding:"required" because the field
		// IS present, but the flight_recorder table columns are NOT NULL TEXT. A
		// whitespace-only or empty value would surface as a 500 (driver
		// constraint violation) instead of a clean 400. Validate after
		// the trim so legitimate "mission.created" / "mission.updated" event
		// types with surrounding whitespace still get through cleanly.
		if request.Event == "" {
			validationError(c, errors.New("event must not be empty or whitespace-only"))
			return
		}
		if request.Note == "" {
			validationError(c, errors.New("note must not be empty or whitespace-only"))
			return
		}

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		// Step 4 review fix (Review B, WARNING W1): mirror
		// createMissionStepHandler's existence check. Without it, a
		// non-empty but unknown mission_id fails the
		// flight_recorder.mission_id REFERENCES missions(id) foreign key and falls
		// into the generic internalError 500 path — a client input
		// error surfacing as a server fault. Skip the check when
		// mission_id is empty: nullIfEmpty already turns that into an
		// unconstrained NULL event, so there is nothing to verify.
		if strings.TrimSpace(request.MissionID) != "" {
			var exists int
			err = tx.QueryRowContext(
				c.Request.Context(),
				`SELECT 1 FROM missions WHERE id = ?`,
				request.MissionID,
			).Scan(&exists)
			if errors.Is(err, sql.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{
					"error": "mission not found",
				})
				return
			}
			if err != nil {
				internalError(c, err)
				return
			}
		}

		result, err := tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO flight_recorder (
				mission_id, step, event, note, occurred_at, agent
			) VALUES (?, ?, ?, ?, ?, ?)`,
			nullIfEmpty(request.MissionID),
			nullIfEmpty(request.Step),
			request.Event,
			request.Note,
			nullIfEmpty(request.OccurredAt),
			nullIfEmpty(request.Agent),
		)
		if err != nil {
			internalError(c, err)
			return
		}

		id, err := result.LastInsertId()
		if err != nil {
			internalError(c, err)
			return
		}

		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"id":       id,
			"appended": true,
		})
	}
}

// listFlightRecorderHandler handles GET /v1/flight-recorder.
//
// Optional query params:
//
//	mission_id — filter to events for one mission (NULL mission_id events excluded)
//	limit      — cap at 100, default 50
//
// Events are returned newest-first (ORDER BY created_at DESC).
func listFlightRecorderHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		missionID := strings.TrimSpace(c.Query("mission_id"))

		limit := 50
		if raw := c.Query("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				if n > 100 {
					n = 100
				}
				limit = n
			}
		}

		query := `
			SELECT id, mission_id, step, occurred_at, event, note, agent, created_at
			FROM flight_recorder
		`
		args := []any{}

		if missionID != "" {
			query += ` WHERE mission_id = ?`
			args = append(args, missionID)
		}

		query += ` ORDER BY created_at DESC LIMIT ?`
		args = append(args, limit)

		rows, err := db.QueryContext(c.Request.Context(), query, args...)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()

		entries := make([]model.FlightRecorderEntry, 0)

		for rows.Next() {
			var entry model.FlightRecorderEntry
			var missionID sql.NullString
			var step sql.NullString
			var occurredAt sql.NullString
			var agent sql.NullString

			if err := rows.Scan(
				&entry.ID,
				&missionID,
				&step,
				&occurredAt,
				&entry.Event,
				&entry.Note,
				&agent,
				&entry.CreatedAt,
			); err != nil {
				internalError(c, err)
				return
			}
			entry.MissionID = missionID.String
			if step.Valid {
				entry.Step = &step.String
			}
			if occurredAt.Valid {
				entry.OccurredAt = &occurredAt.String
			}
			entry.Agent = agent.String
			entries = append(entries, entry)
		}

		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"flight_recorder": entries})
	}
}

// syncDocumentsHandler handles POST /v1/documents/sync. It triggers an
// import of the agent's memory tree into the documents + FTS5 tables by
// invoking importer.Sync.
//
// The import root is resolved in order: the request body Root, the
// MEMORY_ROOT env var, or the "testdata/memory" default. On success,
// the handler returns HTTP 200 with {"root":...,"synced":true}. On
// import failure, it returns HTTP 502 with an error message.
func syncDocumentsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request model.SyncRequest
		// Body is optional; an empty body leaves request.Root unset.
		_ = c.ShouldBindJSON(&request)

		root := strings.TrimSpace(request.Root)
		if root == "" {
			root = strings.TrimSpace(os.Getenv("MEMORY_ROOT"))
		}
		if root == "" {
			root = "testdata/memory"
		}

		// Step 4 review fix (minimal, sanctioned scope — see README.md
		// "Root `'/'` edge case"): reject a root that is (or resolves
		// via Abs+Clean to) the filesystem root, so a caller cannot
		// point this service at "/" and have importer.Sync walk the
		// entire filesystem. This is deliberately NOT a full allow-list
		// of permitted roots — that is a larger, separately-tracked
		// design decision (see docs/api-contract.md "Root safety") —
		// it only refuses the one unbounded case named in the roadmap.
		if isFilesystemRoot(root) {
			validationError(c, errors.New("root must not be the filesystem root"))
			return
		}

		if err := importer.Sync(db, root); err != nil {
			// CONV-033 (task-20260821-02 step 7a class sweep): importer.Sync's
			// own error text (see internal/importer/importer.go) wraps the
			// caller-supplied root and paths derived from walking it —
			// e.g. `fmt.Errorf("memory root %q does not exist", absRoot)` —
			// which is a direct, UNBOUNDED echo of request-derived input
			// (the entire root string, not a single bounded token like the
			// FTS5 case) if passed through verbatim. Log the real error
			// server-side for operator diagnosis; respond with a static,
			// authored message only.
			log.Printf("request_id=%v error=%v", requestID(c), err)
			c.JSON(http.StatusBadGateway, gin.H{
				"error":   "import failed",
				"details": "memory tree import failed; see server logs for details",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"synced": true,
			"root":   root,
		})
	}
}

// isFilesystemRoot reports whether root is, or resolves to, the
// filesystem root — "/" on POSIX, or a bare volume root such as "C:\"
// on Windows. It resolves root the same way importer.Sync does
// (filepath.Abs + filepath.Clean) so a relative-looking input that
// still lands on "/" (e.g. running with CWD "/" and root ".") is also
// caught, without duplicating importer.Sync's symlink resolution — this
// is a syntactic pre-check, not the importer's own path-safety walk.
func isFilesystemRoot(root string) bool {
	abs, err := filepath.Abs(root)
	if err != nil {
		// Let importer.Sync surface the resolution failure; this guard
		// only rejects a root it can positively identify as "/".
		return false
	}
	cleaned := filepath.Clean(abs)
	volRoot := filepath.VolumeName(cleaned) + string(filepath.Separator)
	return cleaned == string(filepath.Separator) || cleaned == volRoot
}
