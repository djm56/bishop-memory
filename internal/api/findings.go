// Package api — HTTP handlers for /v1/findings.
//
// Findings are the improvement ledger (mirrors .claude/memory/findings/FINDINGS.md).
// Status, approver, date_approved and decision_note belong to the human operator.
// Agents reach findings only through mcpd's harness profile, which registers the
// list and create routes and nothing else. The one write path for status is
// decideFindingHandler (POST /v1/findings/:id/decision), which is called by the
// review page, by scripts/reconcile-memory.py when FINDINGS.md already carries a
// human decision, and by nothing else.
package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/model"
)

// findingStatuses is the valid status values for findings.
// Matches the CHECK constraint in db/schema.sql.
var findingStatuses = []string{"proposed", "approved", "applied", "rejected", "retired", "superseded"}

// findingTransitions is the operator's allowed status moves. A same-status
// decision is accepted as a no-op so the reconciler can replay a Markdown
// status idempotently. Every terminal state can be reopened to proposed,
// which is the operator's undo.
var findingTransitions = map[string][]string{
	"proposed":   {"approved", "applied", "rejected", "retired", "superseded"},
	"approved":   {"applied", "rejected", "retired", "superseded", "proposed"},
	"applied":    {"retired", "superseded", "proposed"},
	"rejected":   {"proposed"},
	"retired":    {"proposed"},
	"superseded": {"proposed"},
}

// findingSelect is the shared SELECT for every read of the ledger. It joins the
// classification row and ONE recommendation row; which recommendation is
// chosen is the caller's join clause (the pending one for the list route, the
// most recent one for the calibration route).
const findingSelect = `
	SELECT f.id, f.finding_date, f.target, f.suggestion, f.rationale, f.status,
	       f.approver, f.date_approved, f.mission_id, f.harness, f.decision_note, f.created_at,
	       t.category, t.secondary_category, t.directive_candidate, t.confidence, t.summary,
	       t.classified_by, t.run_id, t.classified_at,
	       r.id, r.group_id, r.recommendation, r.superseded_by, r.rationale, r.proposed_change,
	       r.state, r.decided_by, r.decided_at, r.run_id, r.created_at
	FROM findings f
	LEFT JOIN finding_triage t ON t.finding_id = f.id
`

const pendingRecommendationJoin = `
	LEFT JOIN finding_recommendations r ON r.finding_id = f.id AND r.state = 'pending'
`

const latestRecommendationJoin = `
	LEFT JOIN finding_recommendations r ON r.id = (
		SELECT id FROM finding_recommendations WHERE finding_id = f.id ORDER BY id DESC LIMIT 1
	)
`

// listFindingsHandler handles GET /v1/findings.
//
// Filters (all optional, all AND-ed):
//
//	status=<ledger status>         validated against findingStatuses
//	category=<slug>                primary classification
//	harness=<name>                 owning harness
//	unclassified=1                 no finding_triage row
//	pending=1                      has a pending recommendation
//	no_pending=1                   has no pending recommendation
//	directive_candidate=1          classifier flagged it
//	mission_id=<id>                the mission it came from
//	ids=1,2,3                      explicit id list
//	order=asc|desc                 default desc (newest first)
//	limit=<n>&offset=<n>           default: everything (the reconciler relies on that)
func listFindingsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		status := strings.TrimSpace(c.Query("status"))
		if status != "" && !isOneOf(status, findingStatuses) {
			validationError(c, errors.New("status must be one of: proposed, approved, applied, rejected, retired, superseded"))
			return
		}

		where := []string{}
		args := []any{}

		if status != "" {
			where = append(where, "f.status = ?")
			args = append(args, status)
		}
		if category := strings.TrimSpace(c.Query("category")); category != "" {
			if category == "uncategorised" {
				where = append(where, "t.finding_id IS NULL")
			} else {
				where = append(where, "t.category = ?")
				args = append(args, category)
			}
		}
		if harness := strings.TrimSpace(c.Query("harness")); harness != "" {
			where = append(where, "f.harness = ?")
			args = append(args, harness)
		}
		if missionID := strings.TrimSpace(c.Query("mission_id")); missionID != "" {
			where = append(where, "f.mission_id = ?")
			args = append(args, missionID)
		}
		if c.Query("unclassified") == "1" {
			where = append(where, "t.finding_id IS NULL")
		}
		if c.Query("pending") == "1" {
			where = append(where, "r.id IS NOT NULL")
		}
		if c.Query("no_pending") == "1" {
			where = append(where, "r.id IS NULL")
		}
		if c.Query("directive_candidate") == "1" {
			where = append(where, "t.directive_candidate = 1")
		}
		if ids := strings.TrimSpace(c.Query("ids")); ids != "" {
			parts := strings.Split(ids, ",")
			placeholders := make([]string, 0, len(parts))
			for _, p := range parts {
				n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
				if err != nil {
					validationError(c, errors.New("ids must be a comma-separated list of integers"))
					return
				}
				placeholders = append(placeholders, "?")
				args = append(args, n)
			}
			where = append(where, "f.id IN ("+strings.Join(placeholders, ",")+")")
		}

		order := "DESC"
		if c.Query("order") == "asc" {
			order = "ASC"
		}

		query := findingSelect + pendingRecommendationJoin
		if len(where) > 0 {
			query += " WHERE " + strings.Join(where, " AND ")
		}
		query += " ORDER BY f.id " + order

		limit, offset, err := parseLimitOffset(c)
		if err != nil {
			validationError(c, err)
			return
		}
		if limit > 0 {
			query += " LIMIT ? OFFSET ?"
			args = append(args, limit, offset)
		}

		findings, err := queryFindings(c, db, query, args...)
		if err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"findings": findings})
	}
}

// parseLimitOffset reads ?limit= and ?offset=. limit 0 means "no limit".
func parseLimitOffset(c *gin.Context) (int64, int64, error) {
	var limit, offset int64
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return 0, 0, errors.New("limit must be a non-negative integer")
		}
		limit = n
	}
	if raw := strings.TrimSpace(c.Query("offset")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return 0, 0, errors.New("offset must be a non-negative integer")
		}
		offset = n
	}
	return limit, offset, nil
}

// queryFindings runs a findingSelect-shaped query and scans every row.
func queryFindings(c *gin.Context, db *sql.DB, query string, args ...any) ([]model.Finding, error) {
	rows, err := db.QueryContext(c.Request.Context(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	findings := make([]model.Finding, 0)
	for rows.Next() {
		finding, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	return findings, rows.Err()
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
		request.Harness = strings.TrimSpace(request.Harness)

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		// Note: mission_id has NO foreign key constraint in the schema.
		// A finding outlives mission-folder cleanup, so we store what we are given
		// without verifying it exists.
		if request.MissionID == "" && request.Harness != "" {
			request.MissionID, err = openMissionFor(c.Request.Context(), tx, request.Harness, request.FindingDate)
			if err != nil {
				internalError(c, err)
				return
			}
		}

		result, err := tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO findings (
				finding_date, target, suggestion, rationale, mission_id, harness
			) VALUES (?, ?, ?, ?, ?, ?)`,
			nullIfEmpty(request.FindingDate),
			nullIfEmpty(request.Target),
			request.Suggestion,
			nullIfEmpty(request.Rationale),
			nullIfEmpty(request.MissionID),
			nullIfEmpty(request.Harness),
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

// decideFindingHandler handles POST /v1/findings/:findingID/decision — the
// operator's status change on a finding. It is the ONLY code path that writes
// findings.status, approver, date_approved and decision_note.
//
// In one transaction it: validates the transition against findingTransitions;
// writes the four human fields; closes the finding's pending recommendation as
// accepted (when the recommendation agreed with the decision) or declined
// (when it did not); and appends a finding.decided audit row. A same-status
// decision is a 200 no-op so the reconciler can replay FINDINGS.md.
func decideFindingHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		findingID, err := strconv.ParseInt(c.Param("findingID"), 10, 64)
		if err != nil {
			validationError(c, errors.New("finding id must be an integer"))
			return
		}

		var request model.FindingDecisionRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		request.Approver = strings.TrimSpace(request.Approver)
		request.Note = strings.TrimSpace(request.Note)
		request.DateApproved = strings.TrimSpace(request.DateApproved)
		if request.Approver == "" {
			validationError(c, errors.New("approver must not be empty or whitespace-only"))
			return
		}
		if isOneOf(request.Status, []string{"rejected", "retired", "superseded"}) && request.Note == "" {
			validationError(c, errors.New("note is required when the status is rejected, retired or superseded"))
			return
		}
		if request.DateApproved == "" {
			request.DateApproved = time.Now().UTC().Format("2006-01-02")
		}

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		var current string
		err = tx.QueryRowContext(c.Request.Context(),
			`SELECT status FROM findings WHERE id = ?`, findingID).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "finding not found"})
			return
		}
		if err != nil {
			internalError(c, err)
			return
		}

		if current != request.Status && !isOneOf(request.Status, findingTransitions[current]) {
			validationError(c, fmt.Errorf("a %s finding cannot move to %s", current, request.Status))
			return
		}

		if current != request.Status {
			if request.Status == "proposed" {
				// Reopening clears the decision fields so the ledger reads as it did.
				_, err = tx.ExecContext(c.Request.Context(),
					`UPDATE findings SET status = 'proposed', approver = NULL, date_approved = NULL,
					        decision_note = NULL WHERE id = ?`, findingID)
			} else {
				_, err = tx.ExecContext(c.Request.Context(),
					`UPDATE findings SET status = ?, approver = ?, date_approved = ?,
					        decision_note = ? WHERE id = ?`,
					request.Status, request.Approver, request.DateApproved,
					nullIfEmpty(request.Note), findingID)
			}
			if err != nil {
				internalError(c, err)
				return
			}

			if err := closePendingRecommendation(c, tx, findingID, request.Status, request.Approver); err != nil {
				internalError(c, err)
				return
			}

			_, err = tx.ExecContext(c.Request.Context(),
				`INSERT INTO flight_recorder (event, note, agent) VALUES ('finding.decided', ?, ?)`,
				fmt.Sprintf("Finding #%d: %s -> %s", findingID, current, request.Status),
				"operator:"+request.Approver)
			if err != nil {
				internalError(c, err)
				return
			}
		}

		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"id":       findingID,
			"status":   request.Status,
			"previous": current,
			"changed":  current != request.Status,
		})
	}
}

// closePendingRecommendation marks a finding's pending recommendation accepted
// when it agreed with the operator's decision and declined otherwise. A
// reopen to proposed leaves recommendations alone. Executes inside the caller's
// transaction.
func closePendingRecommendation(c *gin.Context, tx *sql.Tx, findingID int64, newStatus, decidedBy string) error {
	if newStatus == "proposed" {
		return nil
	}
	agree := map[string]string{
		"approve":   "approved",
		"reject":    "rejected",
		"supersede": "superseded",
	}
	var recID int64
	var recommendation string
	err := tx.QueryRowContext(c.Request.Context(),
		`SELECT id, recommendation FROM finding_recommendations
		 WHERE finding_id = ? AND state = 'pending'`, findingID).Scan(&recID, &recommendation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	state := "declined"
	if want, ok := agree[recommendation]; ok && (want == newStatus || (want == "approved" && newStatus == "applied")) {
		state = "accepted"
	}
	_, err = tx.ExecContext(c.Request.Context(),
		`UPDATE finding_recommendations SET state = ?, decided_by = ?, decided_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, state, decidedBy, recID)
	return err
}

// setFindingHarnessHandler handles PUT /v1/findings/:findingID/harness — the
// operator-side backfill route used by scripts/triage-backfill-harness.py for
// rows mirrored before findings.harness existed. Not registered in any mcpd
// profile.
func setFindingHarnessHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		findingID, err := strconv.ParseInt(c.Param("findingID"), 10, 64)
		if err != nil {
			validationError(c, errors.New("finding id must be an integer"))
			return
		}
		var request struct {
			Harness string `json:"harness" binding:"required,max=128"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		harness := strings.TrimSpace(request.Harness)
		if harness == "" {
			validationError(c, errors.New("harness must not be empty or whitespace-only"))
			return
		}
		result, err := db.ExecContext(c.Request.Context(),
			`UPDATE findings SET harness = ? WHERE id = ?`, harness, findingID)
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
			c.JSON(http.StatusNotFound, gin.H{"error": "finding not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"id": findingID, "harness": harness, "updated": true})
	}
}

func scanFinding(row scanner) (model.Finding, error) {
	var finding model.Finding
	var (
		findingDate, target, rationale, approver, dateApproved, missionID, harness, decisionNote sql.NullString

		tCategory, tSecondary, tSummary, tClassifiedBy, tClassifiedAt sql.NullString
		tDirective, tRunID                                            sql.NullInt64
		tConfidence                                                   sql.NullFloat64

		rID, rGroupID, rSupersededBy, rRunID                               sql.NullInt64
		rRecommendation, rRationale, rChange, rState, rDecidedBy, rDecided sql.NullString
		rCreatedAt                                                         sql.NullString
	)

	err := row.Scan(
		&finding.ID, &findingDate, &target, &finding.Suggestion, &rationale, &finding.Status,
		&approver, &dateApproved, &missionID, &harness, &decisionNote, &finding.CreatedAt,
		&tCategory, &tSecondary, &tDirective, &tConfidence, &tSummary,
		&tClassifiedBy, &tRunID, &tClassifiedAt,
		&rID, &rGroupID, &rRecommendation, &rSupersededBy, &rRationale, &rChange,
		&rState, &rDecidedBy, &rDecided, &rRunID, &rCreatedAt,
	)
	if err != nil {
		return finding, err
	}

	finding.FindingDate = nullStringPtr(findingDate)
	finding.Target = nullStringPtr(target)
	finding.Rationale = nullStringPtr(rationale)
	finding.Approver = nullStringPtr(approver)
	finding.DateApproved = nullStringPtr(dateApproved)
	finding.MissionID = nullStringPtr(missionID)
	finding.Harness = nullStringPtr(harness)
	finding.DecisionNote = nullStringPtr(decisionNote)

	if tCategory.Valid {
		finding.Triage = &model.FindingTriage{
			FindingID:          finding.ID,
			Category:           tCategory.String,
			SecondaryCategory:  nullStringPtr(tSecondary),
			DirectiveCandidate: tDirective.Valid && tDirective.Int64 != 0,
			Summary:            nullStringPtr(tSummary),
			ClassifiedBy:       tClassifiedBy.String,
			RunID:              nullInt64Ptr(tRunID),
			ClassifiedAt:       tClassifiedAt.String,
		}
		if tConfidence.Valid {
			v := tConfidence.Float64
			finding.Triage.Confidence = &v
		}
	}

	if rID.Valid {
		finding.Recommendation = &model.FindingRecommendation{
			ID:             rID.Int64,
			FindingID:      finding.ID,
			GroupID:        nullInt64Ptr(rGroupID),
			Recommendation: rRecommendation.String,
			SupersededBy:   nullInt64Ptr(rSupersededBy),
			Rationale:      rRationale.String,
			ProposedChange: nullStringPtr(rChange),
			State:          rState.String,
			DecidedBy:      nullStringPtr(rDecidedBy),
			DecidedAt:      nullStringPtr(rDecided),
			RunID:          nullInt64Ptr(rRunID),
			CreatedAt:      rCreatedAt.String,
		}
	}

	return finding, nil
}

func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// openMissionFor returns the harness's open mission (in-progress or blocked,
// most recently updated) for a finding that arrives without a mission_id, or
// "" when there is none. A finding dated before that mission opened is not
// from it — the reconciler replaying an older FINDINGS.md backlog — and also
// gets "".
func openMissionFor(ctx context.Context, tx *sql.Tx, harness, findingDate string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM missions
		  WHERE harness = ? AND status IN ('in-progress', 'blocked')
		    AND (? = '' OR ? >= date(opened_at))
		  ORDER BY updated_at DESC, id DESC
		  LIMIT 1`,
		harness, findingDate, findingDate,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
