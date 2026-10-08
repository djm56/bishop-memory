package api

import (
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/config"
	"bishop-memory/internal/middleware"
	"bishop-memory/internal/ui"
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

	// The operator's review page. A single embedded HTML file that talks to
	// the /v1 triage routes with fetch; loopback-only like everything else.
	router.GET("/triage", ui.TriagePageHandler())
	router.GET("/missions", ui.MissionsPageHandler())
	router.GET("/", func(c *gin.Context) { c.Redirect(http.StatusFound, "/missions") })
	router.GET("/favicon.svg", ui.FaviconSVGHandler())
	router.GET("/favicon.ico", ui.FaviconICOHandler())

	v1 := router.Group("/v1")
	{
		v1.GET("/hud/missions", hudMissionsHandler(db))
		v1.GET("/hud/missions/:missionID", hudMissionHandler(db))

		missions := v1.Group("/missions")
		{
			missions.GET("", listMissionsHandler(db))
			missions.POST("", createMissionHandler(db))
			// Allocation must be registered before the "/:missionID"
			// routes below only for readability — gin's tree gives a
			// static segment priority over a wildcard regardless of
			// registration order, so /v1/missions/allocate cannot be
			// swallowed by /v1/missions/:missionID.
			missions.POST("/allocate", allocateMissionHandler(db))
			missions.GET("/:missionID", getMissionHandler(db))
			missions.PATCH("/:missionID", updateMissionHandler(db))
			missions.GET("/:missionID/steps", listMissionStepsHandler(db))
			missions.POST("/:missionID/steps", createMissionStepHandler(db))
		}

		v1.POST("/flight-recorder", appendFlightRecorderHandler(db))
		v1.GET("/flight-recorder", listFlightRecorderHandler(db))

		// Knowledge surface: findings, patterns, service records, and directives.
		v1.GET("/findings", listFindingsHandler(db))
		v1.POST("/findings", createFindingHandler(db))
		// Operator-only: the single write path for findings.status. Not
		// registered in any mcpd profile.
		v1.POST("/findings/:findingID/decision", decideFindingHandler(db))
		v1.PUT("/findings/:findingID/harness", setFindingHarnessHandler(db))

		v1.GET("/patterns", listPatternsHandler(db))
		v1.POST("/patterns", createPatternHandler(db))

		v1.GET("/service-records", listServiceRecordsHandler(db))
		v1.POST("/service-records", createServiceRecordHandler(db))

		v1.GET("/directives", listDirectivesHandler(db))
		// Operator-side mirror of DIRECTIVES.md (reconciler). No mcpd profile
		// registers it — agents still have no mechanism to write a directive.
		v1.PUT("/directives/:directiveID", upsertDirectiveHandler(db))

		// Findings triage (docs/FINDINGS-TRIAGE.md). The triage agents reach
		// these through mcpd's triage profile; the operator through the review
		// page at /triage and the scripts.
		v1.GET("/finding-categories", listFindingCategoriesHandler(db))
		v1.PUT("/finding-categories/:slug", upsertFindingCategoryHandler(db))
		v1.GET("/finding-groups", listFindingGroupsHandler(db))
		v1.POST("/finding-groups", createFindingGroupHandler(db))
		v1.POST("/finding-recommendations", recommendFindingsHandler(db))
		v1.GET("/directive-proposals", listDirectiveProposalsHandler(db))
		v1.POST("/directive-proposals", createDirectiveProposalHandler(db))
		v1.POST("/directive-proposals/:proposalID/decision", decideDirectiveProposalHandler(db))
		v1.GET("/harnesses", listHarnessesHandler(db))
		v1.PUT("/harnesses/:name", upsertHarnessHandler(db))
		triage := v1.Group("/triage")
		{
			triage.GET("/pending", pendingTriageHandler(db))
			triage.GET("/decisions", recentDecisionsHandler(db))
			triage.GET("/next-category", nextCategoryHandler(db))
			triage.PUT("/classifications", classifyFindingsHandler(db))
			triage.GET("/runs", listTriageRunsHandler(db))
			triage.POST("/runs", startTriageRunHandler(db))
			triage.PATCH("/runs/:runID", finishTriageRunHandler(db))
		}

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
