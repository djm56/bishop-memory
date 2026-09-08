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
//     "claude-code") used to compose Agent identity on
//     write tools that capture the actor (flight_recorder_append,
//     mission_step_record).
//   - Each of those write tools ALSO requires a per-call `agent`
//     argument — the caller names itself (the sub-agent within the
//     harness, e.g. "bishop", "hicks"). mcpd then
//     composes Agent = "<harness>:<agent>" (e.g. "claude-code:bishop")
//     and forwards it to the bishop-memory HTTP API.
//
// Configuration (env vars, read with sensible defaults):
//
//   - BISHOP_MEMORY_URL: base URL of the bishop-memory HTTP API
//     (default http://127.0.0.1:8787).
//   - BISHOP_HARNESS: harness / agent-family prefix used in the composed
//     Agent identity (default "claude-code").
//
// The 15 MCP tools are real HTTP proxies with typed input schemas
// (matching the bishop-memory wire shapes) and agent-identity composition
// for the two write tools that capture it (flight_recorder_append,
// mission_step_record). See docs/migration-plan.md for the plan.
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
	defaultMemoryURL = "http://127.0.0.1:8787"
	// defaultHarness is "claude-code" by deliberate design: this is the
	// bishop-harness project, and claude-code is the intended default harness.
	// The install scripts explicitly set the BISHOP_HARNESS env var (to
	// "claude-code" or "opencode" depending on which installer ran), so the
	// default is only reached by a bare invocation. For this project, the
	// claude-code default is correct.
	defaultHarness     = "claude-code"
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

// registerTools registers the 15 bishop-memory MCP tools on the server.
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
		mcp.NewTool("mission_list",
			mcp.WithDescription("List missions from bishop-memory, newest-updated first. Returns {\"missions\":[...]}."),
		),
		makeMissionListHandler(c),
	)

	s.AddTool(
		mcp.NewTool("mission_get",
			mcp.WithDescription("Get a single mission by its ID. Returns the mission JSON or a 404 if the ID is unknown."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("Mission ID (the same value used by mission_create / mission_update). Required."),
			),
		),
		makeMissionGetHandler(c),
	)

	// --- Write tools (HTTP method varies) ---

	// mission_allocate — the multi-harness entry point. Unlike
	// mission_create, the caller does NOT supply an id: the service
	// computes the next sequence for the UTC day across every harness and
	// inserts the mission in the same transaction, so two harnesses
	// opening a mission on the same day cannot collide. `harness` is
	// filled from BISHOP_HARNESS rather than asked of the agent — the
	// agent does not know which harness it is running inside, and letting
	// it guess is how misattributed rows happen.
	s.AddTool(
		mcp.NewTool("mission_allocate",
			mcp.WithDescription("Allocate a centrally-unique mission ID and create the mission in one atomic step. Use this INSTEAD of mission_create when the harness runs in central mode, so mission IDs never collide between harnesses. The harness is read from the calling harness's .claude/bishop-memory.conf (key BISHOP_HARNESS) and passed as the harness parameter; omit it to fall back to the server's BISHOP_HARNESS env var. Supply it whenever more than one harness shares this bishop-memory, because the env var is user-scoped and identical across projects. Returns {\"id\":...,\"harness\":...,\"date\":...,\"seq\":...,\"created\":true}."),
			mcp.WithString("title",
				mcp.Required(),
				mcp.Description("Human-readable mission title, max 500 chars. Required."),
			),
			mcp.WithString("harness",
				mcp.Description("The owning harness, read from the calling harness's .claude/bishop-memory.conf; omit it to fall back to the server's BISHOP_HARNESS env var; supply it whenever more than one harness shares this bishop-memory, because the env var is user-scoped and identical across projects. Optional."),
			),
			mcp.WithString("owner",
				mcp.Description("Mission owner name, max 128 chars. Optional."),
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
			mcp.WithString("date",
				mcp.Description("Override the UTC day the ID is scoped to, as YYYYMMDD. Omit in normal use — the SERVER's UTC clock is authoritative, so that harnesses in different timezones cannot disagree about what day it is. Optional."),
			),
		),
		makeMissionAllocateHandler(c),
	)

	// mission_create — NO agent param this round. CreateMissionRequest is
	// plan-frozen (Phase 2); adding an agent field is a documented
	// deferral. Agent identity on the mission.created event is captured
	// indirectly by a flight_recorder_append the caller can make around this
	// call.
	s.AddTool(
		mcp.NewTool("mission_create",
			mcp.WithDescription("Create a new mission. Returns {\"id\":...,\"created\":true}."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("Unique mission ID, max 128 chars. Required."),
			),
			mcp.WithString("title",
				mcp.Required(),
				mcp.Description("Human-readable mission title, max 500 chars. Required."),
			),
			mcp.WithString("status",
				mcp.Description("One of not-started|in-progress|blocked|complete. Defaults to \"not-started\" server-side when omitted."),
			),
			mcp.WithString("owner",
				mcp.Description("Mission owner name, max 128 chars. Optional."),
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
		makeMissionCreateHandler(c),
	)

	// mission_update — NO agent param this round (UpdateMissionRequest is
	// plan-frozen; same deferral as mission_create).
	s.AddTool(
		mcp.NewTool("mission_update",
			mcp.WithDescription("Update an existing mission (status / owner / outcome / priority / next_action / blockers). Returns {\"id\":...,\"updated\":true} or a 404 if the mission is unknown."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("Mission ID to update. Required."),
			),
			mcp.WithString("status",
				mcp.Description("New status, one of not-started|in-progress|blocked|complete. Omitted fields are unchanged."),
			),
			mcp.WithString("owner",
				mcp.Description("New owner name, max 128 chars. Omitted fields are unchanged."),
			),
			mcp.WithString("outcome",
				mcp.Description("Mission outcome, one of done|failed. Optional, independently settable from status. Omitted fields are unchanged."),
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
		makeMissionUpdateHandler(c),
	)

	// flight_recorder_append — agent identity IS captured on the appended event
	// (composed "<harness>:<agent>"). The caller names itself via the
	// `agent` argument.
	s.AddTool(
		mcp.NewTool("flight_recorder_append",
			mcp.WithDescription("Append an event to the flight recorder (audit log). Agent identity is composed as \"<BISHOP_HARNESS>:<agent>\" (e.g. \"claude-code:bishop\") and stored on the event row for actor attribution. Returns {\"appended\":true,\"id\":...}."),
			mcp.WithString("mission_id",
				mcp.Description("Optional mission ID to scope the event to. Omit for system / agent-scoped events."),
			),
			mcp.WithString("step",
				mcp.Description("Optional step label from PROGRESS.md (e.g., \"4a\", \"—\"), max 16 chars."),
			),
			mcp.WithString("event",
				mcp.Required(),
				mcp.Description("Dotted event discriminator, e.g. \"mission.created\", \"agent.heartbeat\", max 64 chars. Required."),
			),
			mcp.WithString("note",
				mcp.Required(),
				mcp.Description("Short human-readable note about the event, max 2000 chars. Required."),
			),
			mcp.WithString("occurred_at",
				mcp.Description("Optional timestamp when the event occurred (YYYY-MM-DD HH:MM UTC format), max 64 chars."),
			),
			mcp.WithString("agent",
				mcp.Required(),
				mcp.Description("Sub-agent name (the part AFTER the harness prefix), e.g. \"bishop\", \"hicks\". Composed with BISHOP_HARNESS to form the stored actor identity (\"<harness>:<agent>\"); the SERVER enforces a 64-char cap on that composed string (flight_recorder.agent), so a long agent name combined with the harness prefix may be rejected as invalid. Required."),
			),
		),
		makeFlightRecorderAppendHandler(c),
	)

	// mission_step_record — agent identity IS captured on the mission_steps
	// row (composed "<harness>:<agent>").
	s.AddTool(
		mcp.NewTool("mission_step_record",
			mcp.WithDescription("Record a mission step (one agent execution attempt against a mission). Agent identity is composed as \"<BISHOP_HARNESS>:<agent>\". Returns {\"recorded\":true,\"mission_id\":...} or a 404 if the mission is unknown."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("Mission ID the step is recorded against. Required."),
			),
			mcp.WithString("step",
				mcp.Description("Optional step label from PROGRESS.md (e.g., \"4a\"), max 16 chars."),
			),
			mcp.WithString("phase",
				mcp.Description("Optional phase label from PROGRESS.md (e.g., \"Core rename\"), max 64 chars."),
			),
			mcp.WithString("agent",
				mcp.Required(),
				mcp.Description("Sub-agent name (the part AFTER the harness prefix), e.g. \"hicks\", \"bishop\". Composed with BISHOP_HARNESS to form the stored actor identity (\"<harness>:<agent>\"); the SERVER enforces a 128-char cap on that composed string (mission_steps.agent, per missionStepRequest's binding tag), so a long agent name combined with the harness prefix may be rejected as invalid. Required."),
			),
			mcp.WithString("status",
				mcp.Description("Step status, one of pending|in-progress|done|failed."),
			),
			mcp.WithString("notes",
				mcp.Description("Free-form notes about the step, max 2000 chars."),
			),
			mcp.WithString("summary",
				mcp.Description("Free-form summary of the step outcome, max 2000 chars. Independently set from notes: use summary for a brief result statement, notes for detailed observations."),
			),
			mcp.WithString("started_at",
				mcp.Description("ISO-8601 start timestamp. Optional."),
			),
			mcp.WithString("ended_at",
				mcp.Description("ISO-8601 end timestamp. Optional."),
			),
		),
		makeMissionStepRecordHandler(c),
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

	// mission_steps_list — read tool, no agent identity required. Returns the
	// mission's step table (PROGRESS.md view).
	s.AddTool(
		mcp.NewTool("mission_steps_list",
			mcp.WithDescription("List steps for a mission. Returns {\"steps\":[...]}. Returns 404 if the mission is unknown."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("Mission ID. Required."),
			),
		),
		makeMissionStepsListHandler(c),
	)

	// finding_list — read tool, no agent identity required. Returns findings
	// newest-first; status filter is optional but validated against the schema enum.
	// Agents can create findings but cannot change status — that belongs to the
	// operator — so `status` here is useful for reading proposed findings awaiting
	// approval or approved (binding) findings to apply.
	s.AddTool(
		mcp.NewTool("finding_list",
			mcp.WithDescription("List findings from the improvement ledger, newest-first. Optional status filter to view findings at a particular stage (e.g., 'proposed' to see what's awaiting operator approval, or 'approved' to see binding findings). Agents can only CREATE findings; status changes belong to the human operator. Returns {\"findings\":[...]} or a validation error if the status value is unrecognized."),
			mcp.WithString("status",
				mcp.Description("Optional status filter: one of proposed|approved|applied|rejected|retired|superseded. Omit to list all findings regardless of status."),
			),
		),
		makeFindingListHandler(c),
	)

	// pattern_list — read tool, no agent identity required. Returns patterns
	// newest-first. Patterns are advisory and non-binding; directives win on
	// any conflict.
	s.AddTool(
		mcp.NewTool("pattern_list",
			mcp.WithDescription("List advisory patterns. Patterns are non-binding; directives win on any conflict. Returns {\"patterns\":[...]} newest-first."),
		),
		makePatternListHandler(c),
	)

	// service_record_list — read tool, no agent identity required. Returns service
	// records (calibration notes about crew performance) newest-first. Optional
	// agent filter to return only records about a specific crew member.
	// IMPORTANT: the `agent` parameter names the SUBJECT (which crew member the
	// records are about), not the caller — it is NOT composed with BISHOP_HARNESS.
	s.AddTool(
		mcp.NewTool("service_record_list",
			mcp.WithDescription("List service records (observations about agent performance and behaviour), newest-first. Optional agent filter to view only records about a specific crew member (returns empty list if no matches). Agents can CREATE records only; no update route exists. Returns {\"service_records\":[...]}."),
			mcp.WithString("agent",
				mcp.Description("Optional filter: crew member name (the subject of the records, not the caller). Omit to list all service records regardless of agent."),
			),
		),
		makeServiceRecordListHandler(c),
	)

	// finding_append — write tool. Creates a finding with status='proposed' (always).
	// Status, approver, and date_approved are never accepted; they belong to the
	// human operator alone. Every finding starts in 'proposed' status.
	// Note: mission_id is stored without existence checking — findings.mission_id
	// has no foreign key constraint, so a finding outlives mission-folder cleanup.
	s.AddTool(
		mcp.NewTool("finding_append",
			mcp.WithDescription("Append a finding to the findings ledger. Creates with status='proposed' — agents cannot set or advance status, approver, or date_approved. Those fields belong to the human operator. Returns {\"id\":...,\"created\":true}."),
			mcp.WithString("suggestion",
				mcp.Required(),
				mcp.Description("The finding itself, max 2000 chars. Required."),
			),
			mcp.WithString("finding_date",
				mcp.Description("Optional finding date, max 64 chars."),
			),
			mcp.WithString("target",
				mcp.Description("Optional target entity (agent, skill, or tool concept name), max 256 chars. Passed through unchanged — target names the subject the finding is ABOUT, not the caller. Not composed with harness prefix."),
			),
			mcp.WithString("rationale",
				mcp.Description("Optional supporting rationale, max 2000 chars."),
			),
			mcp.WithString("mission_id",
				mcp.Description("Optional mission ID to associate the finding with, max 128 chars. Stored without existence checking — findings outlive missions."),
			),
		),
		makeFindingAppendHandler(c),
	)

	// pattern_append — write tool. Creates an advisory pattern.
	// Patterns are non-binding; directives win on any conflict.
	s.AddTool(
		mcp.NewTool("pattern_append",
			mcp.WithDescription("Append an advisory pattern to the patterns ledger. Patterns are non-binding; directives win on any conflict. Returns {\"id\":...,\"created\":true}."),
			mcp.WithString("name",
				mcp.Required(),
				mcp.Description("Pattern name, max 256 chars. Required."),
			),
			mcp.WithString("context",
				mcp.Description("Optional context where the pattern applies, max 2000 chars."),
			),
			mcp.WithString("solution",
				mcp.Description("Optional solution or recommendation, max 2000 chars."),
			),
			mcp.WithString("example",
				mcp.Description("Optional worked example, max 2000 chars."),
			),
			mcp.WithString("discovered_at",
				mcp.Description("Optional discovery timestamp, max 64 chars."),
			),
			mcp.WithString("discovered_mission",
				mcp.Description("Optional mission ID where the pattern was discovered, max 128 chars."),
			),
		),
		makePatternAppendHandler(c),
	)

	// service_record_append — write tool. Creates an agent calibration note.
	// Agent is a required subject identifier (the crew member the note is about),
	// NOT the actor. Source must be 'self-reported' or 'bishop-observed' if present.
	s.AddTool(
		mcp.NewTool("service_record_append",
			mcp.WithDescription("Record a service observation about an agent's performance or behaviour. Agent names the subject (which crew member the record is about). Source must be 'self-reported' or 'bishop-observed'. Returns {\"id\":...,\"created\":true}."),
			mcp.WithString("agent",
				mcp.Required(),
				mcp.Description("Agent name (the crew member being recorded about), max 128 chars. Required. Passed through unchanged — not composed with harness prefix."),
			),
			mcp.WithString("record_date",
				mcp.Description("Optional record date, max 64 chars."),
			),
			mcp.WithString("title",
				mcp.Description("Optional title summarizing the observation, max 256 chars."),
			),
			mcp.WithString("note",
				mcp.Description("Optional observation note, max 2000 chars."),
			),
			mcp.WithString("adjustment",
				mcp.Description("Optional adjustment or recommendation, max 2000 chars."),
			),
			mcp.WithString("source",
				mcp.Description("Optional source classification: 'self-reported' or 'bishop-observed'. Anything else is rejected with a 400 error."),
			),
		),
		makeServiceRecordAppendHandler(c),
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
// allocateMissionBody is the POST /v1/missions/allocate payload. It has
// no ID field on purpose: the service picks the id, which is the whole
// reason the endpoint exists.
type allocateMissionBody struct {
	Harness    string `json:"harness"`
	Title      string `json:"title"`
	Owner      string `json:"owner,omitempty"`
	Priority   string `json:"priority,omitempty"`
	NextAction string `json:"next_action,omitempty"`
	Blockers   string `json:"blockers,omitempty"`
	Date       string `json:"date,omitempty"`
}

type createMissionBody struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status,omitempty"`
	Owner      string `json:"owner,omitempty"`
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
	Owner      *string `json:"owner,omitempty"`
	Outcome    *string `json:"outcome,omitempty"`
	Priority   *string `json:"priority,omitempty"`
	NextAction *string `json:"next_action,omitempty"`
	Blockers   *string `json:"blockers,omitempty"`
}

// appendFlightRecorderBody is the JSON body for POST /v1/flight-recorder. Agent is the
// composed "<harness>:<agent>" identity (Phase 3, additive change to
// model.AppendFlightRecorderRequest).
type appendFlightRecorderBody struct {
	MissionID  string `json:"mission_id,omitempty"`
	Step       string `json:"step,omitempty"`
	Event      string `json:"event"`
	Note       string `json:"note"`
	OccurredAt string `json:"occurred_at,omitempty"`
	Agent      string `json:"agent,omitempty"`
}

// missionStepBody is the JSON body for POST /v1/missions/<id>/steps. It
// mirrors the internal/api/missionStepRequest wire shape so mcpd can
// build the request without importing the server's internal package.
type missionStepBody struct {
	Step      string  `json:"step,omitempty"`
	Phase     string  `json:"phase,omitempty"`
	Agent     string  `json:"agent,omitempty"`
	Status    string  `json:"status,omitempty"`
	Notes     string  `json:"notes,omitempty"`
	Summary   string  `json:"summary,omitempty"`
	StartedAt *string `json:"started_at,omitempty"`
	EndedAt   *string `json:"ended_at,omitempty"`
}

// syncBody is the JSON body for POST /v1/documents/sync. Root is
// optional; the server falls back to MEMORY_ROOT then "testdata/memory".
type syncBody struct {
	Root string `json:"root,omitempty"`
}

// createFindingBody is the JSON body for POST /v1/findings. It mirrors
// model.CreateFindingRequest. Status, approver, and date_approved are
// deliberately omitted (agents never set them). Target names the subject
// (the agent, skill, or tool concept the finding is about), not the actor
// writing the finding.
type createFindingBody struct {
	FindingDate string `json:"finding_date,omitempty"`
	Target      string `json:"target,omitempty"`
	Suggestion  string `json:"suggestion"`
	Rationale   string `json:"rationale,omitempty"`
	MissionID   string `json:"mission_id,omitempty"`
}

// createPatternBody is the JSON body for POST /v1/patterns. It mirrors
// model.CreatePatternRequest.
type createPatternBody struct {
	Name              string `json:"name"`
	Context           string `json:"context,omitempty"`
	Solution          string `json:"solution,omitempty"`
	Example           string `json:"example,omitempty"`
	DiscoveredAt      string `json:"discovered_at,omitempty"`
	DiscoveredMission string `json:"discovered_mission,omitempty"`
}

// createServiceRecordBody is the JSON body for POST /v1/service-records. It
// mirrors model.CreateServiceRecordRequest. Agent names the subject (the crew
// member being recorded about), not the actor writing the record.
type createServiceRecordBody struct {
	Agent      string `json:"agent"`
	RecordDate string `json:"record_date,omitempty"`
	Title      string `json:"title,omitempty"`
	Note       string `json:"note,omitempty"`
	Adjustment string `json:"adjustment,omitempty"`
	Source     string `json:"source,omitempty"`
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

// makeMissionListHandler wires the mission_list tool to GET /v1/missions.
func makeMissionListHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("mission_list: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "missions")

		body, status, httpErr := c.do(ctx, http.MethodGet, u.String(), nil)
		if httpErr != nil {
			return toolHTTPError("mission_list", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("mission_list", status, body), nil
		}
		return toolSuccess("mission_list", body), nil
	}
}

// makeMissionGetHandler wires the mission_get tool to GET /v1/missions/<id>.
//
// `id` is escaped with url.PathEscape so a user-supplied ID containing
// "/" or other URL-special bytes cannot escape the path segment. A
// malicious or stray value yields a clean 404 from the server rather
// than a path-injection surprise.
func makeMissionGetHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := strings.TrimSpace(mcp.ParseString(req, "id", ""))
		if id == "" {
			return mcp.NewToolResultError("mission_get: `id` is required and must not be empty/whitespace-only"), nil
		}
		if isDotSegment(id) {
			return mcp.NewToolResultError(`mission_get: "id" must not be "." or ".." — that collapses to the list route instead of a single-mission lookup`), nil
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("mission_get: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "missions", url.PathEscape(id))

		body, status, httpErr := c.do(ctx, http.MethodGet, u.String(), nil)
		if httpErr != nil {
			return toolHTTPError("mission_get", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("mission_get", status, body), nil
		}
		return toolSuccess("mission_get", body), nil
	}
}

// makeMissionAllocateHandler wires the mission_allocate tool to
// POST /v1/missions/allocate.
//
// The harness may be passed as an optional tool parameter (read from the
// calling harness's .claude/bishop-memory.conf, key BISHOP_HARNESS) or
// resolved from the server's BISHOP_HARNESS env var. Parameter takes precedence.
// The env var is user-scoped and identical across projects, so an explicit
// parameter is essential when more than one harness shares this bishop-memory
// instance. The value must always be read from the harness's own config rather
// than invented by the agent — that is where the authoritative harness identity
// lives.
func makeMissionAllocateHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		title := strings.TrimSpace(mcp.ParseString(req, "title", ""))
		if title == "" {
			return mcp.NewToolResultError("mission_allocate: `title` is required"), nil
		}
		harness := strings.TrimSpace(mcp.ParseString(req, "harness", ""))
		if harness == "" {
			harness = strings.TrimSpace(c.harness)
		}
		if harness == "" {
			return mcp.NewToolResultError(
				"mission_allocate: harness is empty — pass it as a parameter from the calling harness's " +
					".claude/bishop-memory.conf, or set BISHOP_HARNESS in the MCP server registration " +
					"so allocated missions can be attributed to a harness"), nil
		}

		body := allocateMissionBody{
			Harness:    harness,
			Title:      title,
			Owner:      strings.TrimSpace(mcp.ParseString(req, "owner", "")),
			Priority:   strings.TrimSpace(mcp.ParseString(req, "priority", "")),
			NextAction: mcp.ParseString(req, "next_action", ""),
			Blockers:   mcp.ParseString(req, "blockers", ""),
			Date:       strings.TrimSpace(mcp.ParseString(req, "date", "")),
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("mission_allocate: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "missions", "allocate")

		payload, merr := json.Marshal(body)
		if merr != nil {
			return toolInternalErr("mission_allocate: marshal request body", merr), nil
		}
		respBody, status, httpErr := c.do(ctx, http.MethodPost, u.String(), payload)
		if httpErr != nil {
			return toolHTTPError("mission_allocate", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("mission_allocate", status, respBody), nil
		}
		return toolSuccess("mission_allocate", respBody), nil
	}
}

// makeMissionCreateHandler wires the mission_create tool to POST /v1/missions.
//
// Per the Step 2 deferral, no agent param — CreateMissionRequest is
// plan-frozen. The caller can attribute the surrounding mission.created
// event via flight_recorder_append if it needs an actor on the audit trail.
func makeMissionCreateHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := strings.TrimSpace(mcp.ParseString(req, "id", ""))
		title := strings.TrimSpace(mcp.ParseString(req, "title", ""))
		if id == "" {
			return mcp.NewToolResultError("mission_create: `id` is required"), nil
		}
		if title == "" {
			return mcp.NewToolResultError("mission_create: `title` is required"), nil
		}

		body := createMissionBody{
			ID:         id,
			Title:      title,
			Status:     strings.TrimSpace(mcp.ParseString(req, "status", "")),
			Owner:      strings.TrimSpace(mcp.ParseString(req, "owner", "")),
			Priority:   strings.TrimSpace(mcp.ParseString(req, "priority", "")),
			NextAction: mcp.ParseString(req, "next_action", ""),
			Blockers:   mcp.ParseString(req, "blockers", ""),
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("mission_create: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "missions")

		payload, merr := json.Marshal(body)
		if merr != nil {
			return toolInternalErr("mission_create: marshal request body", merr), nil
		}
		respBody, status, httpErr := c.do(ctx, http.MethodPost, u.String(), payload)
		if httpErr != nil {
			return toolHTTPError("mission_create", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("mission_create", status, respBody), nil
		}
		return toolSuccess("mission_create", respBody), nil
	}
}

// makeMissionUpdateHandler wires the mission_update tool to PATCH /v1/missions/<id>.
//
// `id` is path-escaped for the same reason as mission_get. Fields the
// caller did not supply are omitted from the JSON body (pointer
// nil → `omitempty` → absent) so the server's COALESCE-based update
// leaves them untouched.
func makeMissionUpdateHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := strings.TrimSpace(mcp.ParseString(req, "id", ""))
		if id == "" {
			return mcp.NewToolResultError("mission_update: `id` is required"), nil
		}
		if isDotSegment(id) {
			return mcp.NewToolResultError(`mission_update: "id" must not be "." or ".." — that collapses to the list/no-route path instead of a single-mission update`), nil
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
		if raw, ok := args["owner"]; ok {
			o := strings.TrimSpace(fmt.Sprint(raw))
			patch.Owner = &o
		}
		if raw, ok := args["outcome"]; ok {
			out := strings.TrimSpace(fmt.Sprint(raw))
			patch.Outcome = &out
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
			return toolInternalErr("mission_update: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "missions", url.PathEscape(id))

		payload, merr := json.Marshal(patch)
		if merr != nil {
			return toolInternalErr("mission_update: marshal request body", merr), nil
		}
		respBody, status, httpErr := c.do(ctx, http.MethodPatch, u.String(), payload)
		if httpErr != nil {
			return toolHTTPError("mission_update", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("mission_update", status, respBody), nil
		}
		return toolSuccess("mission_update", respBody), nil
	}
}

// makeFlightRecorderAppendHandler wires the flight_recorder_append tool to POST /v1/flight-recorder.
//
// Agent identity is composed as "<harness>:<agent>" so the audit
// trail records the originating sub-agent (the per-call `agent`
// argument) AND the harness family (the BISHOP_HARNESS env var). The
// caller MUST supply `agent`; the harness is sourced from the env.
func makeFlightRecorderAppendHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		event := strings.TrimSpace(mcp.ParseString(req, "event", ""))
		note := strings.TrimSpace(mcp.ParseString(req, "note", ""))
		agentParam := strings.TrimSpace(mcp.ParseString(req, "agent", ""))

		if event == "" {
			return mcp.NewToolResultError("flight_recorder_append: `event` is required"), nil
		}
		if note == "" {
			return mcp.NewToolResultError("flight_recorder_append: `note` is required"), nil
		}
		if agentParam == "" {
			return mcp.NewToolResultError("flight_recorder_append: `agent` is required (the sub-agent name, composed with BISHOP_HARNESS as the actor identity)"), nil
		}

		// Agent-identity composition (carry-forward #3): per-call
		// `agent` argument is REQUIRED; BISHOP_HARNESS is the env
		// prefix. No hard-coded fallback at the handler layer — if
		// the harness is empty we still accept the per-call value so
		// the server side never sees a malformed identity.
		agent := composeAgent(c.harness, agentParam)

		body := appendFlightRecorderBody{
			MissionID:  strings.TrimSpace(mcp.ParseString(req, "mission_id", "")),
			Step:       strings.TrimSpace(mcp.ParseString(req, "step", "")),
			Event:      event,
			Note:       note,
			OccurredAt: strings.TrimSpace(mcp.ParseString(req, "occurred_at", "")),
			Agent:      agent,
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("flight_recorder_append: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "flight-recorder")

		payload, merr := json.Marshal(body)
		if merr != nil {
			return toolInternalErr("flight_recorder_append: marshal request body", merr), nil
		}
		respBody, status, httpErr := c.do(ctx, http.MethodPost, u.String(), payload)
		if httpErr != nil {
			return toolHTTPError("flight_recorder_append", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("flight_recorder_append", status, respBody), nil
		}
		return toolSuccess("flight_recorder_append", respBody), nil
	}
}

// makeMissionStepRecordHandler wires the mission_step_record tool to
// POST /v1/missions/<id>/steps.
//
// Same agent-identity composition as flight_recorder_append: per-call `agent`
// required, BISHOP_HARNESS is the env prefix.
func makeMissionStepRecordHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := strings.TrimSpace(mcp.ParseString(req, "id", ""))
		agentParam := strings.TrimSpace(mcp.ParseString(req, "agent", ""))

		if id == "" {
			return mcp.NewToolResultError("mission_step_record: `id` is required"), nil
		}
		if isDotSegment(id) {
			return mcp.NewToolResultError(`mission_step_record: "id" must not be "." or ".." — that collapses the joined path onto an unintended route`), nil
		}
		if agentParam == "" {
			return mcp.NewToolResultError("mission_step_record: `agent` is required (the sub-agent name, composed with BISHOP_HARNESS as the actor identity)"), nil
		}

		agent := composeAgent(c.harness, agentParam)

		// Build the body; optional fields default to "" or are nil
		// depending on whether the caller supplied them.
		body := missionStepBody{
			Step:    strings.TrimSpace(mcp.ParseString(req, "step", "")),
			Phase:   strings.TrimSpace(mcp.ParseString(req, "phase", "")),
			Agent:   agent,
			Status:  strings.TrimSpace(mcp.ParseString(req, "status", "")),
			Notes:   mcp.ParseString(req, "notes", ""),
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
			return toolInternalErr("mission_step_record: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "missions", url.PathEscape(id), "steps")

		payload, merr := json.Marshal(body)
		if merr != nil {
			return toolInternalErr("mission_step_record: marshal request body", merr), nil
		}
		respBody, status, httpErr := c.do(ctx, http.MethodPost, u.String(), payload)
		if httpErr != nil {
			return toolHTTPError("mission_step_record", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("mission_step_record", status, respBody), nil
		}
		return toolSuccess("mission_step_record", respBody), nil
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

// makeMissionStepsListHandler wires the mission_steps_list tool to
// GET /v1/missions/<id>/steps.
//
// `id` is escaped with url.PathEscape and guarded against dot segments
// (same as mission_get and mission_update) to prevent path traversal.
func makeMissionStepsListHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := strings.TrimSpace(mcp.ParseString(req, "id", ""))
		if id == "" {
			return mcp.NewToolResultError("mission_steps_list: `id` is required and must not be empty/whitespace-only"), nil
		}
		if isDotSegment(id) {
			return mcp.NewToolResultError(`mission_steps_list: "id" must not be "." or ".." — that collapses the joined path onto an unintended route`), nil
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("mission_steps_list: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "missions", url.PathEscape(id), "steps")

		body, status, httpErr := c.do(ctx, http.MethodGet, u.String(), nil)
		if httpErr != nil {
			return toolHTTPError("mission_steps_list", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("mission_steps_list", status, body), nil
		}
		return toolSuccess("mission_steps_list", body), nil
	}
}

// makeFindingListHandler wires the finding_list tool to GET /v1/findings?status=<optional>.
//
// The optional status filter validates against the schema enum: proposed, approved,
// applied, rejected, retired, superseded. An out-of-vocabulary value produces a
// clean validation failure (400 from the server).
func makeFindingListHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("finding_list: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "findings")

		// Optional status filter. If supplied, pass it through; server validates.
		if status, ok := req.GetArguments()["status"]; ok {
			statusStr := strings.TrimSpace(fmt.Sprint(status))
			if statusStr != "" {
				qry := u.Query()
				qry.Set("status", statusStr)
				u.RawQuery = qry.Encode()
			}
		}

		body, httpStatus, httpErr := c.do(ctx, http.MethodGet, u.String(), nil)
		if httpErr != nil {
			return toolHTTPError("finding_list", httpErr), nil
		}
		if httpStatus < 200 || httpStatus >= 300 {
			return toolHTTPStatusError("finding_list", httpStatus, body), nil
		}
		return toolSuccess("finding_list", body), nil
	}
}

// makePatternListHandler wires the pattern_list tool to GET /v1/patterns.
//
// No arguments. Returns patterns newest-first.
func makePatternListHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("pattern_list: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "patterns")

		body, httpStatus, httpErr := c.do(ctx, http.MethodGet, u.String(), nil)
		if httpErr != nil {
			return toolHTTPError("pattern_list", httpErr), nil
		}
		if httpStatus < 200 || httpStatus >= 300 {
			return toolHTTPStatusError("pattern_list", httpStatus, body), nil
		}
		return toolSuccess("pattern_list", body), nil
	}
}

// makeServiceRecordListHandler wires the service_record_list tool to GET /v1/service-records?agent=<optional>.
//
// IMPORTANT: The `agent` parameter names the SUBJECT of the records (the crew member
// being recorded about), NOT the actor writing the record. Unlike flight_recorder_append
// and mission_step_record which compose "<harness>:<agent>" to capture the actor,
// service_record_list passes the agent name through UNCHANGED. This is because
// service records are keyed by the subject's name in the filesystem mirror, and
// composing would break that matching. The parameter name is identical to
// composeAgent-using tools, so this comment prevents an oversight.
func makeServiceRecordListHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("service_record_list: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "service-records")

		// Optional agent filter. If supplied, pass it through unchanged (no composition).
		if agent, ok := req.GetArguments()["agent"]; ok {
			agentStr := strings.TrimSpace(fmt.Sprint(agent))
			if agentStr != "" {
				qry := u.Query()
				qry.Set("agent", agentStr)
				u.RawQuery = qry.Encode()
			}
		}

		body, httpStatus, httpErr := c.do(ctx, http.MethodGet, u.String(), nil)
		if httpErr != nil {
			return toolHTTPError("service_record_list", httpErr), nil
		}
		if httpStatus < 200 || httpStatus >= 300 {
			return toolHTTPStatusError("service_record_list", httpStatus, body), nil
		}
		return toolSuccess("service_record_list", body), nil
	}
}

// makeFindingAppendHandler wires the finding_append tool to POST /v1/findings.
//
// Status, approver, and date_approved are deliberately not accepted as arguments.
// Every finding is created with status='proposed', and advancing that status
// belongs to the human operator alone — the server has no write path that sets them
// and there is no update route.
//
// finding_append passes the target name through UNCHANGED. Target names the subject
// of the finding (what it is about), not the writer — it is data about a subject,
// not actor attribution. The field name is identical to those that DO compose
// with harness prefix, so this comment prevents an oversight.
func makeFindingAppendHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		suggestion := strings.TrimSpace(mcp.ParseString(req, "suggestion", ""))
		if suggestion == "" {
			return mcp.NewToolResultError("finding_append: `suggestion` is required and must not be empty/whitespace-only"), nil
		}

		body := createFindingBody{
			FindingDate: strings.TrimSpace(mcp.ParseString(req, "finding_date", "")),
			Target:      strings.TrimSpace(mcp.ParseString(req, "target", "")),
			Suggestion:  suggestion,
			Rationale:   mcp.ParseString(req, "rationale", ""),
			MissionID:   strings.TrimSpace(mcp.ParseString(req, "mission_id", "")),
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("finding_append: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "findings")

		payload, merr := json.Marshal(body)
		if merr != nil {
			return toolInternalErr("finding_append: marshal request body", merr), nil
		}
		respBody, status, httpErr := c.do(ctx, http.MethodPost, u.String(), payload)
		if httpErr != nil {
			return toolHTTPError("finding_append", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("finding_append", status, respBody), nil
		}
		return toolSuccess("finding_append", respBody), nil
	}
}

// makePatternAppendHandler wires the pattern_append tool to POST /v1/patterns.
//
// Patterns are advisory and non-binding; directives win on any conflict.
func makePatternAppendHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := strings.TrimSpace(mcp.ParseString(req, "name", ""))
		if name == "" {
			return mcp.NewToolResultError("pattern_append: `name` is required and must not be empty/whitespace-only"), nil
		}

		body := createPatternBody{
			Name:              name,
			Context:           mcp.ParseString(req, "context", ""),
			Solution:          mcp.ParseString(req, "solution", ""),
			Example:           mcp.ParseString(req, "example", ""),
			DiscoveredAt:      strings.TrimSpace(mcp.ParseString(req, "discovered_at", "")),
			DiscoveredMission: strings.TrimSpace(mcp.ParseString(req, "discovered_mission", "")),
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("pattern_append: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "patterns")

		payload, merr := json.Marshal(body)
		if merr != nil {
			return toolInternalErr("pattern_append: marshal request body", merr), nil
		}
		respBody, status, httpErr := c.do(ctx, http.MethodPost, u.String(), payload)
		if httpErr != nil {
			return toolHTTPError("pattern_append", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("pattern_append", status, respBody), nil
		}
		return toolSuccess("pattern_append", respBody), nil
	}
}

// makeServiceRecordAppendHandler wires the service_record_append tool to
// POST /v1/service-records.
//
// IMPORTANT: The `agent` field names the SUBJECT of the record (the crew member
// being recorded about), NOT the actor writing it. Unlike flight_recorder_append
// and mission_step_record which compose "<harness>:<agent>" to capture the actor,
// service_record_append passes the agent name through UNCHANGED. This is because
// findings/service-records/<agent-name>.md is keyed by the subject's name, not the writer.
// If the field were composed, every record would file under "claude-code:hicks"
// and records would stop matching the files they mirror. The field name is
// identical to those that DO compose, so this comment prevents an oversight.
func makeServiceRecordAppendHandler(c *client) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		agent := strings.TrimSpace(mcp.ParseString(req, "agent", ""))
		if agent == "" {
			return mcp.NewToolResultError("service_record_append: `agent` is required and must not be empty/whitespace-only"), nil
		}

		// Validate source if supplied.
		source := strings.TrimSpace(mcp.ParseString(req, "source", ""))
		if source != "" && source != "self-reported" && source != "bishop-observed" {
			return mcp.NewToolResultError("service_record_append: `source` must be 'self-reported' or 'bishop-observed' (or omitted)"), nil
		}

		body := createServiceRecordBody{
			Agent:      agent,
			RecordDate: strings.TrimSpace(mcp.ParseString(req, "record_date", "")),
			Title:      strings.TrimSpace(mcp.ParseString(req, "title", "")),
			Note:       mcp.ParseString(req, "note", ""),
			Adjustment: mcp.ParseString(req, "adjustment", ""),
			Source:     source,
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr("service_record_append: parse base URL", err), nil
		}
		u = u.JoinPath("v1", "service-records")

		payload, merr := json.Marshal(body)
		if merr != nil {
			return toolInternalErr("service_record_append: marshal request body", merr), nil
		}
		respBody, status, httpErr := c.do(ctx, http.MethodPost, u.String(), payload)
		if httpErr != nil {
			return toolHTTPError("service_record_append", httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError("service_record_append", status, respBody), nil
		}
		return toolSuccess("service_record_append", respBody), nil
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
// agent family and the specific sub-agent (e.g. "claude-code:bishop",
// "claude-code:hicks").
//
// The empty-harness branch below is defensive, not reachable via any
// env var an operator can currently set: envOrDefault("BISHOP_HARNESS",
// defaultHarness) already treats an unset OR explicitly-empty
// BISHOP_HARNESS as unset and substitutes "claude-code", so c.harness is
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
// go.mod (net/url stdlib): mission_get/mission_update with id="." escapes to
// "." and joins "v1/missions/." down to "v1/missions" — the list route,
// returning HTTP 200 with the full mission list instead of a 404 for what
// the caller thinks is a single-mission lookup. id=".." joins "v1/missions/.."
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
