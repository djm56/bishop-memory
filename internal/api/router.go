package api

import (
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/config"
	"bishop-memory/internal/middleware"
)

func NewRouter(cfg config.Config, db *sql.DB) *gin.Engine {
	if cfg.AppEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()

	router.Use(
		middleware.RequestID(),
		middleware.AccessLog(),
		middleware.Recovery(),
	)

	router.GET("/healthz", healthHandler(db))

	v1 := router.Group("/v1")
	{
		missions := v1.Group("/missions")
		{
			missions.GET("", listMissionsHandler(db))
			missions.POST("", createMissionHandler(db))
			missions.GET("/:missionID", getMissionHandler(db))
			missions.PATCH("/:missionID", updateMissionHandler(db))
			missions.POST("/:missionID/steps", createMissionStepHandler(db))
		}

		v1.POST("/flight-recorder", appendFlightRecorderHandler(db))
		v1.GET("/flight-recorder", listFlightRecorderHandler(db))

		memory := v1.Group("/memory")
		{
			memory.GET("/search", searchMemoryHandler(db))
		}

		v1.POST("/documents/sync", syncDocumentsHandler(db))
	}

	router.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "route not found",
		})
	})

	return router
}
