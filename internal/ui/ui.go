// Package ui serves the operator's findings-triage review page.
//
// The page is one embedded HTML file with inline CSS and JavaScript. It holds
// no state of its own: everything it shows comes from the /v1 triage routes,
// and every button is a fetch against a /v1 decision route. Keeping it inside
// memoryd means it is reachable wherever the API is (loopback, or through the
// same SSH tunnel) with nothing extra to install.
package ui

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed triage.html
var triagePage []byte

// TriagePageHandler serves GET /triage.
func TriagePageHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", triagePage)
	}
}
