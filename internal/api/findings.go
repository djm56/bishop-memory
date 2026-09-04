// Package api — HTTP handlers for /v1/findings.
//
// Findings are the improvement ledger (mirrors .claude/memory/findings/FINDINGS.md).
// Status, approver, and date_approved are human-only and never settable through the API.
// No PATCH or PUT route exists — agents have no mechanism to update a finding's status.
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/model"
)

// findingStatuses is the valid status values for findings.
// Matches the CHECK constraint in db/schema.sql.
var findingStatuses = []string{"proposed", "approved", "applied", "rejected", "retired", "superseded"}

// listFindingsHandler handles GET /v1/findings?status=<optional>.
// Returns findings newest-first (ORDER BY id DESC).
// The optional status filter validates against findingStatuses and returns 400 on invalid input.
func listFindingsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		status := strings.TrimSpace(c.Query("status"))

		// Validate the optional status filter against the same enum
		// the CHECK constraint and schema define.
		if status != "" && !isOneOf(status, findingStatuses) {
			validationError(c, errors.New("status must be one of: proposed, approved, applied, rejected, retired, superseded"))
			return
		}

		query := `
			SELECT id, finding_date, target, suggestion, rationale, status,
			       approver, date_approved, mission_id, created_at
			FROM findings
		`
		args := []any{}

		if status != "" {
			query += ` WHERE status = ?`
			args = append(args, status)
		}

		query += ` ORDER BY id DESC`

		rows, err := db.QueryContext(c.Request.Context(), query, args...)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()

		findings := make([]model.Finding, 0)

		for rows.Next() {
			finding, err := scanFinding(rows)
			if err != nil {
				internalError(c, err)
				return
			}
			findings = append(findings, finding)
		}

		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"findings": findings})
	}
}

// createFindingHandler handles POST /v1/findings.
// Creates a new finding with status='proposed' (always; never settable by the caller).
// Approver and date_approved are also never accepted; they belong to the human operator.
// Audit row appended to flight_recorder in the same transaction.
func createFindingHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request model.CreateFindingRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		// Trim free-form string fields for consistency.
		request.FindingDate = strings.TrimSpace(request.FindingDate)
		request.Target = strings.TrimSpace(request.Target)
		request.Suggestion = strings.TrimSpace(request.Suggestion)
		request.Rationale = strings.TrimSpace(request.Rationale)
		request.MissionID = strings.TrimSpace(request.MissionID)

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		// Note: mission_id has NO foreign key constraint in the schema.
		// A finding outlives mission-folder cleanup, so we store what we are given
		// without verifying it exists.

		result, err := tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO findings (
				finding_date, target, suggestion, rationale, mission_id
			) VALUES (?, ?, ?, ?, ?)`,
			nullIfEmpty(request.FindingDate),
			nullIfEmpty(request.Target),
			request.Suggestion,
			nullIfEmpty(request.Rationale),
			nullIfEmpty(request.MissionID),
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

		// Audit row: leave mission_id, step, occurred_at NULL.
		// flight_recorder.mission_id has a real foreign key to missions(id),
		// while findings.mission_id does not. Copying an unverified findings.mission_id
		// across would turn a legitimate finding into a foreign-key failure.
		_, err = tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO flight_recorder (
				event, note
			) VALUES (?, ?)`,
			"finding.created",
			"Finding created",
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
			"id":      id,
			"created": true,
		})
	}
}

func scanFinding(row scanner) (model.Finding, error) {
	var finding model.Finding
	var findingDate sql.NullString
	var target sql.NullString
	var rationale sql.NullString
	var approver sql.NullString
	var dateApproved sql.NullString
	var missionID sql.NullString

	err := row.Scan(
		&finding.ID,
		&findingDate,
		&target,
		&finding.Suggestion,
		&rationale,
		&finding.Status,
		&approver,
		&dateApproved,
		&missionID,
		&finding.CreatedAt,
	)

	if findingDate.Valid {
		finding.FindingDate = &findingDate.String
	}
	if target.Valid {
		finding.Target = &target.String
	}
	if rationale.Valid {
		finding.Rationale = &rationale.String
	}
	if approver.Valid {
		finding.Approver = &approver.String
	}
	if dateApproved.Valid {
		finding.DateApproved = &dateApproved.String
	}
	if missionID.Valid {
		finding.MissionID = &missionID.String
	}

	return finding, err
}
