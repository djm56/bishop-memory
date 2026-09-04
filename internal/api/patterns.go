// Package api — HTTP handlers for /v1/patterns.
//
// Patterns are advisory patterns (mirrors .claude/memory/findings/PATTERNS.md).
// Non-binding; directives win on any conflict. No update routes needed.
package api

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/model"
)

// listPatternsHandler handles GET /v1/patterns.
// Returns patterns newest-first (ORDER BY id DESC).
func listPatternsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := `
			SELECT id, name, context, solution, example, discovered_at, discovered_mission, created_at
			FROM patterns
			ORDER BY id DESC
		`

		rows, err := db.QueryContext(c.Request.Context(), query)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()

		patterns := make([]model.Pattern, 0)

		for rows.Next() {
			pattern, err := scanPattern(rows)
			if err != nil {
				internalError(c, err)
				return
			}
			patterns = append(patterns, pattern)
		}

		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"patterns": patterns})
	}
}

// createPatternHandler handles POST /v1/patterns.
// Creates a new pattern. No status field; name is required.
// Audit row appended to flight_recorder in the same transaction.
func createPatternHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request model.CreatePatternRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		// Trim free-form string fields for consistency.
		request.Name = strings.TrimSpace(request.Name)
		request.Context = strings.TrimSpace(request.Context)
		request.Solution = strings.TrimSpace(request.Solution)
		request.Example = strings.TrimSpace(request.Example)
		request.DiscoveredAt = strings.TrimSpace(request.DiscoveredAt)
		request.DiscoveredMission = strings.TrimSpace(request.DiscoveredMission)

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		result, err := tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO patterns (
				name, context, solution, example, discovered_at, discovered_mission
			) VALUES (?, ?, ?, ?, ?, ?)`,
			request.Name,
			nullIfEmpty(request.Context),
			nullIfEmpty(request.Solution),
			nullIfEmpty(request.Example),
			nullIfEmpty(request.DiscoveredAt),
			nullIfEmpty(request.DiscoveredMission),
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
		_, err = tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO flight_recorder (
				event, note
			) VALUES (?, ?)`,
			"pattern.created",
			"Pattern created",
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

func scanPattern(row scanner) (model.Pattern, error) {
	var pattern model.Pattern
	var context sql.NullString
	var solution sql.NullString
	var example sql.NullString
	var discoveredAt sql.NullString
	var discoveredMission sql.NullString

	err := row.Scan(
		&pattern.ID,
		&pattern.Name,
		&context,
		&solution,
		&example,
		&discoveredAt,
		&discoveredMission,
		&pattern.CreatedAt,
	)

	if context.Valid {
		pattern.Context = &context.String
	}
	if solution.Valid {
		pattern.Solution = &solution.String
	}
	if example.Valid {
		pattern.Example = &example.String
	}
	if discoveredAt.Valid {
		pattern.DiscoveredAt = &discoveredAt.String
	}
	if discoveredMission.Valid {
		pattern.DiscoveredMission = &discoveredMission.String
	}

	return pattern, err
}
