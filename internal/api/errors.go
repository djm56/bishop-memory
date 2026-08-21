// Package api — shared error-response helpers for every handler.
//
// validationError and internalError are the single funnel every handler
// in this package uses to shape a non-2xx JSON body, per
// docs/api-contract.md's "Standard error format". Recovery (see
// internal/middleware/recovery.go) also routes panics through
// internalError so a handler crash produces the exact same body shape
// as every other internal fault, rather than an empty 500.
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// validationError writes the standard 400 body for a request the
// caller can fix. The `details` field is built so that it (a) never
// echoes a raw request-derived scalar back to the caller (CONV-033),
// while (b) preserving every hand-authored, already-safe diagnostic
// message this package's handlers construct themselves (CONV-048 — a
// prior fix here collapsed all three cases below into one generic
// string, discarding case 3 entirely; see docs/api-contract.md's
// "Standard error format" and CONVENTIONS.md CONV-048 for the incident
// this restores):
//
//  1. validator.ValidationErrors (the go-playground/validator failure
//     type returned by gin's ShouldBindJSON for a struct-tag violation,
//     e.g. `oneof`/`required`/`max`) is rendered as a structured list of
//     {field, tag} pairs — go-playground's own fieldError.Error() does
//     not interpolate the submitted value for these tags, but we build
//     the structured form explicitly rather than depend on that being
//     true of every tag forever.
//  2. A JSON syntax error (*json.SyntaxError) or an encoding/json
//     UnmarshalTypeError (*json.UnmarshalTypeError) — whose Error()
//     string embeds the offending literal value or byte, e.g. `json:
//     cannot unmarshal number -5 into Go struct field ... of type
//     string` — is reported with a single static, authored message and
//     no library-derived text at all. This is the ONLY branch CONV-033
//     applies to; it must not be widened to catch case 3 below.
//  3. Everything else — every hand-authored errors.New/fmt.Errorf call
//     a handler passes to this function directly (e.g. "q is
//     required", "status must be one of: ..."), plus any other bind
//     error gin's ShouldBindJSON can return that is neither of the
//     above (e.g. io.EOF/io.ErrUnexpectedEOF for an empty or truncated
//     body) — passes its own Error() text through as `details`. This
//     branch is safe ONLY when the caller's message is fully static —
//     a literal string, or one built entirely from other static
//     strings — with no third-party/driver error's own Error() text
//     folded in. io.EOF's text is the static string "EOF", so it is
//     genuinely safe to name here. validationError has no way to tell
//     a fully-static message from one that wraps a non-static error:
//     the discipline is enforced at the CALL SITE, not here. A caller
//     that does `fmt.Errorf("...: %v", driverErr)` and passes the
//     result to this function reopens a CONV-033 leak the moment the
//     wrapped error's own text is request-derived — this is exactly
//     what happened at internal/api/search.go's two FTS5 call sites
//     (fixed by making those sites construct a fully static message
//     instead of wrapping the modernc.org/sqlite driver error, whose
//     text embeds a fragment of the caller's query). Collapsing every
//     hand-authored message into one generic string, on the other
//     hand, is the CONV-048 defect this branch exists to avoid
//     reintroducing — the fix for a non-static wrapped message belongs
//     at the call site, never by widening this branch back into a
//     single collapsed string.
func validationError(c *gin.Context, err error) {
	var verrs validator.ValidationErrors
	if errors.As(err, &verrs) {
		fields := make([]gin.H, 0, len(verrs))
		for _, fe := range verrs {
			fields = append(fields, gin.H{
				"field": fe.Field(),
				"tag":   fe.Tag(),
			})
		}
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid request",
			"details": fields,
		})
		return
	}

	var unmarshalTypeErr *json.UnmarshalTypeError
	var syntaxErr *json.SyntaxError
	if errors.As(err, &unmarshalTypeErr) || errors.As(err, &syntaxErr) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid request",
			"details": "request body failed validation",
		})
		return
	}

	c.JSON(http.StatusBadRequest, gin.H{
		"error":   "invalid request",
		"details": err.Error(),
	})
}

// internalError writes the standard 500 body and logs the real error
// server-side, tagged with the request's correlation id. No internal
// detail (SQL text, file path, driver message) ever reaches the
// response body — only the static "internal server error" string.
func internalError(c *gin.Context, err error) {
	log.Printf("request_id=%v error=%v", requestID(c), err)

	c.JSON(http.StatusInternalServerError, gin.H{
		"error": "internal server error",
	})
}

func requestID(c *gin.Context) string {
	value, exists := c.Get("request_id")
	if !exists {
		return ""
	}

	requestID, _ := value.(string)
	return requestID
}
