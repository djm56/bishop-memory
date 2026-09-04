package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestValidationError_PreservesHandAuthoredMessages is the regression
// test for the CONV-048 defect found in Step 5's review of Step 4's
// CONV-033 fix (docs/api-contract.md is silent on the exact literal
// text handlers construct, but every call site below is a real
// errors.New/fmt.Errorf a handler in this package passes to
// validationError, and each one previously reached the caller verbatim
// before the over-corrected fallback branch collapsed all of them into
// one generic string). None of these embed a request-derived scalar —
// they are static, authored text — so CONV-033 does not apply to them,
// and validationError's third branch (see its docblock) MUST pass them
// through unchanged.
//
// The "search.go FTS5 syntax wrap" case below is updated as of
// task-20260821-02 step 7a: it used to name the literal
// `"invalid search query: fts5: syntax error near \"*\""` — a message
// that WRAPPED the real modernc.org/sqlite driver error and, as step
// 7a's review found, could echo a fragment of the caller's own query.
// search.go no longer constructs that wrapped literal; it now passes
// validationError a fully static `"invalid search query syntax"`
// string with no driver text folded in — see
// TestSearchMemoryHandler_FTSSyntaxErrorDoesNotEchoQuery in
// search_test.go for the empirical, real-FTS5-driver regression test
// for that fix. This case exists only to confirm validationError's
// third branch still passes THIS static string through unchanged.
func TestValidationError_PreservesHandAuthoredMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"missions.go status filter", &staticErr{"status must be one of: not-started, in-progress, blocked, complete"}, "status must be one of: not-started, in-progress, blocked, complete"},
		{"search.go q required", &staticErr{"q is required"}, "q is required"},
		{"search.go FTS5 syntax error (static, post-step-7a)", &staticErr{"invalid search query syntax"}, "invalid search query syntax"},
		{"events.go event_type empty", &staticErr{"event_type must not be empty or whitespace-only"}, "event_type must not be empty or whitespace-only"},
		{"events.go summary empty", &staticErr{"summary must not be empty or whitespace-only"}, "summary must not be empty or whitespace-only"},
		{"flight_recorder.go root is filesystem root", &staticErr{"root must not be the filesystem root"}, "root must not be the filesystem root"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			details := decodeValidationDetails(t, tc.err)
			got, ok := details.(string)
			if !ok || got != tc.want {
				t.Fatalf("details = %#v, want %q", details, tc.want)
			}
		})
	}
}

// TestValidationError_StaticMessageForJSONTypeMismatch confirms the
// CONV-033 branch itself is unchanged by the CONV-048 fix: a genuine
// *json.UnmarshalTypeError or *json.SyntaxError from a real
// c.ShouldBindJSON call — which is the only case whose Error() text can
// carry a raw request-supplied literal — still gets the static message,
// never the library's own text.
func TestValidationError_StaticMessageForJSONTypeMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	type target struct {
		Title string `json:"title"`
	}

	cases := []struct {
		name string
		body string
	}{
		{"UnmarshalTypeError (number into string field)", `{"title": -5}`},
		{"SyntaxError (malformed JSON)", `{not valid json`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			c.Request = req

			var tgt target
			bindErr := c.ShouldBindJSON(&tgt)
			if bindErr == nil {
				t.Fatal("expected a bind error, got nil")
			}

			details := decodeValidationDetails(t, bindErr)
			if details != "request body failed validation" {
				t.Fatalf("details = %#v, want the static message (bindErr was %v)", details, bindErr)
			}
		})
	}
}

// staticErr is a minimal error type for exercising validationError's
// fallback branch with text that must pass through unchanged — it is
// deliberately NOT errors.New so this test cannot accidentally rely on
// errors.New's own type ever satisfying errors.As(&someOtherType{}).
type staticErr struct{ msg string }

func (e *staticErr) Error() string { return e.msg }

// decodeValidationDetails calls validationError with err and returns the
// decoded `details` field of the JSON body it writes.
func decodeValidationDetails(t *testing.T, err error) any {
	t.Helper()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/x", nil)

	validationError(c, err)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	var body map[string]any
	if decodeErr := json.Unmarshal(w.Body.Bytes(), &body); decodeErr != nil {
		t.Fatalf("response body is not valid JSON: %v (raw: %s)", decodeErr, w.Body.String())
	}

	return body["details"]
}
