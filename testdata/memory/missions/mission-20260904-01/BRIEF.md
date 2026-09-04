# Brief — mission-20260904-01

## Goal
Restructure testdata fixtures to align with the harness memory tree layout, updating the importer to match the new path-to-kind mappings and the CURRENT-MISSION.md parser.

## Acceptance Criteria
- 13 Markdown files plus 1 JSONL with 4 non-blank lines in new structure
- kindFromPath correctly maps missions/*/BRIEF.md, missions/*/PROGRESS.md, missions/*/DEBRIEF.md, findings/PATTERNS.md, findings/service-records/*.md, reference/DIRECTIVES.md
- CURRENT-MISSION.md parser recognizes exactly six fields: Mission ID, Status, Owner, Next Action, Last Updated, Blockers
- Importer tests pass with new count and mappings verified

## Key Files
- `testdata/memory/` — fixture tree restructured
- `internal/importer/importer.go` — kindFromPath and CURRENT-MISSION.md parser updated
- `internal/importer/importer_test.go` — count assertions and kind mappings verified

## Notes
Fixtures and importer are mutually coupled: the test asserts exact document count and specific path-to-kind mappings. They must land together.
