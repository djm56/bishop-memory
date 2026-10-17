// Package api — HTTP handler for GET /v1/brief-prep.
//
// briefPrepHandler constructs an insight brief by analyzing a goal statement:
// extracting and tokenizing terms, searching documents for related briefs,
// and returning ranked missions, lessons, directives, and patterns.
//
// The goal is tokenized once; those tokens drive one FTS query for missions,
// one detail query per matched mission, one lesson query for all matched
// missions, and in-Go scoring of every active directive and every pattern.
// No schema changes. All required tables (documents_fts, missions, mission_lessons,
// directives, directive_proposals, patterns) must exist; if any are missing
// or malformed, the handler returns 500.
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// briefPrepMission is one ranked mission returned by GET /v1/brief-prep.
type briefPrepMission struct {
	MissionID        string            `json:"mission_id"`
	Title            string            `json:"title"`
	Status           string            `json:"status"`
	Outcome          *string           `json:"outcome"`
	Grade            *string           `json:"grade"`
	Rank             float64           `json:"rank"`
	WrongAssumptions []briefPrepLesson `json:"wrong_assumptions"`
	ReuseChecklist   []briefPrepReuse  `json:"reuse_checklist"`
}

// briefPrepReuse is one reuse-step row from mission_lessons. It carries only
// ord and text; why and correction are never serialised for reuse items.
type briefPrepReuse struct {
	Ord  int    `json:"ord"`
	Text string `json:"text"`
}

// briefPrepLesson is one wrong-assumption row from mission_lessons.
type briefPrepLesson struct {
	Ord        int     `json:"ord"`
	Text       string  `json:"text"`
	Why        *string `json:"why"`
	Correction *string `json:"correction"`
}

// briefPrepDirective is one directive returned by GET /v1/brief-prep.
type briefPrepDirective struct {
	DirectiveID string `json:"directive_id"`
	Title       string `json:"title"`
	Rule        string `json:"rule"`
	Score       int    `json:"score"`
}

// briefPrepPattern is one pattern returned by GET /v1/brief-prep.
type briefPrepPattern struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Context  string `json:"context"`
	Solution string `json:"solution"`
	Score    int    `json:"score"`
}

// briefPrepResponse is the JSON response body for GET /v1/brief-prep.
type briefPrepResponse struct {
	Goal       string               `json:"goal"`
	Harness    *string              `json:"harness"`
	Missions   []briefPrepMission   `json:"missions"`
	Directives []briefPrepDirective `json:"directives"`
	Patterns   []briefPrepPattern   `json:"patterns"`
}

// briefPrepHandler handles GET /v1/brief-prep?goal=...&harness=...&exclude=...
//
// Behaviour:
//   - goal trimmed; empty → 400 {"error":"invalid request","details":"goal is required"}
//     (validationError's body for a plain error).
//   - Tokens: split on whitespace and non-alphanumeric boundaries, lowercase,
//     drop tokens shorter than 3 runes and a short stopword list
//     (the, and, for, with, from, into, that, this, are, was, not, by, as, on, or, to, of, in),
//     de-duplicate, keep the first 12. No tokens left → 200 with empty arrays.
//   - FTS query: each token as a quoted phrase with internal " doubled,
//     joined with " OR ". Errors from MATCH go through isCallerQueryError:
//     caller error → 400, else 500.
//   - Missions: SELECT d.mission_id, MIN(rank) AS r FROM documents_fts
//     JOIN documents d ON d.id = documents_fts.rowid
//     JOIN missions m ON m.id = d.mission_id
//     WHERE documents_fts MATCH ? AND d.kind = 'brief' AND d.mission_id IS NOT NULL
//     [AND m.harness = ?] [AND d.mission_id <> ?]
//     GROUP BY d.mission_id ORDER BY r LIMIT 5.
//     For each: mission_id, title, status, outcome, grade (LEFT JOIN mission_grades; null when absent), rank,
//     then one IN query for all mission_ids at once:
//     SELECT mission_id, kind, ord, text, why, correction FROM mission_lessons
//     WHERE mission_id IN (...) AND kind IN ('wrong-assumption','reuse-step')
//     ORDER BY mission_id, kind, ord.
//     Lessons nested per mission: wrong_assumptions: [{ord,text,why,correction}] where why and correction
//     are JSON null when the database column is NULL; reuse_checklist: [{ord,text}] only.
//   - Directives: load all active directives with derived status, score = number of distinct tokens found
//     case-insensitively in title+rule+rationale; keep score > 0; sort score desc then directive_id asc;
//     top 5; fields directive_id, title, rule, score.
//   - Patterns: same scoring over name+context+solution; top 5 by score desc then id asc.
//   - JSON: {"goal","harness"(string|null),"missions":[…with nested wrong_assumptions/reuse_checklist],"directives":[…],"patterns":[…]};
//     arrays never null.
func briefPrepHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		goal := strings.TrimSpace(c.Query("goal"))
		if goal == "" {
			validationError(c, errors.New("goal is required"))
			return
		}

		harness := c.Query("harness")
		exclude := c.Query("exclude")

		ftsQuery, tokens := buildBriefPrepQuery(goal)
		if len(tokens) == 0 {
			// No tokens after filtering; return empty arrays.
			c.JSON(http.StatusOK, briefPrepResponse{
				Goal:       goal,
				Harness:    stringPtr(harness),
				Missions:   []briefPrepMission{},
				Directives: []briefPrepDirective{},
				Patterns:   []briefPrepPattern{},
			})
			return
		}

		// Query missions via FTS.
		missionQuery := `
			SELECT d.mission_id, MIN(rank) AS r
			FROM documents_fts
			JOIN documents d ON d.id = documents_fts.rowid
			JOIN missions m ON m.id = d.mission_id
			WHERE documents_fts MATCH ? AND d.kind = 'brief' AND d.mission_id IS NOT NULL
		`
		args := []interface{}{ftsQuery}

		if harness != "" {
			missionQuery += " AND m.harness = ?"
			args = append(args, harness)
		}
		if exclude != "" {
			missionQuery += " AND d.mission_id <> ?"
			args = append(args, exclude)
		}

		missionQuery += " GROUP BY d.mission_id ORDER BY r LIMIT 5"

		rows, err := db.QueryContext(c.Request.Context(), missionQuery, args...)
		if err != nil {
			if isCallerQueryError(err) {
				validationError(c, errors.New("invalid search query syntax"))
				return
			}
			internalError(c, err)
			return
		}

		// Collect mission IDs and ranks from FTS results.
		type missionRank struct {
			id   string
			rank float64
		}
		var missionRanks []missionRank

		for rows.Next() {
			var missionID string
			var rank float64
			if err := rows.Scan(&missionID, &rank); err != nil {
				rows.Close()
				internalError(c, err)
				return
			}
			missionRanks = append(missionRanks, missionRank{missionID, rank})
		}
		rows.Close()

		if err := rows.Err(); err != nil {
			if isCallerQueryError(err) {
				validationError(c, errors.New("invalid search query syntax"))
				return
			}
			internalError(c, err)
			return
		}

		// Fetch full mission details for all IDs.
		missions := make([]briefPrepMission, 0)
		missionIDs := make([]string, 0)

		for _, mr := range missionRanks {
			detailQuery := `
				SELECT m.id, m.title, m.status, m.outcome, g.grade
				FROM missions m
				LEFT JOIN mission_grades g ON g.mission_id = m.id
				WHERE m.id = ?
			`
			var mission briefPrepMission
			var outcome sql.NullString
			var grade sql.NullString
			err := db.QueryRowContext(c.Request.Context(), detailQuery, mr.id).Scan(
				&mission.MissionID, &mission.Title, &mission.Status, &outcome, &grade)
			if err != nil {
				internalError(c, err)
				return
			}
			if outcome.Valid {
				mission.Outcome = &outcome.String
			}
			if grade.Valid {
				mission.Grade = &grade.String
			}
			mission.Rank = mr.rank
			mission.WrongAssumptions = []briefPrepLesson{}
			mission.ReuseChecklist = []briefPrepReuse{}
			missions = append(missions, mission)
			missionIDs = append(missionIDs, mr.id)
		}

		// Fetch lessons for all missions in one query and attach to mission objects.
		if len(missionIDs) > 0 {
			placeholders := make([]string, len(missionIDs))
			lessonArgs := make([]interface{}, len(missionIDs))
			for i, id := range missionIDs {
				placeholders[i] = "?"
				lessonArgs[i] = id
			}

			lessonQuery := `
				SELECT mission_id, kind, ord, text, why, correction
				FROM mission_lessons
				WHERE mission_id IN (` + strings.Join(placeholders, ",") + `)
				AND kind IN ('wrong-assumption', 'reuse-step')
				ORDER BY mission_id, kind, ord
			`

			lessonRows, err := db.QueryContext(c.Request.Context(), lessonQuery, lessonArgs...)
			if err != nil {
				internalError(c, err)
				return
			}
			defer lessonRows.Close()

			// Per-mission wrong-assumption and reuse-step rows.
			wrongByMission := make(map[string][]briefPrepLesson)
			reuseByMission := make(map[string][]briefPrepReuse)
			for lessonRows.Next() {
				var missionID, kind string
				var ord int
				var text string
				var why, correction sql.NullString
				if err := lessonRows.Scan(&missionID, &kind, &ord, &text, &why, &correction); err != nil {
					internalError(c, err)
					return
				}

				if kind == "reuse-step" {
					reuseByMission[missionID] = append(reuseByMission[missionID], briefPrepReuse{Ord: ord, Text: text})
					continue
				}

				lesson := briefPrepLesson{Ord: ord, Text: text}
				if why.Valid {
					lesson.Why = &why.String
				}
				if correction.Valid {
					lesson.Correction = &correction.String
				}
				wrongByMission[missionID] = append(wrongByMission[missionID], lesson)
			}

			if err := lessonRows.Err(); err != nil {
				internalError(c, err)
				return
			}

			// Attach lessons to missions.
			for i := range missions {
				if wa, ok := wrongByMission[missions[i].MissionID]; ok {
					missions[i].WrongAssumptions = wa
				}
				if rc, ok := reuseByMission[missions[i].MissionID]; ok {
					missions[i].ReuseChecklist = rc
				}
			}
		}

		// Fetch directives with derived status, filter to active, score, top 5.
		directiveQuery := `
			SELECT d.directive_id, d.title, d.rule, d.rationale
			FROM directives d
			WHERE CASE WHEN EXISTS(
			         SELECT 1 FROM directive_proposals p
			         WHERE p.directive_id = d.directive_id AND p.state = 'pending'
			       ) THEN 'under-review'
			       WHEN EXISTS(
			         SELECT 1 FROM directive_proposals p
			         WHERE p.directive_id = d.directive_id AND p.state = 'declined'
			       ) THEN 'retired'
			       ELSE 'active' END = 'active'
			ORDER BY d.directive_id ASC
		`

		dirRows, err := db.QueryContext(c.Request.Context(), directiveQuery)
		if err != nil {
			internalError(c, err)
			return
		}
		defer dirRows.Close()

		directives := make([]briefPrepDirective, 0)

		for dirRows.Next() {
			var directiveID, title, rule string
			var rationale sql.NullString
			if err := dirRows.Scan(&directiveID, &title, &rule, &rationale); err != nil {
				internalError(c, err)
				return
			}

			fullText := title + " " + rule
			if rationale.Valid {
				fullText += " " + rationale.String
			}

			score := scoreTokens(fullText, tokens)
			if score > 0 {
				directives = append(directives, briefPrepDirective{
					DirectiveID: directiveID,
					Title:       title,
					Rule:        rule,
					Score:       score,
				})
			}
		}

		if err := dirRows.Err(); err != nil {
			internalError(c, err)
			return
		}

		// Sort directives by score desc then id asc and keep top 5.
		directives = sortAndCapDirectives(directives)

		// Fetch patterns with scoring, top 5.
		patternQuery := `
			SELECT id, name, context, solution
			FROM patterns
			ORDER BY id ASC
		`

		patRows, err := db.QueryContext(c.Request.Context(), patternQuery)
		if err != nil {
			internalError(c, err)
			return
		}
		defer patRows.Close()

		patterns := make([]briefPrepPattern, 0)

		for patRows.Next() {
			var id int
			var name string
			var context, solution sql.NullString
			if err := patRows.Scan(&id, &name, &context, &solution); err != nil {
				internalError(c, err)
				return
			}

			fullText := name
			if context.Valid {
				fullText += " " + context.String
			}
			if solution.Valid {
				fullText += " " + solution.String
			}

			score := scoreTokens(fullText, tokens)
			if score > 0 {
				patterns = append(patterns, briefPrepPattern{
					ID:       id,
					Name:     name,
					Context:  context.String,
					Solution: solution.String,
					Score:    score,
				})
			}
		}

		if err := patRows.Err(); err != nil {
			internalError(c, err)
			return
		}

		// Sort patterns by score desc then id asc and keep top 5.
		patterns = sortAndCapPatterns(patterns)

		c.JSON(http.StatusOK, briefPrepResponse{
			Goal:       goal,
			Harness:    stringPtr(harness),
			Missions:   missions,
			Directives: directives,
			Patterns:   patterns,
		})
	}
}

// buildBriefPrepQuery tokenizes the goal and returns an FTS query string
// and the list of normalized tokens.
//
// Algorithm:
//   - Split on whitespace and punctuation boundaries (anything not alphanumeric/underscore)
//   - Lowercase each token
//   - Drop tokens with fewer than 3 runes (counted via utf8.RuneCountInString)
//   - Drop tokens in the stopword list
//   - Deduplicate
//   - Keep the first 12
//   - For FTS: wrap each token as a quoted phrase with internal " doubled,
//     join with " OR "
func buildBriefPrepQuery(goal string) (string, []string) {
	stopwords := map[string]bool{
		"the": true, "and": true, "for": true, "with": true, "from": true,
		"into": true, "that": true, "this": true, "are": true, "was": true,
		"not": true, "by": true, "as": true, "on": true, "or": true, "to": true,
		"of": true, "in": true,
	}

	// Split on whitespace and non-alphanumeric characters.
	var tokens []string
	var current strings.Builder
	for _, r := range goal {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			current.WriteRune(r)
		} else {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}

	// Normalize, filter, and deduplicate.
	seen := make(map[string]bool)
	normalized := make([]string, 0)

	for _, t := range tokens {
		lower := strings.ToLower(t)
		if utf8.RuneCountInString(lower) < 3 || stopwords[lower] || seen[lower] {
			continue
		}
		seen[lower] = true
		normalized = append(normalized, lower)
		if len(normalized) >= 12 {
			break
		}
	}

	// Build FTS query: each token as a quoted phrase.
	if len(normalized) == 0 {
		return "", normalized
	}

	var ftsBuilder strings.Builder
	for i, token := range normalized {
		if i > 0 {
			ftsBuilder.WriteString(" OR ")
		}
		ftsBuilder.WriteByte('"')
		escaped := strings.ReplaceAll(token, `"`, `""`)
		ftsBuilder.WriteString(escaped)
		ftsBuilder.WriteByte('"')
	}

	return ftsBuilder.String(), normalized
}

// scoreTokens counts the number of distinct normalized tokens found
// case-insensitively in the text.
func scoreTokens(text string, tokens []string) int {
	lower := strings.ToLower(text)
	count := 0
	seen := make(map[string]bool)

	for _, token := range tokens {
		if !seen[token] && strings.Contains(lower, token) {
			count++
			seen[token] = true
		}
	}

	return count
}

// sortAndCapDirectives sorts directives by score desc then directive_id asc,
// keeping only the top 5.
func sortAndCapDirectives(directives []briefPrepDirective) []briefPrepDirective {
	sort.Slice(directives, func(i, j int) bool {
		if directives[i].Score != directives[j].Score {
			return directives[i].Score > directives[j].Score
		}
		return directives[i].DirectiveID < directives[j].DirectiveID
	})

	if len(directives) > 5 {
		directives = directives[:5]
	}
	return directives
}

// sortAndCapPatterns sorts patterns by score desc then id asc,
// keeping only the top 5.
func sortAndCapPatterns(patterns []briefPrepPattern) []briefPrepPattern {
	sort.Slice(patterns, func(i, j int) bool {
		if patterns[i].Score != patterns[j].Score {
			return patterns[i].Score > patterns[j].Score
		}
		return patterns[i].ID < patterns[j].ID
	})

	if len(patterns) > 5 {
		patterns = patterns[:5]
	}
	return patterns
}

// stringPtr returns nil if s is empty, otherwise a pointer to s.
func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
