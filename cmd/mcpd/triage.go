// mcpd — the triage profile.
//
// MCPD_PROFILE=triage swaps the harness tool set for the one the findings
// triage agents need (see the wiki page Developer-Triage-Agents). The two profiles never
// overlap on write tools: a harness session cannot classify or recommend, and a
// triage session cannot allocate missions, append journal rows or create
// findings. Both can read patterns, findings and the search index.
//
// Nothing in either profile reaches POST /v1/findings/:id/decision or the
// directive-proposal decision route: those stay operator-only by never being
// registered here.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// profileHarness and profileTriage are the two values MCPD_PROFILE accepts.
const (
	profileHarness = "harness"
	profileTriage  = "triage"
)

// registerTriageTools registers the triage profile's tool set.
func registerTriageTools(s *server.MCPServer, c *client) {
	// Shared read tools, identical to the harness profile's.
	s.AddTool(
		mcp.NewTool("memory_search",
			mcp.WithDescription("Search bishop-memory imported documents (FTS5) by query string. Returns ranked hits with snippets."),
			mcp.WithString("q", mcp.Required(), mcp.Description("FTS5 query string. Required.")),
			mcp.WithInteger("limit", mcp.Description("Optional cap on results (the server caps at 20).")),
		),
		makeSearchHandler(c),
	)
	s.AddTool(
		mcp.NewTool("pattern_list",
			mcp.WithDescription("List advisory patterns, newest-first. Use them to see whether a finding's suggestion already has a recorded solution."),
		),
		makePatternListHandler(c),
	)
	s.AddTool(
		mcp.NewTool("finding_list",
			mcp.WithDescription("List findings with optional filters. Returns {\"findings\":[...]} with each finding's triage row and pending recommendation when present."),
			mcp.WithString("status", mcp.Description("Optional ledger status filter: proposed|approved|applied|rejected|retired|superseded.")),
			mcp.WithString("category", mcp.Description("Optional primary category slug, or 'uncategorised'.")),
			mcp.WithString("harness", mcp.Description("Optional owning-harness filter.")),
			mcp.WithString("ids", mcp.Description("Optional comma-separated list of finding ids.")),
			mcp.WithInteger("limit", mcp.Description("Optional page size; omit for everything.")),
		),
		makeFindingListHandler(c),
	)

	// Triage read tools.
	s.AddTool(
		mcp.NewTool("triage_categories",
			mcp.WithDescription("List the active finding categories with their descriptions and examples, plus per-category counts of proposed findings and pending recommendations. Classify ONLY into slugs returned here."),
		),
		makeGetProxy(c, "triage_categories", nil, "v1", "finding-categories"),
	)
	s.AddTool(
		mcp.NewTool("triage_next_unclassified",
			mcp.WithDescription("Fetch the next batch of findings that have no classification yet, oldest first. Returns {\"findings\":[...]}; an empty list means classification is complete."),
			mcp.WithInteger("limit", mcp.Description("Batch size, default 50, max 200.")),
		),
		makeNextUnclassifiedHandler(c),
	)
	s.AddTool(
		mcp.NewTool("triage_category_findings",
			mcp.WithDescription("List the proposed findings in one category that have no pending recommendation yet (the processor's work list), oldest first, each with its classification summary."),
			mcp.WithString("category", mcp.Required(), mcp.Description("Category slug. Required.")),
			mcp.WithInteger("limit", mcp.Description("Cap on findings returned (the runner passes TRIAGE_ITEMS_PER_RUN). Omit for all.")),
			mcp.WithBoolean("include_pending", mcp.Description("Set true to also include findings that already have a pending recommendation (for context only; do not re-recommend them).")),
		),
		makeCategoryFindingsHandler(c),
	)
	s.AddTool(
		mcp.NewTool("triage_recent_decisions",
			mcp.WithDescription("The operator's most recent decisions in a category, each with the recommendation that preceded it. Read these before recommending so your recommendations follow the operator's demonstrated judgement."),
			mcp.WithString("category", mcp.Description("Category slug. Omit for every category.")),
			mcp.WithInteger("limit", mcp.Description("Default 20.")),
		),
		makeRecentDecisionsHandler(c),
	)

	// Triage write tools.
	s.AddTool(
		mcp.NewTool("triage_run_start",
			mcp.WithDescription("Open a triage run row. Call once at the start; pass the returned id as run_id on every write and to triage_run_finish."),
			mcp.WithString("kind", mcp.Required(), mcp.Description("classify, process or grade. Required.")),
			mcp.WithString("category", mcp.Description("For a process run: the category being processed.")),
			mcp.WithString("model", mcp.Description("The model id doing the work, e.g. claude-haiku-4-5-20251001.")),
		),
		makeBodyProxy(c, "triage_run_start", http.MethodPost, []string{"kind", "category", "model"}, []string{"kind"}, "v1", "triage", "runs"),
	)
	s.AddTool(
		mcp.NewTool("triage_run_finish",
			mcp.WithDescription("Close a triage run with its counts. A finished process run advances the category rotation."),
			mcp.WithInteger("id", mcp.Required(), mcp.Description("Run id from triage_run_start. Required.")),
			mcp.WithString("status", mcp.Required(), mcp.Description("done or failed. Required.")),
			mcp.WithInteger("considered", mcp.Description("How many findings were read.")),
			mcp.WithInteger("written", mcp.Description("How many rows were written (classifications or recommendations).")),
			mcp.WithString("notes", mcp.Description("One paragraph: what was done, anything the operator should know.")),
		),
		makeRunFinishHandler(c),
	)
	s.AddTool(
		mcp.NewTool("triage_classify",
			mcp.WithDescription("Write classifications for a batch of findings (upsert; re-classifying a finding replaces its row). The server rejects the whole batch if any category slug is not active or any finding_id is unknown."),
			mcp.WithString("classified_by", mcp.Required(), mcp.Description("Model id doing the classification. Required.")),
			mcp.WithInteger("run_id", mcp.Description("Run id from triage_run_start.")),
			mcp.WithArray("items", mcp.Required(), mcp.Description("Array of {finding_id, category, secondary_category?, directive_candidate?, confidence (0-1), summary (<=140 chars)}. Required, 1-200 items.")),
		),
		makeBodyProxy(c, "triage_classify", http.MethodPut, []string{"classified_by", "run_id", "items"}, []string{"classified_by", "items"}, "v1", "triage", "classifications"),
	)
	s.AddTool(
		mcp.NewTool("triage_group_create",
			mcp.WithDescription("Create a group: a cluster of findings that are instances of one underlying rule. Returns {\"id\":...}; pass it as group_id in triage_recommend and directive_propose."),
			mcp.WithString("category", mcp.Required(), mcp.Description("Category slug. Required.")),
			mcp.WithString("title", mcp.Required(), mcp.Description("Short name for the rule, <=200 chars. Required.")),
			mcp.WithString("summary", mcp.Required(), mcp.Description("The one rule every member is an instance of, 1-3 sentences. Required.")),
			mcp.WithString("target", mcp.Description("The doctrine file the fix belongs in, e.g. .claude/skills/mission-lifecycle/SKILL.md.")),
			mcp.WithInteger("run_id", mcp.Description("Run id from triage_run_start.")),
		),
		makeBodyProxy(c, "triage_group_create", http.MethodPost, []string{"category", "title", "summary", "target", "run_id"}, []string{"category", "title", "summary"}, "v1", "finding-groups"),
	)
	s.AddTool(
		mcp.NewTool("triage_recommend",
			mcp.WithDescription("Write recommendations for a batch of proposed findings. Each item: {finding_id, recommendation: approve|reject|supersede|defer, rationale, group_id?, superseded_by? (required for supersede), proposed_change? (exact before/after text for an approve)}. A finding that is no longer proposed is skipped and reported. Never changes a finding's status."),
			mcp.WithInteger("run_id", mcp.Description("Run id from triage_run_start.")),
			mcp.WithArray("items", mcp.Required(), mcp.Description("Array of recommendation items. Required, 1-200.")),
		),
		makeBodyProxy(c, "triage_recommend", http.MethodPost, []string{"run_id", "items"}, []string{"items"}, "v1", "finding-recommendations"),
	)
	s.AddTool(
		mcp.NewTool("directive_propose",
			mcp.WithDescription("Draft a DIRECTIVES.md entry for the operator to ratify. Fields mirror the DIRECTIVES-TEMPLATE; the server rejects a draft whose rendered entry exceeds 1,510 characters. This creates a PROPOSAL only; a human ratifies it on the review page."),
			mcp.WithString("title", mcp.Required(), mcp.Description("<=60 chars. Required.")),
			mcp.WithString("applies_when", mcp.Required(), mcp.Description("Observable trigger, phrased 'A change that <verb>s <object>'. Required.")),
			mcp.WithString("rule", mcp.Required(), mcp.Description("One to four sentences, MUST / MUST NOT, a position not a site list. Required.")),
			mcp.WithString("rationale", mcp.Required(), mcp.Description("One sentence: the failure mode this prevents. Required.")),
			mcp.WithString("reviewer_check", mcp.Required(), mcp.Description("One or two yes/no questions against the change. Required.")),
			mcp.WithString("example", mcp.Description("Optional one-line synthetic example (the 'Illustrative —' prefix is added for you).")),
			mcp.WithArray("evidence", mcp.Required(), mcp.Description("Array of finding ids that are instances of this rule. Required.")),
			mcp.WithInteger("group_id", mcp.Description("The group this directive generalises.")),
			mcp.WithString("harness", mcp.Description("Which harness's DIRECTIVES.md this is for. Omit when it applies to every harness.")),
			mcp.WithInteger("run_id", mcp.Description("Run id from triage_run_start.")),
		),
		makeBodyProxy(c, "directive_propose", http.MethodPost,
			[]string{"title", "applies_when", "rule", "rationale", "reviewer_check", "example", "evidence", "group_id", "harness", "run_id"},
			[]string{"title", "applies_when", "rule", "rationale", "reviewer_check", "evidence"},
			"v1", "directive-proposals"),
	)

	// Mission-grader tools. There is no tool for reopening a graded mission:
	// regrading is the operator's DELETE /v1/mission-grades/:id.
	s.AddTool(
		mcp.NewTool("grade_claim",
			mcp.WithDescription("Claim up to 10 finished missions that have no grade and return a grading packet for each, as plain text: one '=== MISSION <id>' block per mission with its row, the signals the server counted (criteria met, steps by status, injected steps, escalations, QA steps, blocked events, findings), the brief's goal and acceptance criteria, the debrief's judgement sections, the steps, the findings and the agent notes. Everything you may grade on is in the packet. 'NO MISSIONS WAITING' means there is nothing to grade. If the result was saved to a file, read the whole file before grading. Call it once per run."),
			mcp.WithInteger("run_id", mcp.Description("Run id from triage_run_start.")),
			mcp.WithInteger("limit", mcp.Description("How many missions to claim, 1-10 (default 10).")),
			mcp.WithArray("mission_ids", mcp.Description("Optional: claim exactly these missions (the runner passes them when the operator named some).")),
		),
		makeGradeClaimHandler(c),
	)
	s.AddTool(
		mcp.NewTool("grade_write",
			mcp.WithDescription("Write the verdicts for missions you claimed. Each item: {mission_id, grade: A|B|C|D|E|F, summary, suggestions} or {mission_id, insufficient: true, summary} when the packet does not hold enough to grade. A verdict is final; an unclaimed mission or one that already has a verdict is skipped and reported."),
			mcp.WithString("graded_by", mcp.Required(), mcp.Description("Model id doing the grading. Required.")),
			mcp.WithInteger("run_id", mcp.Description("Run id from triage_run_start.")),
			mcp.WithArray("items", mcp.Required(), mcp.Description("Array of verdict items, 1-10. Required.")),
		),
		makeBodyProxy(c, "grade_write", http.MethodPost, []string{"graded_by", "run_id", "items"}, []string{"graded_by", "items"}, "v1", "mission-grades"),
	)
}

// --- Generic proxies -----------------------------------------------------------

type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// makeGetProxy proxies a tool to a fixed GET path. query, when non-nil, is
// called with the request arguments to add query parameters.
func makeGetProxy(c *client, tool string, query func(args map[string]any, q url.Values), pathParts ...string) toolHandler {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr(tool+": parse base URL", err), nil
		}
		u = u.JoinPath(pathParts...)
		if query != nil {
			q := u.Query()
			query(req.GetArguments(), q)
			u.RawQuery = q.Encode()
		}
		body, status, httpErr := c.do(ctx, http.MethodGet, u.String(), nil)
		if httpErr != nil {
			return toolHTTPError(tool, httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError(tool, status, body), nil
		}
		return toolSuccess(tool, body), nil
	}
}

// makeBodyProxy proxies a tool to a JSON-body route. keys are the argument
// names copied verbatim into the body (absent ones are omitted); required
// names must be present and, for strings, non-blank. Arrays and objects pass
// through as-is so the server's binding tags validate their shape.
func makeBodyProxy(c *client, tool, method string, keys, required []string, pathParts ...string) toolHandler {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		body := map[string]any{}
		for _, k := range keys {
			if v, ok := args[k]; ok && v != nil {
				if s, isString := v.(string); isString {
					if strings.TrimSpace(s) == "" {
						continue
					}
					v = strings.TrimSpace(s)
				}
				body[k] = v
			}
		}
		for _, k := range required {
			if _, ok := body[k]; !ok {
				return mcp.NewToolResultErrorf("%s: `%s` is required and must not be empty", tool, k), nil
			}
		}
		u, err := url.Parse(c.baseURL)
		if err != nil {
			return toolInternalErr(tool+": parse base URL", err), nil
		}
		u = u.JoinPath(pathParts...)
		payload, err := json.Marshal(body)
		if err != nil {
			return toolInternalErr(tool+": marshal request body", err), nil
		}
		respBody, status, httpErr := c.do(ctx, method, u.String(), payload)
		if httpErr != nil {
			return toolHTTPError(tool, httpErr), nil
		}
		if status < 200 || status >= 300 {
			return toolHTTPStatusError(tool, status, respBody), nil
		}
		return toolSuccess(tool, respBody), nil
	}
}

func intArg(args map[string]any, key string) (int64, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return parsed, err == nil
	}
	return 0, false
}

func makeNextUnclassifiedHandler(c *client) toolHandler {
	return makeGetProxy(c, "triage_next_unclassified", func(args map[string]any, q url.Values) {
		limit := int64(50)
		if n, ok := intArg(args, "limit"); ok && n > 0 {
			limit = n
		}
		if limit > 200 {
			limit = 200
		}
		q.Set("unclassified", "1")
		q.Set("order", "asc")
		q.Set("limit", strconv.FormatInt(limit, 10))
	}, "v1", "findings")
}

func makeCategoryFindingsHandler(c *client) toolHandler {
	inner := makeGetProxy(c, "triage_category_findings", func(args map[string]any, q url.Values) {
		q.Set("category", strings.TrimSpace(fmt.Sprint(args["category"])))
		q.Set("status", "proposed")
		q.Set("order", "asc")
		if include, _ := args["include_pending"].(bool); !include {
			q.Set("no_pending", "1")
		}
		if n, ok := intArg(args, "limit"); ok && n > 0 {
			q.Set("limit", strconv.FormatInt(n, 10))
		}
	}, "v1", "findings")
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if strings.TrimSpace(mcp.ParseString(req, "category", "")) == "" {
			return mcp.NewToolResultError("triage_category_findings: `category` is required"), nil
		}
		return inner(ctx, req)
	}
}

func makeRecentDecisionsHandler(c *client) toolHandler {
	return makeGetProxy(c, "triage_recent_decisions", func(args map[string]any, q url.Values) {
		if cat := strings.TrimSpace(mcp.ParseString(mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}}, "category", "")); cat != "" {
			q.Set("category", cat)
		}
		if n, ok := intArg(args, "limit"); ok && n > 0 {
			q.Set("limit", strconv.FormatInt(n, 10))
		}
	}, "v1", "triage", "decisions")
}

func makeRunFinishHandler(c *client) toolHandler {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		id, ok := intArg(args, "id")
		if !ok || id <= 0 {
			return mcp.NewToolResultError("triage_run_finish: `id` is required and must be a positive integer"), nil
		}
		proxy := makeBodyProxy(c, "triage_run_finish", http.MethodPatch,
			[]string{"status", "considered", "written", "notes"}, []string{"status"},
			"v1", "triage", "runs", strconv.FormatInt(id, 10))
		return proxy(ctx, req)
	}
}

// makeGradeClaimHandler proxies grade_claim and returns the packets as plain
// text rather than the server's JSON. Ten packets are tens of kilobytes on
// one JSON line; OpenCode saves a result that large to a file, and its read
// tool cuts every line at 2,000 characters, so the grader saw nothing. Text
// keeps every line short and is smaller to read either way.
func makeGradeClaimHandler(c *client) toolHandler {
	proxy := makeBodyProxy(c, "grade_claim", http.MethodPost, []string{"run_id", "limit", "mission_ids"}, nil, "v1", "mission-grades", "claim")
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := proxy(ctx, req)
		if err != nil || result == nil || result.IsError || len(result.Content) != 1 {
			return result, err
		}
		text, ok := result.Content[0].(mcp.TextContent)
		if !ok {
			return result, nil
		}
		rendered, err := renderGradingPackets([]byte(text.Text))
		if err != nil {
			return result, nil // the JSON as it came: still usable, never lost
		}
		return mcp.NewToolResultText(rendered), nil
	}
}

// renderGradingPackets turns POST /v1/mission-grades/claim's JSON into one
// plain-text block per mission.
func renderGradingPackets(body []byte) (string, error) {
	var response struct {
		Missions []struct {
			Mission  map[string]any    `json:"mission"`
			Signals  map[string]any    `json:"signals"`
			Brief    map[string]string `json:"brief"`
			Debrief  map[string]string `json:"debrief"`
			Steps    []string          `json:"steps"`
			Findings []string          `json:"findings"`
			Notes    []string          `json:"notes"`
		} `json:"missions"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", err
	}
	if len(response.Missions) == 0 {
		return "NO MISSIONS WAITING: every finished mission has a verdict. Close the run with considered 0, written 0.", nil
	}
	str := func(v any) string {
		if v == nil {
			return "—"
		}
		return fmt.Sprint(v)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CLAIMED %d MISSIONS. Grade every one, then make ONE grade_write call with all of them.\n", len(response.Missions))
	for _, p := range response.Missions {
		m, s := p.Mission, p.Signals
		fmt.Fprintf(&b, "\n=== MISSION %s — %s\n", str(m["id"]), str(m["title"]))
		fmt.Fprintf(&b, "harness: %s | status: %s | outcome: %s | opened: %s | closed: %s | duration_minutes: %s\n",
			str(m["harness"]), str(m["status"]), str(m["outcome"]), str(m["opened_at"]), str(m["closed_at"]), str(m["duration_minutes"]))
		fmt.Fprintf(&b, "SIGNALS: criteria planned %s, met %s, partial %s, unmet %s, unclear %s | steps %s by status %s | injected steps %s | escalation steps %s | QA steps %s | blocked events %s | findings %s by status %s | has brief %s, has debrief %s\n",
			str(s["criteria_planned"]), str(s["criteria_met"]), str(s["criteria_partial"]), str(s["criteria_unmet"]), str(s["criteria_unclear"]),
			str(s["steps"]), compactJSON(s["steps_by_status"]), str(s["injected_steps"]), str(s["escalation_steps"]), str(s["qa_steps"]),
			str(s["blocked_events"]), str(s["findings"]), compactJSON(s["findings_by_status"]), str(s["has_brief"]), str(s["has_debrief"]))
		writeBlock(&b, "GOAL", p.Brief["goal"])
		writeBlock(&b, "ACCEPTANCE CRITERIA (brief)", p.Brief["acceptance_criteria"])
		for _, name := range []string{"Mission Summary", "Acceptance Criteria Outcome", "Tracker And Reality", "Wrong Assumptions", "Sub-Agent Mistakes", "QA Verdict"} {
			if text, ok := p.Debrief[name]; ok {
				writeBlock(&b, "DEBRIEF — "+name, text)
			}
		}
		writeList(&b, "STEPS", p.Steps)
		writeList(&b, "FINDINGS", p.Findings)
		writeList(&b, "AGENT NOTES", p.Notes)
	}
	return b.String(), nil
}

func writeBlock(b *strings.Builder, heading, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		fmt.Fprintf(b, "%s: (none)\n", heading)
		return
	}
	fmt.Fprintf(b, "%s:\n%s\n", heading, text)
}

func writeList(b *strings.Builder, heading string, items []string) {
	if len(items) == 0 {
		fmt.Fprintf(b, "%s: (none)\n", heading)
		return
	}
	fmt.Fprintf(b, "%s:\n", heading)
	for _, item := range items {
		fmt.Fprintf(b, "- %s\n", strings.ReplaceAll(item, "\n", " "))
	}
}

func compactJSON(v any) string {
	if v == nil {
		return "{}"
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}
