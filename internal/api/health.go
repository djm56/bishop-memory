package api

import (
	"database/sql"
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

		// Check that the expected schema is present.
		var missing []string
		for _, obj := range expectedSchemaObjects {
			var count int
			err := db.QueryRowContext(
				c.Request.Context(),
				`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`,
				obj,
			).Scan(&count)
			if err != nil || count == 0 {
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
