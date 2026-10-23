// Package api — mission grading: the nightly mission-grader's routes and the
// harness performance views built on its grades.
//
//	GET    /v1/mission-grades/waiting      how many finished missions await a grade
//	POST   /v1/mission-grades/claim        claim up to 10 and return their grading packets
//	POST   /v1/mission-grades              write verdicts for claimed missions
//	GET    /v1/mission-grades              list grades (harness, state, grade filters)
//	GET    /v1/mission-grades/performance  per-harness average, spread and recent grades
//	DELETE /v1/mission-grades/:missionID   operator only: reopen a mission for regrading; refused for an excluded one
//	PUT    /v1/mission-grades/:missionID/exclusion  operator only: set a finished mission aside as N/A
//	DELETE /v1/mission-grades/:missionID/exclusion  operator only: lift that exclusion
//
// The grader works from the database alone. Each claimed mission comes back
// as a compact packet the server assembles here — the brief's goal and
// criteria, the debrief's judgement sections, the steps, the findings and the
// agent notes, every text field capped — plus signals the server counts
// itself. The agent reads the packet and writes a letter, a summary and
// suggestions; it never fetches anything else and never writes the signals.
//
// No mission is graded in a loop. Claiming creates the mission's row (state
// 'pending', attempts + 1); a verdict closes it for good. A claim that is
// never answered is retried by one later run and then marked 'skipped'. The
// operator's DELETE reopens a mission, but refuses an excluded one, which only
// the exclusion route's DELETE lifts. The exclusion routes set a mission aside
// as N/A, and an excluded mission is never claimed. mcpd registers no tool that
// reaches a DELETE or the exclusion routes.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/criteria"
	"bishop-memory/internal/importer"
	"bishop-memory/internal/model"
)

// gradableSQL is the WHERE clause, over missions m, for a mission the grader
// may grade: closed as complete, or ended as failed.
const gradableSQL = `(m.status = 'complete' OR m.outcome = 'failed')`

// maxGradeAttempts is how many runs may claim a mission without answering
// before it is marked skipped.
const maxGradeAttempts = 2

// gradePoints maps a letter to the points the averages use.
var gradePoints = map[string]float64{"A": 5, "B": 4, "C": 3, "D": 2, "E": 1, "F": 0}

// pointsToGrade turns an average back into the nearest letter.
func pointsToGrade(p float64) string {
	switch {
	case p >= 4.5:
		return "A"
	case p >= 3.5:
		return "B"
	case p >= 2.5:
		return "C"
	case p >= 1.5:
		return "D"
	case p >= 0.5:
		return "E"
	}
	return "F"
}

// --- Work check --------------------------------------------------------------

// waitingGradesHandler handles GET /v1/mission-grades/waiting. The runner asks
// this before starting an agent, so a night with nothing to grade costs nothing.
func waitingGradesHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var n int64
		err := db.QueryRowContext(c.Request.Context(), `
			SELECT COUNT(*) FROM missions m
			  LEFT JOIN mission_grades g ON g.mission_id = m.id
			 WHERE `+gradableSQL+`
			   AND (g.mission_id IS NULL OR (g.state = 'pending' AND g.attempts < ?))`,
			maxGradeAttempts).Scan(&n)
		if err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"waiting": n})
	}
}

// --- Claim -------------------------------------------------------------------

// claimGradesHandler handles POST /v1/mission-grades/claim.
func claimGradesHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		var request model.ClaimGradesRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		limit := request.Limit
		if limit == 0 {
			limit = 10
		}
		var runID any
		if request.RunID != nil {
			runID = *request.RunID
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		// A claim abandoned by maxGradeAttempts earlier runs stops here.
		if _, err := tx.ExecContext(ctx, `
			UPDATE mission_grades SET state = 'skipped'
			 WHERE state = 'pending' AND attempts >= ?
			   AND (run_id IS NULL OR ? IS NULL OR run_id <> ?)`,
			maxGradeAttempts, runID, runID); err != nil {
			internalError(c, err)
			return
		}

		var ids []string
		if len(request.MissionIDs) > 0 {
			var refused []gin.H
			for _, id := range request.MissionIDs {
				id = strings.TrimSpace(id)
				var gradable bool
				var state sql.NullString
				// COALESCE keeps the gradable test from yielding NULL for an
				// unfinished mission with no outcome, which would not scan into a bool.
				err := tx.QueryRowContext(ctx, `
					SELECT COALESCE(`+gradableSQL+`, 0), g.state FROM missions m
					  LEFT JOIN mission_grades g ON g.mission_id = m.id WHERE m.id = ?`, id).Scan(&gradable, &state)
				switch {
				case errors.Is(err, sql.ErrNoRows):
					refused = append(refused, gin.H{"mission_id": id, "reason": "no such mission"})
				case err != nil:
					internalError(c, err)
					return
				case !gradable:
					refused = append(refused, gin.H{"mission_id": id, "reason": "mission is not finished"})
				case state.Valid && state.String == "excluded":
					// Its own case, ahead of the verdict case: the upsert below would overwrite the exclusion.
					refused = append(refused, gin.H{"mission_id": id, "reason": "excluded by the operator; DELETE /v1/mission-grades/" + id + "/exclusion lifts it"})
				case state.Valid && (state.String == "graded" || state.String == "insufficient"):
					refused = append(refused, gin.H{"mission_id": id, "reason": "already has a verdict; DELETE /v1/mission-grades/" + id + " to regrade"})
				default:
					ids = append(ids, id)
				}
			}
			if len(refused) > 0 {
				c.JSON(http.StatusConflict, gin.H{"error": "some missions cannot be claimed", "refused": refused})
				return
			}
		} else {
			rows, err := tx.QueryContext(ctx, `
				SELECT m.id FROM missions m
				  LEFT JOIN mission_grades g ON g.mission_id = m.id
				 WHERE `+gradableSQL+`
				   AND (g.mission_id IS NULL
				        OR (g.state = 'pending' AND g.attempts < ? AND (g.run_id IS NULL OR ? IS NULL OR g.run_id <> ?)))
				 ORDER BY COALESCE(m.closed_at, m.updated_at), m.id
				 LIMIT ?`, maxGradeAttempts, runID, runID, limit)
			if err != nil {
				internalError(c, err)
				return
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					internalError(c, err)
					return
				}
				ids = append(ids, id)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				internalError(c, err)
				return
			}
		}

		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO mission_grades (mission_id, state, attempts, run_id, claimed_at)
				VALUES (?, 'pending', 1, ?, CURRENT_TIMESTAMP)
				ON CONFLICT(mission_id) DO UPDATE SET
				  state = 'pending', attempts = attempts + 1, run_id = excluded.run_id,
				  claimed_at = CURRENT_TIMESTAMP`, id, runID); err != nil {
				internalError(c, err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}

		packets := make([]gin.H, 0, len(ids))
		for _, id := range ids {
			packet, err := gradingPacket(ctx, db, id)
			if err != nil {
				internalError(c, err)
				return
			}
			packets = append(packets, packet)
		}
		c.JSON(http.StatusOK, gin.H{"missions": packets})
	}
}

// --- The grading packet ------------------------------------------------------

// Caps on the text the packet carries, so ten missions stay small enough to
// grade in one read. Steps, findings and notes go out as one line each: the
// signals already count them, and the grader needs only their outline.
const (
	packetSectionCap = 900
	packetStepCap    = 100
	packetLineCap    = 140
	packetMaxSteps   = 30
	packetMaxRows    = 12
)

// debriefSections are the debrief headings the grader needs, matched by
// prefix ("Wrong Assumptions (Mandatory)" matches "Wrong Assumptions").
var debriefSections = []string{
	"Mission Summary",
	"Acceptance Criteria Outcome",
	"Tracker And Reality",
	"Wrong Assumptions",
	"Sub-Agent Mistakes",
	"QA Verdict",
}

var (
	injectedStep = regexp.MustCompile(`^\d+[a-z]+$`)
)

// criteriaOutcome counts the debrief's "Acceptance Criteria Outcome" lines by
// result. Debriefs write the result as a word after a dash and often leave
// the checkbox unticked even on a pass, so the word wins; the checkbox
// decides only a line that carries no word. A line with neither is unclear.
// This function delegates to the criteria package's shared parsing logic.
func criteriaOutcome(section string) (met, partial, unmet, unclear int) {
	parsed := criteria.ParseDebriefOutcome(section)
	for _, c := range parsed {
		switch c.Result {
		case "met":
			met++
		case "partial":
			partial++
		case "unmet":
			unmet++
		case "unclear":
			unclear++
		}
	}
	return met, partial, unmet, unclear
}

// gradingPacket assembles everything the grader may consider about one
// mission, from the database only.
func gradingPacket(ctx context.Context, db *sql.DB, id string) (gin.H, error) {
	missions, err := queryMaps(ctx, db, `
		SELECT id, title, harness, status, outcome, opened_at, closed_at,
		       -- NULL when the row cannot say: no close time, or an imported mission
		       -- whose opened and closed times are the same import timestamp.
		       CASE WHEN closed_at IS NULL OR closed_at <= opened_at THEN NULL
		            ELSE CAST(ROUND((julianday(closed_at) - julianday(opened_at)) * 1440) AS INTEGER) END AS duration_minutes
		  FROM missions WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(missions) == 0 {
		return nil, fmt.Errorf("mission %s vanished while being claimed", id)
	}

	docs := map[string]string{}
	docRows, err := queryMaps(ctx, db,
		`SELECT kind, body FROM documents WHERE mission_id = ? AND kind IN ('brief', 'debrief')`, id)
	if err != nil {
		return nil, err
	}
	for _, d := range docRows {
		docs[fmt.Sprint(d["kind"])] = fmt.Sprint(d["body"])
	}
	brief := markdownSections(docs["brief"])
	debrief := markdownSections(docs["debrief"])

	debriefOut := gin.H{}
	for _, name := range debriefSections {
		if text, ok := sectionByPrefix(debrief, name); ok {
			debriefOut[name] = capText(text, packetSectionCap)
		}
	}

	steps, err := queryMaps(ctx, db, `
		SELECT step, agent, status, COALESCE(summary, notes) AS summary
		  FROM mission_steps WHERE mission_id = ?
		 ORDER BY CAST(step AS INTEGER), step`, id)
	if err != nil {
		return nil, err
	}
	for _, s := range steps {
		s["summary"] = capText(stringOf(s["summary"]), packetStepCap)
	}

	findings, err := queryMaps(ctx, db, `
		SELECT f.id, f.target, f.status, t.category,
		       COALESCE(t.summary, f.suggestion) AS summary
		  FROM findings f LEFT JOIN finding_triage t ON t.finding_id = f.id
		 WHERE f.mission_id = ? ORDER BY f.id`, id)
	if err != nil {
		return nil, err
	}
	for _, f := range findings {
		f["summary"] = capText(stringOf(f["summary"]), packetLineCap)
	}

	notes, err := missionServiceRecords(ctx, db, missions[0], steps)
	if err != nil {
		return nil, err
	}

	signals, err := gradeSignals(ctx, db, id, brief, debrief, steps, findings)
	if err != nil {
		return nil, err
	}

	stepLines := make([]string, 0, len(steps))
	for _, s := range firstRows(steps, packetMaxSteps) {
		stepLines = append(stepLines, capText(fmt.Sprintf("%s %s %s: %s",
			stringOf(s["step"]), stringOf(s["agent"]), stringOf(s["status"]), stringOf(s["summary"])), packetStepCap))
	}
	if len(steps) > packetMaxSteps {
		stepLines = append(stepLines, fmt.Sprintf("… %d more steps (counted in signals)", len(steps)-packetMaxSteps))
	}
	findingLines := make([]string, 0, len(findings))
	for _, f := range firstRows(findings, packetMaxRows) {
		findingLines = append(findingLines, capText(fmt.Sprintf("#%v [%s/%s] %s: %s",
			f["id"], stringOf(f["status"]), stringOf(f["category"]), stringOf(f["target"]), stringOf(f["summary"])), packetLineCap))
	}
	noteLines := make([]string, 0, len(notes))
	for _, n := range firstRows(notes, packetMaxRows) {
		noteLines = append(noteLines, capText(fmt.Sprintf("%s (%s) %s: %s",
			stringOf(n["agent"]), stringOf(n["source"]), stringOf(n["title"]), stringOf(n["note"])), packetLineCap))
	}

	packet := gin.H{
		"mission":  missions[0],
		"signals":  signals,
		"brief":    gin.H{"goal": capText(brief["Goal"], packetSectionCap), "acceptance_criteria": capText(brief["Acceptance Criteria"], packetSectionCap)},
		"debrief":  debriefOut,
		"steps":    stepLines,
		"findings": findingLines,
		"notes":    noteLines,
	}
	return packet, nil
}

// missionServiceRecords returns the agent notes recorded about the mission's
// crew while it ran — the same rows the HUD shows.
func missionServiceRecords(ctx context.Context, db *sql.DB, mission map[string]any, steps []map[string]any) ([]map[string]any, error) {
	names := []any{}
	seen := map[string]bool{}
	for _, step := range steps {
		if name := importer.CrewName(stringOf(step["agent"])); name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return []map[string]any{}, nil
	}
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(names)), ",") + ")"
	rows, err := queryMaps(ctx, db,
		`SELECT agent, title, note, source FROM service_records
		  WHERE `+crewNameSQL+` IN `+in+`
		    AND record_date >= date(?) AND record_date <= date(COALESCE(?, 'now'))
		  ORDER BY record_date, id`,
		append(names, mission["opened_at"], mission["closed_at"])...)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		r["note"] = capText(stringOf(r["note"]), packetLineCap)
	}
	return rows, nil
}

// gradeSignals counts, from the database, the facts a grade rests on. They
// are stored with the grade, so a reader can check any letter against them.
func gradeSignals(ctx context.Context, db *sql.DB, id string, brief, debrief map[string]string,
	steps, findings []map[string]any) (gin.H, error) {

	outcome, _ := sectionByPrefix(debrief, "Acceptance Criteria Outcome")
	met, partial, unmet, unclear := criteriaOutcome(outcome)
	stepStatus := map[string]int{}
	injected, escalations, qa := 0, 0, 0
	for _, s := range steps {
		status := stringOf(s["status"])
		if status == "" {
			status = "unknown"
		}
		stepStatus[status]++
		if injectedStep.MatchString(strings.TrimSpace(stringOf(s["step"]))) {
			injected++
		}
		switch importer.CrewName(stringOf(s["agent"])) {
		case "vasquez":
			escalations++
		case "ripley":
			qa++
		}
	}
	findingStatus := map[string]int{}
	for _, f := range findings {
		findingStatus[stringOf(f["status"])]++
	}
	var blocked int64
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM flight_recorder WHERE mission_id = ? AND event = 'blocked'`, id).Scan(&blocked); err != nil {
		return nil, err
	}
	return gin.H{
		"criteria_planned":   len(criteria.ParseBriefCriteria(brief["Acceptance Criteria"])),
		"criteria_met":       met,
		"criteria_partial":   partial,
		"criteria_unmet":     unmet,
		"criteria_unclear":   unclear,
		"steps":              len(steps),
		"steps_by_status":    stepStatus,
		"injected_steps":     injected,
		"escalation_steps":   escalations,
		"qa_steps":           qa,
		"blocked_events":     blocked,
		"findings":           len(findings),
		"findings_by_status": findingStatus,
		"has_brief":          brief != nil && len(brief) > 0,
		"has_debrief":        debrief != nil && len(debrief) > 0,
	}, nil
}

// markdownSections splits a document into its "## " sections by heading.
func markdownSections(body string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(body) == "" {
		return out
	}
	key := ""
	var buf []string
	flush := func() {
		if key != "" {
			out[key] = strings.TrimSpace(strings.Join(buf, "\n"))
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "## ") {
			flush()
			key, buf = strings.TrimSpace(strings.TrimPrefix(line, "## ")), nil
			continue
		}
		buf = append(buf, line)
	}
	flush()
	return out
}

func sectionByPrefix(sections map[string]string, prefix string) (string, bool) {
	for heading, text := range sections {
		if strings.HasPrefix(strings.ToLower(heading), strings.ToLower(prefix)) {
			return text, true
		}
	}
	return "", false
}

func capText(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= limit {
		return s
	}
	return string([]rune(s)[:limit-1]) + "…"
}

func stringOf(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func firstRows(rows []map[string]any, n int) []map[string]any {
	if len(rows) > n {
		return rows[:n]
	}
	return rows
}

// --- Write -------------------------------------------------------------------

// writeGradesHandler handles POST /v1/mission-grades. Only a mission this
// or an earlier run claimed (state 'pending') takes a verdict; anything else
// is reported and skipped, never applied.
func writeGradesHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		var request model.WriteGradesRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		var runID any
		if request.RunID != nil {
			runID = *request.RunID
		}

		written := 0
		skipped := []gin.H{}
		for _, item := range request.Items {
			id := strings.TrimSpace(item.MissionID)
			if item.Insufficient == (item.Grade != "") {
				skipped = append(skipped, gin.H{"mission_id": id, "reason": "give exactly one of grade (A-F) or insufficient=true"})
				continue
			}
			state, grade := "graded", any(item.Grade)
			if item.Insufficient {
				state, grade = "insufficient", nil
			}
			packet, err := gradingPacketSignals(ctx, db, id)
			if err != nil {
				internalError(c, err)
				return
			}
			signals, err := json.Marshal(packet)
			if err != nil {
				internalError(c, err)
				return
			}
			result, err := db.ExecContext(ctx, `
				UPDATE mission_grades
				   SET state = ?, grade = ?, summary = ?, suggestions = ?, signals = ?,
				       graded_by = ?, run_id = COALESCE(?, run_id), graded_at = CURRENT_TIMESTAMP
				 WHERE mission_id = ? AND state = 'pending'`,
				state, grade, strings.TrimSpace(item.Summary), nullIfEmpty(item.Suggestions), string(signals),
				strings.TrimSpace(request.GradedBy), runID, id)
			if err != nil {
				internalError(c, err)
				return
			}
			if n, _ := result.RowsAffected(); n == 0 {
				skipped = append(skipped, gin.H{"mission_id": id, "reason": "not claimed, or already has a verdict"})
				continue
			}
			written++
		}
		c.JSON(http.StatusOK, gin.H{"written": written, "skipped": skipped})
	}
}

// gradingPacketSignals recomputes only a mission's signals, for storing with
// its verdict.
func gradingPacketSignals(ctx context.Context, db *sql.DB, id string) (gin.H, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM missions WHERE id = ?)`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return gin.H{}, nil
	}
	packet, err := gradingPacket(ctx, db, id)
	if err != nil {
		return nil, err
	}
	return packet["signals"].(gin.H), nil
}

// --- Read --------------------------------------------------------------------

// listGradesHandler handles GET /v1/mission-grades. Filters: harness, state
// (default: graded, insufficient and excluded), grade, limit (default 100).
func listGradesHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, _, err := parseLimitOffset(c)
		if err != nil {
			validationError(c, err)
			return
		}
		if limit == 0 {
			limit = 100
		}
		where := []string{}
		args := []any{}
		if v := strings.TrimSpace(c.Query("state")); v != "" {
			where = append(where, "g.state = ?")
			args = append(args, v)
		} else {
			where = append(where, "g.state IN ('graded', 'insufficient', 'excluded')")
		}
		if v := strings.TrimSpace(c.Query("harness")); v != "" {
			where = append(where, "m.harness = ?")
			args = append(args, v)
		}
		if v := strings.TrimSpace(c.Query("grade")); v != "" {
			where = append(where, "g.grade = ?")
			args = append(args, strings.ToUpper(v))
		}
		rows, err := queryMaps(c.Request.Context(), db, `
			SELECT g.mission_id, m.title, m.harness, m.outcome, m.closed_at,
			       g.state, g.grade, g.summary, g.suggestions, g.signals, g.graded_by,
			       g.run_id, g.attempts, g.graded_at
			  FROM mission_grades g JOIN missions m ON m.id = g.mission_id
			 WHERE `+strings.Join(where, " AND ")+`
			 ORDER BY COALESCE(m.closed_at, g.graded_at) DESC, g.mission_id DESC
			 LIMIT ?`, append(args, limit)...)
		if err != nil {
			internalError(c, err)
			return
		}
		for _, r := range rows {
			decodeSignals(r)
		}
		c.JSON(http.StatusOK, gin.H{"grades": rows})
	}
}

// decodeSignals turns a row's stored signals JSON into an object.
func decodeSignals(row map[string]any) {
	raw, ok := row["signals"].(string)
	if !ok || raw == "" {
		row["signals"] = nil
		return
	}
	var v any
	if json.Unmarshal([]byte(raw), &v) == nil {
		row["signals"] = v
	}
}

// harnessPerformance is one harness's line on the performance page. Excluded
// counts the missions the operator set aside; they are in no average or
// distribution.
type harnessPerformance struct {
	Harness      string         `json:"harness"`
	Graded       int            `json:"graded"`
	Insufficient int            `json:"insufficient"`
	Skipped      int            `json:"skipped"`
	Excluded     int            `json:"excluded"`
	Waiting      int            `json:"waiting"`
	AvgPoints    *float64       `json:"avg_points"`
	AvgGrade     *string        `json:"avg_grade"`
	Distribution map[string]int `json:"distribution"`
	// Recent is the last ten graded missions, newest first.
	Recent []gin.H `json:"recent"`
}

// performanceHandler handles GET /v1/mission-grades/performance: one line per
// harness that has finished missions, plus the same figures over every
// harness. Only graded missions enter an average or the distribution; an
// excluded mission is counted under excluded and nowhere else.
func performanceHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		rows, err := queryMaps(ctx, db, `
			SELECT COALESCE(m.harness, '') AS harness, m.id, m.title, m.closed_at,
			       g.state, g.grade
			  FROM missions m LEFT JOIN mission_grades g ON g.mission_id = m.id
			 WHERE `+gradableSQL+`
			 ORDER BY COALESCE(m.closed_at, m.updated_at) DESC, m.id DESC`)
		if err != nil {
			internalError(c, err)
			return
		}
		byHarness := map[string]*harnessPerformance{}
		overall := &harnessPerformance{Harness: "", Distribution: map[string]int{}, Recent: []gin.H{}}
		sums := map[*harnessPerformance]float64{}
		for _, r := range rows {
			name := stringOf(r["harness"])
			h := byHarness[name]
			if h == nil {
				h = &harnessPerformance{Harness: name, Distribution: map[string]int{}, Recent: []gin.H{}}
				byHarness[name] = h
			}
			for _, p := range []*harnessPerformance{h, overall} {
				switch stringOf(r["state"]) {
				case "graded":
					grade := stringOf(r["grade"])
					p.Graded++
					p.Distribution[grade]++
					sums[p] += gradePoints[grade]
					if len(p.Recent) < 10 {
						p.Recent = append(p.Recent, gin.H{"mission_id": r["id"], "title": r["title"], "grade": grade, "closed_at": r["closed_at"]})
					}
				case "insufficient":
					p.Insufficient++
				case "excluded":
					p.Excluded++
				case "skipped":
					p.Skipped++
				default:
					p.Waiting++
				}
			}
		}
		finish := func(p *harnessPerformance) {
			if p.Graded == 0 {
				return
			}
			avg := math.Round(sums[p]/float64(p.Graded)*100) / 100
			grade := pointsToGrade(avg)
			p.AvgPoints, p.AvgGrade = &avg, &grade
		}
		harnesses := make([]*harnessPerformance, 0, len(byHarness))
		for _, h := range byHarness {
			finish(h)
			harnesses = append(harnesses, h)
		}
		finish(overall)
		sort.Slice(harnesses, func(i, j int) bool {
			if harnesses[i].Graded != harnesses[j].Graded {
				return harnesses[i].Graded > harnesses[j].Graded
			}
			return harnesses[i].Harness < harnesses[j].Harness
		})
		c.JSON(http.StatusOK, gin.H{"harnesses": harnesses, "overall": overall})
	}
}

// --- Regrade -----------------------------------------------------------------

// deleteGradeHandler handles DELETE /v1/mission-grades/:missionID: the
// operator reopening a mission for grading. It deletes the mission's row in
// any state except 'excluded'. An excluded row is the operator's N/A, lifted
// only by DELETE .../exclusion, so this route answers 409 and keeps the row. A
// mission with no row answers 200 with deleted false. No mcpd profile registers it.
func deleteGradeHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		id := c.Param("missionID")

		// One statement carries both the state test and the delete, so no write
		// can land between them and an excluded row is never deleted. state is
		// NOT NULL, so <> leaves no row out of the test.
		result, err := db.ExecContext(ctx, `DELETE FROM mission_grades WHERE mission_id = ? AND state <> 'excluded'`, id)
		if err != nil {
			internalError(c, err)
			return
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			// Nothing was deleted: the mission has no row, or its row is
			// excluded. Only now is the row read, to tell the two apart.
			var one int
			err := db.QueryRowContext(ctx, `SELECT 1 FROM mission_grades WHERE mission_id = ? AND state = 'excluded'`, id).Scan(&one)
			if err == nil {
				c.JSON(http.StatusConflict, gin.H{"error": "mission is excluded; DELETE /v1/mission-grades/" + id + "/exclusion lifts it"})
				return
			}
			if !errors.Is(err, sql.ErrNoRows) {
				internalError(c, err)
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"mission_id": id, "deleted": n > 0})
	}
}

// --- Exclusion (N/A) ---------------------------------------------------------

// setExclusionHandler handles PUT /v1/mission-grades/:missionID/exclusion: the
// operator sets a finished mission aside as N/A. The mission's row becomes
// state 'excluded' with grade, suggestions and signals NULL, the note in
// summary and graded_by 'operator'. graded_at is not written; it keeps the
// value the row had. A row that held a verdict, a claim or a skip is
// overwritten. The response names the state and grade the row held before,
// both null when there was no row. Refused with 404 for an unknown mission
// and 409 for one the grader could not grade. No mcpd profile registers it.
func setExclusionHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		id := c.Param("missionID")
		var request model.ExclusionRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		// The binding's required tag admits a note of spaces, so the trimmed note is checked here.
		note := strings.TrimSpace(request.Note)
		if note == "" {
			validationError(c, errors.New("note must not be empty or whitespace-only"))
			return
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		// COALESCE keeps the gradable test from yielding NULL for an unfinished
		// mission with no outcome, which would not scan into a bool.
		var gradable bool
		var prevState, prevGrade sql.NullString
		err = tx.QueryRowContext(ctx, `
			SELECT COALESCE(`+gradableSQL+`, 0), g.state, g.grade FROM missions m
			  LEFT JOIN mission_grades g ON g.mission_id = m.id WHERE m.id = ?`, id).Scan(&gradable, &prevState, &prevGrade)
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "mission not found"})
			return
		}
		if err != nil {
			internalError(c, err)
			return
		}
		if !gradable {
			c.JSON(http.StatusConflict, gin.H{"error": "mission is not finished; only a finished mission can be excluded"})
			return
		}

		// graded_at is never written here. triageRunLastActivitySQL reads it as
		// the grader's activity, so an operator's N/A must not make a hung run
		// look alive. An excluded row keeps the graded_at of any verdict it replaced.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mission_grades (mission_id, state, grade, summary, graded_by)
			VALUES (?, 'excluded', NULL, ?, 'operator')
			ON CONFLICT(mission_id) DO UPDATE SET
			  state = 'excluded', grade = NULL, summary = excluded.summary,
			  suggestions = NULL, signals = NULL, graded_by = 'operator'`, id, note); err != nil {
			internalError(c, err)
			return
		}
		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"mission_id":     id,
			"state":          "excluded",
			"previous_state": nullStringPtr(prevState),
			"previous_grade": nullStringPtr(prevGrade),
		})
	}
}

// clearExclusionHandler handles DELETE /v1/mission-grades/:missionID/exclusion:
// the operator lifts an exclusion. The row is deleted, so the mission has no
// grade and the nightly grader claims it again. Refused with 404 when the
// mission is not excluded: unknown, ungraded, or holding another state. No
// mcpd profile registers it.
func clearExclusionHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("missionID")
		result, err := db.ExecContext(c.Request.Context(),
			`DELETE FROM mission_grades WHERE mission_id = ? AND state = 'excluded'`, id)
		if err != nil {
			internalError(c, err)
			return
		}
		if n, _ := result.RowsAffected(); n == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "mission is not excluded"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"mission_id": id, "deleted": true})
	}
}
