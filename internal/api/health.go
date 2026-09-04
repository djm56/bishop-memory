package api

import (
	"database/sql"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// expectedSchemaObjects is the list of expected tables and virtual tables
// that comprise the Phase 3 harness vocabulary schema. This list is the
// single source of truth for schema readiness checks.
//
// This check exists because during step 5, db/schema.sql was reverted, ApplySchema
// created the legacy tables, and every mission route returned 500 — but /healthz
// returned 200 {"ok":true} because the database was reachable. Naming the missing
// objects is the point — a bare failure would have told us the service was
// unhealthy but not that the schema had been reverted.
var expectedSchemaObjects = []string{
	"missions",
	"mission_steps",
	"flight_recorder",
	"crew",
	"findings",
	"patterns",
	"service_records",
	"directives",
	"documents",
	"documents_fts",
}

func healthHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := db.PingContext(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"ok":      false,
				"storage": "sqlite",
				"error":   "database unavailable",
			})
			return
		}

		// Check that the expected schema is present with a single query.
		// Query all objects and compute missing in Go rather than issuing
		// one query per object. Distinguish a real query failure (logged
		// server-side) from missing tables.
		rows, err := db.QueryContext(
			c.Request.Context(),
			`SELECT name FROM sqlite_master WHERE type IN ('table', 'view')`,
		)
		if err != nil {
			// Real query failure — log server-side the way syncDocumentsHandler does.
			// A schema check that cannot read the schema is a readiness failure (503),
			// not an internal error (500).
			log.Printf("request_id=%v error=%v", requestID(c), err)
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"ok":      false,
				"storage": "sqlite",
				"error":   "database unavailable",
			})
			return
		}
		defer rows.Close()

		present := make(map[string]bool)
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				log.Printf("request_id=%v error=%v", requestID(c), err)
				c.JSON(http.StatusServiceUnavailable, gin.H{
					"ok":      false,
					"storage": "sqlite",
					"error":   "database unavailable",
				})
				return
			}
			present[name] = true
		}
		if err := rows.Err(); err != nil {
			log.Printf("request_id=%v error=%v", requestID(c), err)
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"ok":      false,
				"storage": "sqlite",
				"error":   "database unavailable",
			})
			return
		}

		// Compute missing set
		var missing []string
		for _, obj := range expectedSchemaObjects {
			if !present[obj] {
				missing = append(missing, obj)
			}
		}

		if len(missing) > 0 {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"ok":      false,
				"storage": "sqlite",
				"error":   "schema incomplete",
				"missing": missing,
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"ok":      true,
			"service": "bishop-memory",
			"storage": "sqlite",
			"schema":  "ok",
		})
	}
}
