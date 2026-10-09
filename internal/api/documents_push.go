// Package api — document push (docs/plans/NETWORK-DEPLOYMENT-PLAN.md §4).
//
// A memory tree lives on the harness machine. When memoryd runs elsewhere it
// cannot read the files, so a client sends them:
//
//	POST /v1/documents/push    Markdown files and agent definitions, by content
//	GET  /v1/documents/hashes  source_path → sha256 for one harness, to send only changes
//	POST /v1/documents/delete  remove documents by source_path
//
// scripts/push-memory.py and the reconciler drive these. A pushed file is
// imported exactly as a local sync would import it (importer.ImportMarkdown).
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/importer"
)

const (
	// maxPushBody caps one push request; the client splits larger trees.
	maxPushBody = 8 << 20
	// maxPushFile caps one file's content.
	maxPushFile = 1 << 20
	// maxPushItems caps files plus agents in one request.
	maxPushItems = 500
)

type pushFile struct {
	// Path is the file's absolute path on the client: the document's key,
	// so rows match those a local sync of the same file made.
	Path string `json:"path" binding:"required,max=1024"`
	// RelPath is the path relative to the memory root; it decides kind and
	// mission id. Required for files, unused for agents.
	RelPath string `json:"rel_path" binding:"max=512"`
	Content string `json:"content"`
}

type pushRequest struct {
	Harness string     `json:"harness" binding:"required,max=128"`
	Files   []pushFile `json:"files"`
	Agents  []pushFile `json:"agents"`
}

// pushDocumentsHandler handles POST /v1/documents/push.
func pushDocumentsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPushBody)
		var request pushRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request too large", "details": "send at most 8 MiB per push"})
				return
			}
			validationError(c, err)
			return
		}
		harness := strings.TrimSpace(request.Harness)
		if len(request.Files)+len(request.Agents) > maxPushItems {
			validationError(c, errors.New("send at most 500 files and agents per push"))
			return
		}
		for _, f := range append(append([]pushFile{}, request.Files...), request.Agents...) {
			if len(f.Content) > maxPushFile {
				validationError(c, errors.New("a file is larger than 1 MiB: "+f.Path))
				return
			}
			if !strings.HasSuffix(strings.ToLower(f.Path), ".md") {
				validationError(c, errors.New("only Markdown (.md) files can be pushed: "+f.Path))
				return
			}
		}
		for _, f := range request.Files {
			if !validRelPath(f.RelPath) {
				validationError(c, errors.New("rel_path must be a relative path inside the memory root, with no hidden or '..' segments: "+f.RelPath))
				return
			}
		}

		imported := 0
		for _, f := range request.Files {
			if err := importer.ImportMarkdown(db, f.Path, f.RelPath, harness, []byte(f.Content)); err != nil {
				internalError(c, err)
				return
			}
			imported++
		}
		agents := 0
		for _, a := range request.Agents {
			if err := importer.ImportAgent(db, a.Path, []byte(a.Content)); err != nil {
				validationError(c, err)
				return
			}
			agents++
		}
		c.JSON(http.StatusOK, gin.H{"harness": harness, "files": imported, "agents": agents})
	}
}

// validRelPath accepts a clean, relative, forward-slash path with no hidden
// or parent segments — what the walker would produce for a file under root.
func validRelPath(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") || path.Clean(rel) != rel {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == ".." || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

// documentHashesHandler handles GET /v1/documents/hashes?harness=<name>.
func documentHashesHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		harness := strings.TrimSpace(c.Query("harness"))
		if harness == "" {
			validationError(c, errors.New("harness is required"))
			return
		}
		rows, err := db.QueryContext(c.Request.Context(),
			`SELECT source_path, sha256 FROM documents WHERE harness = ?`, harness)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()
		hashes := map[string]string{}
		for rows.Next() {
			var p, sha string
			if err := rows.Scan(&p, &sha); err != nil {
				internalError(c, err)
				return
			}
			hashes[p] = sha
		}
		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"harness": harness, "hashes": hashes})
	}
}

// deleteDocumentsHandler handles POST /v1/documents/delete {"paths": [...]}:
// each document whose source_path is listed (and, for a JSONL file, each of
// its lines) is removed with its search row.
func deleteDocumentsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request struct {
			Paths []string `json:"paths" binding:"required,max=5000"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}
		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()
		deleted := 0
		for _, p := range request.Paths {
			rows, err := tx.QueryContext(c.Request.Context(),
				`SELECT id FROM documents WHERE source_path = ? OR source_path LIKE ? ESCAPE '\'`,
				p, escapeLike(p)+":%")
			if err != nil {
				internalError(c, err)
				return
			}
			var ids []int64
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					internalError(c, err)
					return
				}
				ids = append(ids, id)
			}
			rows.Close()
			for _, id := range ids {
				if _, err := tx.ExecContext(c.Request.Context(), `DELETE FROM documents_fts WHERE rowid = ?`, id); err != nil {
					internalError(c, err)
					return
				}
				if _, err := tx.ExecContext(c.Request.Context(), `DELETE FROM documents WHERE id = ?`, id); err != nil {
					internalError(c, err)
					return
				}
				deleted++
			}
		}
		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": deleted})
	}
}

// escapeLike escapes LIKE's wildcards in s, for use with ESCAPE '\'.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
