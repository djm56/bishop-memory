# Developer: MCP Adapter

`cmd/mcpd` is a stdio Model Context Protocol server built on `github.com/mark3labs/mcp-go`. It is a pure HTTP client of `memoryd`: it imports none of the service's internal packages and never opens the database. Every tool builds a request against `BISHOP_MEMORY_URL`, forwards the JSON body as text on success, and turns every failure into an MCP error result that names the tool.

## Configuration

| Variable | Default | Effect |
|---|---|---|
| `BISHOP_MEMORY_URL` | `http://127.0.0.1:8787` | Validated at start with `url.ParseRequestURI`; must be absolute |
| `BISHOP_HARNESS` | `claude-code` | Composed into actor identity and stored on findings; set per harness by its `.mcp.json` |
| `MCPD_PROFILE` | `harness` | `harness` registers the 16 crew tools (`main.go`); `triage` registers the 13 triage tools (`triage.go`); anything else is fatal |

The harness's `connect-bishop-memory.sh` writes `.mcp.json` at project scope so each harness carries its own `BISHOP_HARNESS`; a user-scope registration would give every project the same name and collide on mission ids.

## Structure of `main.go`

- `client` holds two `http.Client`s (30 s default; 5 min for `documents_sync`, whose duration scales with the caller's tree), the base URL and the harness.
- `registerTools(s, c)` registers each tool with `mcp.NewTool(name, mcp.WithDescription, mcp.WithString/Integer/Boolean/Array …)` and a `make…Handler(c)` closure.
- Wire-shape structs (`createFindingBody` etc.) redeclare the JSON bodies instead of importing `internal/model`, so `mcpd` stays decoupled and the documented wire shape is the contract.
- Helpers: `doWithClient`, `toolHTTPError`, `toolHTTPStatusError` (truncates the body to 512 bytes), `toolInternalErr`, `toolSuccess` (refuses a 2xx with malformed JSON), `isDotSegment`, `composeAgent`, `envOrDefault`.

Two guards are easy to forget when adding a tool:

- `mcp.Required()` is schema metadata only. The framework does not enforce it; every required argument needs a handler check (`finding_append: suggestion is required…`).
- Path parameters go through `url.PathEscape` and `JoinPath`, which cleans `.` and `..` **after** escaping. `isDotSegment` refuses those before any request, otherwise `mission_get` with `.` returns the whole mission list.

## Structure of `triage.go`

The triage profile uses two generic proxies so each tool is a few lines:

- `makeGetProxy(c, tool, queryFn, pathParts…)` — a GET with an optional function that maps arguments to query parameters.
- `makeBodyProxy(c, tool, method, keys, required, pathParts…)` — copies the named arguments into a JSON body (trimming strings, dropping blanks), checks the required ones, sends. Arrays and objects pass through untouched so the server's binding tags validate their shape.

`triage_run_finish` wraps a body proxy to put the run id in the path. `triage_next_unclassified` and `triage_category_findings` are GET proxies that fix the filters (`unclassified=1&order=asc`, `status=proposed&no_pending=1`).

## Adding a tool

1. Decide the profile. A crew-facing tool goes in `registerTools`; a triage tool in `registerTriageTools`. A tool must never appear in both unless it is read-only.
2. Register it with a description that says what comes back and what is validated server-side. Descriptions are what the model reads; write them for the model.
3. Use a generic proxy where the route is a plain GET or JSON POST; write a handler only when the tool composes identity, guards a path parameter, or needs a special client.
4. Add the tool's method and path to `mcpdPaths` in `cmd/mcpd/main_test.go`. `TestMCPDRoutePathsMatchServerRoutes` instantiates the real router and asserts exactly one registered route matches, so a typo in a path segment fails the suite.
5. Document it in `README.md`'s tool table and the wiki's [MCP Tool Reference](MCP-Tool-Reference) (run `scripts/gen-mcp-reference.py`, adding an example to its `EXAMPLES` table).

## Testing a profile by hand

```bash
printf '%s\n' \
 '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}' \
 '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
 '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
 | MCPD_PROFILE=triage bin/mcpd 2>/dev/null | python3 -c 'import sys,json
for l in sys.stdin:
    d=json.loads(l)
    if d.get("id")==2: print(sorted(t["name"] for t in d["result"]["tools"]))'
```

## Why the profile is the access-control model

The HTTP API's API keys (see [Developer: HTTP API](Developer-HTTP-API#authentication)) decide which machines may call the service at all, and every key has full access, so they do not separate an agent from the operator. `mcpd` sends the key itself: it reads `BISHOP_MEMORY_API_KEY` (from its environment, or from `~/.config/bishop-memory/client.env` for any variable the environment does not set) and adds `Authorization: Bearer` to every request, so no key appears in a harness's `.mcp.json`. What an agent can do is exactly the set of tools its `mcpd` registers. So the operator routes — the finding decision, the directive-proposal decision, the directive upsert, the harness backfill — are never registered in either profile, and the triage agents cannot touch missions, the journal or the ledger. Any change that registers one of those routes as a tool changes the security model and belongs in a design discussion, not a quick patch.
