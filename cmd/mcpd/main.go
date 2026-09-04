// Package main implements mcpd, the bishop-memory MCP adapter.
//
// mcpd is a stdio Model Context Protocol server that proxies the
// bishop-memory HTTP API. It is a pure HTTP *client* of the
// bishop-memory service: it does not import any bishop-memory internal
// package and never touches the SQLite database directly. All state
// mutations flow through the HTTP API exposed by cmd/memoryd.
//
// Agent identity is conveyed two ways:
//
//   - BISHOP_HARNESS env var sets the harness prefix (e.g.
//     "opencode", "claude-code") used to compose Agent identity on
//     write tools that capture the actor (event_append,
//     task_run_record).
//   - Each of those write tools ALSO requires a per-call `agent`
//     argument — the caller names itself (the sub-agent within the
//     harness, e.g. "orchestrator", "junior-developer"). mcpd then
//     composes Agent = "<harness>:<agent>" (e.g. "opencode:orchestrator")
//     and forwards it to the bishop-memory HTTP API.
//
// Configuration (env vars, read with sensible defaults):
//
//   - BISHOP_MEMORY_URL: base URL of the bishop-memory HTTP API
//     (default http://127.0.0.1:8787).
//   - BISHOP_HARNESS: harness / agent-family prefix used in the composed
//     Agent identity (default "opencode").
//
// This is the Step 3 implementation for task-20260820-03 (Phase 3,
// ProjectMemory/bishop-memory). The 8 MCP tools are real HTTP proxies
// with typed input schemas (matching the bishop-memory wire shapes) and
// agent-identity composition for the two write tools that capture it
// (event_append, task_run_record). See docs/migration-plan.md for the
// plan.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Default configuration values used when the corresponding env vars
// are unset or empty.
const (
	defaultMemoryURL   = "http://127.0.0.1:8787"
	defaultHarness     = "opencode"
	defaultHTTPTimeout = 30 * time.Second

	// defaultSyncHTTPTimeout is the timeout used ONLY for documents_sync
	// (Step 4 review fix, Review C WARNING 4). documents_sync's
	// server-side work is a filesystem tree walk + FTS5 import — the one
	// tool whose duration scales with the caller's data, not with a
	// fixed HTTP round-trip. http.Client.Timeout is a hard wall-clock
	// cap applied regardless of any per-call context deadline, so a
	// longer context alone would still be cut off at 30s by the shared
	// client below; documents_sync therefore gets its OWN *http.Client
	// (client.syncHTTPClient) with a longer ceiling instead.
	defaultSyncHTTPTimeout = 5 * time.Minute
)

// client holds the shared HTTP configuration used to proxy bishop-memory
// API calls. It is constructed once at startup; each tool handler
// closes over `c` so the real HTTP calls share the same client, base
// URL (validated with url.ParseRequestURI), and harness prefix.
type client struct {
	httpClient *http.Client
	// syncHTTPClient is used ONLY by documents_sync — see
	// defaultSyncHTTPTimeout above for why it needs a longer ceiling
	// than every other tool's httpClient.
	syncHTTPClient *http.Client
	baseURL        string // already parsed/validated at startup; safe to JoinPath
	harness        string
}

func main() {
	baseURL := envOrDefault("BISHOP_MEMORY_URL", defaultMemoryURL)
	harness := envOrDefault("BISHOP_HARNESS", defaultHarness)

	// Validate the base URL is an absolute URI (not a relative path or
	// fragment) before we try to use it. url.ParseRequestURI rejects
	// bare paths and other inputs url.Parse would silently accept —
	// see Step 2 review carry-forward #1.
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		log.Fatalf("mcpd: invalid BISHOP_MEMORY_URL %q: must be an absolute URL (http://host:port)", baseURL)
	}

	c := &client{
		httpClient:     &http.Client{Timeout: defaultHTTPTimeout},
		syncHTTPClient: &http.Client{Timeout: defaultSyncHTTPTimeout},
		baseURL:        baseURL,
		harness:        harness,
	}

	s := server.NewMCPServer("mcpd", "0.1.0")

	registerTools(s, c)

	log.Printf("mcpd serving stdio (base_url=%s harness=%s)", c.baseURL, c.harness)

	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("mcpd: stdio serve failed: %v", err)
	}
}

// registerTools registers the 8 bishop-memory MCP tools on the server.
//
// Each tool gets a typed input schema (mcp.WithString / WithInteger /
// ...) that mirrors the underlying HTTP request shape, and a handler
// that builds an HTTP request via c.httpClient + c.baseURL, returns
// the JSON body as MCP text, and surfaces non-2xx / parse failures as
// MCP error results (never panics).
func registerTools(s *server.MCPServer, c *client) {
	// --- Read tools (no agent identity required) ---

	s.AddTool(
		mcp.NewTool("memory_search",
			mcp.WithDescription("Search bishop-memory imported documents (FTS5) by query string. Returns ranked hits with snippets; an empty FTS5 index yields an empty results list."),
			mcp.WithString("q",
				mcp.Required(),
				mcp.Description("FTS5 query string. Multi-word is implicit-AND; wrap phrases in double-quotes; '*' is a prefix wildcard. Required."),
			),
			mcp.WithInteger("limit",
				mcp.Description("Optional cap on results (the server caps at 20). Currently advisory only — the server always returns its internal LIMIT 20."),
			),
		),
		makeSearchHandler(c),
	)

	s.AddTool(
		mcp.NewTool("task_list",
			mcp.WithDescription("List tasks from bishop-memory, newest-updated first. Returns {\"tasks\":[...]}."),
		),
		makeTaskListHandler(c),
	)

	s.AddTool(
		mcp.NewTool("task_get",
			mcp.WithDescription("Get a single task by its ID. Returns the task JSON or a 404 if the ID is unknown."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("Task ID (the same value used by task_create / task_update). Required."),
			),
		),
		makeTaskGetHandler(c),
	)

	// --- Write tools (HTTP method varies) ---

	// task_create — NO agent param this round. CreateTaskRequest is
	// plan-frozen (Phase 2); adding an agent field is a documented
	// deferral. Agent identity on the task.created event is captured
	// indirectly by an event_append the caller can make around this
	// call.
	s.AddTool(
		mcp.NewTool("task_create",
			mcp.WithDescription("Create a new task. Returns {\"id\":...,\"created\":true}."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("Unique task ID, max 128 chars. Required."),
			),
			mcp.WithString("title",
				mcp.Required(),
				mcp.Description("Human-readable task title, max 500 chars. Required."),
			),
			mcp.WithString("status",
				mcp.Description("One of open|active|blocked|complete|cancelled. Defaults to \"open\" server-side when omitted."),
			),
			mcp.WithString("priority",
				mcp.Description("One of low|normal|high|urgent. Defaults to \"normal\" server-side when omitted."),
			),
			mcp.WithString("next_action",
				mcp.Description("Free-form next-action note, max 2000 chars. Optional."),
			),
			mcp.WithString("blockers",
				mcp.Description("Free-form blockers note, max 2000 chars. Optional."),
			),
		),
		makeTaskCreateHandler(c),
	)

	// task_update — NO agent param this round (UpdateTaskRequest is
	// plan-frozen; same deferral as task_create).
	s.AddTool(
		mcp.NewTool("task_update",
			mcp.WithDescription("Update an existing task (status / priority / next_action / blockers). Returns {\"id\":...,\"updated\":true} or a 404 if the task is unknown."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("Task ID to update. Required."),
			),
			mcp.WithString("status",
				mcp.Description("New status, one of open|active|blocked|complete|cancelled. Omitted fields are unchanged."),
			),
			mcp.WithString("priority",
				mcp.Description("New priority, one of low|normal|high|urgent. Omitted fields are unchanged."),
			),
			mcp.WithString("next_action",
				mcp.Description("New next_action text. Omitted fields are unchanged."),
			),
			mcp.WithString("blockers",
				mcp.Description("New blockers text. Omitted fields are unchanged."),
			),
		),
		makeTaskUpdateHandler(c),
	)

	// event_append — agent identity IS captured on the appended event
	// (composed "<harness>:<agent>"). The caller names itself via the
	// `agent` argument.
	s.AddTool(
		mcp.NewTool("event_append",
			mcp.WithDescription("Append an event to the memory log. Agent identity is composed as \"<BISHOP_HARNESS>:<agent>\" (e.g. \"opencode:orchestrator\") and stored on the event row for actor attribution. Returns {\"appended\":true,\"id\":...}."),
			mcp.WithString("task_id",
				mcp.Description("Optional task ID to scope the event to. Omit for system / agent-scoped events."),
			),
			mcp.WithString("event_type",
				mcp.Required(),
				mcp.Description("Dotted event discriminator, e.g. \"task.created\", \"agent.heartbeat\", max 64 chars. Required."),
			),
			mcp.WithString("summary",
				mcp.Required(),
				mcp.Description("Short human-readable summary of the event, max 2000 chars. Required."),
			),
			mcp.WithString("agent",
				mcp.Required(),
				mcp.Description("Sub-agent name (the part AFTER the harness prefix), e.g. \"orchestrator\", \"junior-developer\". Composed with BISHOP_HARNESS to form the stored actor identity (\"<harness>:<agent>\"); the SERVER enforces a 64-char cap on that composed string (events.agent), so a long agent name combined with the harness prefix may be rejected as invalid. Required."),
			),
		),
		makeEventAppendHandler(c),
	)

	// task_run_record — agent identity IS captured on the task_runs
	// row (composed "<harness>:<agent>").
	s.AddTool(
		mcp.NewTool("task_run_record",
			mcp.WithDescription("Record a task run (one agent execution attempt against a task). Agent identity is composed as \"<BISHOP_HARNESS>:<agent>\". Returns {\"recorded\":true,\"task_id\":...} or a 404 if the task is unknown."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("Task ID the run is recorded against. Required."),
			),
			mcp.WithString("agent",
				mcp.Required(),
				mcp.Description("Sub-agent name (the part AFTER the harness prefix), e.g. \"junior-developer\", \"orchestrator\". Composed with BISHOP_HARNESS to form the stored actor identity (\"<harness>:<agent>\"); the SERVER enforces a 128-char cap on that composed string (task_runs.agent, per taskRunRequest's binding tag), so a long agent name combined with the harness prefix may be rejected as invalid. Required."),
			),
			mcp.WithString("status",
				mcp.Description("Run status, free-form, max 64 chars. E.g. \"started\", \"completed\", \"failed\"."),
			),
			mcp.WithString("summary",
				mcp.Description("Free-form summary of the run, max 2000 chars."),
			),
			mcp.WithString("started_at",
				mcp.Description("ISO-8601 start timestamp. Optional."),
			),
			mcp.WithString("ended_at",
				mcp.Description("ISO-8601 end timestamp. Optional."),
			),
		),
		makeTaskRunRecordHandler(c),
	)

	// documents_sync — triggers an importer run. NOT an agent action
	// (the sync is system-driven), so no agent composition.
	s.AddTool(
		mcp.NewTool("documents_sync",
			mcp.WithDescription("Trigger a document sync / import from the configured memory root into the FTS5 index. Returns {\"root\":...,\"synced\":true} or a 502 if the import failed."),
			mcp.WithString("root",
				mcp.Description("Optional override of the memory root. Omit to use the server's configured MEMORY_ROOT (or \"testdata/memory\" default)."),
			),
		),
		makeDocumentsSyncHandler(c),
	)
}

// --- Wire-shape structs ---
//
// These redeclare the minimum JSON shapes mcpd needs to build
// well-formed request bodies for the bishop-memory HTTP API. They are
// intentionally NOT imports of bishop-memory/internal/model: mcpd
// stays decoupled from the server's internal packages, and the JSON
// wire shapes are the documented contract.

// createMissionBody is the JSON body for POST /v1/missions.
type createMissionBody struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status,omitempty"`
	Priority   string `json:"priority,omitempty"`
	NextAction string `json:"next_action,omitempty"`
	Blockers   string `json:"blockers,omitempty"`
}

// updateMissionBody is the JSON body for PATCH /v1/missions/<id>. Fields
// mirror model.UpdateMissionRequest; *string pointers carry
// "omitted vs explicit null" semantics so a PATCH with no fields is
// still a valid (no-op) update.
type updateMissionBody struct {
	Status     *string `json:"status,omitempty"`
	Priority   *string `json:"priority,omitempty"`
	NextAction *string `json:"next_action,omitempty"`
	Blockers   *string `json:"blockers,omitempty"`
}

// appendFlightRecorderBody is the JSON body for POST /v1/flight-recorder. Agent is the
// composed "<harness>:<agent>" identity (Phase 3, additive change to
// model.AppendFlightRecorderRequest).
type appendFlightRecorderBody struct {
	MissionID string `json:"mission_id,omitempty"`
	Event     string `json:"event"`
	Note      string `json:"note"`
	Agent     string `json:"agent,omitempty"`
}

// missionStepBody is the JSON body for POST /v1/missions/<id>/steps. It
// mirrors the internal/api/missionStepRequest wire shape so mcpd can
// build the request without importing the server's internal package.
type missionStepBody struct {
	Agent     string  `json:"agent,omitempty"`
	Status    string  `json:"status,omitempty"`
	Summary   string  `json:"summary,omitempty"`
	StartedAt *string `json:"started_at,omitempty"`
	EndedAt   *string `json:"ended_at,omitempty"`
}

// syncBody is the JSON body for POST /v1/documents/sync. Root is
// optional; the server falls back to MEMORY_ROOT then "testdata/memory".
type syncBody struct {
	Root string `json:"root,omitempty"`
}

// --- Tool handlers ---

// makeSearchHandler wires the memory_search tool to GET /v1/memory/search.
func makeSearchHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q := strings.TrimSpace(mcp.ParseString(req, "q", ""))
		if q == "" {
			return mcp.NewToolResultError("memory_search: `q` is required and must not be empty/whitespace-only"), nil
		}

		// limit is currently advisory only — the server always caps at
		// 20. We forward it anyway so a future server-side change
		// lights up without an mcpd release; if `limit` parses to <=0
		// or fails to parse, we omit it.
		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("memory_search: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "memory", "search")
		qry := u.Query()
		qry.Set("q", q)
		if rawLimit, ok := req.GetArguments()["limit"]; ok {
			if n, err := strconv.Atoi(fmt.Sprint(rawLimit)); err == nil && n > 0 {
				qry.Set("limit", strconv.Itoa(n))
			}
		}
		u.RawQuery = qry.Encode()

		body, status, httpErr := c.do(ctx, http.MethodGet, u.String(), nil)
		if httpErr != nil {
			return toolHTTPError("memory_search", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("memory_search", status, body), nil
		}
		return toolSuccess("memory_search", body), nil
	}
}

// makeTaskListHandler wires the task_list tool to GET /v1/missions.
func makeTaskListHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("task_list: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "missions")

		body, status, httpErr := c.do(ctx, http.MethodGet, u.String(), nil)
		if httpErr != nil {
			return toolHTTPError("task_list", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("task_list", status, body), nil
		}
		return toolSuccess("task_list", body), nil
	}
}

// makeTaskGetHandler wires the task_get tool to GET /v1/missions/<id>.
//
// `id` is escaped with url.PathEscape so a user-supplied ID containing
// "/" or other URL-special bytes cannot escape the path segment. A
// malicious or stray value yields a clean 404 from the server rather
// than a path-injection surprise.
func makeTaskGetHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := strings.TrimSpace(mcp.ParseString(req, "id", ""))
		if id == "" {
			return mcp.NewToolResultError("task_get: `id` is required and must not be empty/whitespace-only"), nil
		}
		if isDotSegment(id) {
			return mcp.NewToolResultError(`task_get: "id" must not be "." or ".." — that collapses to the list route instead of a single-mission lookup`), nil
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("task_get: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "missions", url.PathEscape(id))

		body, status, httpErr := c.do(ctx, http.MethodGet, u.String(), nil)
		if httpErr != nil {
			return toolHTTPError("task_get", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("task_get", status, body), nil
		}
		return toolSuccess("task_get", body), nil
	}
}

// makeTaskCreateHandler wires the task_create tool to POST /v1/missions.
//
// Per the Step 2 deferral, no agent param — CreateMissionRequest is
// plan-frozen. The caller can attribute the surrounding mission.created
// event via event_append if it needs an actor on the audit trail.
func makeTaskCreateHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := strings.TrimSpace(mcp.ParseString(req, "id", ""))
		title := strings.TrimSpace(mcp.ParseString(req, "title", ""))
		if id == "" {
			return mcp.NewToolResultError("task_create: `id` is required"), nil
		}
		if title == "" {
			return mcp.NewToolResultError("task_create: `title` is required"), nil
		}

		body := createMissionBody{
			ID:         id,
			Title:      title,
			Status:     strings.TrimSpace(mcp.ParseString(req, "status", "")),
			Priority:   strings.TrimSpace(mcp.ParseString(req, "priority", "")),
			NextAction: mcp.ParseString(req, "next_action", ""),
			Blockers:   mcp.ParseString(req, "blockers", ""),
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("task_create: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "missions")

		payload, merr := json.Marshal(body)
		if merr != nil {
			return toolInternalErr("task_create: marshal request body", merr), nil
		}
		respBody, status, httpErr := c.do(ctx, http.MethodPost, u.String(), payload)
		if httpErr != nil {
			return toolHTTPError("task_create", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("task_create", status, respBody), nil
		}
		return toolSuccess("task_create", respBody), nil
	}
}

// makeTaskUpdateHandler wires the task_update tool to PATCH /v1/missions/<id>.
//
// `id` is path-escaped for the same reason as task_get. Fields the
// caller did not supply are omitted from the JSON body (pointer
// nil → `omitempty` → absent) so the server's COALESCE-based update
// leaves them untouched.
func makeTaskUpdateHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := strings.TrimSpace(mcp.ParseString(req, "id", ""))
		if id == "" {
			return mcp.NewToolResultError("task_update: `id` is required"), nil
		}
		if isDotSegment(id) {
			return mcp.NewToolResultError(`task_update: "id" must not be "." or ".." — that collapses to the list/no-route path instead of a single-mission update`), nil
		}

		// Build a PATCH body that only includes fields the caller
		// actually supplied. mcp.ParseString returns "" when the key
		// is missing; we use a presence check via GetArguments so an
		// explicit "" (clear-the-field) still gets forwarded.
		args := req.GetArguments()
		patch := updateMissionBody{}
		if raw, ok := args["status"]; ok {
			s := strings.TrimSpace(fmt.Sprint(raw))
			patch.Status = &s
		}
		if raw, ok := args["priority"]; ok {
			p := strings.TrimSpace(fmt.Sprint(raw))
			patch.Priority = &p
		}
		if raw, ok := args["next_action"]; ok {
			n := fmt.Sprint(raw)
			patch.NextAction = &n
		}
		if raw, ok := args["blockers"]; ok {
			b := fmt.Sprint(raw)
			patch.Blockers = &b
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("task_update: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "missions", url.PathEscape(id))

		payload, merr := json.Marshal(patch)
		if merr != nil {
			return toolInternalErr("task_update: marshal request body", merr), nil
		}
		respBody, status, httpErr := c.do(ctx, http.MethodPatch, u.String(), payload)
		if httpErr != nil {
			return toolHTTPError("task_update", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("task_update", status, respBody), nil
		}
		return toolSuccess("task_update", respBody), nil
	}
}

// makeEventAppendHandler wires the event_append tool to POST /v1/flight-recorder.
//
// Agent identity is composed as "<harness>:<agent>" so the audit
// trail records the originating sub-agent (the per-call `agent`
// argument) AND the harness family (the BISHOP_HARNESS env var). The
// caller MUST supply `agent`; the harness is sourced from the env.
func makeEventAppendHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		event := strings.TrimSpace(mcp.ParseString(req, "event_type", ""))
		note := strings.TrimSpace(mcp.ParseString(req, "summary", ""))
		agentParam := strings.TrimSpace(mcp.ParseString(req, "agent", ""))

		if event == "" {
			return mcp.NewToolResultError("event_append: `event_type` is required"), nil
		}
		if note == "" {
			return mcp.NewToolResultError("event_append: `summary` is required"), nil
		}
		if agentParam == "" {
			return mcp.NewToolResultError("event_append: `agent` is required (the sub-agent name, composed with BISHOP_HARNESS as the actor identity)"), nil
		}

		// Agent-identity composition (carry-forward #3): per-call
		// `agent` argument is REQUIRED; BISHOP_HARNESS is the env
		// prefix. No hard-coded fallback at the handler layer — if
		// the harness is empty we still accept the per-call value so
		// the server side never sees a malformed identity.
		agent := composeAgent(c.harness, agentParam)

		body := appendFlightRecorderBody{
			MissionID: strings.TrimSpace(mcp.ParseString(req, "task_id", "")),
			Event:     event,
			Note:      note,
			Agent:     agent,
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("event_append: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "flight-recorder")

		payload, merr := json.Marshal(body)
		if merr != nil {
			return toolInternalErr("event_append: marshal request body", merr), nil
		}
		respBody, status, httpErr := c.do(ctx, http.MethodPost, u.String(), payload)
		if httpErr != nil {
			return toolHTTPError("event_append", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("event_append", status, respBody), nil
		}
		return toolSuccess("event_append", respBody), nil
	}
}

// makeTaskRunRecordHandler wires the task_run_record tool to
// POST /v1/missions/<id>/steps.
//
// Same agent-identity composition as event_append: per-call `agent`
// required, BISHOP_HARNESS is the env prefix.
func makeTaskRunRecordHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := strings.TrimSpace(mcp.ParseString(req, "id", ""))
		agentParam := strings.TrimSpace(mcp.ParseString(req, "agent", ""))

		if id == "" {
			return mcp.NewToolResultError("task_run_record: `id` is required"), nil
		}
		if isDotSegment(id) {
			return mcp.NewToolResultError(`task_run_record: "id" must not be "." or ".." — that collapses the joined path onto an unintended route`), nil
		}
		if agentParam == "" {
			return mcp.NewToolResultError("task_run_record: `agent` is required (the sub-agent name, composed with BISHOP_HARNESS as the actor identity)"), nil
		}

		agent := composeAgent(c.harness, agentParam)

		// Build the body; optional fields default to "" or are nil
		// depending on whether the caller supplied them.
		body := missionStepBody{
			Agent:   agent,
			Status:  strings.TrimSpace(mcp.ParseString(req, "status", "")),
			Summary: mcp.ParseString(req, "summary", ""),
		}
		if raw, ok := req.GetArguments()["started_at"]; ok {
			s := fmt.Sprint(raw)
			body.StartedAt = &s
		}
		if raw, ok := req.GetArguments()["ended_at"]; ok {
			e := fmt.Sprint(raw)
			body.EndedAt = &e
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("task_run_record: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "missions", url.PathEscape(id), "steps")

		payload, merr := json.Marshal(body)
		if merr != nil {
			return toolInternalErr("task_run_record: marshal request body", merr), nil
		}
		respBody, status, httpErr := c.do(ctx, http.MethodPost, u.String(), payload)
		if httpErr != nil {
			return toolHTTPError("task_run_record", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("task_run_record", status, respBody), nil
		}
		return toolSuccess("task_run_record", respBody), nil
	}
}

// makeDocumentsSyncHandler wires documents_sync to POST /v1/documents/sync.
//
// Sync is a system trigger, not an agent action, so no agent
// composition. The body is `{"root": "..."}` when the caller supplied
// one, or `{}` otherwise (server falls back to MEMORY_ROOT / default).
func makeDocumentsSyncHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		body := syncBody{
			Root: strings.TrimSpace(mcp.ParseString(req, "root", "")),
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("documents_sync: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "documents", "sync")

		payload, merr := json.Marshal(body)
		if merr != nil {
			return toolInternalErr("documents_sync: marshal request body", merr), nil
		}
		respBody, status, httpErr := c.doSync(ctx, http.MethodPost, u.String(), payload)
		if httpErr != nil {
			return toolHTTPError("documents_sync", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("documents_sync", status, respBody), nil
		}
		return toolSuccess("documents_sync", respBody), nil
	}
}

// --- HTTP helper ---

// do executes one HTTP request via c.httpClient (the standard,
// defaultHTTPTimeout-bounded client used by every tool except
// documents_sync) and returns the response body, the HTTP status code,
// and a transport error (if any).
func (c *client) do(ctx context.Context, method, urlStr string, body []byte) ([]byte, int, error) {
	return doWithClient(ctx, c.httpClient, method, urlStr, body)
}

// doSync is identical to do except it executes via c.syncHTTPClient —
// the longer-timeout client reserved for documents_sync (Step 4 review
// fix, Review C WARNING 4; see defaultSyncHTTPTimeout above).
func (c *client) doSync(ctx context.Context, method, urlStr string, body []byte) ([]byte, int, error) {
	return doWithClient(ctx, c.syncHTTPClient, method, urlStr, body)
}

// doWithClient executes one HTTP request against the given httpClient
// and returns the response body, the HTTP status code, and a transport
// error (if any). body is JSON (may be nil for GETs); the response
// body is fully read so the underlying connection can be reused.
//
// Returns body="" when the response was empty.
func doWithClient(ctx context.Context, httpClient *http.Client, method, urlStr string, body []byte) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, urlStr, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	httpReq.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	return respBody, resp.StatusCode, nil
}

// composeAgent joins the harness prefix and the per-call sub-agent
// name with ":" so the bishop-memory audit trail records BOTH the
// agent family and the specific sub-agent (e.g. "opencode:orchestrator",
// "claude-code:junior-developer").
//
// The empty-harness branch below is defensive, not reachable via any
// env var an operator can currently set: envOrDefault("BISHOP_HARNESS",
// defaultHarness) already treats an unset OR explicitly-empty
// BISHOP_HARNESS as unset and substitutes "opencode", so c.harness is
// never "" in main()'s current wiring (Step 4 review, Review C
// SUGGESTION 11 — this docblock previously described a scenario the
// code as written cannot reach). The branch is kept because
// composeAgent is a small, independently testable pure function and a
// future caller (or a test) may legitimately pass "" directly.
func composeAgent(harness, subAgent string) string {
	harness = strings.TrimSpace(harness)
	subAgent = strings.TrimSpace(subAgent)
	if harness == "" {
		return subAgent
	}
	return harness + ":" + subAgent
}

// --- MCP error helpers (carry-forward #4) ---
//
// All non-2xx HTTP responses and any transport / parse errors are
// surfaced to the MCP caller as text inside a CallToolResult (with
// IsError=true for HTTP status errors), NEVER as a returned error
// from the handler — a returned error would surface as an MCP
// protocol-level error instead of a tool result and would skip the
// model's normal "use the result" branch.

// toolHTTPError turns a transport error into an MCP tool error result.
func toolHTTPError(tool string, err error) *mcp.CallToolResult {
	return mcp.NewToolResultErrorf("%s: HTTP transport error: %v", tool, err)
}

// toolHTTPStatusError turns a non-2xx HTTP response into an MCP tool
// error result. The response body (typically a JSON {"error": ...}
// from the server) is included verbatim so the caller has the server's
// own diagnostic; we trim to a safe length to avoid flooding logs.
func toolHTTPStatusError(tool string, status int, body []byte) *mcp.CallToolResult {
	const snippetBytes = 512
	snippet := string(body)
	if len(snippet) > snippetBytes {
		snippet = snippet[:snippetBytes] + "…"
	}
	return mcp.NewToolResultErrorf("%s: HTTP %d: %s", tool, status, snippet)
}

// toolInternalErr surfaces an mcpd-internal error (URL parse, JSON
// marshal — none of which should fire in practice, but we don't want
// a panic to bubble out).
func toolInternalErr(where string, err error) *mcp.CallToolResult {
	return mcp.NewToolResultErrorf("%s: %v", where, err)
}

// toolSuccess wraps a 2xx response body as an MCP text result, after
// confirming it is well-formed JSON.
//
// Step 4 review fix (Review C, WARNING 3): every handler's success path
// previously forwarded body as mcp.NewToolResultText(string(body)) with
// no validity check. A malformed-but-200 server response (a bug on the
// bishop-memory side, not something mcpd controls) would be forwarded
// as a SUCCESS result rather than the "distinct, actionable MCP error"
// this file's own docblock promises for every other failure mode —
// silently handing the caller unparseable text as if it were the
// documented JSON envelope.
func toolSuccess(tool string, body []byte) *mcp.CallToolResult {
	if !json.Valid(body) {
		return mcp.NewToolResultErrorf("%s: server returned a 2xx response with a malformed JSON body", tool)
	}
	return mcp.NewToolResultText(string(body))
}

// isDotSegment reports whether id, once path-escaped and joined, would
// collapse into a dot path segment ("." or "..") that url.URL.JoinPath
// cleans away.
//
// Step 4 review fix (Review C, CRITICAL 1): url.PathEscape does not
// escape "." (it is an RFC 3986 unreserved character), and
// url.URL.JoinPath lexically cleans "." / ".." segments out of the
// joined path AFTER escaping. Reproduced live against this module's
// go.mod (net/url stdlib): task_get/task_update with id="." escapes to
// "." and joins "v1/tasks/." down to "v1/tasks" — the list route,
// returning HTTP 200 with the full task list instead of a 404 for what
// the caller thinks is a single-task lookup. id=".." joins "v1/tasks/.."
// down to "v1", which happens to 404 today by accident of the router's
// shape, not by design — so both are rejected here rather than relying
// on that accident to persist.
func isDotSegment(id string) bool {
	return id == "." || id == ".."
}

// envOrDefault returns the value of name, or fallback when unset or empty.
// An empty-string env var is treated as unset so callers can blank a
// default without an empty string propagating. Mirrors the helper in
// internal/config/config.go to keep mcpd self-contained (it does not
// import internal/config).
func envOrDefault(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		return value
	}
	return fallback
}
