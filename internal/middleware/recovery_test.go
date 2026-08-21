package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestRecovery_PanicReturnsJSONBody is the observation Step 4's brief
// requires for CRITICAL C1 (Review B): a panicking handler must still
// return the documented {"error": "..."} JSON body, not an empty 500.
// This test would have failed against the pre-fix gin.Recovery() (an
// empty body decodes to a json.SyntaxError, not the expected map), so
// it is a real regression guard, not a check that could not fail.
func TestRecovery_PanicReturnsJSONBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(Recovery())
	router.GET("/boom", func(c *gin.Context) {
		panic("simulated handler panic")
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	body := rec.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("response body is empty — panic recovery did not write the documented JSON error shape")
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("response body is not valid JSON (%v): %q", err, body)
	}

	if decoded["error"] != "internal server error" {
		t.Fatalf(`decoded["error"] = %v, want "internal server error"`, decoded["error"])
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want application/json; charset=utf-8", ct)
	}
}
