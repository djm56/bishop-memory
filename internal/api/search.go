// Package api — HTTP handler for /v1/memory/search.
//
// searchMemoryHandler runs an FTS5 query against imported documents and
// returns ranked results. It is the read-only counterpart to the
// importer triggered by /v1/documents/sync.
//
// # Hyphen defect (task-20260821-03) and the rewrite that fixes it
//
// FTS5's bare (unquoted) query grammar treats several punctuation
// characters as syntax rather than as text: '"' opens a phrase, '*' is
// a prefix marker, '(' ')' group, and — confirmed empirically against a
// real FTS5 index built from db/schema.sql's documents_fts table — a
// bareword containing '-' is liable to be parsed as a column-filter
// expression (`<col> : <term>`-shaped), because the token *after* the
// hyphen gets resolved against documents_fts's declared columns
// (title, body) and fails with driver errors of the shape `no such
// column: <token>` when it isn't one of them. Reproduced directly
// against the running service before this fix: "step-sync" failed
// with `no such column: sync`, "task-20260821-01" with
// `no such column: 20260821`, and "EVENT-LOG" with `no such column:
// LOG` — none of these strings contain the literal substring
// "fts5: syntax error", so the previous isFTSSyntaxErr classifier (a
// tight substring match anchored on exactly that text) never caught
// them and they fell through to internalError, a 500. The same probe
// also turned up two further, previously-unreported 500s the hyphen
// report didn't name: an unbalanced quote produces a driver error
// `unterminated string`, and a lone "*" produces `unknown special
// query: `  — neither contains "fts5: syntax error" either.
//
// This matters in practice because nearly every identifier the
// consuming harness searches for is hyphenated (step-sync,
// code-reviewer, agent-documents, EVENT-LOG, every task ID), so a bare
// hyphenated query — the ordinary way a human or an agent would type
// one — was failing on almost every realistic search.
//
// The fix has two independent layers, deliberately not just one:
//
//  1. sanitizeFTS5Query (below) rewrites the caller's raw query into
//     FTS5 syntax that cannot hit the failure above, by walking the
//     string once and quoting — turning into a literal phrase — every
//     whitespace-delimited token that contains anything other than a
//     Unicode letter, digit, or underscore (optionally followed by a
//     single trailing '*' prefix marker). A token that is already a
//     complete `"quoted phrase"` is copied through byte-for-byte
//     unchanged. This is a deliberate trade-off, not the only one
//     available: the alternative was to preserve every FTS5 operator
//     — AND / OR / NOT, prefix '*', parenthetical grouping, ':'
//     column filters — at the cost of leaving punctuated barewords
//     exactly as fragile as they are today. This fix instead keeps
//     every capability this API actually documents (docs/
//     api-contract.md: implicit AND across words, explicit "phrase"
//     queries, a trailing '*' prefix wildcard on a plain word) and,
//     as a side effect of only touching tokens that contain
//     punctuation, ALSO keeps AND / OR / NOT working exactly as
//     before when a caller uses them validly (those are pure-letter
//     tokens, indistinguishable at this layer from an ordinary word,
//     so this rewrite does not touch them). What a caller loses:
//     parenthetical grouping and ':' column-filter syntax (neither
//     ever documented), and the ability to use a bare '-', '*', '('
//     etc. as a token in its own right — none of which had a
//     sensible meaning as a search term to begin with. A caller who
//     happens to search for the literal, all-uppercase word "AND",
//     "OR", or "NOT" still gets FTS5's operator behaviour instead of
//     a literal match — that ambiguity is pre-existing (it is how
//     FTS5's own grammar has always worked here) and is not something
//     this fix changes either way; it is called out here rather than
//     left silent.
//
//     A malformed input this rewrite cannot make safe — an unbalanced
//     '"' at a genuine token boundary — is rejected before any query
//     ever reaches the database, with the same static "invalid search
//     query syntax" 400 this package has always documented for that
//     case (docs/api-contract.md's "q present but malformed FTS5
//     query syntax (e.g. an unbalanced quote)" example), rather than
//     reaching the driver and surfacing as the `unterminated string`
//     500 above.
//
//     Two step 2a corrections to this layer, closing warnings a review
//     raised against the version above: (a) a '"' embedded inside a
//     whitespace-delimited word — with no whitespace on either side,
//     so it was never a phrase boundary the caller could have
//     intended — is now escaped (doubled) as literal content of that
//     one word's phrase, rather than being read as a fresh phrase-open
//     and splitting the word into several implicit-AND fragments (see
//     escapeFTS5Bareword's and sanitizeFTS5Query's own docblocks for
//     why the old behaviour was a real correctness defect, not merely
//     a cosmetic one); (b) a Unicode combining mark (e.g. a bare
//     combining acute accent in an NFD-decomposed word) now counts as
//     an ordinary word rune alongside letters/digits/underscore, so a
//     composed and a decomposed encoding of the same visible word are
//     classified identically instead of one being quoted and the
//     other not (see isFTS5WordRune's docblock).
//
//  2. isCallerQueryError is a second, independent safety net for
//     whatever the rewrite in (1) does not foresee. It inspects two
//     independent signals together — the structured SQLite result
//     code modernc.org/sqlite attaches to every driver error
//     (*sqlite.Error.Code()), AND whether the driver's error text
//     carries the FTS5 module's own "fts5:" prefix. See that
//     function's own docblock for the full reasoning and for the
//     step 2a correction below.
//
// # Step 2a correction — a false claim in this docblock, and the regression it hid
//
// An earlier version of this docblock claimed item (2)'s discriminator
// could rely on the structured result code ALONE, reasoning that
// because the SQL text executed here is fixed, any SQLITE_ERROR from
// it "can only" originate in the bound MATCH argument. That claim was
// false, and it was confidently wrong in exactly the way that let the
// defect through a review that read the code rather than exercising
// it: a missing or mismatched documents_fts table (a schema/deployment
// fault — this service's migration path includes a manual ALTER
// TABLE step, and a server about to be deployed can hit exactly this)
// raises the identical SQLITE_ERROR code and was, before this
// correction, misrouted to the caller as a 400 "invalid search query
// syntax" — the previous substring classifier this rewrite replaced
// did NOT match those messages and correctly sent them to
// internalError (500), so this was a regression relative to it, not
// merely an incomplete improvement. isCallerQueryError's own docblock
// states what actually distinguishes the two fault classes and why
// the fix does not simply reintroduce driver-message-content matching
// under a different name; this paragraph exists so a future reader of
// THIS docblock does not inherit the same false claim.
//
// Neither layer echoes the driver's own Error() text — which embeds a
// fragment of the caller's query — into the response; only the
// static, authored "invalid search query syntax" string is ever
// returned (CONV-033). The real driver error is still logged
// server-side for operator diagnosability. "q" missing and "q"
// present-but-unusable remain two distinguishable outcomes with two
// distinct `details` strings (CONV-048); see docs/api-contract.md.
//
// Join contract: documents_fts is a standalone FTS5 table. The
// importer inserts into documents_fts with rowid = documents.id, so
// the join key documents_fts.rowid = d.id ties each FTS hit back to
// its metadata row. An empty index (before the first sync) returns
// {"results": [], "q": ...}.
package api

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// searchHit is one ranked FTS5 result returned by /v1/memory/search. It
// is a focused projection of model.SearchResult so the response shape is
// exactly {id, source_path, title, kind, snippet, rank} without leaking
// the full document body / sha / timestamps.
type searchHit struct {
	// ID is the documents.id of the matched document.
	ID int64 `json:"id"`

	// SourcePath is the absolute path the importer stored for the doc.
	SourcePath string `json:"source_path"`

	// Title is the document heading (first H1 for Markdown).
	Title string `json:"title,omitempty"`

	// Kind classifies the source ("graph", "reference", ...).
	Kind string `json:"kind,omitempty"`

	// Snippet is the FTS5 snippet() output with matched terms wrapped
	// in <mark> tags, built from the body column (index 1).
	Snippet string `json:"snippet,omitempty"`

	// Rank is the FTS5 bm25 score (lower = more relevant).
	Rank float64 `json:"rank,omitempty"`
}

// searchMemoryHandler handles GET /v1/memory/search?q=...
//
// Query handling: the caller's raw q is trimmed, validated for
// presence, then rewritten by sanitizeFTS5Query into FTS5 query syntax
// before it is bound to the MATCH clause — see the package docblock
// above for why and what that rewrite does and does not preserve. The
// response's "q" field always echoes the caller's trimmed, ORIGINAL
// input, never the rewritten form, so the documented response shape
// (docs/api-contract.md) is unaffected by the rewrite.
func searchMemoryHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := strings.TrimSpace(c.Query("q"))
		if q == "" {
			validationError(c, errors.New("q is required"))
			return
		}

		ftsQuery, ok := sanitizeFTS5Query(q)
		if !ok {
			// The only rejection sanitizeFTS5Query itself raises is an
			// unbalanced '"' — malformed FTS5 phrase syntax no rewrite
			// can safely repair. Same static, authored message as the
			// driver-side syntax-error branch below, and for the same
			// CONV-033 reason: never surface caller input, and never
			// distinguish this from the driver-detected case, since
			// both are "q was present but unusable" (CONV-048 asks
			// only that missing-q and unusable-q be told apart, not
			// that every flavour of "unusable" be told apart from one
			// another).
			validationError(c, errors.New("invalid search query syntax"))
			return
		}

		const query = `
			SELECT d.id, d.source_path, d.title, d.kind,
			       snippet(documents_fts, 1, '<mark>', '</mark>', '…', 24) AS snippet,
			       rank
			FROM documents_fts
			JOIN documents d ON d.id = documents_fts.rowid
			WHERE documents_fts MATCH ?
			ORDER BY rank
			LIMIT 20
		`

		rows, err := db.QueryContext(c.Request.Context(), query, ftsQuery)
		if err != nil {
			// See isCallerQueryError's docblock: only a SQLITE_ERROR
			// (code 1) from this exact, fixed statement is treated as
			// the caller's fault; anything else is a genuine 500.
			if isCallerQueryError(err) {
				// Log the real driver error server-side (it is useful
				// for debugging a caller's malformed FTS5 query) but
				// respond with a static, authored message only — see
				// the package docblock above and CONV-033. The
				// driver's own Error() text embeds a fragment of q and
				// MUST NOT reach the client.
				log.Printf("request_id=%v error=%v", requestID(c), err)
				validationError(c, errors.New("invalid search query syntax"))
				return
			}
			internalError(c, err)
			return
		}
		defer rows.Close()

		results := make([]searchHit, 0)

		for rows.Next() {
			var hit searchHit
			if err := rows.Scan(
				&hit.ID,
				&hit.SourcePath,
				&hit.Title,
				&hit.Kind,
				&hit.Snippet,
				&hit.Rank,
			); err != nil {
				internalError(c, err)
				return
			}
			results = append(results, hit)
		}

		if err := rows.Err(); err != nil {
			if isCallerQueryError(err) {
				log.Printf("request_id=%v error=%v", requestID(c), err)
				validationError(c, errors.New("invalid search query syntax"))
				return
			}
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"results": results,
			"q":       q,
		})
	}
}

// isCallerQueryError reports whether err is an error FTS5's own query-
// text parser raised while rejecting the caller's bound MATCH
// argument, as opposed to a genuine server-side fault — including,
// but not limited to, a missing or mismatched documents_fts table.
//
// # Step 2a correction — the previous version of this function was wrong
//
// The version this replaces treated *sqlite.Error.Code() ==
// SQLITE_ERROR alone as sufficient, on the reasoning that because the
// SQL text this package executes is a fixed, hand-authored constant,
// any SQLITE_ERROR from it "can only" come from the bound MATCH
// argument. That reasoning was false, and was falsified empirically
// (task-20260821-03 step 2 review, reproduced again in step 2a — see
// search_test.go's TestIsCallerQueryError_SchemaFaultIsNotCallerFault):
// a missing documents_fts table and a documents_fts that exists but
// is not the FTS5 virtual table the query expects (e.g. after a
// botched manual ALTER TABLE — this service's own deployment/migration
// path includes one) BOTH raise SQLITE_ERROR (code 1) from this exact
// statement, with nothing to do with the caller's MATCH text. Code
// alone cannot tell those two fault classes apart, because
// SQLITE_ERROR is SQLite's generic "the statement as given could not
// be executed" code, shared by every failure reason the engine does
// not have a more specific code for — the fixed-SQL-text premise says
// nothing about which subsystem raised the error, only that the
// caller cannot have altered the SQL's shape.
//
// # What actually distinguishes the two classes
//
// A second, independent signal is required: err.Error() must also
// contain the literal substring "fts5:". This is NOT the same kind of
// check as the driver-message substring matching that caused the
// original hyphen defect (task-20260821-03's step 1 bug): that
// classifier matched on the *content* specific error messages happen
// to mention (column names, e.g.) and had to be extended one message
// shape at a time. "fts5:" is different in kind — it is the FTS5
// virtual-table module's own fixed, unconditional prefix for every
// error IT raises from inside its query-text parser (confirmed
// against the real modernc.org/sqlite driver: a dangling boolean
// operator like "hello AND OR" raises `fts5: syntax error near
// "OR"`), which the engine's core table/column resolver never
// prepends to anything, because that resolver runs BEFORE FTS5's own
// module code is ever invoked for a table or column it cannot find —
// confirmed the other direction too: "no such table: documents_fts"
// and "no such column: documents_fts" (the two schema-fault
// reproductions above) carry no "fts5:" text at all. This is
// classifying by which subsystem raised the error, not by what its
// message happens to say about the caller's specific input — the
// distinction Delegation Brief Standard 15's "most direct route to
// hand" warns against collapsing.
//
// This value is read only for internal classification and is never
// echoed to the caller (CONV-033; see searchMemoryHandler and the
// package docblock) — err.Error() embeds a fragment of the caller's
// MATCH text for a genuine syntax fault, exactly why it is logged
// server-side and never put in the response body.
//
// # What this guarantees, and what it does not
//
// Given sanitizeFTS5Query already quotes or rejects every punctuation-
// based failure mode described in the package docblock before a
// query ever reaches the database, the only caller-fault error shape
// this function still needs to route to 400 is a dangling boolean
// operator chain (AND/OR/NOT used invalidly) — confirmed to always
// carry "fts5:". A future caller-fault shape this task did not
// encounter, if it somehow lacked "fts5:" in its text, would be
// misrouted to 500 rather than 400 — the safe direction to fail in,
// since it reports a real caller mistake as a server fault rather
// than the reverse (a real server fault reported as the caller's
// fault), which is the specific inversion this correction exists to
// close. This function makes no claim about disk I/O, a locked
// database, or a corrupt file — those were not reproducible in this
// task's session (see search_test.go's docblock) and are simply
// whatever SQLITE_ERROR-with-no-"fts5:" is left to fall through to
// internalError, same as the schema faults above.
func isCallerQueryError(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	if sqliteErr.Code() != sqlite3.SQLITE_ERROR {
		return false
	}
	// "fts5:" is the FTS5 module's own fixed error-prefix convention —
	// see the docblock above. It is read from the driver's internal
	// error text purely to decide which branch handles the request;
	// it is never included in any response (CONV-033).
	return strings.Contains(sqliteErr.Error(), "fts5:")
}

// isFTS5WordRune reports whether r is a character FTS5's configured
// tokenizer (porter unicode61 — see db/schema.sql's documents_fts
// definition) treats as part of an ordinary token rather than a
// separator, and that FTS5's own bare-query grammar never assigns a
// syntactic meaning to. Deliberately conservative: a Unicode letter,
// digit, underscore, or combining mark qualifies; anything else —
// including '-', ':', '(', ')', '*', quotes — is routed through
// sanitizeFTS5Query's quoting branch instead of being trusted to mean
// only what it looks like it means.
//
// Combining marks (unicode.IsMark — category M: Mn/Mc/Me, e.g. a bare
// combining acute accent, U+0301) are included alongside letters
// specifically so composed and decomposed forms of the same visible
// text are treated identically by this classifier. A precomposed
// character like "é" (U+00E9, one codepoint) satisfies
// unicode.IsLetter on its own; its NFD-decomposed equivalent ("e" +
// U+0301 combining acute) does not — U+0301 alone is category Mn, not
// a letter — so before this fix a decomposed word was routed through
// the quoting branch while its composed equivalent was not, even
// though both are the same visible text and, empirically, produce the
// same match set from FTS5 either way (a task-20260821-03 step 2a
// finding: quoting a single-token bareword changes nothing about what
// it matches, confirmed against a real FTS5 index — see
// search_test.go's TestSanitizeFTS5Query_ComposedAndDecomposedTreatedIdentically
// and its sibling integration test). Accepting Mn/Mc/Me here removes
// that inconsistency at the source rather than leaving it as a
// documented asymmetry: a lone combining mark with no preceding base
// character (a malformed or adversarial input) was also confirmed
// safe to pass through bare — it is not one of the ASCII bytes this
// package's bare-query grammar assigns any syntax to, so it can never
// be misparsed as an operator, and it simply matches nothing.
func isFTS5WordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_'
}

// isAllFTS5WordRunes reports whether every rune in s satisfies
// isFTS5WordRune, and s is non-empty (an empty string is never
// "safe" — it has nothing for a caller to have intended literally).
func isAllFTS5WordRunes(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isFTS5WordRune(r) {
			return false
		}
	}
	return true
}

// isASCIISpace reports whether b is one of the whitespace bytes
// sanitizeFTS5Query splits bareword tokens on. Matching
// unicode.IsSpace's ASCII subset is sufficient here: '"' and every
// byte this function treats as space are single-byte ASCII code
// points that can never appear as a continuation byte of a multi-byte
// UTF-8 sequence, so scanning raw's bytes for them (rather than
// decoding runes) never mis-splits a multi-byte character.
func isASCIISpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	}
	return false
}

// escapeFTS5Bareword returns token unchanged if it is safe to send to
// FTS5 as a bare (unquoted) term — every rune a word rune, or every
// rune but a single trailing '*' prefix marker a word rune — and
// otherwise wraps it in '"' so FTS5 treats it as a literal phrase
// instead of parsing its punctuation as query syntax. token is
// guaranteed by its only caller (sanitizeFTS5Query) to contain no
// whitespace, but — unlike an earlier version of this function — is
// NOT guaranteed to contain no '"': sanitizeFTS5Query's bareword scan
// treats a '"' as ordinary token content whenever it is not the very
// first character of a whitespace-delimited word (only a leading '"'
// opens an explicit phrase; see that function's docblock), so a
// caller-typed word like `abc"def"ghi` (no surrounding whitespace)
// reaches this function as one token containing two literal '"'
// characters.
//
// Wrapping such a token therefore doubles every '"' it contains
// before adding the enclosing pair — FTS5's own convention for a
// literal double-quote inside a phrase, the same doubling SQL string
// literals use. This was verified empirically, not assumed (a
// task-20260821-03 step 2a finding): against a real FTS5 index,
// `"abc""def""ghi"` (doubled) matches only a document containing the
// literal adjacent text `abc"def"ghi`, while the un-escaped
// concatenation `"abc"def"ghi"` a naive fix would produce instead
// degrades into three implicit-AND fragments (`"abc"`, `def`, `"ghi"`)
// and also matches a document that merely contains all three words
// scattered far apart — a real correctness defect (false positives),
// not merely a cosmetic one, and the reason this function escapes
// rather than concatenates. See search_test.go's
// TestSanitizeFTS5Query_EmbeddedQuotesEscapedAsOnePhrase and its
// sibling integration test.
func escapeFTS5Bareword(token string) string {
	if body, ok := strings.CutSuffix(token, "*"); ok && isAllFTS5WordRunes(body) {
		return token
	}
	if isAllFTS5WordRunes(token) {
		return token
	}
	escaped := strings.ReplaceAll(token, `"`, `""`)
	var b strings.Builder
	b.Grow(len(escaped) + 2)
	b.WriteByte('"')
	b.WriteString(escaped)
	b.WriteByte('"')
	return b.String()
}

// sanitizeFTS5Query rewrites raw — a trimmed, non-empty caller search
// string — into FTS5 MATCH syntax that cannot be misparsed the way
// described in this file's package docblock. It returns ok=false only
// when raw contains an unbalanced '"' immediately at a token boundary
// (see Algorithm below), which no rewrite can safely repair; the
// caller maps that to the same 400 documented for malformed FTS5
// syntax.
//
// Algorithm: a single left-to-right scan splits raw into
// whitespace-delimited words. A '"' is treated as opening an explicit
// phrase — running verbatim (byte-for-byte, no re-escaping) to its
// closing '"', which may be many words further on — ONLY when it is
// the very first character scanned at a token boundary (string start,
// or immediately after whitespace or a previously-closed phrase).
// Everything else is a bareword: it runs to the next whitespace
// WITHOUT stopping early at an embedded '"', and is passed through
// escapeFTS5Bareword, which quotes it (doubling any '"' it contains)
// if it is not made entirely of safe runes. This last point is a
// step 2a correction: an earlier version of this scan stopped a
// bareword at its first embedded '"' and re-entered the loop there,
// misreading that '"' as a fresh phrase-open — splitting one
// whitespace-delimited word like `abc"def"ghi` into three
// implicit-AND tokens instead of the one literal phrase
// docs/api-contract.md documents, and (for an odd embedded-quote
// count) sometimes rejecting the query outright as unbalanced. Since
// a '"' with no whitespace on either side was never something the
// caller could have intended as a phrase boundary, treating it as
// ordinary word content — literal by construction, once escaped — is
// what makes every count of embedded '"' in a single word behave
// alike, rather than the old scan's behaviour depending on whether
// that count happened to be even or odd. Tokens are rejoined with a
// single space, which is FTS5's own implicit-AND separator — so a
// query untouched by escaping (no punctuation, no pre-existing
// phrase) comes out byte-for-byte identical to what was sent in,
// which is what keeps this rewrite from changing the result of any
// query that worked before it existed.
func sanitizeFTS5Query(raw string) (rewritten string, ok bool) {
	var out strings.Builder
	out.Grow(len(raw))

	n := len(raw)
	i := 0
	wroteToken := false

	writeSeparator := func() {
		if wroteToken {
			out.WriteByte(' ')
		}
		wroteToken = true
	}

	for i < n {
		switch {
		case isASCIISpace(raw[i]):
			i++

		case raw[i] == '"':
			j := i + 1
			for j < n && raw[j] != '"' {
				j++
			}
			if j >= n {
				return "", false
			}
			writeSeparator()
			out.WriteString(raw[i : j+1])
			i = j + 1

		default:
			// A bareword runs to the next whitespace only — it does
			// NOT stop early at an embedded '"'. Only a '"' reached
			// at a true token boundary (this switch's other case)
			// opens a phrase; one appearing mid-word is ordinary
			// content that escapeFTS5Bareword will escape (double)
			// when it wraps the word. See the function docblock's
			// "step 2a correction" paragraph for why.
			j := i
			for j < n && !isASCIISpace(raw[j]) {
				j++
			}
			writeSeparator()
			out.WriteString(escapeFTS5Bareword(raw[i:j]))
			i = j
		}
	}

	return out.String(), true
}
