// Package api — HTTP handlers for /v1/directives.
//
// Directives are binding, human-ratified rules (mirrors reference/DIRECTIVES.md).
// Agents read only: no mcpd profile registers a write tool for this table. The
// two writers are both operator-side — the reconciler mirroring DIRECTIVES.md
// through PUT /v1/directives/:directiveID, and the directive-proposal decision
// route in triage.go, which is the ratification act itself.
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/model"
)

// upsertDirectiveHandler handles PUT /v1/directives/:directiveID — the
// reconciler's mirror of one DIRECTIVES.md entry, keyed on its DIR-NNN id.
// Idempotent. Not registered in any mcpd profile.
func upsertDirectiveHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		directiveID := strings.TrimSpace(c.Param("directiveID"))
		if !directiveIDPattern.MatchString(directiveID) {
			validationError(c, errors.New("directive id must have the form DIR-NNN"))
			return
		}
		var request model.UpsertDirectiveRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		if _, err := db.ExecContext(c.Request.Context(),
			`INSERT INTO directives (directive_id, title, rule, rationale, ratified_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(directive_id) DO UPDATE SET title = excluded.title, rule = excluded.rule,
			   rationale = excluded.rationale, ratified_at = excluded.ratified_at`,
			directiveID, strings.TrimSpace(request.Title), strings.TrimSpace(request.Rule),
			nullIfEmpty(request.Rationale), nullIfEmpty(request.RatifiedAt)); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"directive_id": directiveID, "upserted": true})
	}
}

// listDirectivesHandler handles GET /v1/directives.
// Returns directives in order (ORDER BY id ASC). Directives are a rulebook,
// read in rule order, not a feed.
func listDirectivesHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := `
			SELECT id, directive_id, title, rule, rationale, ratified_at, created_at
			FROM directives
			ORDER BY id ASC
		`

		rows, err := db.QueryContext(c.Request.Context(), query)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()

		directives := make([]model.Directive, 0)

		for rows.Next() {
			directive, err := scanDirective(rows)
			if err != nil {
				internalError(c, err)
				return
			}
			directives = append(directives, directive)
		}

		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"directives": directives})
	}
}

func scanDirective(row scanner) (model.Directive, error) {
	var directive model.Directive
	var directiveID sql.NullString
	var title sql.NullString
	var rule sql.NullString
	var rationale sql.NullString
	var ratifiedAt sql.NullString

	err := row.Scan(
		&directive.ID,
		&directiveID,
		&title,
		&rule,
		&rationale,
		&ratifiedAt,
		&directive.CreatedAt,
	)

	if directiveID.Valid {
		directive.DirectiveID = &directiveID.String
	}
	if title.Valid {
		directive.Title = &title.String
	}
	if rule.Valid {
		directive.Rule = &rule.String
	}
	if rationale.Valid {
		directive.Rationale = &rationale.String
	}
	if ratifiedAt.Valid {
		directive.RatifiedAt = &ratifiedAt.String
	}

	return directive, err
}
