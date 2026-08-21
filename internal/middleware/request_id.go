package middleware

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

const RequestIDHeader = "X-Request-ID"

// isSafeRequestID reports whether id is safe to echo back on the
// response header and, more importantly, to write unescaped into a
// log.Printf line via access_log.go / errors.go's "%v" formatting.
//
// Step 4 review fix (Review B, WARNING W4): net/http's Header.Write
// already strips CR/LF before writing the wire header, so the
// response-header echo was never at risk — but log.Printf has no such
// protection, so a caller-supplied "X-Request-ID: abc\nrequest_id=fake
// method=DELETE ..." would land in the access log as a forged second
// line. Restricting the accepted charset to the same one our own
// newRequestID() generates (lowercase hex) closes that off entirely,
// independent of whatever the log formatter does.
func isSafeRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader(RequestIDHeader)

		if !isSafeRequestID(requestID) {
			requestID = newRequestID()
		}

		c.Set("request_id", requestID)
		c.Header(RequestIDHeader, requestID)
		c.Next()
	}
}

func newRequestID() string {
	bytes := make([]byte, 12)

	if _, err := rand.Read(bytes); err != nil {
		return "request-id-unavailable"
	}

	return hex.EncodeToString(bytes)
}
