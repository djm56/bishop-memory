package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/auth"
)

// APIKey requires a valid API key on every request it guards, presented as
// "Authorization: Bearer <key>" or "X-API-Key: <key>". While the store holds
// no key at all, authentication is off and every request passes — unless
// required is set (a non-loopback service), in which case an empty store
// refuses everything, so revoking the last key never opens a network-facing
// service. The key's name is kept on the context as "api_key" for the access
// log.
func APIKey(keys *auth.Store, required bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if (keys == nil || keys.Empty()) && !required {
			c.Next()
			return
		}
		name, ok := "", false
		if keys != nil {
			name, ok = keys.Verify(presentedKey(c.Request))
		}
		if !ok {
			c.Header("WWW-Authenticate", `Bearer realm="bishop-memory"`)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "unauthorized",
				"details": "send a valid API key as Authorization: Bearer <key>",
			})
			return
		}
		c.Set("api_key", name)
		c.Next()
	}
}

// presentedKey returns the key from the Authorization (Bearer) or X-API-Key
// header, or "".
func presentedKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if scheme, token, ok := strings.Cut(h, " "); ok && strings.EqualFold(scheme, "Bearer") {
			return strings.TrimSpace(token)
		}
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}
