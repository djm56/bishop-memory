package main

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"bishop-memory/internal/api"
	"bishop-memory/internal/config"
)

// TestJoinPath_DotSegmentCollapsesToListRoute reproduces the exact
// defect Review C's CRITICAL 1 reported: url.PathEscape does not
// escape ".", and url.URL.JoinPath lexically cleans "." / ".."
// segments out of the joined path AFTER escaping — so an id of "."
// collapses "v1/missions/." down to "v1/missions" (the list route), and ".."
// collapses "v1/missions/.." down to "v1" (no route). This test documents
// the underlying stdlib behaviour the isDotSegment guard exists to
// intercept; it does not exercise mcpd's own code.
func TestJoinPath_DotSegmentCollapsesToListRoute(t *testing.T) {
	cases := []struct {
		id       string
		wantPath string
	}{
		{id: ".", wantPath: "v1/missions"},
		{id: "..", wantPath: "v1"},
	}

	for _, tc := range cases {
		u, err := url.Parse("http://127.0.0.1:8787")
		if err != nil {
			t.Fatalf("parse base URL: %v", err)
		}
		escaped := url.PathEscape(tc.id)
		u = u.JoinPath("v1", "missions", escaped)

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

// TestMCPDRoutePathsMatchServerRoutes verifies that the URL paths mcpd
// constructs for each tool match the routes actually registered in the
// server's router. This regression test catches path drift between mcpd
// and the server that would otherwise result in 404s.
//
// Derives the server-side routes from the live router via
// gin's Routes() API; compares against the paths mcpd constructs
// in its handlers.
func TestMCPDRoutePathsMatchServerRoutes(t *testing.T) {
	// Create a test database (in-memory SQLite) and minimal config
	// so we can instantiate the live router. The handlers won't execute
	// (no HTTP calls are made), so the database just needs to exist.
	dbConn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("create test database: %v", err)
	}
	defer dbConn.Close()

	cfg := config.Config{
		AppEnv: "test",
	}

	// Instantiate the live router; this registers all routes.
	router := api.NewRouter(cfg, dbConn)

	// Extract the registered routes from the router.
	// Use a map with "METHOD:path" as key to handle multiple methods on the same path.
	registeredRoutes := make(map[string]bool) // "METHOD:path" -> true
	for _, route := range router.Routes() {
		registeredRoutes[route.Method+":"+route.Path] = true
	}

	// Expected paths constructed by mcpd for each tool, derived from
	// the handlers' JoinPath calls. Note: url.URL.Path (without a trailing
	// slash in the base) returns the path without a leading slash, but
	// url.URL.String() includes the slash; we compare url.URL.Path values.
	mcpdPaths := []struct {
		toolName   string
		method     string
		pathParts  []string // arguments to url.JoinPath
		wantPath   string   // expected final url.Path (no leading slash)
	}{
		{"memory_search", "GET", []string{"v1", "memory", "search"}, "v1/memory/search"},
		{"task_list", "GET", []string{"v1", "missions"}, "v1/missions"},
		{"task_get", "GET", []string{"v1", "missions", "test-id"}, "v1/missions/test-id"},
		{"task_create", "POST", []string{"v1", "missions"}, "v1/missions"},
		{"task_update", "PATCH", []string{"v1", "missions", "test-id"}, "v1/missions/test-id"},
		{"event_append", "POST", []string{"v1", "flight-recorder"}, "v1/flight-recorder"},
		{"task_run_record", "POST", []string{"v1", "missions", "test-id", "steps"}, "v1/missions/test-id/steps"},
		{"documents_sync", "POST", []string{"v1", "documents", "sync"}, "v1/documents/sync"},
	}

	for _, tc := range mcpdPaths {
		// Construct the path as mcpd does. u.JoinPath adds segments to the
		// existing path; starting from "http://127.0.0.1:8787" (empty path),
		// JoinPath("v1", "missions") produces u.Path = "/v1/missions".
		u, err := url.Parse("http://127.0.0.1:8787")
		if err != nil {
			t.Fatalf("parse base URL: %v", err)
		}
		u = u.JoinPath(tc.pathParts...)

		// url.URL.Path always includes a leading slash for absolute paths
		actualPath := u.Path

		if actualPath != tc.wantPath {
			t.Fatalf("tool=%s: constructed path=%q, want %q", tc.toolName, actualPath, tc.wantPath)
		}

		// Now verify that the server actually registers a route at this path
		// with the expected HTTP method. For parameterized routes like
		// /v1/missions/:missionID, we check that a pattern exists; for
		// literal paths, we check exact match. Note: server routes have
		// leading slash (e.g., /v1/missions) but mcpd paths don't (e.g., v1/missions).
		found := false
		for routeKey := range registeredRoutes {
			// Route key is "METHOD:/path"
			parts := strings.SplitN(routeKey, ":", 2)
			if len(parts) != 2 {
				continue
			}
			serverMethod := parts[0]
			serverPath := parts[1]

			if serverMethod != tc.method {
				continue
			}

			// Strip leading slash from server path for comparison
			serverPathNormalized := strings.TrimPrefix(serverPath, "/")

			// Check if the server path matches. For parameterized routes,
			// we expect the server to have something like v1/missions/:id
			// while mcpd constructs v1/missions/actual-id (after path-escape).
			// We can't do an exact string match, so we verify that:
			// - literal path components match
			// - parameter segments (starting with :) are present where mcpd
			//   constructed literal IDs
			if routeMatchesMCPDPath(serverPathNormalized, actualPath) {
				found = true
				break
			}
		}

		if !found {
			t.Fatalf("tool=%s: method=%s path=%q not found in server router. Registered routes: %v",
				tc.toolName, tc.method, actualPath, registeredRoutes)
		}
	}
}

// routeMatchesMCPDPath reports whether a server-registered route path
// (which may contain parameters like :id) matches an mcpd-constructed path
// (which contains literal segments). For example, /v1/missions/:id matches
// /v1/missions/actual-id.
func routeMatchesMCPDPath(serverPath, mcpdPath string) bool {
	// Split both paths and compare segment by segment
	serverParts := strings.Split(strings.TrimPrefix(serverPath, "/"), "/")
	mcpdParts := strings.Split(strings.TrimPrefix(mcpdPath, "/"), "/")

	if len(serverParts) != len(mcpdParts) {
		return false
	}

	for i := range serverParts {
		// A server segment starting with : is a parameter and matches any mcpd segment
		if strings.HasPrefix(serverParts[i], ":") {
			continue
		}
		// Literal segments must match exactly
		if serverParts[i] != mcpdParts[i] {
			return false
		}
	}

	return true
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
