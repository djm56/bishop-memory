package importer

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// writeFile creates path's parent directories and writes content to it.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// docTags returns the mission_id and harness stored for the document whose
// source_path ends with suffix.
func docTags(t *testing.T, db *sql.DB, suffix string) (missionID, harness sql.NullString) {
	t.Helper()
	err := db.QueryRow(
		`SELECT mission_id, harness FROM documents WHERE source_path LIKE '%' || ?`, suffix,
	).Scan(&missionID, &harness)
	if err != nil {
		t.Fatalf("lookup document %s: %v", suffix, err)
	}
	return missionID, harness
}

// TestSyncHarnessTagsMissionDocuments checks that brief, progress and debrief
// documents carry their mission id, every document carries the harness, and
// other documents get no mission id.
func TestSyncHarnessTagsMissionDocuments(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	for _, name := range []string{"BRIEF.md", "PROGRESS.md", "DEBRIEF.md"} {
		writeFile(t, filepath.Join(root, "missions", "mission-20261008-01", name), "# "+name+"\n")
	}
	writeFile(t, filepath.Join(root, "state", "CURRENT-MISSION.md"), "# Current\n")

	if err := SyncHarness(db, root, "kirsch"); err != nil {
		t.Fatalf("SyncHarness: %v", err)
	}

	for _, name := range []string{"BRIEF.md", "PROGRESS.md", "DEBRIEF.md"} {
		mission, harness := docTags(t, db, "/missions/mission-20261008-01/"+name)
		if mission.String != "mission-20261008-01" {
			t.Errorf("%s mission_id = %q, want mission-20261008-01", name, mission.String)
		}
		if harness.String != "kirsch" {
			t.Errorf("%s harness = %q, want kirsch", name, harness.String)
		}
	}
	mission, harness := docTags(t, db, "/state/CURRENT-MISSION.md")
	if mission.Valid {
		t.Errorf("CURRENT-MISSION.md mission_id = %q, want NULL", mission.String)
	}
	if harness.String != "kirsch" {
		t.Errorf("CURRENT-MISSION.md harness = %q, want kirsch", harness.String)
	}
}

// TestSyncHarnessTagsUnchangedDocuments checks that re-syncing an unchanged
// file fills the tags on a row imported without them and corrects a stale
// kind, and that a later sync with no harness keeps the stored harness.
func TestSyncHarnessTagsUnchangedDocuments(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "missions", "m-1", "BRIEF.md"), "# Brief\n")

	if err := Sync(db, root); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := db.Exec(`UPDATE documents SET mission_id = NULL, kind = '.opencode'`); err != nil {
		t.Fatalf("clear mission_id: %v", err)
	}
	if err := SyncHarness(db, root, "kirsch"); err != nil {
		t.Fatalf("SyncHarness: %v", err)
	}
	if err := Sync(db, root); err != nil {
		t.Fatalf("second Sync: %v", err)
	}

	mission, harness := docTags(t, db, "/missions/m-1/BRIEF.md")
	if mission.String != "m-1" || harness.String != "kirsch" {
		t.Errorf("tags = (%q, %q), want (m-1, kirsch)", mission.String, harness.String)
	}
	var kind string
	if err := db.QueryRow(`SELECT kind FROM documents`).Scan(&kind); err != nil {
		t.Fatalf("lookup kind: %v", err)
	}
	if kind != "brief" {
		t.Errorf("kind = %q, want brief", kind)
	}
}

// TestSyncSkipsHiddenDirectories checks that a root pointed at a repository
// does not import its .claude or .git trees.
func TestSyncSkipsHiddenDirectories(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".claude", "memory", "state", "CURRENT-MISSION.md"), "# Hidden\n")
	writeFile(t, filepath.Join(root, "notes", "README.md"), "# Visible\n")

	if err := Sync(db, root); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents WHERE source_path LIKE '%/.claude/%'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("imported %d documents from .claude, want 0", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents WHERE kind = 'notes'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("imported %d notes documents, want 1", n)
	}
}

// TestSyncMission checks that SyncMission imports only the named mission's
// files, skips ones that do not exist, and refuses an id that is not a single
// path segment.
func TestSyncMission(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "missions", "m-1", "BRIEF.md"), "# Brief\n")
	writeFile(t, filepath.Join(root, "missions", "m-1", "PROGRESS.md"), "# Progress\n")
	writeFile(t, filepath.Join(root, "missions", "m-2", "BRIEF.md"), "# Other\n")
	writeFile(t, filepath.Join(root, "state", "CURRENT-MISSION.md"), "# Current\n")

	if err := SyncMission(db, root, "kirsch", "m-1"); err != nil {
		t.Fatalf("SyncMission: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("imported %d documents, want 2 (m-1 brief and progress)", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents WHERE mission_id = 'm-1' AND harness = 'kirsch'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("%d documents tagged m-1/kirsch, want 2", n)
	}

	for _, bad := range []string{"", "..", "../m-2", "m-1/x", ".hidden"} {
		if err := SyncMission(db, root, "kirsch", bad); err == nil {
			t.Errorf("SyncMission(%q) succeeded, want error", bad)
		}
	}
}

// TestSyncAgents checks that agent definitions become one crew row per name,
// with the role shortened from the description, and that a second harness
// defining the same agent updates that row rather than adding one.
func TestSyncAgents(t *testing.T) {
	db := openTestDB(t)
	claude, opencode := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(claude, "apone.md"),
		"---\nname: apone\ndescription: \"Code reviewer (apone). Reads diffs.\"\nskills:\n  - code-review\n---\n# Apone\n")
	writeFile(t, filepath.Join(claude, "notes.txt"), "not an agent")
	writeFile(t, filepath.Join(opencode, "Apone.md"),
		"---\nmode: subagent\ndescription: Code reviewer. Advisory only.\n---\n")
	writeFile(t, filepath.Join(opencode, "ripley.md"), "# Ripley, no frontmatter\n")
	writeFile(t, filepath.Join(claude, "vasquez.md"), "---\ndescription: Senior developer (vasquez), held in reserve. Rarely called.\n---\n")

	if err := SyncAgents(db, claude); err != nil {
		t.Fatalf("SyncAgents claude: %v", err)
	}
	var role, description string
	if err := db.QueryRow(`SELECT role, description FROM crew WHERE name = 'apone'`).Scan(&role, &description); err != nil {
		t.Fatalf("lookup apone: %v", err)
	}
	if role != "Code reviewer" || description != "Code reviewer (apone). Reads diffs." {
		t.Errorf("apone = (%q, %q)", role, description)
	}

	if err := SyncAgents(db, opencode); err != nil {
		t.Fatalf("SyncAgents opencode: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crew`).Scan(&n); err != nil {
		t.Fatalf("count crew: %v", err)
	}
	if n != 3 {
		t.Errorf("%d crew rows, want 3 (apone, ripley, vasquez)", n)
	}
	if err := db.QueryRow(`SELECT role FROM crew WHERE name = 'vasquez'`).Scan(&role); err != nil {
		t.Fatalf("lookup vasquez: %v", err)
	}
	if role != "Senior developer, held in reserve" {
		t.Errorf("vasquez role = %q", role)
	}
	if err := db.QueryRow(`SELECT description FROM crew WHERE name = 'apone'`).Scan(&description); err != nil {
		t.Fatalf("lookup apone: %v", err)
	}
	if description != "Code reviewer. Advisory only." {
		t.Errorf("apone description = %q, want the later definition", description)
	}

	if err := SyncAgents(db, filepath.Join(claude, "missing")); err != nil {
		t.Errorf("missing agents dir: %v, want nil", err)
	}
}

func TestCrewName(t *testing.T) {
	for in, want := range map[string]string{"@Hicks": "hicks", "hicks": "hicks", "bishopharnessopencode:hicks": "hicks", " @apone ": "apone", "": ""} {
		if got := CrewName(in); got != want {
			t.Errorf("CrewName(%q) = %q, want %q", in, got, want)
		}
	}
}
