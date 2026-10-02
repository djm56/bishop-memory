// mcpd — the triage profile.
//
// MCPD_PROFILE=triage swaps the harness tool set for the one the findings
// triage agents need (see docs/FINDINGS-TRIAGE.md). The two profiles never
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
			mcp.WithString("kind", mcp.Required(), mcp.Description("classify or process. Required.")),
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
