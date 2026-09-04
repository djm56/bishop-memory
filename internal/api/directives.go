// Package api — HTTP handlers for /v1/directives (read-only).
//
// Directives are binding, human-ratified rules (mirrors reference/DIRECTIVES.md).
// The API exposes read access only. No POST, PATCH, or DELETE route exists.
// This is by design: agents must have no mechanism to write, update, or delete a directive.
// The absence of these routes is the guarantee.
package api

import (
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/model"
)

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
