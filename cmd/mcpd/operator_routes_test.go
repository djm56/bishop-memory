package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"bishop-memory/internal/api"
	"bishop-memory/internal/config"
)

// exclusionRoutePath is the operator's exclusion route for one mission, with
// the id a tool would send through it.
const exclusionRoutePath = "/v1/mission-grades/mission-20261022-99/exclusion"

// requestLog records each request a stub memoryd receives, as "METHOD path".
type requestLog struct {
	mu   sync.Mutex
	seen []string
}

// add records one request.
func (l *requestLog) add(method, path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, method+" "+path)
}

// take returns the requests recorded since the last call, and clears the log.
func (l *requestLog) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.seen
	l.seen = nil
	return out
}

// hasRequest reports whether seen holds the request "METHOD path" exactly.
func hasRequest(seen []string, want string) bool {
	for _, r := range seen {
		if r == want {
			return true
		}
	}
	return false
}

// newRecordingClient returns an mcpd client aimed at a stub memoryd, which
// answers every request with 200 and an empty object, and the log of the
// requests it receives. The stub closes when the test ends.
func newRecordingClient(t *testing.T) (*client, *requestLog) {
	t.Helper()
	log := &requestLog{}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(stub.Close)
	return &client{httpClient: stub.Client(), syncHTTPClient: stub.Client(), baseURL: stub.URL, harness: "test"}, log
}

// argumentOverrides gives a tool's arguments values the generic filler in
// everyArgument cannot. service_record_append refuses any source but its two
// enumerated values before it sends anything, so with the filler's value it
// sends no request and the sweep could not see it. Keyed by tool name, then by
// argument name.
var argumentOverrides = map[string]map[string]any{
	"service_record_append": {"source": "self-reported"},
}

// everyArgument fills each argument a tool declares, so a handler that checks
// its required arguments goes on to the request it would send. Arguments listed
// in argumentOverrides for the tool take the override's value instead.
func everyArgument(tool mcp.Tool) map[string]any {
	args := map[string]any{}
	for name, raw := range tool.InputSchema.Properties {
		schema, _ := raw.(map[string]any)
		switch schema["type"] {
		case "integer", "number":
			args[name] = 1
		case "boolean":
			args[name] = true
		case "array":
			args[name] = []any{"mission-20261022-99"}
		case "object":
			args[name] = map[string]any{}
		default:
			args[name] = "mission-20261022-99"
		}
	}
	for name, v := range argumentOverrides[tool.Name] {
		args[name] = v
	}
	return args
}

// callTool sends one tools/call request for the named tool, with args as its
// arguments, through the server's JSON-RPC dispatch: HandleMessage reads the
// request as a client's message and routes it to the tool. The response is not
// read. A tool that fails answers with an error result, and the request log is
// what the tests check.
func callTool(t *testing.T, s *server.MCPServer, name string, args map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		t.Fatalf("encode tools/call for %s: %v", name, err)
	}
	_ = s.HandleMessage(context.Background(), raw)
}

// sweep registers one mcpd profile on a fresh server with the given client, then
// sends a tools/call for every tool it registers, each with every argument
// filled. The register function is the profile's own registration, the one
// main() calls for that MCPD_PROFILE value. It returns, keyed by tool name, the
// requests that tool alone sent to the stub, so each tool is judged on its own.
func sweep(t *testing.T, c *client, log *requestLog, register func(*server.MCPServer, *client)) map[string][]string {
	t.Helper()
	s := server.NewMCPServer("mcpd", "test")
	register(s, c)
	perTool := map[string][]string{}
	for name, st := range s.ListTools() {
		log.take() // clear what came before, so this tool's list holds only its own requests
		callTool(t, s, name, everyArgument(st.Tool))
		perTool[name] = log.take()
	}
	return perTool
}

// assertNeverReachesExclusion sweeps one profile and fails on any tool whose
// request reaches the operator's exclusion route, or sends PUT or DELETE to a
// grade. Each tool must send at least one request of its own: a tool that
// returns before it sends anything would otherwise pass as clean.
func assertNeverReachesExclusion(t *testing.T, profile string, register func(*server.MCPServer, *client)) {
	t.Helper()
	c, log := newRecordingClient(t)
	perTool := sweep(t, c, log, register)
	if len(perTool) == 0 {
		t.Fatalf("%s profile registers no tools; the sweep proves nothing", profile)
	}
	names := make([]string, 0, len(perTool))
	for name := range perTool {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		seen := perTool[name]
		if len(seen) == 0 {
			t.Errorf("%s profile: tool %s sent no request; its silence proves nothing", profile, name)
		}
		for _, rec := range seen {
			method, path, _ := strings.Cut(rec, " ")
			if strings.HasSuffix(path, "/exclusion") {
				t.Errorf("%s profile: tool %s reaches the operator exclusion route: %s", profile, name, rec)
			}
			if strings.HasPrefix(path, "/v1/mission-grades") && (method == http.MethodPut || method == http.MethodDelete) {
				t.Errorf("%s profile: tool %s sends %s, which only the operator may do to a grade", profile, name, rec)
			}
		}
	}
}

// TestMCPToolsNeverReachOperatorExclusionRoutes checks that no tool in either
// mcpd profile reaches the operator's exclusion route, and that no tool deletes
// or overwrites a grade with PUT or DELETE. Each profile is registered exactly
// as main() registers it, on a server whose client points at a stub memoryd.
// Every tool it registers is called through the server's tools/call JSON-RPC
// dispatch (MCPServer.HandleMessage), the path an MCP client's call takes, and
// not by invoking the tool's handler directly. Each tool is judged on the
// requests it sent alone. Each subtest runs on its own stub, so a broken path
// fails that subtest alone.
//
// The absence is paired with controls. The server side: the router must
// register both exclusion routes, so there is something to keep mcpd away from.
// The recorder: a probe aimed at the exclusion route must show up in the log,
// and grade_write must reach the grade write route, so an empty log cannot pass
// as a clean one.
func TestMCPToolsNeverReachOperatorExclusionRoutes(t *testing.T) {
	t.Run("router_registers_exclusion_routes", func(t *testing.T) {
		dbConn, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("create test database: %v", err)
		}
		defer dbConn.Close()
		router := api.NewRouter(config.Config{AppEnv: "test"}, dbConn)
		routes := map[string]bool{}
		for _, route := range router.Routes() {
			routes[route.Method+":"+route.Path] = true
		}
		for _, want := range []string{
			"PUT:/v1/mission-grades/:missionID/exclusion",
			"DELETE:/v1/mission-grades/:missionID/exclusion",
		} {
			if !routes[want] {
				t.Errorf("router registers no %s; the operator route this test keeps mcpd away from is missing", want)
			}
		}
	})

	t.Run("control_probe_recorded", func(t *testing.T) {
		// The probe is the proxy called directly, not through tools/call: this
		// subtest checks the recorder, and the dispatch subtests check the tools.
		c, log := newRecordingClient(t)
		probe := makeBodyProxy(c, "control_probe", http.MethodPut, nil, nil,
			"v1", "mission-grades", "mission-20261022-99", "exclusion")
		if _, err := probe(context.Background(), mcp.CallToolRequest{}); err != nil {
			t.Fatalf("control probe: %v", err)
		}
		if !hasRequest(log.take(), "PUT "+exclusionRoutePath) {
			t.Fatal("the control probe's request to the exclusion route was not recorded; the recorder cannot see the route")
		}
	})

	t.Run("harness_profile_never_reaches_exclusion", func(t *testing.T) {
		assertNeverReachesExclusion(t, "harness", registerTools)
	})

	t.Run("triage_profile_never_reaches_exclusion", func(t *testing.T) {
		assertNeverReachesExclusion(t, "triage", registerTriageTools)
	})

	t.Run("triage_profile_reaches_grade_write", func(t *testing.T) {
		// Control: grade_write, called through tools/call, reaches the grade write
		// route, so the absence above is not the absence of traffic.
		c, log := newRecordingClient(t)
		perTool := sweep(t, c, log, registerTriageTools)
		if !hasRequest(perTool["grade_write"], "POST /v1/mission-grades") {
			t.Errorf("grade_write sent %v; want POST /v1/mission-grades", perTool["grade_write"])
		}
	})
}
