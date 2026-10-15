package importer

import (
	"database/sql"
	"errors"
	"fmt"
	"log"

	"bishop-memory/internal/debrief"
)

// storeMissionDeliverables refreshes mission_deliverables for missionID after
// a debrief document was imported by harness. It never returns an error: failures
// are logged and counted in the returned DeliverablesErrors so the import that
// called it is not failed.
//
// The returned DeliverablesSkipped is incremented in the following cases:
//   - +1 when no missions row exists for missionID (a later import of the same
//     file fills the deliverables once the row exists);
//   - +1 when the selected document of kind debrief has the "Deliverables Changed"
//     heading but the section contains no readable bullets (ok=false from
//     ParseDeliverables).
//
// The debrief document is read from the database, choosing the document tagged
// with harness first and then the lowest source_path.
//
// Rows are replaced as follows:
//   - debrief section has readable deliverable bullets: replace the rows with
//     those, numbered from 1;
//   - a section whose only bullets are none-rows is readable and deletes every row;
//   - debrief heading present but unreadable (ok=false): keep the rows;
//   - no debrief heading: keep the rows.
//
// Replacing happens in one transaction.
func storeMissionDeliverables(db *sql.DB, missionID, harness string) SyncStats {
	var st SyncStats
	if missionID == "" {
		return st
	}
	fail := func(what string, err error) SyncStats {
		log.Printf("deliverables: %s for %s: %v", what, missionID, err)
		st.DeliverablesErrors++
		return st
	}

	var exists int
	switch err := db.QueryRow(`SELECT 1 FROM missions WHERE id = ?`, missionID).Scan(&exists); {
	case errors.Is(err, sql.ErrNoRows):
		log.Printf("deliverables: skipping %s: mission has no missions row", missionID)
		st.DeliverablesSkipped++
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

	section, found := extractSection(debriefBody, "Deliverables Changed")
	if !found {
		return st
	}

	rows, _, ok := debrief.ParseDeliverables(section)
	// ParseDeliverables never reports skipped rows.
	if !ok {
		log.Printf("deliverables: skipping %s: deliverables section unreadable", missionID)
		st.DeliverablesSkipped++
		return st
	}

	if err := replaceDeliverables(db, missionID, rows); err != nil {
		return fail("replace mission_deliverables", err)
	}
	return st
}

// replaceDeliverables deletes every mission_deliverables row for missionID and
// inserts rows as given, numbered from 1, in one transaction. A nil rows deletes
// only. Nothing is changed when it returns an error.
func replaceDeliverables(db *sql.DB, missionID string, rows []debrief.Deliverable) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM mission_deliverables WHERE mission_id = ?`, missionID); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	for i, row := range rows {
		if _, err := tx.Exec(
			`INSERT INTO mission_deliverables (mission_id, ord, path, note)
			 VALUES (?, ?, ?, ?)`,
			missionID, i+1, nullIfEmpty(row.Path), row.Note,
		); err != nil {
			return fmt.Errorf("insert ord %d: %w", i+1, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
