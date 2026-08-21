// Package store — schema bootstrap. ApplySchema idempotently brings the
// open database to the schema described by db/schema.sql. It is called
// once at startup by cmd/memoryd/main.go.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// ApplySchema reads the SQL file at path and executes it against db. It
// is safe to call repeatedly because the DDL uses CREATE TABLE IF NOT
// EXISTS and CREATE INDEX IF NOT EXISTS.
//
// database/sql's Exec does not support multiple statements in one call,
// so the file is split into individual statements by splitSQLStatements
// (which correctly handles SQL comments and single-quoted string
// literals — the naive strings.Split(content, ";") approach breaks on
// ";" inside "--" comments, e.g. the schema's header line
// "-- All access goes through modernc.org/sqlite (CGo-free); the service
// runs ..."). Each non-empty statement is then db.Exec'd; benign
// "already exists" errors are tolerated so repeated boots stay
// idempotent even if the driver surfaces them.
func ApplySchema(db *sql.DB, path string) error {
	sqlBytes, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read schema %s: %w", path, err)
	}

	statements := splitSQLStatements(string(sqlBytes))

	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			// The schema uses IF NOT EXISTS throughout, so benign
			// "already exists" errors should not occur; still tolerate
			// them so re-running stays idempotent even if the driver
			// emits them.
			if isAlreadyExistsErr(err) {
				continue
			}
			// Truncate the failing statement in the error message so a
			// multi-KiB CREATE TABLE (with comments / CHECK constraints)
			// does not flood logs. ~200 bytes keeps enough context to
			// identify the failing statement (the DDL keyword + first
			// column definitions) without the full body. The truncation
			// is only for logging — the full statement has already been
			// handed to db.Exec and is not recoverable here anyway.
			const snippetBytes = 200
			snippet := stmt
			if len(snippet) > snippetBytes {
				snippet = stmt[:snippetBytes] + "…"
			}
			return fmt.Errorf("apply schema statement %q: %w", snippet, err)
		}
	}

	return nil
}

// splitSQLStatements splits a SQL script into individual statements on
// top-level ";" terminators. The splitter is a single-pass byte state
// machine that correctly handles, at minimum:
//
//   - "--" line comments: a "--" sequence in normal state begins a
//     comment that runs to (and past) the next "\n". A ";" inside a
//     line comment does NOT terminate a statement.
//   - "/* ... */" block comments: a "/*" sequence in normal state
//     begins a comment that runs to the closing "*/". Block comments
//     may span multiple lines. A ";" inside a block comment does NOT
//     terminate a statement.
//   - single-quoted string literals: a single-quote byte in normal
//     state begins a string that ends at the next unescaped single-
//     quote byte. Two consecutive single-quote bytes inside a string
//     (the SQL-standard escaped-quote sequence) do not end the string
//     and are kept verbatim. A ";" inside a string does NOT terminate
//     a statement.
//
// The output is the list of non-empty (after TrimSpace) statements.
// Trailing whitespace / blank lines are dropped. A final statement
// without a trailing ";" is emitted if it has content.
//
// Trigger bodies that use "BEGIN ... END" with embedded ";" are NOT
// handled as a special case. The current db/schema.sql contains no
// triggers; the comment/string rules above are sufficient for any
// well-formed handwritten SQL file. A real trigger-body parser would
// need BEGIN/END depth tracking and is out of scope for Phase 2.
//
// Backtick / double-quoted identifiers and MySQL-style "/*! ... */"
// conditional comments are intentionally NOT handled — SQLite has
// no need for them in the bishop-memory schema, and adding the
// branches would only complicate the state machine.
func splitSQLStatements(sqlText string) []string {
	var (
		stmts []string
		buf   strings.Builder
		state = splitStateNormal
	)

	// emit flushes the current buffer as one statement (if non-empty)
	// and resets the buffer.
	emit := func() {
		s := strings.TrimSpace(buf.String())
		if s != "" {
			stmts = append(stmts, s)
		}
		buf.Reset()
	}

	// Byte iteration is safe: SQL is ASCII for our purposes (the schema
	// file contains only ASCII; UTF-8 multi-byte sequences cannot start
	// with any byte we treat as a state transition).
	for i := 0; i < len(sqlText); i++ {
		c := sqlText[i]
		switch state {
		case splitStateNormal:
			switch c {
			case '\'':
				// Enter a single-quoted string literal. The leading quote
				// is preserved so error messages still show the original
				// source text.
				state = splitStateString
				buf.WriteByte(c)
			case '-':
				// Look for "--" line-comment opener. We do NOT require
				// preceding whitespace: the SQLite parser recognises "--"
				// wherever it appears as a complete token, and the bishop-
				// memory schema never uses "--" inside an identifier.
				if i+1 < len(sqlText) && sqlText[i+1] == '-' {
					state = splitStateLineComment
					i++ // consume the second '-'
					continue
				}
				buf.WriteByte(c)
			case '/':
				// Look for "/*" block-comment opener.
				if i+1 < len(sqlText) && sqlText[i+1] == '*' {
					state = splitStateBlockComment
					i++ // consume the '*'
					continue
				}
				buf.WriteByte(c)
			case ';':
				// Top-level statement terminator. Emit the buffer (which
				// may be empty if the script had consecutive ";;" or
				// trailing whitespace) and reset.
				emit()
			default:
				buf.WriteByte(c)
			}
		case splitStateLineComment:
			// Skip everything until the next "\n" (which we keep in the
			// buffer to preserve line numbers for error messages).
			if c == '\n' {
				state = splitStateNormal
				buf.WriteByte(c)
			}
		case splitStateBlockComment:
			// Skip everything until "*/". We deliberately do NOT keep
			// the comment body in the output; SQLite does not care
			// about whitespace, and stripping keeps each emitted
			// statement close to its original source for error context.
			if c == '*' && i+1 < len(sqlText) && sqlText[i+1] == '/' {
				state = splitStateNormal
				i++ // consume the '/'
				continue
			}
		case splitStateString:
			if c == '\'' {
				// "''" is an escaped single quote (SQL standard for
				// embedding a quote in a string literal); both bytes
				// are kept and we stay in string state.
				if i+1 < len(sqlText) && sqlText[i+1] == '\'' {
					buf.WriteByte('\'')
					buf.WriteByte('\'')
					i++ // consume the second quote
					continue
				}
				// End of string. The closing quote is preserved.
				state = splitStateNormal
				buf.WriteByte(c)
			} else {
				// String content (including newlines, since SQLite
				// string literals may span lines).
				buf.WriteByte(c)
			}
		}
	}

	// Emit any trailing statement (e.g. if the file does not end with
	// a ";" — uncommon, but harmless to support).
	emit()
	return stmts
}

// splitState is the state of the SQL splitter's single-pass scanner.
// Each value is exclusive; the splitter is in exactly one state per
// cursor position.
type splitState int

const (
	// splitStateNormal is the default state — outside any comment or
	// string literal. A ";" here terminates a statement; "--" / "/*"
	// open comments; "'" opens a string.
	splitStateNormal splitState = iota
	// splitStateLineComment is inside a "--" line comment; everything
	// up to (but not including) the next "\n" is ignored.
	splitStateLineComment
	// splitStateBlockComment is inside a "/*" "*/" block comment;
	// everything up to (but not including) the closing "*/" is ignored.
	splitStateBlockComment
	// splitStateString is inside a single-quoted string literal; the
	// next unescaped "'" (i.e. a "'" not immediately followed by "'")
	// returns to splitStateNormal.
	splitStateString
)

// isAlreadyExistsErr reports whether err is a benign "already exists"
// error that ApplySchema can safely skip.
func isAlreadyExistsErr(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "already exist")
}
