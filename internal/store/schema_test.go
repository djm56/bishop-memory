// Package store — tests for splitSQLStatements.
//
// These tests are the focused unit coverage for the SQL splitter that
// ApplySchema relies on (Step 6 re-review suggestion 1). The importer
// tests (internal/importer/importer_test.go) cover the splitter
// indirectly via the production ApplySchema path against db/schema.sql,
// but they only prove one input works — the cases here cover the edge
// classes the splitter is documented to handle:
//
//   - "--" line comments with an embedded ";"
//   - "/* ... */" block comments (single + multi-line)
//   - single-quoted string literals with an embedded ";"
//   - the SQL-standard escaped-quote form (two adjacent single quotes
//     inside a string literal) — see the strings test
//   - consecutive ";;" (empty statements must be skipped)
//   - a trailing statement with no final ";"
//   - unterminated strings (consume to EOF, no panic)
package store

import (
	"reflect"
	"strings"
	"testing"
)

// TestSplitSQLStatements_Comments covers the two SQL comment forms
// against the schema's own header line (which embeds a ";" inside a
// "--" comment) — the case the Step 6 CRITICAL regression hit.
//
// Contract: line comments are skipped up to (and discarding) their
// text, but the trailing "\n" is kept in the buffer (so SQLite's line
// numbering survives). Block comments are stripped entirely (both the
// "/* ... */" markers and the body between them). Both forms correctly
// do NOT terminate a statement on an embedded ";".
//
// Net effect for the input below: comments are stripped, only the
// CREATE TABLE survives (with leading newlines that TrimSpace removes
// at emit time).
func TestSplitSQLStatements_Comments(t *testing.T) {
	input := strings.Join([]string{
		"-- A header comment with an embedded ; that must NOT terminate.",
		"-- All access goes through modernc.org/sqlite; the service runs in WAL mode.",
		"",
		"/* Single-line block comment with ; inside. */",
		"/* Multi-line",
		"   block comment with ; inside.",
		"   Spans three lines. */",
		"CREATE TABLE t (id INTEGER PRIMARY KEY);",
	}, "\n")

	want := []string{
		"CREATE TABLE t (id INTEGER PRIMARY KEY)",
	}

	got := splitSQLStatements(input)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitSQLStatements(comments):\n got: %q\nwant: %q", got, want)
	}
}

// TestSplitSQLStatements_Strings covers single-quoted string literals
// with embedded ";", and the SQL-standard escaped-quote sequence
// (two adjacent single quotes inside a string literal).
func TestSplitSQLStatements_Strings(t *testing.T) {
	input := strings.Join([]string{
		`CREATE TABLE s (note TEXT);`,
		`INSERT INTO s VALUES ('one; semicolon', 'two '' escaped; quotes');`,
	}, "\n")

	want := []string{
		"CREATE TABLE s (note TEXT)",
		"INSERT INTO s VALUES ('one; semicolon', 'two '' escaped; quotes')",
	}

	got := splitSQLStatements(input)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitSQLStatements(strings):\n got: %q\nwant: %q", got, want)
	}
}

// TestSplitSQLStatements_EmptyAndTrailing covers ";"-only padding
// (must produce zero statements) and a final statement with no
// trailing ";" (must still be emitted).
func TestSplitSQLStatements_EmptyAndTrailing(t *testing.T) {
	input := strings.Join([]string{
		";;", // empty + empty
		"CREATE TABLE a (id INTEGER PRIMARY KEY);", // full statement
		";", // trailing empty
		"CREATE TABLE b (id INTEGER PRIMARY KEY)", // NO trailing ";"
	}, "\n")

	want := []string{
		"CREATE TABLE a (id INTEGER PRIMARY KEY)",
		"CREATE TABLE b (id INTEGER PRIMARY KEY)",
	}

	got := splitSQLStatements(input)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitSQLStatements(empty+trailing):\n got: %q\nwant: %q", got, want)
	}
}

// TestSplitSQLStatements_UnterminatedString covers the "unterminated
// string" case. The contract is "consume to EOF, no panic" — the
// partial statement is still emitted so a malformed schema is surfaced
// as a db.Exec error rather than silently truncated or lost.
func TestSplitSQLStatements_UnterminatedString(t *testing.T) {
	input := "CREATE TABLE u (note TEXT DEFAULT 'oops, never closed"

	got := splitSQLStatements(input)
	if len(got) != 1 {
		t.Fatalf("splitSQLStatements(unterminated string): want 1 statement, got %d (%q)",
			len(got), got)
	}
	if !strings.Contains(got[0], "'oops, never closed") {
		t.Fatalf("splitSQLStatements(unterminated string): partial content not preserved, got %q",
			got[0])
	}
}
