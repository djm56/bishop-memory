// Package api — HTTP handlers for /v1/events and /v1/documents/sync.
//
// appendEventHandler appends a durable agent/system event.
// listEventsHandler reads recent events, optionally filtered by task.
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

// appendEventHandler handles POST /v1/events.
//
// It binds an AppendEventRequest, inserts one row into the events table
// (task_id and agent are nullable for system/agent-scoped events), and
// returns the new event id. Validation is driven by the binding tags on
// model.AppendEventRequest (event_type + summary required).
//
// Agent identity (Phase 3): the mcpd HTTP proxy composes
// Agent = "<harness>:<sub-agent>" (e.g. "opencode:orchestrator") and
// forwards it here so the audit trail records who emitted each event.
// Agent is optional; system / non-agent callers (and the task-lifecycle
// event INSERTs in createTaskHandler / updateTaskHandler /
// createTaskRunHandler) leave it NULL.
func appendEventHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request model.AppendEventRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		request.EventType = strings.TrimSpace(request.EventType)
		request.Summary = strings.TrimSpace(request.Summary)
		// Trim agent for parity with event_type / summary so a stray " "
		// in the composed "<harness>:<agent>" string does not leak
		// through; the events.agent column is a free-form TEXT.
		request.Agent = strings.TrimSpace(request.Agent)

		// Post-trim non-empty guard: a JSON body of {"event_type":"   "}
		// (or {"summary":""}) passes binding:"required" because the field
		// IS present, but the events table columns are NOT NULL TEXT. A
		// whitespace-only or empty value would surface as a 500 (driver
		// constraint violation) instead of a clean 400. Validate after
		// the trim so legitimate "task.created" / "task.updated" event
		// types with surrounding whitespace still get through cleanly.
		if request.EventType == "" {
			validationError(c, errors.New("event_type must not be empty or whitespace-only"))
			return
		}
		if request.Summary == "" {
			validationError(c, errors.New("summary must not be empty or whitespace-only"))
			return
		}

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		// Step 4 review fix (Review B, WARNING W1): mirror
		// createTaskRunHandler's existence check. Without it, a
		// non-empty but unknown task_id fails the
		// events.task_id REFERENCES tasks(id) foreign key and falls
		// into the generic internalError 500 path — a client input
		// error surfacing as a server fault. Skip the check when
		// task_id is empty: nullIfEmpty already turns that into an
		// unconstrained NULL event, so there is nothing to verify.
		if strings.TrimSpace(request.TaskID) != "" {
			var exists int
			err = tx.QueryRowContext(
				c.Request.Context(),
				`SELECT 1 FROM tasks WHERE id = ?`,
				request.TaskID,
			).Scan(&exists)
			if errors.Is(err, sql.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{
					"error": "task not found",
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
			`INSERT INTO events (
				task_id, event_type, summary, agent
			) VALUES (?, ?, ?, ?)`,
			nullIfEmpty(request.TaskID),
			request.EventType,
			request.Summary,
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

// listEventsHandler handles GET /v1/events.
//
// Optional query params:
//
//	task_id — filter to events for one task (NULL task_id events excluded)
//	limit   — cap at 100, default 50
//
// Events are returned newest-first (ORDER BY created_at DESC).
func listEventsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		taskID := strings.TrimSpace(c.Query("task_id"))

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
			SELECT id, task_id, event_type, summary, agent, created_at
			FROM events
		`
		args := []any{}

		if taskID != "" {
			query += ` WHERE task_id = ?`
			args = append(args, taskID)
		}

		query += ` ORDER BY created_at DESC LIMIT ?`
		args = append(args, limit)

		rows, err := db.QueryContext(c.Request.Context(), query, args...)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()

		events := make([]model.Event, 0)

		for rows.Next() {
			var event model.Event
			var taskID sql.NullString
			var agent sql.NullString

			if err := rows.Scan(
				&event.ID,
				&taskID,
				&event.EventType,
				&event.Summary,
				&agent,
				&event.CreatedAt,
			); err != nil {
				internalError(c, err)
				return
			}
			event.TaskID = taskID.String
			event.Agent = agent.String
			events = append(events, event)
		}

		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"events": events})
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
