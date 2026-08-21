package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"bishop-memory/internal/model"
)

// taskStatuses is the shared, single-source-of-truth value set for the
// `status` column. It mirrors the `oneof` binding tags on
// model.CreateTaskRequest / model.UpdateTaskRequest and the CHECK
// constraint in db/schema.sql (CONV-028: one implementation per
// contract) — listTasksHandler's optional ?status= filter validates
// against taskStatuses instead of re-listing the five literals inline.
// There is no equivalent ?priority= filter today, so no taskPriorities
// counterpart is declared until one exists to consume it.
var taskStatuses = []string{"open", "active", "blocked", "complete", "cancelled"}

// isOneOf reports whether value is present in allowed.
func isOneOf(value string, allowed []string) bool {
	for _, v := range allowed {
		if value == v {
			return true
		}
	}
	return false
}

func listTasksHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		status := strings.TrimSpace(c.Query("status"))

		// Step 4 review fix (Review B, WARNING W3): validate the optional
		// filter against the same enum the CHECK constraint and the
		// oneof binding tags already enforce on write, so a typo'd
		// filter value (e.g. "?status=complet") gets a 400 telling the
		// caller their filter is invalid, rather than a silent 200
		// with an empty "tasks" list indistinguishable from "no tasks
		// match".
		if status != "" && !isOneOf(status, taskStatuses) {
			validationError(c, errors.New("status must be one of: open, active, blocked, complete, cancelled"))
			return
		}

		query := `
			SELECT id, title, status, priority, next_action, blockers,
			       opened_at, closed_at, created_at, updated_at
			FROM tasks
		`
		args := []any{}

		if status != "" {
			query += ` WHERE status = ?`
			args = append(args, status)
		}

		query += ` ORDER BY updated_at DESC`

		rows, err := db.QueryContext(c.Request.Context(), query, args...)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()

		tasks := make([]model.Task, 0)

		for rows.Next() {
			task, err := scanTask(rows)
			if err != nil {
				internalError(c, err)
				return
			}
			tasks = append(tasks, task)
		}

		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"tasks": tasks})
	}
}

func getTaskHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		taskID := c.Param("taskID")

		row := db.QueryRowContext(
			c.Request.Context(),
			`SELECT id, title, status, priority, next_action, blockers,
			        opened_at, closed_at, created_at, updated_at
			 FROM tasks
			 WHERE id = ?`,
			taskID,
		)

		task, err := scanTask(row)
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

		c.JSON(http.StatusOK, task)
	}
}

func createTaskHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request model.CreateTaskRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		request.ID = strings.TrimSpace(request.ID)
		request.Title = strings.TrimSpace(request.Title)

		if request.Status == "" {
			request.Status = "open"
		}
		if request.Priority == "" {
			request.Priority = "normal"
		}

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		_, err = tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO tasks (
				id, title, status, priority, next_action, blockers
			) VALUES (?, ?, ?, ?, ?, ?)`,
			request.ID,
			request.Title,
			request.Status,
			request.Priority,
			nullIfEmpty(request.NextAction),
			nullIfEmpty(request.Blockers),
		)
		if err != nil {
			// Step 4 review fix (Review B, CRITICAL C2): only a genuine
			// PRIMARY KEY / UNIQUE collision on the submitted id is a
			// client-fixable 409 ("pick a different id and retry").
			// Anything else (disk full, database locked, an unexpected
			// constraint violation) is a real internal fault and MUST
			// go through internalError — a 409 for those actively
			// misleads an autonomous agent caller about what happened
			// and masks an operational problem as a semantic conflict.
			var sqliteErr *sqlite.Error
			if errors.As(err, &sqliteErr) &&
				(sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE ||
					sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
				c.JSON(http.StatusConflict, gin.H{
					"error": "task could not be created",
				})
				return
			}
			internalError(c, err)
			return
		}

		_, err = tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO events (
				task_id, event_type, summary
			) VALUES (?, 'task.created', ?)`,
			request.ID,
			"Task created: "+request.Title,
		)
		if err != nil {
			internalError(c, err)
			return
		}

		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"id":      request.ID,
			"created": true,
		})
	}
}

func updateTaskHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		taskID := c.Param("taskID")

		var request model.UpdateTaskRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		// Step 4 review fix (Review B, WARNING W2): createTaskHandler
		// normalises next_action/blockers via nullIfEmpty (a
		// whitespace-only value stores NULL). updateTaskHandler had no
		// equivalent, so a PATCH of {"next_action": "   "} stored three
		// literal spaces instead of NULL — the same conceptual field
		// behaving differently across the two write paths. Trim ONLY
		// when the pointer is non-nil: that preserves the nil-vs-non-nil
		// distinction (omitted vs. explicitly present) the COALESCE
		// pattern depends on for "clear the field" semantics (CONV-030)
		// — we are not replacing an absent value with anything, only
		// normalising a present one before it is written.
		if request.NextAction != nil {
			trimmed := strings.TrimSpace(*request.NextAction)
			request.NextAction = &trimmed
		}
		if request.Blockers != nil {
			trimmed := strings.TrimSpace(*request.Blockers)
			request.Blockers = &trimmed
		}

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		result, err := tx.ExecContext(
			c.Request.Context(),
			`UPDATE tasks
			 SET
				status = COALESCE(?, status),
				priority = COALESCE(?, priority),
				next_action = COALESCE(?, next_action),
				blockers = COALESCE(?, blockers),
				closed_at = CASE
					WHEN ? IN ('complete', 'cancelled') THEN CURRENT_TIMESTAMP
					ELSE closed_at
				END,
				updated_at = CURRENT_TIMESTAMP
			 WHERE id = ?`,
			request.Status,
			request.Priority,
			request.NextAction,
			request.Blockers,
			request.Status,
			taskID,
		)
		if err != nil {
			internalError(c, err)
			return
		}

		affected, err := result.RowsAffected()
		if err != nil {
			internalError(c, err)
			return
		}
		if affected == 0 {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "task not found",
			})
			return
		}

		_, err = tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO events (
				task_id, event_type, summary
			) VALUES (?, 'task.updated', 'Task updated')`,
			taskID,
		)
		if err != nil {
			internalError(c, err)
			return
		}

		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"id":      taskID,
			"updated": true,
		})
	}
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(row scanner) (model.Task, error) {
	var task model.Task

	err := row.Scan(
		&task.ID,
		&task.Title,
		&task.Status,
		&task.Priority,
		&task.NextAction,
		&task.Blockers,
		&task.OpenedAt,
		&task.ClosedAt,
		&task.CreatedAt,
		&task.UpdatedAt,
	)

	return task, err
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	return value
}

// taskRunRequest is the JSON body for POST /v1/tasks/:taskID/runs. It is
// intentionally unexported and local to tasks.go; agent and status are
// free-form at this stage (agents register dynamically and the run-status
// vocabulary will tighten once the Phase 3 renderer lands).
type taskRunRequest struct {
	Agent     string  `json:"agent" binding:"omitempty,max=128"`
	Status    string  `json:"status" binding:"omitempty,max=64"`
	Summary   string  `json:"summary" binding:"omitempty,max=2000"`
	StartedAt *string `json:"started_at,omitempty"`
	EndedAt   *string `json:"ended_at,omitempty"`
}

// createTaskRunHandler handles POST /v1/tasks/:taskID/runs. It records
// one agent execution attempt against a task: it verifies the task exists
// (clean 404 rather than surfacing an FK violation), inserts into
// task_runs, appends a task.run event for the audit trail, and commits.
//
// The transaction mirrors createTaskHandler / updateTaskHandler so the
// run + event are atomic.
func createTaskRunHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		taskID := c.Param("taskID")

		var request taskRunRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		// Trim free-form string fields consistently with
		// createTaskHandler (which trims ID / Title) and appendEventHandler
		// (which trims event_type / summary). agent and status are
		// free-form so we don't enforce non-empty here, but we DO strip
		// surrounding whitespace so "opencode " and "opencode" are stored
		// identically and a stray " " agent name doesn't sneak through.
		request.Agent = strings.TrimSpace(request.Agent)
		request.Status = strings.TrimSpace(request.Status)
		request.Summary = strings.TrimSpace(request.Summary)

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		// Verify the task exists so an unknown task yields a clean 404
		// rather than an FK-violation error from the INSERT below.
		var exists int
		err = tx.QueryRowContext(
			c.Request.Context(),
			`SELECT 1 FROM tasks WHERE id = ?`,
			taskID,
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

		_, err = tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO task_runs (
				task_id, agent, status, started_at, ended_at, summary
			) VALUES (?, ?, ?, ?, ?, ?)`,
			taskID,
			request.Agent,
			request.Status,
			request.StartedAt,
			request.EndedAt,
			request.Summary,
		)
		if err != nil {
			internalError(c, err)
			return
		}

		_, err = tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO events (
				task_id, event_type, summary
			) VALUES (?, 'task.run', ?)`,
			taskID,
			"Task run recorded",
		)
		if err != nil {
			internalError(c, err)
			return
		}

		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"task_id":  taskID,
			"recorded": true,
		})
	}
}
