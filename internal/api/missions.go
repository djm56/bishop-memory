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

// missionStatuses is the shared, single-source-of-truth value set for the
// `status` column. It mirrors the `oneof` binding tags on
// model.CreateMissionRequest / model.UpdateMissionRequest and the CHECK
// constraint in db/schema.sql (CONV-028: one implementation per
// contract) — listMissionsHandler's optional ?status= filter validates
// against missionStatuses instead of re-listing the four literals inline.
// There is no equivalent ?priority= filter today, so no missionPriorities
// counterpart is declared until one exists to consume it.
var missionStatuses = []string{"not-started", "in-progress", "blocked", "complete"}

// stepStatuses is the valid status values for mission steps.
// Validates the optional status field on step-creation requests.
var stepStatuses = []string{"pending", "in-progress", "done", "failed"}

// isOneOf reports whether value is present in allowed.
func isOneOf(value string, allowed []string) bool {
	for _, v := range allowed {
		if value == v {
			return true
		}
	}
	return false
}

func listMissionsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		status := strings.TrimSpace(c.Query("status"))

		// Step 4 review fix (Review B, WARNING W3): validate the optional
		// filter against the same enum the CHECK constraint and the
		// oneof binding tags already enforce on write, so a typo'd
		// filter value (e.g. "?status=complet") gets a 400 telling the
		// caller their filter is invalid, rather than a silent 200
		// with an empty "missions" list indistinguishable from "no missions
		// match".
		if status != "" && !isOneOf(status, missionStatuses) {
			validationError(c, errors.New("status must be one of: not-started, in-progress, blocked, complete"))
			return
		}

		query := `
			SELECT id, title, status, outcome, owner, priority, next_action, blockers,
			       opened_at, closed_at, created_at, updated_at
			FROM missions
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

		missions := make([]model.Mission, 0)

		for rows.Next() {
			mission, err := scanMission(rows)
			if err != nil {
				internalError(c, err)
				return
			}
			missions = append(missions, mission)
		}

		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"missions": missions})
	}
}

func getMissionHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		missionID := c.Param("missionID")

		row := db.QueryRowContext(
			c.Request.Context(),
			`SELECT id, title, status, outcome, owner, priority, next_action, blockers,
			        opened_at, closed_at, created_at, updated_at
			 FROM missions
			 WHERE id = ?`,
			missionID,
		)

		mission, err := scanMission(row)
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

		c.JSON(http.StatusOK, mission)
	}
}

func createMissionHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request model.CreateMissionRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		request.ID = strings.TrimSpace(request.ID)
		request.Title = strings.TrimSpace(request.Title)

		if request.Status == "" {
			request.Status = "not-started"
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
			`INSERT INTO missions (
				id, title, status, owner, priority, next_action, blockers
			) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			request.ID,
			request.Title,
			request.Status,
			nullIfEmpty(request.Owner),
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
			`INSERT INTO flight_recorder (
				mission_id, event, note
			) VALUES (?, 'mission.created', ?)`,
			request.ID,
			"Mission created: "+request.Title,
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

// updateMissionHandler handles PATCH /v1/missions/:missionID. The outcome field is
// independently settable and is NOT required when status becomes "complete".
// The harness sets them at different moments — CURRENT-MISSION.md goes to
// "complete" at one step and MISSION-ARCHIVE.md records the outcome at a later
// one — so a mission may legitimately sit at status="complete" with outcome=NULL
// for a period. This API mirrors that behaviour.
func updateMissionHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		missionID := c.Param("missionID")

		var request model.UpdateMissionRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		// Step 4 review fix (Review B, WARNING W2): createMissionHandler
		// normalises next_action/blockers via nullIfEmpty (a
		// whitespace-only value stores NULL). updateMissionHandler had no
		// equivalent, so a PATCH of {"next_action": "   "} stored three
		// literal spaces instead of NULL — the same conceptual field
		// behaving differently across the two write paths. Trim ONLY
		// when the pointer is non-nil: that preserves the nil-vs-non-nil
		// distinction (omitted vs. explicitly present) the COALESCE
		// pattern depends on for "clear the field" semantics (CONV-030)
		// — we are not replacing an absent value with anything, only
		// normalising a present one before it is written.
		if request.Owner != nil {
			trimmed := strings.TrimSpace(*request.Owner)
			request.Owner = &trimmed
		}
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
			`UPDATE missions
			 SET
				status = COALESCE(?, status),
				owner = COALESCE(?, owner),
				outcome = COALESCE(?, outcome),
				priority = COALESCE(?, priority),
				next_action = COALESCE(?, next_action),
				blockers = COALESCE(?, blockers),
				closed_at = CASE
					WHEN ? = 'complete' THEN CURRENT_TIMESTAMP
					ELSE closed_at
				END,
				updated_at = CURRENT_TIMESTAMP
			 WHERE id = ?`,
			request.Status,
			request.Owner,
			request.Outcome,
			request.Priority,
			request.NextAction,
			request.Blockers,
			request.Status,
			missionID,
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
				"error": "mission not found",
			})
			return
		}

		_, err = tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO flight_recorder (
				mission_id, event, note
			) VALUES (?, 'mission.updated', 'Mission updated')`,
			missionID,
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
			"id":      missionID,
			"updated": true,
		})
	}
}

type scanner interface {
	Scan(dest ...any) error
}

func scanMission(row scanner) (model.Mission, error) {
	var mission model.Mission

	err := row.Scan(
		&mission.ID,
		&mission.Title,
		&mission.Status,
		&mission.Outcome,
		&mission.Owner,
		&mission.Priority,
		&mission.NextAction,
		&mission.Blockers,
		&mission.OpenedAt,
		&mission.ClosedAt,
		&mission.CreatedAt,
		&mission.UpdatedAt,
	)

	return mission, err
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	return value
}

// missionStepRequest is the JSON body for POST /v1/missions/:missionID/steps. It is
// intentionally unexported and local to missions.go; agent is free-form
// (agents register dynamically) and status must be one of the validated
// step-status values to match the database CHECK constraint.
type missionStepRequest struct {
	Step      string  `json:"step" binding:"omitempty,max=16"`
	Phase     string  `json:"phase" binding:"omitempty,max=64"`
	Agent     string  `json:"agent" binding:"omitempty,max=128"`
	Status    string  `json:"status" binding:"omitempty,oneof=pending in-progress done failed"`
	Notes     string  `json:"notes" binding:"omitempty,max=2000"`
	Summary   string  `json:"summary" binding:"omitempty,max=2000"`
	StartedAt *string `json:"started_at,omitempty"`
	EndedAt   *string `json:"ended_at,omitempty"`
}

// createMissionStepHandler handles POST /v1/missions/:missionID/steps. It records
// one agent execution attempt against a mission: it verifies the mission exists
// (clean 404 rather than surfacing an FK violation), inserts into
// mission_steps, appends a mission.step event for the audit trail, and commits.
//
// The transaction mirrors createMissionHandler / updateMissionHandler so the
// step + event are atomic.
func createMissionStepHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		missionID := c.Param("missionID")

		var request missionStepRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		// Trim free-form string fields consistently with
		// createMissionHandler (which trims ID / Title) and appendFlightRecorderHandler
		// (which trims event / note). agent and status are
		// free-form so we don't enforce non-empty here, but we DO strip
		// surrounding whitespace so "opencode " and "opencode" are stored
		// identically and a stray " " agent name doesn't sneak through.
		request.Step = strings.TrimSpace(request.Step)
		request.Phase = strings.TrimSpace(request.Phase)
		request.Agent = strings.TrimSpace(request.Agent)
		request.Status = strings.TrimSpace(request.Status)
		request.Notes = strings.TrimSpace(request.Notes)
		request.Summary = strings.TrimSpace(request.Summary)

		// Step 4 fix (CRITICAL 2): convert empty optional pointer fields to nil
		// for database storage. status is CHECK-constrained to NULL or
		// a valid enum, so "" fails the CHECK and returns 500; agent,
		// summary, started_at, and ended_at have no CHECK but are the
		// same defect class (should be NULL, not empty string). String fields
		// are handled via nullIfEmpty in the INSERT; pointers require explicit
		// nil conversion.
		if request.StartedAt != nil && strings.TrimSpace(*request.StartedAt) == "" {
			request.StartedAt = nil
		}
		if request.EndedAt != nil && strings.TrimSpace(*request.EndedAt) == "" {
			request.EndedAt = nil
		}

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		// Verify the mission exists so an unknown mission yields a clean 404
		// rather than an FK-violation error from the INSERT below.
		var exists int
		err = tx.QueryRowContext(
			c.Request.Context(),
			`SELECT 1 FROM missions WHERE id = ?`,
			missionID,
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

		_, err = tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO mission_steps (
				mission_id, step, phase, agent, status, notes, started_at, ended_at, summary
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			missionID,
			nullIfEmpty(request.Step),
			nullIfEmpty(request.Phase),
			nullIfEmpty(request.Agent),
			nullIfEmpty(request.Status),
			nullIfEmpty(request.Notes),
			request.StartedAt,
			request.EndedAt,
			nullIfEmpty(request.Summary),
		)
		if err != nil {
			internalError(c, err)
			return
		}

		_, err = tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO flight_recorder (
				mission_id, event, note
			) VALUES (?, 'mission.step', ?)`,
			missionID,
			"Mission step recorded",
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
			"mission_id": missionID,
			"recorded":   true,
		})
	}
}

// listMissionStepsHandler handles GET /v1/missions/:missionID/steps. It returns
// all steps for a mission in PROGRESS.md order (insertion order, ORDER BY id ASC).
// Returns 404 if the mission does not exist.
func listMissionStepsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		missionID := c.Param("missionID")

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		// Verify the mission exists so an unknown mission yields a clean 404.
		var exists int
		err = tx.QueryRowContext(
			c.Request.Context(),
			`SELECT 1 FROM missions WHERE id = ?`,
			missionID,
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

		rows, err := tx.QueryContext(
			c.Request.Context(),
			`SELECT id, mission_id, step, phase, agent, status, notes, started_at, ended_at, summary, created_at
			 FROM mission_steps
			 WHERE mission_id = ?
			 ORDER BY id ASC`,
			missionID,
		)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()

		steps := make([]model.MissionStep, 0)

		for rows.Next() {
			var step model.MissionStep
			var stepVal sql.NullString
			var phaseVal sql.NullString
			var agentVal sql.NullString
			var statusVal sql.NullString
			var notesVal sql.NullString
			var summaryVal sql.NullString

			if err := rows.Scan(
				&step.ID,
				&step.MissionID,
				&stepVal,
				&phaseVal,
				&agentVal,
				&statusVal,
				&notesVal,
				&step.StartedAt,
				&step.EndedAt,
				&summaryVal,
				&step.CreatedAt,
			); err != nil {
				internalError(c, err)
				return
			}

			if stepVal.Valid {
				step.Step = &stepVal.String
			}
			if phaseVal.Valid {
				step.Phase = &phaseVal.String
			}
			if agentVal.Valid {
				step.Agent = &agentVal.String
			}
			if statusVal.Valid {
				step.Status = &statusVal.String
			}
			if notesVal.Valid {
				step.Notes = &notesVal.String
			}
			if summaryVal.Valid {
				step.Summary = &summaryVal.String
			}

			steps = append(steps, step)
		}

		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}

		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"steps": steps})
	}
}
