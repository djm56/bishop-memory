// Package middleware — RequestID, AccessLog, Recovery.
package middleware

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Recovery catches a panic in any downstream handler and turns it into
// the SAME JSON error shape every other internal fault uses
// ({"error":"internal server error"}, per docs/api-contract.md), tagged
// with the request's correlation id in the server log.
//
// Step 4 review fix (Review B, CRITICAL C1): the stock gin.Recovery()
// calls c.AbortWithStatus, which writes only the status line — zero
// bytes of body — breaking the one contract every other error path in
// this API honours. gin.CustomRecoveryWithWriter lets us supply our own
// handler while keeping gin's own panic/stack-trace capture (it still
// writes the recovered value + stack to the writer we pass it, so the
// operator does not lose that diagnostic).
//
// We can't call the api package's internalError directly (importing
// "bishop-memory/internal/api" from middleware would be a cycle: api
// imports middleware, not the reverse). Instead we duplicate exactly
// the same request_id lookup + log line + JSON body shape here, so a
// panic's log line and its response body are indistinguishable from
// any other internal fault's — which is the actual requirement, not
// literal code reuse.
func Recovery() gin.HandlerFunc {
	// gin.DefaultErrorWriter (stderr) still receives the panic value +
	// stack trace via gin's own recovery machinery, so that diagnostic
	// is not lost — we only replace what gets WRITTEN TO THE CLIENT.
	return gin.CustomRecoveryWithWriter(gin.DefaultErrorWriter, func(c *gin.Context, recovered any) {
		requestID, _ := c.Get("request_id")
		log.Printf("request_id=%v error=panic: %v", requestID, recovered)

		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"error": "internal server error",
		})
	})
}
