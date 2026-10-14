package importer

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"bishop-memory/internal/debrief"
)

// storeAgentMistakes refreshes agent_mistakes for missionID after a debrief
// document was imported by harness. It never returns an error: failures are
// logged and counted in the returned MistakesErrors so the import that called
// it is not failed.
//
// The returned MistakesSkipped is incremented in the following cases:
//   - +1 when no missions row exists for missionID (a later import of the same
//     file fills the mistakes once the row exists);
//   - +1 when the selected document of kind debrief has the "Sub-Agent Mistakes
//     and Corrections" heading but the section contains no readable table
//     (missing header in the section, or prose only);
//   - +N when the section is readable but contains N malformed rows (rows with
//     fewer than 5 cells).
//
// A header-only section with no table is readable and replaces with zero rows.
//
// The debrief document is read from the database, choosing the document tagged
// with harness first and then the lowest source_path.
//
// Rows are replaced as follows:
//   - debrief section has readable mistake rows: replace the rows with those,
//     numbered from 1;
//   - debrief heading present but unreadable (no table found): keep the rows;
//   - no debrief heading: keep the rows.
//
// Replacing happens in one transaction.
func storeAgentMistakes(db *sql.DB, missionID, harness string) SyncStats {
	var st SyncStats
	if missionID == "" {
		return st
	}
	fail := func(what string, err error) SyncStats {
		log.Printf("agent_mistakes: %s for %s: %v", what, missionID, err)
		st.MistakesErrors++
		return st
	}

	var exists int
	switch err := db.QueryRow(`SELECT 1 FROM missions WHERE id = ?`, missionID).Scan(&exists); {
	case errors.Is(err, sql.ErrNoRows):
		log.Printf("agent_mistakes: skipping %s: mission has no missions row", missionID)
		st.MistakesSkipped++
		return st
	case err != nil:
		return fail("check mission exists", err)
	}

	// A failed read must not be treated as an absent heading, which would
	// delete the rows; stop without writing.
	debriefBody, hasDebrief, err := documentBody(db, missionID, "debrief", harness)
	if err != nil {
		return fail("read debrief", err)
	}

	if !hasDebrief {
		return st
	}

	section, found := extractSection(debriefBody, "Sub-Agent Mistakes and Corrections")
	if !found {
		return st
	}

	rows, skipped, ok := debrief.ParseMistakes(section)
	st.MistakesSkipped += skipped
	if skipped > 0 {
		log.Printf("agent_mistakes: %s: %d malformed rows skipped", missionID, skipped)
	}
	if !ok {
		log.Printf("agent_mistakes: skipping %s: mistakes section unreadable", missionID)
		st.MistakesSkipped++
		return st
	}

	if err := replaceMistakes(db, missionID, rows); err != nil {
		return fail("replace agent_mistakes", err)
	}
	return st
}

// mistakeAgentName normalises an agent name from a debrief row.
// Rules applied in order:
//  1. If raw contains an @-mention matching @([A-Za-z][A-Za-z0-9_-]*),
//     return the mention's name lowercased.
//  2. Otherwise, trim raw and take the leading token up to the first space,
//     comma, paren, slash, semicolon, or colon. Lowercase it and strip trailing
//     period. Return empty string if the result is empty or does not start with
//     a letter.
//
// agent_raw is kept unchanged in agent_mistakes; this function is the
// agent field's normalisation rule only.
func mistakeAgentName(raw string) string {
	// Rule 1: Extract @mention
	re := regexp.MustCompile(`@([A-Za-z][A-Za-z0-9_-]*)`)
	matches := re.FindStringSubmatch(raw)
	if len(matches) > 1 {
		return strings.ToLower(matches[1])
	}

	// Rule 2: Leading token before delimiter
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	// Find the first occurrence of any delimiter
	delimiters := []string{" ", ",", "(", "/", ";", ":"}
	minIdx := len(raw)
	for _, delim := range delimiters {
		if idx := strings.Index(raw, delim); idx >= 0 && idx < minIdx {
			minIdx = idx
		}
	}

	token := raw[:minIdx]
	token = strings.ToLower(token)
	token = strings.TrimSuffix(token, ".")

	// Return empty if empty or doesn't start with a letter
	if token == "" || !isLetter(rune(token[0])) {
		return ""
	}
	return token
}

// isLetter returns true if ch is an ASCII letter.
func isLetter(ch rune) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

// replaceMistakes deletes every agent_mistakes row for missionID and inserts
// rows as given, numbered from 1, in one transaction. A nil rows deletes only.
// Nothing is changed when it returns an error.
func replaceMistakes(db *sql.DB, missionID string, rows []debrief.Mistake) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM agent_mistakes WHERE mission_id = ?`, missionID); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	for i, row := range rows {
		agent := mistakeAgentName(row.AgentRaw)
		if _, err := tx.Exec(
			`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact, impact_note, correction, prevention)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			missionID, i+1, agent, row.AgentRaw, row.Mistake,
			nullIfEmpty(row.Impact), nullIfEmpty(row.ImpactNote),
			row.Correction, row.Prevention,
		); err != nil {
			return fmt.Errorf("insert ord %d: %w", i+1, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
