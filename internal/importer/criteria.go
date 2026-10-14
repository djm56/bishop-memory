package importer

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"unicode"

	"bishop-memory/internal/criteria"
)

// Section headings the criteria import reads. A heading matches by
// case-insensitive prefix, so briefHeading also matches a heading such as
// "Acceptance Criteria Outcome (QA)".
const (
	briefHeading   = "Acceptance Criteria"
	debriefHeading = "Acceptance Criteria Outcome"
)

var (
	fenceMarker      = regexp.MustCompile("^\\s*([`]{3}|~~~)")
	bulletOrCheckbox = regexp.MustCompile(`^\s*[-*]\s*(?:\[\s*[xX\s]\s*\])?`)
)

// headingLevel returns the number of leading '#' characters of a trimmed
// heading line when they are followed by a space, and 0 otherwise.
func headingLevel(trimmed string) int {
	n := 0
	for n < len(trimmed) && trimmed[n] == '#' {
		n++
	}
	if n == 0 || n >= len(trimmed) || trimmed[n] != ' ' {
		return 0
	}
	return n
}

// matchesHeading reports whether line is a Markdown heading (one or more '#',
// then whitespace) whose text starts with heading, ignoring case. level is the
// number of '#' characters when it matches.
func matchesHeading(line, heading string) (level int, ok bool) {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 {
		return 0, false
	}
	rest := line[n:]
	text := strings.TrimLeftFunc(rest, unicode.IsSpace)
	if len(text) == len(rest) || len(text) < len(heading) {
		return 0, false
	}
	return n, strings.EqualFold(text[:len(heading)], heading)
}

// extractSection returns the body of the first section in a Markdown document
// whose heading text starts with heading (case-insensitive), and whether such a
// heading exists. The section runs from the line after the heading to the next
// heading of the same or a higher level, or to the end of the document, and is
// trimmed of surrounding whitespace. Subsections are part of the section.
// Lines inside fenced code blocks are never treated as headings, whether they
// would start the section or end it. found is true for a heading with an empty
// body.
func extractSection(body, heading string) (section string, found bool) {
	inFence := false
	level := 0
	var out []string

	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		isFence := fenceMarker.MatchString(trimmed)

		if !found {
			if isFence {
				inFence = !inFence
			} else if l, ok := matchesHeading(line, heading); ok && !inFence {
				found = true
				level = l
			}
			continue
		}

		if isFence {
			inFence = !inFence
		} else if !inFence {
			if l := headingLevel(trimmed); l > 0 && l <= level {
				break
			}
		}
		out = append(out, line)
	}

	if !found {
		return "", false
	}
	return strings.TrimSpace(strings.Join(out, "\n")), true
}

// parsedSection is the result of reading one criteria section out of a
// document body.
type parsedSection struct {
	found    bool                 // the heading exists in the document
	criteria []criteria.Criterion // criterion bullets parsed from the section
}

// unreadable reports whether the heading exists but yields no criterion
// bullets (an empty section, or prose only).
func (s parsedSection) unreadable() bool {
	return s.found && len(s.criteria) == 0
}

// readSection extracts the section under heading from body and parses it with
// parse. A document that is not present is represented by hasDoc == false and
// yields a section with found == false.
func readSection(body string, hasDoc bool, heading string, parse func(string) []criteria.Criterion) parsedSection {
	if !hasDoc {
		return parsedSection{}
	}
	section, found := extractSection(body, heading)
	return parsedSection{found: found, criteria: usableCriteria(parse(section))}
}

// usableCriteria returns the criteria with their text cleaned (see
// cleanCriterionText) and a trailing carriage return removed from the
// evidence, in order, without any whose cleaned text is empty (a bare "- [x]").
// An empty criterion is not stored, so a section holding only such bullets
// counts as unreadable.
func usableCriteria(in []criteria.Criterion) []criteria.Criterion {
	var out []criteria.Criterion
	for _, c := range in {
		c.Text = cleanCriterionText(c.Text)
		if c.Text == "" {
			continue
		}
		c.Evidence = strings.TrimRight(c.Evidence, "\r")
		out = append(out, c)
	}
	return out
}

// documentBody returns the body of the document of the given kind for a
// mission. When several harnesses hold one, the document tagged with harness
// is chosen first, then the lowest source_path. ok is false when no document
// of that kind exists.
func documentBody(db *sql.DB, missionID, kind, harness string) (body string, ok bool, err error) {
	var b sql.NullString
	err = db.QueryRow(`
		SELECT body FROM documents
		WHERE mission_id = ? AND kind = ?
		ORDER BY CASE WHEN harness = ? THEN 0 ELSE 1 END, source_path
		LIMIT 1
	`, missionID, kind, harness).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return b.String, b.Valid, nil
}

// storeMissionCriteria refreshes mission_criteria for missionID after a brief
// or debrief document of the given kind was imported by harness. It never
// returns an error: failures are logged and counted in the returned
// CriteriaErrors so the import that called it is not failed.
//
// The returned CriteriaSkipped is 1 when the criteria of the imported kind
// were not read, and 0 otherwise. That happens in two cases: no missions row
// exists for missionID (a later import of the same file fills the criteria
// once the row exists), or the selected document of that kind has its
// criteria heading but no criterion bullets (empty or prose only).
//
// Both documents are read from the database, choosing for each kind the
// document tagged with harness first and then the lowest source_path.
// Rows are chosen as follows:
//   - debrief section has criterion bullets: replace the rows with those,
//     which carry results and evidence;
//   - debrief heading present but unreadable: keep the rows when any has a
//     result; otherwise replace them with the brief's criteria if the brief
//     has some, else keep them;
//   - no debrief heading, brief section has criterion bullets: replace the
//     rows with those, whose result and evidence are NULL;
//   - brief heading present but unreadable: keep the rows;
//   - neither heading present: delete the rows.
//
// Replacing or deleting happens in one transaction.
func storeMissionCriteria(db *sql.DB, missionID, harness, kind string) SyncStats {
	var st SyncStats
	if missionID == "" {
		return st
	}
	fail := func(what string, err error) SyncStats {
		log.Printf("criteria: %s for %s: %v", what, missionID, err)
		st.CriteriaErrors++
		return st
	}

	var exists int
	switch err := db.QueryRow(`SELECT 1 FROM missions WHERE id = ?`, missionID).Scan(&exists); {
	case errors.Is(err, sql.ErrNoRows):
		st.CriteriaSkipped++
		return st
	case err != nil:
		return fail("check mission exists", err)
	}

	// A failed read must not be treated as an absent heading, which would
	// delete the rows; stop without writing.
	briefBody, hasBrief, err := documentBody(db, missionID, "brief", harness)
	if err != nil {
		return fail("read brief", err)
	}
	debriefBody, hasDebrief, err := documentBody(db, missionID, "debrief", harness)
	if err != nil {
		return fail("read debrief", err)
	}
	brief := readSection(briefBody, hasBrief, briefHeading, criteria.ParseBriefCriteria)
	debrief := readSection(debriefBody, hasDebrief, debriefHeading, criteria.ParseDebriefOutcome)

	if (kind == "brief" && brief.unreadable()) || (kind == "debrief" && debrief.unreadable()) {
		st.CriteriaSkipped++
	}

	var rows []criteria.Criterion
	switch {
	case len(debrief.criteria) > 0:
		rows = debrief.criteria
	case debrief.unreadable():
		var graded int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM mission_criteria WHERE mission_id = ? AND result IS NOT NULL`, missionID,
		).Scan(&graded); err != nil {
			return fail("count graded rows", err)
		}
		if graded > 0 || len(brief.criteria) == 0 {
			return st
		}
		rows = brief.criteria
	case len(brief.criteria) > 0:
		rows = brief.criteria
	case brief.unreadable():
		return st
	}

	if err := replaceCriteria(db, missionID, rows); err != nil {
		return fail("replace mission_criteria", err)
	}
	return st
}

// replaceCriteria deletes every mission_criteria row for missionID and inserts
// rows as given, numbered from 1, in one transaction. A nil rows deletes only. Nothing
// is changed when it returns an error.
func replaceCriteria(db *sql.DB, missionID string, rows []criteria.Criterion) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM mission_criteria WHERE mission_id = ?`, missionID); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	for i, row := range rows {
		if _, err := tx.Exec(
			`INSERT INTO mission_criteria (mission_id, ord, criterion, result, evidence)
			 VALUES (?, ?, ?, ?, ?)`,
			missionID, i+1, row.Text, nullIfEmpty(row.Result), nullIfEmpty(row.Evidence),
		); err != nil {
			return fmt.Errorf("insert ord %d: %w", i+1, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// cleanCriterionText trims whitespace and removes the leading bullet marker
// and checkbox from a criterion line. Nothing else is removed: for a debrief
// line the stored text still contains the verdict and evidence, because
// criteria.ParseDebriefOutcome sets Text to the whole trimmed line.
// Example: "- [x] criterion - pass" becomes "criterion - pass".
func cleanCriterionText(text string) string {
	text = strings.TrimSpace(text)
	text = bulletOrCheckbox.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}
