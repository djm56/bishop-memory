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
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/model"
)

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

		var kind string
		var category sql.NullString
		err = tx.QueryRowContext(c.Request.Context(),
			`SELECT kind, category FROM triage_runs WHERE id = ?`, runID).Scan(&kind, &category)
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
			return
		}
		if err != nil {
			internalError(c, err)
			return
		}

		_, err = tx.ExecContext(c.Request.Context(),
			`UPDATE triage_runs SET status = ?, considered = COALESCE(?, considered),
			        written = COALESCE(?, written), notes = COALESCE(?, notes),
			        finished_at = CURRENT_TIMESTAMP WHERE id = ?`,
			request.Status, request.Considered, request.Written, nullIfEmpty(request.Notes), runID)
		if err != nil {
			internalError(c, err)
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

// listTriageRunsHandler handles GET /v1/triage/runs?limit=N (default 50).
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
			       (SELECT COUNT(*) FROM finding_recommendations x WHERE x.run_id = r.id AND x.state = 'declined')
			FROM triage_runs r ORDER BY r.id DESC LIMIT ?`, limit)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()

		runs := make([]model.TriageRun, 0)
		for rows.Next() {
			var run model.TriageRun
			var category, modelName, notes, finished sql.NullString
			if err := rows.Scan(&run.ID, &run.Kind, &category, &modelName, &run.Status, &run.Considered,
				&run.Written, &notes, &run.StartedAt, &finished, &run.Accepted, &run.Declined); err != nil {
				internalError(c, err)
				return
			}
			run.Category = nullStringPtr(category)
			run.Model = nullStringPtr(modelName)
			run.Notes = nullStringPtr(notes)
			run.FinishedAt = nullStringPtr(finished)
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
// allocates the next DIR-NNN across both the directives table and every
// already-accepted proposal, stores the (possibly edited) fields, mirrors the
// directive into the directives table, marks each evidence finding applied
// and closes their pending recommendations. scripts/export-decisions.py then
// writes the entry into the harness's DIRECTIVES.md.
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
		if request.DecidedBy == "" {
			validationError(c, errors.New("decided_by must not be empty or whitespace-only"))
			return
		}
		if request.State == "declined" && strings.TrimSpace(request.Note) == "" {
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
		var example sql.NullString
		err = tx.QueryRowContext(c.Request.Context(),
			`SELECT state, title, applies_when, rule, rationale, reviewer_check, example, evidence
			 FROM directive_proposals WHERE id = ?`, proposalID).
			Scan(&state, &title, &appliesWhen, &rule, &rationale, &reviewerCheck, &example, &evidenceJSON)
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
				        decision_note = ? WHERE id = ?`, request.DecidedBy, request.Note, proposalID); err != nil {
				internalError(c, err)
				return
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

		directiveID, err := nextDirectiveID(c, tx)
		if err != nil {
			internalError(c, err)
			return
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

		if _, err := tx.ExecContext(c.Request.Context(),
			`INSERT INTO directives (directive_id, title, rule, rationale, ratified_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(directive_id) DO UPDATE SET title = excluded.title, rule = excluded.rule,
			   rationale = excluded.rationale, ratified_at = excluded.ratified_at`,
			directiveID, title, rule, rationale, today); err != nil {
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

		if _, err := tx.ExecContext(c.Request.Context(),
			`INSERT INTO flight_recorder (event, note, agent) VALUES ('directive.ratified', ?, ?)`,
			fmt.Sprintf("%s — %s (proposal #%d)", directiveID, title, proposalID),
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
