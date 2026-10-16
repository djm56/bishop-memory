package importer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// lessonRows returns "kind|ord|text|why|correction" for every mission_lessons
// row of missionID, ordered by kind then ord, with NULL shown as <nil>. It
// reads under a 5-second deadline on the test goroutine, so a transaction left
// open by a missing rollback fails the test instead of hanging it.
func lessonRows(t *testing.T, db *sql.DB, missionID string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT kind, ord, text, COALESCE(why, '<nil>'), COALESCE(correction, '<nil>')
		FROM mission_lessons WHERE mission_id = ? ORDER BY kind, ord`, missionID)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("rollback missing: connection still held")
	}
	if err != nil {
		t.Fatalf("query lessons of %s: %v", missionID, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var kind, text, why, correction string
		var ord int
		if err := rows.Scan(&kind, &ord, &text, &why, &correction); err != nil {
			t.Fatalf("scan lessons of %s: %v", missionID, err)
		}
		out = append(out, fmt.Sprintf("%s|%d|%s|%s|%s", kind, ord, text, why, correction))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("lessons of %s: %v", missionID, err)
	}
	return out
}

// storeLessonsDebrief stores body as missionID's debrief for harness "h" and
// runs storeMissionLessons on it.
func storeLessonsDebrief(t *testing.T, db *sql.DB, missionID, body string) SyncStats {
	t.Helper()
	if err := upsertDocument(db, "/h/missions/"+missionID+"/DEBRIEF.md", "DEBRIEF", "debrief", body, sha256Hex([]byte(body)), missionID, "h"); err != nil {
		t.Fatalf("upsert debrief of %s: %v", missionID, err)
	}
	return storeMissionLessons(db, missionID, "h")
}

const lessonsFull = "# Debrief\n\n## Wrong Assumptions (Mandatory)\n| Assumption | Why It Was Wrong | Correction Applied |\n|---|---|---|\n| A one | why one | fix one |\n| A two | why two |  |\n\n## Similar Future Missions\n- Trigger to reuse this report: any merge\n- Reuse checklist:\n  1. First step\n  2. Second step\n"

var lessonsFullRows = []string{
	"reuse-step|1|First step|<nil>|<nil>",
	"reuse-step|2|Second step|<nil>|<nil>",
	"similar-mission|1|any merge|<nil>|<nil>",
	"wrong-assumption|1|A one|why one|fix one",
	"wrong-assumption|2|A two|why two|<nil>",
}

func TestStoreMissionLessonsEmptyMissionID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if got := storeMissionLessons(db, "", "h"); got != (SyncStats{}) {
		t.Errorf("empty mission id: got %+v, want zero stats", got)
	}
}

func TestStoreMissionLessonsNoMissionsRow(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMission(t, db, "m-ctl")
	storeLessonsDebrief(t, db, "m-ctl", lessonsFull)
	if got := lessonRows(t, db, "m-ctl"); !reflect.DeepEqual(got, lessonsFullRows) {
		t.Errorf("no missions row control: seeded mission stored %q, want %q", got, lessonsFullRows)
	}
	st := storeLessonsDebrief(t, db, "m-none", lessonsFull)
	if st.LessonsSkipped != 1 || st.LessonsErrors != 0 {
		t.Errorf("no missions row: got skipped=%d errors=%d, want 1 and 0", st.LessonsSkipped, st.LessonsErrors)
	}
	if got := lessonRows(t, db, "m-none"); len(got) != 0 {
		t.Errorf("no missions row: stored %q, want nothing", got)
	}
}

func TestStoreMissionLessonsStoresBothSections(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMission(t, db, "m-1")
	if st := storeLessonsDebrief(t, db, "m-1", lessonsFull); st != (SyncStats{}) {
		t.Errorf("readable debrief: got %+v, want zero stats", st)
	}
	if got := lessonRows(t, db, "m-1"); !reflect.DeepEqual(got, lessonsFullRows) {
		t.Errorf("stored lessons: got %q, want %q", got, lessonsFullRows)
	}
}

func TestStoreMissionLessonsSectionsReplaceIndependently(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMission(t, db, "m-1")
	storeLessonsDebrief(t, db, "m-1", lessonsFull)
	st := storeLessonsDebrief(t, db, "m-1", "## Wrong Assumptions\n| Assumption | Why | Fix |\n|---|---|---|\n| B one | x | y |\n")
	if st != (SyncStats{}) {
		t.Errorf("second import: got %+v, want zero stats", st)
	}
	want := []string{lessonsFullRows[0], lessonsFullRows[1], lessonsFullRows[2], "wrong-assumption|1|B one|x|y"}
	if got := lessonRows(t, db, "m-1"); !reflect.DeepEqual(got, want) {
		t.Errorf("wrong assumptions replaced, similar-mission rows kept: got %q, want %q", got, want)
	}
}

func TestStoreMissionLessonsUnreadableWrongAssumptionsKeepsRows(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMission(t, db, "m-1")
	storeLessonsDebrief(t, db, "m-1", lessonsFull)
	st := storeLessonsDebrief(t, db, "m-1", "## Wrong Assumptions\nno table here\n")
	if st.LessonsSkipped != 1 || st.LessonsErrors != 0 {
		t.Errorf("unreadable wrong assumptions: got skipped=%d errors=%d, want 1 and 0", st.LessonsSkipped, st.LessonsErrors)
	}
	if got := lessonRows(t, db, "m-1"); !reflect.DeepEqual(got, lessonsFullRows) {
		t.Errorf("unreadable wrong assumptions must keep rows: got %q, want %q", got, lessonsFullRows)
	}
}

func TestStoreMissionLessonsUnreadableSimilarMissionKeepsRows(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMission(t, db, "m-1")
	storeLessonsDebrief(t, db, "m-1", lessonsFull)
	st := storeLessonsDebrief(t, db, "m-1", "## Similar Future Missions\nnothing numbered\n")
	if st.LessonsSkipped != 1 || st.LessonsErrors != 0 {
		t.Errorf("unreadable similar future missions: got skipped=%d errors=%d, want 1 and 0", st.LessonsSkipped, st.LessonsErrors)
	}
	if got := lessonRows(t, db, "m-1"); !reflect.DeepEqual(got, lessonsFullRows) {
		t.Errorf("unreadable similar future missions must keep rows: got %q, want %q", got, lessonsFullRows)
	}
}

func TestStoreMissionLessonsNoneOnlyTableDeletesWrongAssumptions(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMission(t, db, "m-1")
	storeLessonsDebrief(t, db, "m-1", lessonsFull)
	st := storeLessonsDebrief(t, db, "m-1", "## Wrong Assumptions\n| Assumption | Why | Fix |\n|---|---|---|\n| None | — | — |\n")
	if st != (SyncStats{}) {
		t.Errorf("none-only table: got %+v, want zero stats", st)
	}
	want := lessonsFullRows[:3]
	if got := lessonRows(t, db, "m-1"); !reflect.DeepEqual(got, want) {
		t.Errorf("none-only table must delete wrong-assumption rows and keep the rest: got %q, want %q", got, want)
	}
}

func TestStoreMissionLessonsMalformedRowCounted(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMission(t, db, "m-1")
	st := storeLessonsDebrief(t, db, "m-1", "## Wrong Assumptions\n| Assumption | Why | Fix |\n|---|---|---|\n| a | b |\n| c | d | e |\n")
	if st.LessonsSkipped != 1 || st.LessonsErrors != 0 {
		t.Errorf("malformed row: got skipped=%d errors=%d, want 1 and 0", st.LessonsSkipped, st.LessonsErrors)
	}
	want := []string{"wrong-assumption|1|c|d|e"}
	if got := lessonRows(t, db, "m-1"); !reflect.DeepEqual(got, want) {
		t.Errorf("malformed row not stored, valid row stored: got %q, want %q", got, want)
	}
}

func TestStoreMissionLessonsNoDebrief(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMission(t, db, "m-1")
	seedMission(t, db, "m-ctl")
	storeLessonsDebrief(t, db, "m-ctl", lessonsFull)
	if got := lessonRows(t, db, "m-ctl"); !reflect.DeepEqual(got, lessonsFullRows) {
		t.Errorf("no debrief control: mission with a debrief stored %q, want %q", got, lessonsFullRows)
	}
	if st := storeMissionLessons(db, "m-1", "h"); st != (SyncStats{}) {
		t.Errorf("no debrief: got %+v, want zero stats", st)
	}
	if got := lessonRows(t, db, "m-1"); len(got) != 0 {
		t.Errorf("no debrief: stored %q, want nothing", got)
	}
}

func TestStoreMissionLessonsReadFailureKeepsRows(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMission(t, db, "m-1")
	storeLessonsDebrief(t, db, "m-1", lessonsFull)
	breakDocumentKind(t, db, "debrief")
	st := storeMissionLessons(db, "m-1", "h")
	if st.LessonsErrors != 1 || st.LessonsSkipped != 0 {
		t.Errorf("read failure: got errors=%d skipped=%d, want 1 and 0", st.LessonsErrors, st.LessonsSkipped)
	}
	if got := lessonRows(t, db, "m-1"); !reflect.DeepEqual(got, lessonsFullRows) {
		t.Errorf("read failure must keep rows: got %q, want %q", got, lessonsFullRows)
	}
}

func TestStoreMissionLessonsReplaceFailureRollsBack(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMission(t, db, "m-1")
	storeLessonsDebrief(t, db, "m-1", lessonsFull)
	if _, err := db.Exec(`CREATE TRIGGER fail_wa_second BEFORE INSERT ON mission_lessons
		WHEN NEW.kind = 'wrong-assumption' AND NEW.ord = 2 BEGIN SELECT RAISE(ABORT, 'forced'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	st := storeLessonsDebrief(t, db, "m-1", "## Wrong Assumptions\n| Assumption | Why | Fix |\n|---|---|---|\n| N one | p | q |\n| N two | r | s |\n")
	if st.LessonsErrors != 1 || st.LessonsSkipped != 0 {
		t.Errorf("replace failure: got errors=%d skipped=%d, want 1 and 0", st.LessonsErrors, st.LessonsSkipped)
	}
	if got := lessonRows(t, db, "m-1"); !reflect.DeepEqual(got, lessonsFullRows) {
		t.Errorf("replace failure must roll back to the original rows: got %q, want %q", got, lessonsFullRows)
	}
}

func TestImportMarkdownStatsDebriefStoresLessons(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMission(t, db, "m-imp")
	stats, err := ImportMarkdownStats(db, "/h/missions/m-imp/DEBRIEF.md", "missions/m-imp/DEBRIEF.md", "h", []byte(lessonsFull))
	if err != nil {
		t.Fatalf("ImportMarkdownStats: %v", err)
	}
	if stats.LessonsSkipped != 0 || stats.LessonsErrors != 0 {
		t.Errorf("import entry point: got lessons skipped=%d errors=%d, want 0 and 0", stats.LessonsSkipped, stats.LessonsErrors)
	}
	if got := lessonRows(t, db, "m-imp"); !reflect.DeepEqual(got, lessonsFullRows) {
		t.Errorf("import entry point stored: got %q, want %q", got, lessonsFullRows)
	}
}

func TestImportMarkdownStatsDebriefUnreadableLessonsCounted(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMission(t, db, "m-imp")
	stats, err := ImportMarkdownStats(db, "/h/missions/m-imp/DEBRIEF.md", "missions/m-imp/DEBRIEF.md", "h", []byte("## Wrong Assumptions\nno table\n"))
	if err != nil {
		t.Fatalf("ImportMarkdownStats: %v", err)
	}
	if stats.LessonsSkipped != 1 {
		t.Errorf("import entry point through add(): LessonsSkipped = %d, want 1", stats.LessonsSkipped)
	}
}

func TestImportMarkdownStatsDebriefLessonsErrorsCounted(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMission(t, db, "m-imp")
	if _, err := db.Exec(`DROP TABLE mission_lessons`); err != nil {
		t.Fatalf("drop mission_lessons: %v", err)
	}
	stats, err := ImportMarkdownStats(db, "/h/missions/m-imp/DEBRIEF.md", "missions/m-imp/DEBRIEF.md", "h", []byte(lessonsFull))
	if err != nil {
		t.Fatalf("ImportMarkdownStats: %v", err)
	}
	if stats.LessonsErrors != 2 {
		t.Errorf("import entry point through add(): LessonsErrors = %d, want 2 (both section replaces fail)", stats.LessonsErrors)
	}
}
