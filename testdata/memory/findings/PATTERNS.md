# Patterns

Reusable solutions, architecture decisions, and technical approaches discovered during missions. Advisory and non-binding — `.claude/memory/reference/DIRECTIVES.md` wins on any conflict.

Append-only. New entries go below the marker at the bottom of this file, after every entry already there. The marker never moves and nothing already written is displaced.

<!-- Append new entries below this line -->

### Testing with Fixture Data
**Context**: When testing an importer that reads a structured memory tree, the test fixtures must stay in sync with the canonical structure.
**Solution**: Update fixtures alongside the code that consumes them, testing the integration rather than mocking it.
**Example**: The bishop-memory importer test uses real markdown and JSONL files, making the fixtures part of the contract between the test and the importer.
**Discovered**: 2026-09-04, mission-20260904-01
