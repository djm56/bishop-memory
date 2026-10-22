package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
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

// everyArgument fills each argument a tool declares, so a handler that checks
// its required arguments goes on to the request it would send.
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
	return args
}

// sweep registers one mcpd profile on a fresh server with the given client,
// calls every tool it registers with every argument filled, and returns when
// all have answered. The register function is the profile's own registration,
// the one main() calls for that MCPD_PROFILE value.
func sweep(c *client, register func(*server.MCPServer, *client)) {
	s := server.NewMCPServer("mcpd", "test")
	register(s, c)
	for name, st := range s.ListTools() {
		req := mcp.CallToolRequest{}
		req.Params.Name = name
		req.Params.Arguments = everyArgument(st.Tool)
		// A tool reports failure as an error result, not a Go error, and the
		// request log is what this test reads, so the result is not needed.
		_, _ = st.Handler(context.Background(), req)
	}
}

// TestMCPToolsNeverReachOperatorExclusionRoutes checks that no tool in either
// mcpd profile reaches the operator's exclusion route, and that no tool deletes
// or overwrites a grade with PUT or DELETE. Each profile is registered exactly as
// main() registers it, on a server whose client points at a stub memoryd, and
// every tool it registers is called with every argument filled.
//
// Two controls keep the absence honest. The server side first: the router
// must register both exclusion routes, so there is something to keep mcpd away
// from. Then the recorder: a probe proxy aimed at the exclusion route must show
// up in the log, and the sweep must show the grade claim and write routes, so
// an empty log cannot pass as a clean one.
func TestMCPToolsNeverReachOperatorExclusionRoutes(t *testing.T) {
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

	log := &requestLog{}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer stub.Close()
	c := &client{httpClient: stub.Client(), syncHTTPClient: stub.Client(), baseURL: stub.URL, harness: "test"}

	// Control: the recorder sees a request aimed at the exclusion route.
	probe := makeBodyProxy(c, "control_probe", http.MethodPut, nil, nil,
		"v1", "mission-grades", "mission-20261022-99", "exclusion")
	if _, err := probe(context.Background(), mcp.CallToolRequest{}); err != nil {
		t.Fatalf("control probe: %v", err)
	}
	if !hasRequest(log.take(), "PUT "+exclusionRoutePath) {
		t.Fatal("the control probe's request to the exclusion route was not recorded; the recorder cannot see the route")
	}

	for name, register := range map[string]func(*server.MCPServer, *client){
		"harness": registerTools,
		"triage":  registerTriageTools,
	} {
		sweep(c, register)
		seen := log.take()
		if len(seen) == 0 {
			t.Fatalf("%s profile: no request reached the stub; the sweep proves nothing", name)
		}
		// Control: the sweep reaches the grade routes, so the absence is not
		// an absence of traffic.
		if name == "triage" && !hasRequest(seen, "POST /v1/mission-grades") {
			t.Errorf("triage profile: the sweep never reached POST /v1/mission-grades; the grade routes are unreached")
		}
		for _, rec := range seen {
			method, path, _ := strings.Cut(rec, " ")
			if strings.HasSuffix(path, "/exclusion") {
				t.Errorf("%s profile reaches the operator exclusion route: %s", name, rec)
			}
			if strings.HasPrefix(path, "/v1/mission-grades") && (method == http.MethodPut || method == http.MethodDelete) {
				t.Errorf("%s profile sends %s, which only the operator may do to a grade", name, rec)
			}
		}
	}
}
