package api

import (
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"
)

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

		c.JSON(http.StatusOK, gin.H{
			"ok":      true,
			"service": "bishop-memory",
			"storage": "sqlite",
		})
	}
}
