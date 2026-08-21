# Improvements

## Sample improvement

- Title: Extract ACTIVE-TASK.md structured fields for searchability
- Status: proposed
- Body: The bishop-memory importer (Phase 2 Step 5) extracts Task ID,
  Status, Next Action, Blockers and other fields from ACTIVE-TASK.md
  and prepends them as a duplicated block at the top of the documents
  body so they are searchable both in their original position and as
  bare Key: Value pairs. See internal/importer/importer.go.
