// Package api — HTTP handler for /v1/memory/search.
//
// searchMemoryHandler runs an FTS5 query against imported documents and
// returns ranked results. It is the read-only counterpart to the
// importer triggered by /v1/documents/sync. Until the importer populates
// documents_fts (Step 5), this handler returns an empty results list.
package api

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
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
// Query handling: the raw q value is passed straight to FTS5 MATCH. This
// means FTS5 query syntax applies — multi-word queries are implicit-AND,
// "phrase" is a phrase query, * is a prefix. A bare special char or
// invalid syntax yields a MATCH error surfaced as a 400 with a static,
// authored "invalid search query syntax" details string — NEVER the raw
// modernc.org/sqlite driver error text, which embeds a fragment of the
// caller's own query (confirmed empirically: a query like "hello AND OR"
// produces the driver error `fts5: syntax error near "OR"`) and would
// otherwise round-trip caller input back into the response (CONV-033).
// The full driver error is logged server-side instead, so the leak is
// closed without losing operator-side diagnosability. No escaping is
// applied to the query itself because search UX benefits from FTS5's
// native query operators; Step 5 may add sanitization if abuse surfaces.
//
// Join contract: documents_fts is a standalone FTS5 table. The importer
// (Step 5) inserts into documents_fts with rowid = documents.id, so the
// join key documents_fts.rowid = d.id ties each FTS hit back to its
// metadata row. Until the importer runs, documents_fts is empty and
// this returns {"results": [], "q": ...}.
func searchMemoryHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := strings.TrimSpace(c.Query("q"))
		if q == "" {
			validationError(c, errors.New("q is required"))
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

		rows, err := db.QueryContext(c.Request.Context(), query, q)
		if err != nil {
			// FTS5 syntax errors from the user-supplied q are surfaced
			// as a 400; anything else is a 500.
			if isFTSSyntaxErr(err) {
				// Log the real driver error server-side (it is useful
				// for debugging a caller's malformed FTS5 query) but
				// respond with a static, authored message only — see
				// the package/handler docblock above and CONV-033. The
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
			if isFTSSyntaxErr(err) {
				// Log the real driver error server-side (it is useful
				// for debugging a caller's malformed FTS5 query) but
				// respond with a static, authored message only — see
				// the package/handler docblock above and CONV-033. The
				// driver's own Error() text embeds a fragment of q and
				// MUST NOT reach the client.
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

// isFTSSyntaxErr reports whether err is an FTS5 query-syntax error that
// should be surfaced as a 400 rather than a 500. modernc.org/sqlite
// surfaces these as "fts5: syntax error ...".
//
// The substring match is intentionally tight: a bare "syntax error"
// could come from a non-FTS SQL error (e.g., a future migration that
// runs DDL through this code path) and would otherwise be misclassified
// as a user-input 400 instead of a 500. Anchoring on "fts5: " keeps the
// classification precise.
func isFTSSyntaxErr(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "fts5: syntax error")
}
