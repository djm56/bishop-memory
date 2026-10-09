// Package api — the mission HUD's read routes (docs/plans/MISSION-HUD-PLAN.md §4).
//
//	GET /v1/hud/missions      the mission board: every mission with counts
//	GET /v1/hud/missions/:id  everything recorded about one mission
//
// Both are read-only and return plain rows keyed by column name, so the page
// can show a column without a model change. A mission's findings come from
// GET /v1/findings?mission_id=, which already carries each finding's triage
// classification and recommendation in the shape the triage page uses.
package api

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/importer"
)

// crewNameSQL normalises an agent column the way importer.CrewName does:
// "@Hicks", "hicks" and "harness:hicks" all become "hicks".
const crewNameSQL = `lower(replace(CASE WHEN instr(agent, ':') > 0
	THEN substr(agent, instr(agent, ':') + 1) ELSE agent END, '@', ''))`

// hudMissionsHandler handles GET /v1/hud/missions.
//
// Filters (all optional, AND-ed): harness, status, outcome, and q — matched
// against the mission id and title, and as a full-text search over the
// mission's brief, progress and debrief. Newest first.
func hudMissionsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		where := []string{}
		args := []any{}
		for _, column := range []string{"harness", "status", "outcome"} {
			if v := strings.TrimSpace(c.Query(column)); v != "" {
				where = append(where, "m."+column+" = ?")
				args = append(args, v)
			}
		}
		if q := strings.TrimSpace(c.Query("q")); q != "" {
			like := "%" + q + "%"
			clause := "m.id LIKE ? OR m.title LIKE ?"
			args = append(args, like, like)
			if match, ok := sanitizeFTS5Query(q); ok && match != "" {
				clause += ` OR m.id IN (SELECT d.mission_id FROM documents_fts
					JOIN documents d ON d.id = documents_fts.rowid
					WHERE documents_fts MATCH ? AND d.mission_id IS NOT NULL)`
				args = append(args, match)
			}
			where = append(where, "("+clause+")")
		}

		query := `
			SELECT m.id, m.title, m.status, m.outcome, m.harness, m.owner, m.priority,
			       m.opened_at, m.closed_at, m.updated_at,
			       (SELECT COUNT(*) FROM mission_steps s WHERE s.mission_id = m.id) AS steps,
			       (SELECT COUNT(*) FROM mission_steps s WHERE s.mission_id = m.id AND s.status = 'done') AS steps_done,
			       (SELECT COUNT(*) FROM findings f WHERE f.mission_id = m.id) AS findings,
			       (SELECT COUNT(*) FROM patterns p WHERE p.discovered_mission = m.id) AS patterns,
			       EXISTS (SELECT 1 FROM documents d WHERE d.mission_id = m.id AND d.kind = 'brief') AS has_brief,
			       EXISTS (SELECT 1 FROM documents d WHERE d.mission_id = m.id AND d.kind = 'debrief') AS has_debrief
			  FROM missions m`
		if len(where) > 0 {
			query += " WHERE " + strings.Join(where, " AND ")
		}
		query += " ORDER BY m.opened_at DESC, m.id DESC"

		missions, err := queryMaps(c.Request.Context(), db, query, args...)
		if err != nil {
			internalError(c, err)
			return
		}
		harnesses, err := queryMaps(c.Request.Context(), db,
			`SELECT harness AS name, COUNT(*) AS missions FROM missions
			  WHERE harness IS NOT NULL GROUP BY harness ORDER BY harness`)
		if err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"missions": missions, "harnesses": harnesses})
	}
}

// hudMissionHandler handles GET /v1/hud/missions/:missionID.
//
// It returns the mission row; its steps (in step order) and flight-recorder
// events; its brief, progress and debrief bodies; the patterns it discovered;
// directives ratified from proposals whose evidence includes its findings;
// the crew rows and service records of the agents that ran its steps, the
// records limited to the mission's dates.
func hudMissionHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		id := c.Param("missionID")

		missions, err := queryMaps(ctx, db, `SELECT * FROM missions WHERE id = ?`, id)
		if err != nil {
			internalError(c, err)
			return
		}
		if len(missions) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "mission not found"})
			return
		}

		out := gin.H{"mission": missions[0]}
		queries := []struct {
			key   string
			query string
		}{
			{"steps", `SELECT step, phase, agent, status, notes, summary, started_at, ended_at, created_at
			             FROM mission_steps WHERE mission_id = ?
			            ORDER BY CAST(step AS INTEGER), step`},
			{"events", `SELECT id, step, agent, event, note, occurred_at, created_at
			              FROM flight_recorder WHERE mission_id = ? ORDER BY id`},
			{"documents", `SELECT kind, title, body, source_path, updated_at
			                 FROM documents WHERE mission_id = ? AND kind IN ('brief', 'progress', 'debrief')`},
			{"patterns", `SELECT id, name, context, solution, example, discovered_at
			                FROM patterns WHERE discovered_mission = ? ORDER BY id`},
			{"directives", `SELECT p.id AS proposal_id, p.directive_id, p.title, p.rule, p.decided_at,
			                       d.ratified_at
			                  FROM directive_proposals p
			                  LEFT JOIN directives d ON d.directive_id = p.directive_id
			                 WHERE p.state = 'accepted'
			                   AND EXISTS (SELECT 1 FROM json_each(p.evidence) e
			                               JOIN findings f ON f.id = e.value
			                              WHERE f.mission_id = ?)
			                 ORDER BY p.directive_id`},
		}
		for _, q := range queries {
			rows, err := queryMaps(ctx, db, q.query, id)
			if err != nil {
				internalError(c, err)
				return
			}
			out[q.key] = rows
		}

		// The crew of this mission: every agent named on a step.
		names := []any{}
		seen := map[string]bool{}
		for _, step := range out["steps"].([]map[string]any) {
			agent, _ := step["agent"].(string)
			if name := importer.CrewName(agent); name != "" && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
		out["crew"] = []map[string]any{}
		out["service_records"] = []map[string]any{}
		if len(names) > 0 {
			in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(names)), ",") + ")"
			crew, err := queryMaps(ctx, db,
				`SELECT name, role, description FROM crew WHERE name IN `+in+` ORDER BY name`, names...)
			if err != nil {
				internalError(c, err)
				return
			}
			out["crew"] = crew

			mission := missions[0]
			records, err := queryMaps(ctx, db,
				`SELECT id, agent, record_date, title, note, adjustment, source
				   FROM service_records
				  WHERE `+crewNameSQL+` IN `+in+`
				    AND record_date >= date(?) AND record_date <= date(COALESCE(?, 'now'))
				  ORDER BY record_date, id`,
				append(names, mission["opened_at"], mission["closed_at"])...)
			if err != nil {
				internalError(c, err)
				return
			}
			out["service_records"] = records
		}

		c.JSON(http.StatusOK, out)
	}
}

// queryMaps runs a read query and returns each row as a map keyed by column
// name. TEXT comes back as string, NULL as nil.
func queryMaps(ctx context.Context, db *sql.DB, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			if b, ok := values[i].([]byte); ok {
				row[column] = string(b)
			} else {
				row[column] = values[i]
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
