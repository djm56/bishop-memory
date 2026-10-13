// Package api — HTTP handlers for the findings-triage surface.
//
// Three kinds of caller use these routes, and the split is enforced by which
// routes mcpd registers, not by authentication (there is none):
//
//   - the triage agents (findings-classifier, findings-processor), through
//     mcpd's triage profile: categories, runs, classifications, groups,
//     recommendations, directive proposals, next-category;
//   - the operator, through the review page and the Makefile: the pending
//     view, the decision routes, categories upsert, runs history;
//   - the reconciler: harnesses upsert.
//
// Nothing here writes findings.status. The decision route for findings lives
// in findings.go; the directive-proposal decision below marks evidence findings
// applied by calling the same transition logic inside its own transaction.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/model"
)

// triageRunStalenessThreshold is the duration after which a run with no recent activity
// is considered stale and eligible for operator-directed closure. Fixed at 30 minutes;
// not configurable. The operator must explicitly mark such a run done or failed.
const triageRunStalenessThreshold = 30 * time.Minute

// triageRunLastActivitySQL is the single definition of a triage run's "last
// activity": the newest of the run's own started_at and every timestamp on a
// row the run wrote (classifications, groups, recommendations, directive
// proposals, and the claim, grade and creation times of mission grades).
// It is a scalar expression over the outer alias `r` (triage_runs r) and is
// shared by operator-close and the runs list so the two routes cannot disagree
// about what counts as activity. Add a new run-linked table here and nowhere
// else.
//
// Plain string MAX is correct only because every operand is written by SQLite's
// CURRENT_TIMESTAMP as 'YYYY-MM-DD HH:MM:SS' UTC; see parseTimestamp.
const triageRunLastActivitySQL = `COALESCE(
	(SELECT MAX(ts) FROM (
		SELECT r.started_at AS ts
		UNION ALL SELECT classified_at FROM finding_triage WHERE run_id = r.id
		UNION ALL SELECT created_at FROM finding_groups WHERE run_id = r.id
		UNION ALL SELECT created_at FROM finding_recommendations WHERE run_id = r.id
		UNION ALL SELECT created_at FROM directive_proposals WHERE run_id = r.id
		UNION ALL SELECT claimed_at FROM mission_grades WHERE run_id = r.id
		UNION ALL SELECT graded_at FROM mission_grades WHERE run_id = r.id
		UNION ALL SELECT created_at FROM mission_grades WHERE run_id = r.id
	) AS times WHERE ts IS NOT NULL),
	r.started_at)`

// parseTimestamp parses a run-activity timestamp. The invariant it rests on:
// every run-activity timestamp is written by SQL CURRENT_TIMESTAMP, which
// yields 'YYYY-MM-DD HH:MM:SS' in UTC, and no Go code writes these columns.
// That uniformity is also what lets triageRunLastActivitySQL use a plain string
// MAX. There is deliberately no RFC3339 branch: such a value would sort wrongly
// against the native format in MAX (a space sorts before 'T'), so it would be
// corruption, and it is reported as a parse error rather than accepted.
func parseTimestamp(ts string) (time.Time, error) {
	t, err := time.Parse("2006-01-02 15:04:05", ts)
	if err != nil {
		return time.Time{}, fmt.Errorf("unable to parse timestamp: %q", ts)
	}
	return t, nil
}

// triageRunIsStale is the single stale test: true only when the run is still
// 'running' and its last activity is at least triageRunStalenessThreshold
// before now. A non-running run is never stale and its timestamp is not
// parsed. An unparseable last-activity value on a running run is returned as
// an error and means corrupt data. The caller decides the response: the
// operator-close route refuses with a 500, the list route degrades the one
// row (see listTriageRunsHandler).
func triageRunIsStale(status, lastActivityAt string, now time.Time) (bool, error) {
	if status != "running" {
		return false, nil
	}
	last, err := parseTimestamp(lastActivityAt)
	if err != nil {
		return false, err
	}
	return now.Sub(last) >= triageRunStalenessThreshold, nil
}

// --- Categories ------------------------------------------------------------

// listFindingCategoriesHandler handles GET /v1/finding-categories.
// Inactive categories are included only with ?include_inactive=1. Each row
// carries the count of proposed findings in it and the count of those with a
// pending recommendation.
func listFindingCategoriesHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := `
			SELECT c.slug, c.name, c.description, c.examples, c.sort_order, c.active,
			       c.last_processed_at, c.created_at,
			       (SELECT COUNT(*) FROM finding_triage t JOIN findings f ON f.id = t.finding_id
			         WHERE t.category = c.slug AND f.status = 'proposed') AS proposed_count,
			       (SELECT COUNT(*) FROM finding_triage t JOIN finding_recommendations r
			         ON r.finding_id = t.finding_id AND r.state = 'pending'
			         WHERE t.category = c.slug) AS pending_count
			FROM finding_categories c
		`
		if c.Query("include_inactive") != "1" {
			query += " WHERE c.active = 1"
		}
		query += " ORDER BY c.sort_order ASC, c.slug ASC"

		rows, err := db.QueryContext(c.Request.Context(), query)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()

		categories := make([]model.FindingCategory, 0)
		for rows.Next() {
			var cat model.FindingCategory
			var examples, lastProcessed sql.NullString
			var active int64
			if err := rows.Scan(&cat.Slug, &cat.Name, &cat.Description, &examples, &cat.SortOrder, &active,
				&lastProcessed, &cat.CreatedAt, &cat.ProposedCount, &cat.PendingCount); err != nil {
				internalError(c, err)
				return
			}
			cat.Active = active != 0
			cat.Examples = nullStringPtr(examples)
			cat.LastProcessedAt = nullStringPtr(lastProcessed)
			categories = append(categories, cat)
		}
		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}

		// The uncategorised bucket is virtual: findings with no triage row.
		var uncategorised int64
		if err := db.QueryRowContext(c.Request.Context(),
			`SELECT COUNT(*) FROM findings f LEFT JOIN finding_triage t ON t.finding_id = f.id
			 WHERE t.finding_id IS NULL AND f.status = 'proposed'`).Scan(&uncategorised); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"categories": categories, "uncategorised_count": uncategorised})
	}
}

var categorySlugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// upsertFindingCategoryHandler handles PUT /v1/finding-categories/:slug.
// Idempotent: the seed script calls it once per category on every run.
func upsertFindingCategoryHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		slug := strings.TrimSpace(c.Param("slug"))
		if !categorySlugPattern.MatchString(slug) || slug == "uncategorised" {
			validationError(c, errors.New("slug must be lower-case kebab-case and not the reserved word uncategorised"))
			return
		}
		var request model.UpsertFindingCategoryRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		active := int64(1)
		if request.Active != nil && !*request.Active {
			active = 0
		}
		result, err := db.ExecContext(c.Request.Context(),
			`INSERT INTO finding_categories (slug, name, description, examples, sort_order, active)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(slug) DO UPDATE SET
			   name = excluded.name, description = excluded.description,
			   examples = excluded.examples, sort_order = excluded.sort_order, active = excluded.active`,
			slug, strings.TrimSpace(request.Name), strings.TrimSpace(request.Description),
			nullIfEmpty(request.Examples), request.SortOrder, active)
		if err != nil {
			internalError(c, err)
			return
		}
		// SQLite reports 1 for an insert and 1 for an update through this
		// path, so "created" is derived from whether the row existed before.
		_ = result
		c.JSON(http.StatusOK, gin.H{"slug": slug, "upserted": true})
	}
}

// --- Runs ------------------------------------------------------------------

// startTriageRunHandler handles POST /v1/triage/runs.
func startTriageRunHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request model.StartTriageRunRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		result, err := db.ExecContext(c.Request.Context(),
			`INSERT INTO triage_runs (kind, category, model) VALUES (?, ?, ?)`,
			request.Kind, nullIfEmpty(request.Category), nullIfEmpty(request.Model))
		if err != nil {
			internalError(c, err)
			return
		}
		id, err := result.LastInsertId()
		if err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"id": id, "created": true})
	}
}

// finishTriageRunHandler handles PATCH /v1/triage/runs/:runID. A finished
// process run stamps its category's last_processed_at, which is what moves
// the nightly rotation forward.
//
// A run that is no longer 'running' is refused with 409 and nothing is
// changed: a late agent call must not overwrite an operator close (status,
// finished_at, the operator's audit note), stamp the rotation, or write a second
// flight row. The guard is the UPDATE's `status = 'running'` plus a
// RowsAffected check, so the stamp below happens only when this call really
// changed the row.
func finishTriageRunHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		runID, err := strconv.ParseInt(c.Param("runID"), 10, 64)
		if err != nil {
			validationError(c, errors.New("run id must be an integer"))
			return
		}
		var request model.FinishTriageRunRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		var kind, status string
		var category sql.NullString
		err = tx.QueryRowContext(c.Request.Context(),
			`SELECT kind, category, status FROM triage_runs WHERE id = ?`, runID).Scan(&kind, &category, &status)
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
			return
		}
		if err != nil {
			internalError(c, err)
			return
		}
		if status != "running" {
			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("run is already %s", status)})
			return
		}

		result, err := tx.ExecContext(c.Request.Context(),
			`UPDATE triage_runs SET status = ?, considered = COALESCE(?, considered),
			        written = COALESCE(?, written), notes = COALESCE(?, notes),
			        finished_at = CURRENT_TIMESTAMP WHERE id = ? AND status = 'running'`,
			request.Status, request.Considered, request.Written, nullIfEmpty(request.Notes), runID)
		if err != nil {
			internalError(c, err)
			return
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			internalError(c, err)
			return
		}
		if rowsAffected != 1 {
			// Status-only guard; unreachable in-process (single-connection pool
			// serialises the read above and this write) but cheap to keep honest.
			c.JSON(http.StatusConflict, gin.H{"error": "run is no longer running"})
			return
		}

		if kind == "process" && request.Status == "done" && category.Valid {
			if _, err := tx.ExecContext(c.Request.Context(),
				`UPDATE finding_categories SET last_processed_at = CURRENT_TIMESTAMP WHERE slug = ?`,
				category.String); err != nil {
				internalError(c, err)
				return
			}
		}

		_, err = tx.ExecContext(c.Request.Context(),
			`INSERT INTO flight_recorder (event, note, agent) VALUES (?, ?, ?)`,
			"triage."+kind, fmt.Sprintf("Triage run #%d %s", runID, request.Status), "triage:"+kind)
		if err != nil {
			internalError(c, err)
			return
		}

		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"id": runID, "status": request.Status, "updated": true})
	}
}

// operatorCloseTriageRunHandler handles POST /v1/triage/runs/:runID/operator-close.
// Allows the operator to manually mark a stuck running run as done or failed when it
// has shown no activity for 30 minutes or more. The operator decision is recorded in
// flight_recorder and appended to the run's notes. The run must be status='running'
// and stale; otherwise a 409 is returned. Do NOT stamp last_processed_at on process
// runs marked done this way, since the operator is closing a stuck run, not certifying
// that the category was processed — the nightly rotation should retry it.
func operatorCloseTriageRunHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		runID, err := strconv.ParseInt(c.Param("runID"), 10, 64)
		if err != nil {
			validationError(c, errors.New("run id must be an integer"))
			return
		}
		var request model.OperatorCloseTriageRunRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		// Trim and validate required fields
		request.DecidedBy = strings.TrimSpace(request.DecidedBy)
		request.Note = strings.TrimSpace(request.Note)
		if request.DecidedBy == "" {
			validationError(c, errors.New("decided_by must not be empty or whitespace-only"))
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

		// Fetch current run state and last activity (shared definition).
		var kind string
		var category sql.NullString
		var status string
		var lastActivityAt string

		err = tx.QueryRowContext(c.Request.Context(),
			`SELECT r.kind, r.category, r.status, `+triageRunLastActivitySQL+` AS last_activity_at
			 FROM triage_runs r WHERE r.id = ?`, runID).
			Scan(&kind, &category, &status, &lastActivityAt)
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
			return
		}
		if err != nil {
			internalError(c, err)
			return
		}

		if status != "running" {
			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("run is already %s", status)})
			return
		}

		stale, err := triageRunIsStale(status, lastActivityAt, time.Now().UTC())
		if err != nil {
			internalError(c, err)
			return
		}
		if !stale {
			c.JSON(http.StatusConflict, gin.H{
				"error": fmt.Sprintf("run is not stale: last activity %s, threshold %s", lastActivityAt, triageRunStalenessThreshold),
			})
			return
		}

		// One statement sets status, finished_at and appends to any existing
		// notes. The WHERE re-checks status only, not staleness: the read above
		// and this write share one transaction on a single-connection pool, so no
		// other request in this process can interleave between them. A
		// RowsAffected other than 1 is therefore defensive, not an expected race.
		newNote := fmt.Sprintf("[operator-closed %s by %s, idle since %s] %s",
			request.Status, request.DecidedBy, lastActivityAt, request.Note)
		result, err := tx.ExecContext(c.Request.Context(), `
			UPDATE triage_runs
			SET status = ?, finished_at = CURRENT_TIMESTAMP,
			    notes = CASE WHEN notes IS NULL THEN ? ELSE notes || char(10) || ? END
			WHERE id = ? AND status = 'running'
		`, request.Status, newNote, newNote, runID)
		if err != nil {
			internalError(c, err)
			return
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			internalError(c, err)
			return
		}
		if rowsAffected != 1 {
			c.JSON(http.StatusConflict, gin.H{"error": "run is no longer running"})
			return
		}

		// Flight row. The category is left out of the label when there is none.
		label := kind
		if category.Valid && category.String != "" {
			label = kind + ", " + category.String
		}
		if _, err := tx.ExecContext(c.Request.Context(),
			`INSERT INTO flight_recorder (event, note, agent) VALUES (?, ?, ?)`,
			"triage_run.operator_closed",
			fmt.Sprintf("Triage run #%d (%s) operator-closed %s: %s",
				runID, label, request.Status, request.Note),
			"operator:"+request.DecidedBy); err != nil {
			internalError(c, err)
			return
		}

		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"id":     runID,
			"status": request.Status,
			"closed": true,
		})
	}
}

// listTriageRunsHandler handles GET /v1/triage/runs?limit=N (default 50).
// Each run includes last_activity_at (the most recent timestamp across the run
// and all its child table rows) and stale (true when status='running' and
// last activity is ≥ 30 minutes before now, computed server-side in UTC).
// A running run whose last activity cannot be parsed does not fail the list:
// the error is logged with the run id and the raw value, that row reports
// stale=false and still carries its raw last_activity_at, and the remaining
// runs are served. The operator-close route keeps refusing such a run with 500.
func listTriageRunsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, _, err := parseLimitOffset(c)
		if err != nil {
			validationError(c, err)
			return
		}
		if limit == 0 {
			limit = 50
		}
		rows, err := db.QueryContext(c.Request.Context(), `
				SELECT r.id, r.kind, r.category, r.model, r.status, r.considered, r.written, r.notes,
				       r.started_at, r.finished_at,
				       (SELECT COUNT(*) FROM finding_recommendations x WHERE x.run_id = r.id AND x.state = 'accepted'),
				       (SELECT COUNT(*) FROM finding_recommendations x WHERE x.run_id = r.id AND x.state = 'declined'),
				       `+triageRunLastActivitySQL+` AS last_activity_at
				FROM triage_runs r ORDER BY r.id DESC LIMIT ?`, limit)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()

		now := time.Now().UTC()
		runs := make([]model.TriageRun, 0)
		for rows.Next() {
			var run model.TriageRun
			var category, modelName, notes, finished sql.NullString
			var lastActivityAt string
			if err := rows.Scan(&run.ID, &run.Kind, &category, &modelName, &run.Status, &run.Considered,
				&run.Written, &notes, &run.StartedAt, &finished, &run.Accepted, &run.Declined,
				&lastActivityAt); err != nil {
				internalError(c, err)
				return
			}
			run.Category = nullStringPtr(category)
			run.Model = nullStringPtr(modelName)
			run.Notes = nullStringPtr(notes)
			run.FinishedAt = nullStringPtr(finished)
			run.LastActivityAt = lastActivityAt

			run.Stale, err = triageRunIsStale(run.Status, lastActivityAt, now)
			if err != nil {
				// Degrade this row only; one corrupt timestamp must not hide the history.
				log.Printf("request_id=%v triage run=%d unparseable last_activity_at=%q error=%v",
					requestID(c), run.ID, lastActivityAt, err)
				run.Stale = false
			}

			runs = append(runs, run)
		}
		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"runs": runs})
	}
}

// nextCategoryHandler handles GET /v1/triage/next-category. It returns the
// active category that should be processed next: one with proposed findings
// lacking a pending recommendation, least recently processed first. 404 when
// nothing is waiting, which the runner treats as "nothing to do".
func nextCategoryHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var slug string
		var waiting int64
		err := db.QueryRowContext(c.Request.Context(), `
			SELECT c.slug, COUNT(f.id) AS waiting
			FROM finding_categories c
			JOIN finding_triage t ON t.category = c.slug
			JOIN findings f ON f.id = t.finding_id AND f.status = 'proposed'
			LEFT JOIN finding_recommendations r ON r.finding_id = f.id AND r.state = 'pending'
			WHERE c.active = 1 AND r.id IS NULL
			GROUP BY c.slug
			HAVING waiting > 0
			ORDER BY c.last_processed_at IS NOT NULL, c.last_processed_at ASC, c.sort_order ASC
			LIMIT 1`).Scan(&slug, &waiting)
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "no category has findings waiting for a recommendation"})
			return
		}
		if err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"category": slug, "waiting": waiting})
	}
}

// --- Classification ----------------------------------------------------------

// classifyFindingsHandler handles PUT /v1/triage/classifications — a batch
// upsert into finding_triage. Unknown category slugs and unknown finding ids
// are reported as a 400 naming the offending item index, and nothing is
// written: a classifier that hallucinates a slug should learn it at once.
func classifyFindingsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request model.ClassifyRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		slugs, err := activeCategorySlugs(c, db)
		if err != nil {
			internalError(c, err)
			return
		}
		for i, item := range request.Items {
			if !slugs[item.Category] {
				validationError(c, fmt.Errorf("items[%d]: category is not an active category slug", i))
				return
			}
			if item.SecondaryCategory != "" && !slugs[item.SecondaryCategory] {
				validationError(c, fmt.Errorf("items[%d]: secondary_category is not an active category slug", i))
				return
			}
		}

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		for i, item := range request.Items {
			var exists int64
			if err := tx.QueryRowContext(c.Request.Context(),
				`SELECT COUNT(*) FROM findings WHERE id = ?`, item.FindingID).Scan(&exists); err != nil {
				internalError(c, err)
				return
			}
			if exists == 0 {
				validationError(c, fmt.Errorf("items[%d]: finding_id does not exist", i))
				return
			}
			directive := int64(0)
			if item.DirectiveCandidate {
				directive = 1
			}
			_, err = tx.ExecContext(c.Request.Context(),
				`INSERT INTO finding_triage (finding_id, category, secondary_category, directive_candidate,
				   confidence, summary, classified_by, run_id, classified_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
				 ON CONFLICT(finding_id) DO UPDATE SET
				   category = excluded.category, secondary_category = excluded.secondary_category,
				   directive_candidate = excluded.directive_candidate, confidence = excluded.confidence,
				   summary = excluded.summary, classified_by = excluded.classified_by,
				   run_id = excluded.run_id, classified_at = CURRENT_TIMESTAMP`,
				item.FindingID, item.Category, nullIfEmpty(item.SecondaryCategory), directive,
				item.Confidence, nullIfEmpty(strings.TrimSpace(item.Summary)),
				strings.TrimSpace(request.ClassifiedBy), request.RunID)
			if err != nil {
				internalError(c, err)
				return
			}
		}

		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"classified": len(request.Items)})
	}
}

func activeCategorySlugs(c *gin.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(c.Request.Context(), `SELECT slug FROM finding_categories WHERE active = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	slugs := map[string]bool{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		slugs[s] = true
	}
	return slugs, rows.Err()
}

// --- Groups and recommendations ---------------------------------------------

// createFindingGroupHandler handles POST /v1/finding-groups.
func createFindingGroupHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request model.CreateFindingGroupRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		slugs, err := activeCategorySlugs(c, db)
		if err != nil {
			internalError(c, err)
			return
		}
		if !slugs[request.Category] {
			validationError(c, errors.New("category is not an active category slug"))
			return
		}
		result, err := db.ExecContext(c.Request.Context(),
			`INSERT INTO finding_groups (category, title, summary, target, run_id) VALUES (?, ?, ?, ?, ?)`,
			request.Category, strings.TrimSpace(request.Title), strings.TrimSpace(request.Summary),
			nullIfEmpty(request.Target), request.RunID)
		if err != nil {
			internalError(c, err)
			return
		}
		id, err := result.LastInsertId()
		if err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"id": id, "created": true})
	}
}

// recommendFindingsHandler handles POST /v1/finding-recommendations — a batch
// insert. A finding that already has a pending recommendation gets that one
// marked expired first (a re-run replaces, never duplicates). A finding that
// is no longer proposed is skipped and reported, because a recommendation on
// a decided finding has nobody to act on it.
func recommendFindingsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request model.RecommendRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		for i, item := range request.Items {
			if item.Recommendation == "supersede" && item.SupersededBy == nil {
				validationError(c, fmt.Errorf("items[%d]: supersede requires superseded_by", i))
				return
			}
		}

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		written := 0
		skipped := make([]int64, 0)
		for i, item := range request.Items {
			var status string
			err := tx.QueryRowContext(c.Request.Context(),
				`SELECT status FROM findings WHERE id = ?`, item.FindingID).Scan(&status)
			if errors.Is(err, sql.ErrNoRows) {
				validationError(c, fmt.Errorf("items[%d]: finding_id does not exist", i))
				return
			}
			if err != nil {
				internalError(c, err)
				return
			}
			if status != "proposed" {
				skipped = append(skipped, item.FindingID)
				continue
			}
			if _, err := tx.ExecContext(c.Request.Context(),
				`UPDATE finding_recommendations SET state = 'expired' WHERE finding_id = ? AND state = 'pending'`,
				item.FindingID); err != nil {
				internalError(c, err)
				return
			}
			if _, err := tx.ExecContext(c.Request.Context(),
				`INSERT INTO finding_recommendations (finding_id, group_id, recommendation, superseded_by,
				   rationale, proposed_change, run_id)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				item.FindingID, item.GroupID, item.Recommendation, item.SupersededBy,
				strings.TrimSpace(item.Rationale), nullIfEmpty(item.ProposedChange), request.RunID); err != nil {
				internalError(c, err)
				return
			}
			written++
		}

		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"written": written, "skipped": skipped})
	}
}

// recentDecisionsHandler handles GET /v1/triage/decisions?category=&limit=.
// It returns decided findings (status != proposed) in a category with the
// most recent recommendation each one received — the processor's calibration
// input, so it can see which of its earlier recommendations the operator took.
func recentDecisionsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, _, err := parseLimitOffset(c)
		if err != nil {
			validationError(c, err)
			return
		}
		if limit == 0 {
			limit = 20
		}
		where := "WHERE f.status <> 'proposed'"
		args := []any{}
		if category := strings.TrimSpace(c.Query("category")); category != "" {
			where += " AND t.category = ?"
			args = append(args, category)
		}
		args = append(args, limit)
		findings, err := queryFindings(c, db,
			findingSelect+latestRecommendationJoin+where+" ORDER BY f.date_approved DESC, f.id DESC LIMIT ?", args...)
		if err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"findings": findings})
	}
}

// pendingTriageHandler handles GET /v1/triage/pending — everything awaiting
// an operator decision, shaped for the review page: categories, each with its
// groups and their member findings, plus ungrouped findings, plus pending
// directive proposals.
func pendingTriageHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		findings, err := queryFindings(c, db,
			findingSelect+pendingRecommendationJoin+" WHERE r.id IS NOT NULL ORDER BY f.id ASC")
		if err != nil {
			internalError(c, err)
			return
		}

		groups, err := loadGroups(c, db)
		if err != nil {
			internalError(c, err)
			return
		}

		type groupView struct {
			model.FindingGroup
			Findings []model.Finding `json:"findings"`
		}
		type categoryView struct {
			Slug      string          `json:"slug"`
			Groups    []*groupView    `json:"groups"`
			Ungrouped []model.Finding `json:"ungrouped"`
		}

		byCategory := map[string]*categoryView{}
		order := []string{}
		groupViews := map[int64]*groupView{}
		for _, f := range findings {
			slug := "uncategorised"
			if f.Triage != nil {
				slug = f.Triage.Category
			}
			cv, ok := byCategory[slug]
			if !ok {
				cv = &categoryView{Slug: slug, Groups: []*groupView{}, Ungrouped: []model.Finding{}}
				byCategory[slug] = cv
				order = append(order, slug)
			}
			if f.Recommendation.GroupID == nil {
				cv.Ungrouped = append(cv.Ungrouped, f)
				continue
			}
			gid := *f.Recommendation.GroupID
			gv, ok := groupViews[gid]
			if !ok {
				g, found := groups[gid]
				if !found {
					g = model.FindingGroup{ID: gid, Category: slug, Title: "(missing group)"}
				}
				gv = &groupView{FindingGroup: g, Findings: []model.Finding{}}
				groupViews[gid] = gv
				cv.Groups = append(cv.Groups, gv)
			}
			gv.Findings = append(gv.Findings, f)
		}

		categories := make([]*categoryView, 0, len(order))
		for _, slug := range order {
			categories = append(categories, byCategory[slug])
		}

		proposals, err := queryDirectiveProposals(c, db, "WHERE p.state = 'pending'")
		if err != nil {
			internalError(c, err)
			return
		}

		var unexported int64
		if err := db.QueryRowContext(c.Request.Context(),
			`SELECT COUNT(*) FROM findings WHERE status <> 'proposed'`).Scan(&unexported); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"categories":          categories,
			"directive_proposals": proposals,
			"pending_findings":    len(findings),
			"decided_findings":    unexported,
		})
	}
}

func loadGroups(c *gin.Context, db *sql.DB) (map[int64]model.FindingGroup, error) {
	rows, err := db.QueryContext(c.Request.Context(),
		`SELECT id, category, title, summary, target, run_id, created_at FROM finding_groups`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[int64]model.FindingGroup{}
	for rows.Next() {
		var g model.FindingGroup
		var target sql.NullString
		var runID sql.NullInt64
		if err := rows.Scan(&g.ID, &g.Category, &g.Title, &g.Summary, &target, &runID, &g.CreatedAt); err != nil {
			return nil, err
		}
		g.Target = nullStringPtr(target)
		g.RunID = nullInt64Ptr(runID)
		groups[g.ID] = g
	}
	return groups, rows.Err()
}

// --- Directive proposals ------------------------------------------------------

const directiveProposalSelect = `
	SELECT p.id, p.group_id, p.harness, p.title, p.applies_when, p.rule, p.rationale, p.reviewer_check,
	       p.example, p.evidence, p.state, p.directive_id, p.decided_by, p.decided_at, p.decision_note,
	       p.run_id, p.created_at
	FROM directive_proposals p
`

func queryDirectiveProposals(c *gin.Context, db *sql.DB, where string, args ...any) ([]model.DirectiveProposal, error) {
	rows, err := db.QueryContext(c.Request.Context(), directiveProposalSelect+where+" ORDER BY p.id ASC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	proposals := make([]model.DirectiveProposal, 0)
	for rows.Next() {
		var p model.DirectiveProposal
		var groupID, runID sql.NullInt64
		var harness, example, directiveID, decidedBy, decidedAt, note sql.NullString
		var evidence string
		if err := rows.Scan(&p.ID, &groupID, &harness, &p.Title, &p.AppliesWhen, &p.Rule, &p.Rationale,
			&p.ReviewerCheck, &example, &evidence, &p.State, &directiveID, &decidedBy, &decidedAt, &note,
			&runID, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.GroupID = nullInt64Ptr(groupID)
		p.RunID = nullInt64Ptr(runID)
		p.Harness = nullStringPtr(harness)
		p.Example = nullStringPtr(example)
		p.DirectiveID = nullStringPtr(directiveID)
		p.DecidedBy = nullStringPtr(decidedBy)
		p.DecidedAt = nullStringPtr(decidedAt)
		p.DecisionNote = nullStringPtr(note)
		p.Evidence = []int64{}
		_ = json.Unmarshal([]byte(evidence), &p.Evidence)
		proposals = append(proposals, p)
	}
	return proposals, rows.Err()
}

// createDirectiveProposalHandler handles POST /v1/directive-proposals.
func createDirectiveProposalHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request model.CreateDirectiveProposalRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		rendered := renderDirectiveEntry("DIR-NNN", request.Title, request.AppliesWhen, request.Rule,
			request.Rationale, request.ReviewerCheck, request.Example, time.Now().UTC().Format("2006-01-02"),
			evidencePlaceholder)
		if len(rendered) > directiveAbsoluteMax {
			validationError(c, fmt.Errorf("the rendered entry is %d characters; the DIRECTIVES-TEMPLATE absolute maximum is %d", len(rendered), directiveAbsoluteMax))
			return
		}
		evidence, err := json.Marshal(request.Evidence)
		if err != nil {
			internalError(c, err)
			return
		}
		result, err := db.ExecContext(c.Request.Context(),
			`INSERT INTO directive_proposals (group_id, harness, title, applies_when, rule, rationale,
			   reviewer_check, example, evidence, run_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			request.GroupID, nullIfEmpty(request.Harness), strings.TrimSpace(request.Title),
			strings.TrimSpace(request.AppliesWhen), strings.TrimSpace(request.Rule),
			strings.TrimSpace(request.Rationale), strings.TrimSpace(request.ReviewerCheck),
			nullIfEmpty(request.Example), string(evidence), request.RunID)
		if err != nil {
			internalError(c, err)
			return
		}
		id, err := result.LastInsertId()
		if err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"id": id, "created": true, "rendered_length": len(rendered)})
	}
}

// listDirectiveProposalsHandler handles GET /v1/directive-proposals?state=.
func listDirectiveProposalsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		where := ""
		args := []any{}
		if state := strings.TrimSpace(c.Query("state")); state != "" {
			if !isOneOf(state, []string{"pending", "accepted", "declined", "expired"}) {
				validationError(c, errors.New("state must be one of: pending, accepted, declined, expired"))
				return
			}
			where = "WHERE p.state = ?"
			args = append(args, state)
		}
		proposals, err := queryDirectiveProposals(c, db, where, args...)
		if err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"directive_proposals": proposals})
	}
}

// directiveAbsoluteMax mirrors the DIRECTIVES-TEMPLATE length budget: an entry
// measured from its heading to the next heading must not exceed this.
const directiveAbsoluteMax = 1510

// evidencePlaceholder stands in for the Evidence field when checking the
// budget. The template caps Evidence at 80 characters and the exporter writes
// the real "[date] — target" form, so the check assumes the longest it can be.
var evidencePlaceholder = strings.Repeat("x", 80)

// renderDirectiveEntry produces the Markdown entry exactly as
// scripts/export-decisions.py writes it, so the length check here and the
// text written there cannot disagree.
func renderDirectiveEntry(id, title, appliesWhen, rule, rationale, reviewerCheck, example, added, evidence string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s — %s\n", id, title)
	fmt.Fprintf(&b, "- **Applies when:** %s\n", appliesWhen)
	b.WriteString("- **Status:** active\n")
	fmt.Fprintf(&b, "- **Rule:** %s\n", rule)
	fmt.Fprintf(&b, "- **Rationale:** %s\n", rationale)
	fmt.Fprintf(&b, "- **Reviewer check:** %s\n", reviewerCheck)
	if strings.TrimSpace(example) != "" {
		fmt.Fprintf(&b, "- **Example:** Illustrative — %s\n", example)
	}
	fmt.Fprintf(&b, "- **Added:** %s · **Source:** bishop-memory triage · **Evidence:** %s\n", added, evidence)
	return b.String()
}

var directiveIDPattern = regexp.MustCompile(`^DIR-(\d+)$`)

// decideDirectiveProposalHandler handles POST /v1/directive-proposals/:proposalID/decision.
//
// Accepting a proposal is the human ratification act. In one transaction it
// allocates the next DIR-NNN (or reuses an existing one if the proposal has been
// reopened with an existing directive_id), stores the (possibly edited) fields,
// mirrors the directive into the directives table, marks each evidence finding applied,
// and closes their pending recommendations. A new directive records a directive.ratified
// flight event; an amendment records directive.amended with the amendment date appended
// to the note. Declining a proposal with an existing directive_id records a directive.retired
// event and marks the directive as retired (readable via a derived status field in GET /v1/directives).
// Declining a proposal without a directive_id writes no retirement event.
// scripts/export-decisions.py writes new directives into DIRECTIVES.md; the exporter and
// reconciler handle amendments and retirements.
func decideDirectiveProposalHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		proposalID, err := strconv.ParseInt(c.Param("proposalID"), 10, 64)
		if err != nil {
			validationError(c, errors.New("proposal id must be an integer"))
			return
		}
		var request model.DirectiveProposalDecisionRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		request.DecidedBy = strings.TrimSpace(request.DecidedBy)
		request.Note = strings.TrimSpace(request.Note)
		if request.DecidedBy == "" {
			validationError(c, errors.New("decided_by must not be empty or whitespace-only"))
			return
		}
		if request.State == "declined" && request.Note == "" {
			validationError(c, errors.New("note is required when declining"))
			return
		}

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		var state, title, appliesWhen, rule, rationale, reviewerCheck, evidenceJSON string
		var example, existingDirectiveID sql.NullString
		err = tx.QueryRowContext(c.Request.Context(),
			`SELECT state, title, applies_when, rule, rationale, reviewer_check, example, evidence, directive_id
			 FROM directive_proposals WHERE id = ?`, proposalID).
			Scan(&state, &title, &appliesWhen, &rule, &rationale, &reviewerCheck, &example, &evidenceJSON, &existingDirectiveID)
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "proposal not found"})
			return
		}
		if err != nil {
			internalError(c, err)
			return
		}
		if state != "pending" {
			validationError(c, fmt.Errorf("proposal is already %s", state))
			return
		}

		if request.State == "declined" {
			if _, err := tx.ExecContext(c.Request.Context(),
				`UPDATE directive_proposals SET state = 'declined', decided_by = ?, decided_at = CURRENT_TIMESTAMP,
				        decision_note = ? WHERE id = ?`, request.DecidedBy, nullIfEmpty(request.Note), proposalID); err != nil {
				internalError(c, err)
				return
			}
			// If this proposal has a directive_id, record a directive.retired event.
			if existingDirectiveID.Valid {
				if _, err := tx.ExecContext(c.Request.Context(),
					`INSERT INTO flight_recorder (event, note, agent) VALUES ('directive.retired', ?, ?)`,
					fmt.Sprintf("%s — proposal #%d", existingDirectiveID.String, proposalID),
					"operator:"+request.DecidedBy); err != nil {
					internalError(c, err)
					return
				}
			}
			if err := tx.Commit(); err != nil {
				internalError(c, err)
				return
			}
			c.JSON(http.StatusOK, gin.H{"id": proposalID, "state": "declined"})
			return
		}

		// Apply operator edits.
		pick := func(edit *string, current string) string {
			if edit != nil && strings.TrimSpace(*edit) != "" {
				return strings.TrimSpace(*edit)
			}
			return current
		}
		title = pick(request.Title, title)
		appliesWhen = pick(request.AppliesWhen, appliesWhen)
		rule = pick(request.Rule, rule)
		rationale = pick(request.Rationale, rationale)
		reviewerCheck = pick(request.ReviewerCheck, reviewerCheck)
		exampleText := ""
		if example.Valid {
			exampleText = example.String
		}
		if request.Example != nil {
			exampleText = strings.TrimSpace(*request.Example)
		}

		var evidence []int64
		if err := json.Unmarshal([]byte(evidenceJSON), &evidence); err != nil {
			internalError(c, fmt.Errorf("proposal %d evidence is not a JSON array: %w", proposalID, err))
			return
		}

		// If the proposal already has a directive_id (from a reopened proposal),
		// reuse it. Otherwise, allocate the next one.
		var directiveID string
		isAmendment := false
		if existingDirectiveID.Valid {
			directiveID = existingDirectiveID.String
			isAmendment = true
		} else {
			directiveID, err = nextDirectiveID(c, tx)
			if err != nil {
				internalError(c, err)
				return
			}
		}

		today := time.Now().UTC().Format("2006-01-02")
		budgetCheck := renderDirectiveEntry(directiveID, title, appliesWhen, rule, rationale, reviewerCheck,
			exampleText, today, evidencePlaceholder)
		rendered := renderDirectiveEntry(directiveID, title, appliesWhen, rule, rationale, reviewerCheck,
			exampleText, today, evidenceLabel(evidence))
		if len(budgetCheck) > directiveAbsoluteMax {
			validationError(c, fmt.Errorf("the rendered entry is %d characters; the DIRECTIVES-TEMPLATE absolute maximum is %d", len(budgetCheck), directiveAbsoluteMax))
			return
		}

		if _, err := tx.ExecContext(c.Request.Context(),
			`UPDATE directive_proposals SET state = 'accepted', decided_by = ?, decided_at = CURRENT_TIMESTAMP,
			        decision_note = ?, directive_id = ?, title = ?, applies_when = ?, rule = ?, rationale = ?,
			        reviewer_check = ?, example = ? WHERE id = ?`,
			request.DecidedBy, nullIfEmpty(request.Note), directiveID, title, appliesWhen, rule, rationale,
			reviewerCheck, nullIfEmpty(exampleText), proposalID); err != nil {
			internalError(c, err)
			return
		}

		// On amendment, preserve the original ratified_at; on new directives, set it to today.
		var ratifiedAtValue string
		if isAmendment {
			// Read the existing ratified_at to preserve it.
			var ratifiedAt sql.NullString
			if err := tx.QueryRowContext(c.Request.Context(),
				`SELECT ratified_at FROM directives WHERE directive_id = ?`, directiveID).Scan(&ratifiedAt); err != nil {
				if !errors.Is(err, sql.ErrNoRows) {
					internalError(c, err)
					return
				}
				// Should not happen on amendment, but fallback to today.
				ratifiedAtValue = today
			} else {
				// If ratified_at is NULL, fall back to today.
				if ratifiedAt.Valid {
					ratifiedAtValue = ratifiedAt.String
				} else {
					ratifiedAtValue = today
				}
			}
		} else {
			ratifiedAtValue = today
		}

		if _, err := tx.ExecContext(c.Request.Context(),
			`INSERT INTO directives (directive_id, title, rule, rationale, ratified_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(directive_id) DO UPDATE SET title = excluded.title, rule = excluded.rule,
			   rationale = excluded.rationale, ratified_at = excluded.ratified_at`,
			directiveID, title, rule, rationale, ratifiedAtValue); err != nil {
			internalError(c, err)
			return
		}

		for _, fid := range evidence {
			var status string
			err := tx.QueryRowContext(c.Request.Context(), `SELECT status FROM findings WHERE id = ?`, fid).Scan(&status)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				internalError(c, err)
				return
			}
			if status != "proposed" && status != "approved" {
				continue
			}
			if _, err := tx.ExecContext(c.Request.Context(),
				`UPDATE findings SET status = 'applied', approver = ?, date_approved = ?, decision_note = ?
				 WHERE id = ?`, request.DecidedBy, today, "Ratified as "+directiveID, fid); err != nil {
				internalError(c, err)
				return
			}
			if err := closePendingRecommendation(c, tx, fid, "applied", request.DecidedBy); err != nil {
				internalError(c, err)
				return
			}
		}

		// Use "directive.amended" for reopened proposals with an existing directive_id,
		// "directive.ratified" for new directives.
		event := "directive.ratified"
		noteText := fmt.Sprintf("%s — %s (proposal #%d)", directiveID, title, proposalID)
		if isAmendment {
			event = "directive.amended"
			noteText += fmt.Sprintf("; amended %s", today)
		}
		if _, err := tx.ExecContext(c.Request.Context(),
			`INSERT INTO flight_recorder (event, note, agent) VALUES (?, ?, ?)`,
			event,
			noteText,
			"operator:"+request.DecidedBy); err != nil {
			internalError(c, err)
			return
		}

		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"id":           proposalID,
			"state":        "accepted",
			"directive_id": directiveID,
			"rendered":     rendered,
		})
	}
}

// reopenDirectiveProposalHandler handles POST /v1/directive-proposals/:proposalID/reopen.
// Sets a decided proposal (accepted, declined, or expired) back to pending, keeping
// its directive_id if one is set. This allows the operator to accept it again with
// amendments, decline it to retire the directive, or revisit the decision. The prior
// decision record (decided_by, decided_at, decision_note) is captured in the flight_recorder
// event before being cleared.
func reopenDirectiveProposalHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		proposalID, err := strconv.ParseInt(c.Param("proposalID"), 10, 64)
		if err != nil {
			validationError(c, errors.New("proposal id must be an integer"))
			return
		}
		var request model.ReopenDirectiveProposalRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		request.DecidedBy = strings.TrimSpace(request.DecidedBy)
		request.Note = strings.TrimSpace(request.Note)
		if request.DecidedBy == "" {
			validationError(c, errors.New("decided_by must not be empty or whitespace-only"))
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

		var state string
		var directiveID, decidedBy, decidedAt, decisionNote sql.NullString
		err = tx.QueryRowContext(c.Request.Context(),
			`SELECT state, directive_id, decided_by, decided_at, decision_note FROM directive_proposals WHERE id = ?`, proposalID).
			Scan(&state, &directiveID, &decidedBy, &decidedAt, &decisionNote)
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "proposal not found"})
			return
		}
		if err != nil {
			internalError(c, err)
			return
		}
		if state == "pending" {
			validationError(c, errors.New("proposal is already pending"))
			return
		}

		if _, err := tx.ExecContext(c.Request.Context(),
			`UPDATE directive_proposals SET state = 'pending', decided_by = NULL, decided_at = NULL,
			        decision_note = NULL WHERE id = ?`, proposalID); err != nil {
			internalError(c, err)
			return
		}

		// Compose the flight_recorder note with the directive_id and prior decision record if it exists.
		note := fmt.Sprintf("Proposal #%d reopened from %s", proposalID, state)

		// Build inner parts: directive_id and/or prior decision
		var innerParts []string
		if directiveID.Valid {
			innerParts = append(innerParts, fmt.Sprintf("directive_id=%s", directiveID.String))
		}
		if decidedBy.Valid || decidedAt.Valid || decisionNote.Valid {
			priorDecision := "previously decided by "
			if decidedBy.Valid {
				priorDecision += decidedBy.String
			} else {
				priorDecision += "?"
			}
			if decidedAt.Valid {
				priorDecision += " at " + decidedAt.String
			}
			if decisionNote.Valid {
				priorDecision += ": " + decisionNote.String
			}
			innerParts = append(innerParts, priorDecision)
		}
		if len(innerParts) > 0 {
			note += " (" + strings.Join(innerParts, "; ") + ")"
		}

		note += fmt.Sprintf("; operator note: %s", request.Note)

		if _, err := tx.ExecContext(c.Request.Context(),
			`INSERT INTO flight_recorder (event, note, agent) VALUES ('directive.reopened', ?, ?)`,
			note, "operator:"+request.DecidedBy); err != nil {
			internalError(c, err)
			return
		}

		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"id":    proposalID,
			"state": "pending",
		})
	}
}

// evidenceLabel renders the Evidence field as "findings #1, #2" — the exporter
// resolves ids to the "[date] — target" form the template asks for when it
// writes the file, where it has the ledger text to hand.
func evidenceLabel(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("#%d", id))
	}
	return "findings " + strings.Join(parts, ", ")
}

// nextDirectiveID scans both tables for the highest DIR-NNN and returns the
// next one, zero-padded to three digits. IDs are permanent and never reused,
// so a declined or retired directive still counts.
func nextDirectiveID(c *gin.Context, tx *sql.Tx) (string, error) {
	rows, err := tx.QueryContext(c.Request.Context(),
		`SELECT directive_id FROM directives WHERE directive_id IS NOT NULL
		 UNION ALL
		 SELECT directive_id FROM directive_proposals WHERE directive_id IS NOT NULL`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	max := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		m := directiveIDPattern.FindStringSubmatch(strings.TrimSpace(id))
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		if n > max {
			max = n
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("DIR-%03d", max+1), nil
}

// --- Harnesses ----------------------------------------------------------------

// upsertHarnessHandler handles PUT /v1/harnesses/:name. The reconciler calls it
// on every run; the exporter reads it to find the Markdown tree.
func upsertHarnessHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := strings.TrimSpace(c.Param("name"))
		if name == "" || isDotSegmentName(name) {
			validationError(c, errors.New("harness name must be non-empty"))
			return
		}
		var request model.UpsertHarnessRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		root := strings.TrimSpace(request.MemoryRoot)
		if !strings.HasPrefix(root, "/") {
			validationError(c, errors.New("memory_root must be an absolute path"))
			return
		}
		if _, err := db.ExecContext(c.Request.Context(),
			`INSERT INTO harnesses (name, memory_root, last_seen_at) VALUES (?, ?, CURRENT_TIMESTAMP)
			 ON CONFLICT(name) DO UPDATE SET memory_root = excluded.memory_root, last_seen_at = CURRENT_TIMESTAMP`,
			name, root); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"name": name, "upserted": true})
	}
}

func isDotSegmentName(name string) bool { return name == "." || name == ".." }

// listHarnessesHandler handles GET /v1/harnesses.
func listHarnessesHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := db.QueryContext(c.Request.Context(),
			`SELECT name, memory_root, last_seen_at FROM harnesses ORDER BY name ASC`)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()
		harnesses := make([]model.Harness, 0)
		for rows.Next() {
			var h model.Harness
			if err := rows.Scan(&h.Name, &h.MemoryRoot, &h.LastSeenAt); err != nil {
				internalError(c, err)
				return
			}
			harnesses = append(harnesses, h)
		}
		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"harnesses": harnesses})
	}
}

// listFindingGroupsHandler handles GET /v1/finding-groups?category=.
func listFindingGroupsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := `SELECT id, category, title, summary, target, run_id, created_at FROM finding_groups`
		args := []any{}
		if category := strings.TrimSpace(c.Query("category")); category != "" {
			query += " WHERE category = ?"
			args = append(args, category)
		}
		query += " ORDER BY id DESC"
		rows, err := db.QueryContext(c.Request.Context(), query, args...)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()
		groups := make([]model.FindingGroup, 0)
		for rows.Next() {
			var g model.FindingGroup
			var target sql.NullString
			var runID sql.NullInt64
			if err := rows.Scan(&g.ID, &g.Category, &g.Title, &g.Summary, &target, &runID, &g.CreatedAt); err != nil {
				internalError(c, err)
				return
			}
			g.Target = nullStringPtr(target)
			g.RunID = nullInt64Ptr(runID)
			groups = append(groups, g)
		}
		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"groups": groups})
	}
}
