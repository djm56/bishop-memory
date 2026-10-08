// Package ui serves the operator's pages — the findings-triage review page at
// /triage and the mission HUD at /missions — and their icon.
//
// The page is one embedded HTML file with inline CSS and JavaScript. It holds
// no state of its own: everything it shows comes from the /v1 triage routes,
// and every button is a fetch against a /v1 decision route. Keeping it inside
// memoryd means it is reachable wherever the API is (loopback, or through the
// same SSH tunnel) with nothing extra to install.
//
// The icon is a "B" in the page's accent colour. favicon.svg is what modern
// browsers use; favicon.png is a 32x32 rendering of the same SVG, served at
// /favicon.ico for browsers that do not use SVG icons and for the automatic
// /favicon.ico request a browser makes, which otherwise logs a 404.
package ui

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed triage.html
var triagePage []byte

//go:embed missions.html
var missionsPage []byte

//go:embed favicon.svg
var faviconSVG []byte

//go:embed favicon.png
var faviconPNG []byte

// iconCacheControl lets a browser keep the icon for a day. The icon changes
// only with a new build, and a stale icon for a day is harmless.
const iconCacheControl = "public, max-age=86400"

// TriagePageHandler serves GET /triage.
func TriagePageHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", triagePage)
	}
}

// MissionsPageHandler serves GET /missions.
func MissionsPageHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", missionsPage)
	}
}

// FaviconSVGHandler serves GET /favicon.svg.
func FaviconSVGHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", iconCacheControl)
		c.Data(http.StatusOK, "image/svg+xml", faviconSVG)
	}
}

// FaviconICOHandler serves GET /favicon.ico. The body is a PNG: every browser
// that requests /favicon.ico accepts PNG content there when the Content-Type
// says so, which avoids carrying a separate ICO encoder or file format.
func FaviconICOHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", iconCacheControl)
		c.Data(http.StatusOK, "image/png", faviconPNG)
	}
}
