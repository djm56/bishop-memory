package importer

import (
	"database/sql"
	"errors"
	"fmt"
	"log"

	"bishop-memory/internal/debrief"
)

// storeMissionLessons refreshes mission_lessons for missionID after a debrief
// document was imported by harness. It never returns an error: failures are
// logged and counted in the returned LessonsErrors so the import that called
// it is not failed.
//
// The debrief document is read from the database, choosing the document
// tagged with harness first and then the lowest source_path. Its two sections
// are handled independently:
//   - "Wrong Assumptions" gives the wrong-assumption rows
//     (debrief.ParseWrongAssumptions);
//   - "Similar Future Missions" gives the similar-mission and reuse-step rows
//     (debrief.ParseSimilarMission).
//
// For each section: a missing heading keeps that section's rows; a heading
// whose section is unreadable (ok=false) keeps them and adds 1 to
// LessonsSkipped; a readable section replaces that section's kinds with the
// parsed rows, numbered from 1 within each kind. Wrong Assumptions rows with
// the wrong cell count are not stored and are added to LessonsSkipped.
//
// A readable Wrong Assumptions table whose rows are all None or malformed
// still replaces, so it deletes the stored wrong-assumption rows; a Similar
// Future Missions section with no trigger and no step is unreadable and keeps
// its rows.
//
// LessonsSkipped is also incremented by 1, with nothing written, when no
// missions row exists for missionID. A failed missions-row check or debrief
// read adds 1 to LessonsErrors and writes nothing; a failed replace adds 1 to
// LessonsErrors and leaves that section's rows as they were.
func storeMissionLessons(db *sql.DB, missionID, harness string) SyncStats {
	var st SyncStats
	if missionID == "" {
		return st
	}
	fail := func(what string, err error) {
		log.Printf("lessons: %s for %s: %v", what, missionID, err)
		st.LessonsErrors++
	}

	var exists int
	switch err := db.QueryRow(`SELECT 1 FROM missions WHERE id = ?`, missionID).Scan(&exists); {
	case errors.Is(err, sql.ErrNoRows):
		log.Printf("lessons: skipping %s: mission has no missions row", missionID)
		st.LessonsSkipped++
		return st
	case err != nil:
		fail("check mission exists", err)
		return st
	}

	body, hasDebrief, err := documentBody(db, missionID, "debrief", harness)
	if err != nil {
		fail("read debrief", err)
		return st
	}
	if !hasDebrief {
		return st
	}

	if section, found := extractSection(body, "Wrong Assumptions"); found {
		rows, skipped, ok := debrief.ParseWrongAssumptions(section)
		st.LessonsSkipped += skipped
		if !ok {
			log.Printf("lessons: skipping wrong assumptions for %s: no Assumption table", missionID)
			st.LessonsSkipped++
		} else if err := replaceLessons(db, missionID, []string{debrief.LessonWrongAssumption}, rows); err != nil {
			fail("replace wrong assumptions", err)
		}
	}

	if section, found := extractSection(body, "Similar Future Missions"); found {
		rows, ok := debrief.ParseSimilarMission(section)
		if !ok {
			log.Printf("lessons: skipping similar future missions for %s: no trigger or step", missionID)
			st.LessonsSkipped++
		} else if err := replaceLessons(db, missionID, []string{debrief.LessonSimilarMission, debrief.LessonReuseStep}, rows); err != nil {
			fail("replace similar future missions", err)
		}
	}
	return st
}

// replaceLessons deletes the mission_lessons rows of missionID whose kind is
// in kinds and inserts rows, numbering ord from 1 within each kind, in one
// transaction. Empty why and correction are stored as NULL. Nothing is
// changed when it returns an error.
func replaceLessons(db *sql.DB, missionID string, kinds []string, rows []debrief.Lesson) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	for _, kind := range kinds {
		if _, err := tx.Exec(`DELETE FROM mission_lessons WHERE mission_id = ? AND kind = ?`, missionID, kind); err != nil {
			return fmt.Errorf("delete %s: %w", kind, err)
		}
	}
	ord := map[string]int{}
	for _, row := range rows {
		ord[row.Kind]++
		if _, err := tx.Exec(
			`INSERT INTO mission_lessons (mission_id, kind, ord, text, why, correction)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			missionID, row.Kind, ord[row.Kind], row.Text, nullIfEmpty(row.Why), nullIfEmpty(row.Correction),
		); err != nil {
			return fmt.Errorf("insert %s ord %d: %w", row.Kind, ord[row.Kind], err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
