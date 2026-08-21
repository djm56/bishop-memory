package main

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestJoinPath_DotSegmentCollapsesToListRoute reproduces the exact
// defect Review C's CRITICAL 1 reported: url.PathEscape does not
// escape ".", and url.URL.JoinPath lexically cleans "." / ".."
// segments out of the joined path AFTER escaping — so an id of "."
// collapses "v1/tasks/." down to "v1/tasks" (the list route), and ".."
// collapses "v1/tasks/.." down to "v1" (no route). This test documents
// the underlying stdlib behaviour the isDotSegment guard exists to
// intercept; it does not exercise mcpd's own code.
func TestJoinPath_DotSegmentCollapsesToListRoute(t *testing.T) {
	cases := []struct {
		id       string
		wantPath string
	}{
		{id: ".", wantPath: "v1/tasks"},
		{id: "..", wantPath: "v1"},
	}

	for _, tc := range cases {
		u, err := url.Parse("http://127.0.0.1:8787")
		if err != nil {
			t.Fatalf("parse base URL: %v", err)
		}
		escaped := url.PathEscape(tc.id)
		u = u.JoinPath("v1", "tasks", escaped)

		if u.Path != tc.wantPath {
			t.Fatalf("id=%q: escaped=%q joined path=%q, want %q (stdlib behaviour changed — re-verify isDotSegment is still the correct fix)", tc.id, escaped, u.Path, tc.wantPath)
		}
	}
}

// TestTaskGetHandler_RejectsDotSegment is the Step 4 brief's required
// observation for CRITICAL 1: exercise the composed URL path (via the
// actual handler, not just isDotSegment in isolation) and confirm it no
// longer resolves to the list route. We assert the handler returns an
// error result WITHOUT reaching the HTTP layer — the client's baseURL
// points at a closed port, so if the guard did not fire first, the call
// would fail with a transport error instead, not the specific "must not
// be '.'..." message. That distinguishes "the guard fired" from
// "the request merely failed for some other reason."
func TestTaskGetHandler_RejectsDotSegment(t *testing.T) {
	c := &client{
		httpClient: nil, // deliberately nil: a nil client would panic on
		// any attempted HTTP call, so this test also fails loudly if the
		// guard is ever removed and the handler tries to proceed to c.do.
		baseURL: "http://127.0.0.1:1", // port 1 is not listening
		harness: "test",
	}

	handler := makeTaskGetHandler(c)

	for _, id := range []string{".", ".."} {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{"id": id},
			},
		}

		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("id=%q: handler returned a Go error %v (should return an MCP tool-error result instead, never a Go error)", id, err)
		}
		if !result.IsError {
			t.Fatalf("id=%q: result.IsError = false, want true (dot-segment must be rejected before it reaches url.JoinPath)", id)
		}

		text := resultText(t, result)
		if !strings.Contains(text, "must not be") {
			t.Fatalf("id=%q: error text %q does not mention the dot-segment rejection", id, text)
		}
	}
}

// resultText extracts the text of the first TextContent block in an
// MCP CallToolResult, failing the test if the shape is unexpected.
func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("result has no content blocks")
	}
	tc, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("result.Content[0] is %T, want mcp.TextContent", result.Content[0])
	}
	return tc.Text
}
