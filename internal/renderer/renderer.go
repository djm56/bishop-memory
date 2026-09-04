// Package renderer emits Markdown / JSONL views (e.g. CURRENT-MISSION.md,
// FLIGHT-RECORDER.md) from the SQLite-backed state so older agent
// runtimes that read files directly keep working during the Phase 3
// transition.
//
// Phase 3 groundwork — renders DB state into generated views
// (CURRENT-MISSION.md, FLIGHT-RECORDER.md). Not wired to any API endpoint
// in Phase 2; full implementation + the API-first migration are
// Phase 3.
//
// The current Phase 2 state is a stub. The renderers below will live
// here once the Phase 3 work begins:
//
//	func RenderCurrentMission(w io.Writer, store *sql.DB) error
//	func RenderFlightRecorder(w io.Writer, store *sql.DB, since time.Time) error
//
// Phase 3 also adds stale-document pruning, fail-fast to logged-skip
// transition, and the BeginTx context propagation documented in
// docs/migration-plan.md.
package renderer
