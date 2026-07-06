# MCP server

`oura mcp serve` runs a [Model Context Protocol](https://modelcontextprotocol.io)
server on stdio, built with the official
[`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk).
It registers one tool per entry in `internal/ouraapi/endpoints.go` — the same
registry that generates the `oura` cobra commands and the `oura schema`
manifest — plus one diagnostic tool, `oura_auth_status`. Because all three
surfaces are generated from the same list, the tool set below can never drift
from what the CLI supports; regenerate it yourself at any time with
`oura mcp serve` and a `tools/list` call (see the bottom of this file), or by
reading `internal/ouraapi/endpoints.go` directly.

Add `--sandbox` to the serve command to route every tool call to Oura's
credential-free sandbox environment instead of the real API — handy for
trying the server with zero setup.

```sh
oura mcp serve             # real API, needs credentials (keyring or OURA_TOKEN)
oura mcp serve --sandbox   # fake data, no credentials needed
```

stdout is owned entirely by the MCP transport once `serve` starts; human
progress (e.g. the startup banner) goes to stderr.

## Result and error shape

Every tool returns a single `text` content block. On success it's the raw
Oura JSON (the `{"data":[...],"next_token":...}` envelope for collections, or
a bare object for `oura_profile`/`oura_auth_status`). On failure it's the
same envelope the CLI prints to stdout, with `isError: true` set on the
result so a model sees the failure and can self-correct:

```json
{"content":[{"type":"text","text":"{\"error\":{\"kind\":\"auth\",\"code\":2,\"reason\":\"no_credentials\",\"message\":\"no Oura credentials are configured\",\"hint\":\"run 'oura auth login', set OURA_TOKEN, or use --sandbox for fake data\"}}"}],"isError":true}
```

See `AGENTS.md` / `README.md` for the full `.error.kind` → exit-code →
reaction table; it applies identically here.

## Tool list

Every data tool accepts a subset of these arguments, depending on the
endpoint's parameter style (shown per tool below):

| arg | meaning |
|---|---|
| `start_date` / `end_date` | `YYYY-MM-DD`; default last 7 days |
| `start_datetime` / `end_datetime` | RFC3339, e.g. `2026-07-01T00:00:00Z` |
| `latest` | bool — only the single most recent sample (datetime-series tools only) |
| `next_token` | pagination cursor from a previous call's response |
| `fields` | comma-separated field projection (only on endpoints that support it) |

| tool | endpoint | args | notes |
|---|---|---|---|
| `oura_sleep` | `daily_sleep` | date range + fields | |
| `oura_sleep_periods` | `sleep` | date range + fields | |
| `oura_sleep_time` | `sleep_time` | date range + fields | |
| `oura_activity` | `daily_activity` | date range + fields | |
| `oura_readiness` | `daily_readiness` | date range + fields | |
| `oura_stress` | `daily_stress` | date range + fields | |
| `oura_resilience` | `daily_resilience` | date range (no `fields`) | |
| `oura_spo2` | `daily_spo2` | date range + fields | |
| `oura_cardio_age` | `daily_cardiovascular_age` | date range + fields | |
| `oura_vo2max` | `vO2_max` | date range + fields | |
| `oura_heartrate` | `heartrate` | datetime range + `latest` | no `fields` |
| `oura_battery` | `ring_battery_level` | datetime range + `latest` | no `fields` |
| `oura_workouts` | `workout` | date range + fields | |
| `oura_sessions` | `session` | date range + fields | |
| `oura_tags` | `enhanced_tag` | date range + fields | |
| `oura_tags_legacy` | `tag` | date range + fields | deprecated — prefer `oura_tags` |
| `oura_rest_mode` | `rest_mode_period` | date range + fields | |
| `oura_ring` | `ring_configuration` | `next_token` + `fields` only | no date window |
| `oura_profile` | `personal_info` | none | bare object, not a collection; **no sandbox route** |
| `oura_auth_status` | — | none | reports auth method/backend/scopes/expiry; no secrets, no network call |

Every tool's `description` (visible via `tools/list`) is generated from the
endpoint's `Short` summary plus a parameter guide, so a client that only
reads tool descriptions still has enough to call it correctly without this
file.

## Example: `tools/list` and `tools/call`

Piping raw JSON-RPC at `oura mcp serve --sandbox` (as an MCP client library
would over stdio) — request an initialize handshake, then list tools, then
call one:

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0.0.1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"oura_sleep","arguments":{"start_date":"2026-07-01","end_date":"2026-07-02"}}}
```

produces, among the `tools/list` results, this entry for `oura_sleep`:

```json
{
  "name": "oura_sleep",
  "description": "Daily sleep score (0-100) with contributor breakdown: deep sleep, REM, latency, efficiency, restfulness, and timing. Params: start_date/end_date (YYYY-MM-DD, default last 7 days), next_token to paginate, fields to project columns.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "start_date": {"type": "string", "description": "start date YYYY-MM-DD (default: 7 days ago)"},
      "end_date": {"type": "string", "description": "end date YYYY-MM-DD (default: today)"},
      "next_token": {"type": "string", "description": "pagination cursor: pass a previous response's next_token to fetch the next page"},
      "fields": {"type": "string", "description": "comma-separated field projection; only honored on endpoints that support it"}
    },
    "additionalProperties": false
  }
}
```

and the `tools/call` on `oura_sleep` returns:

```json
{
  "content": [
    {
      "type": "text",
      "text": "{\"data\":[{\"id\":\"daily_sleep-0-2026-7-1\",\"contributors\":{\"deep_sleep\":70,\"efficiency\":80,\"latency\":90,\"rem_sleep\":60,\"restfulness\":70,\"timing\":80,\"total_sleep\":90},\"day\":\"2026-07-01\",\"score\":80,\"timestamp\":\"2026-07-01T00:00:00.000+00:00\"}],\"next_token\":null}"
    }
  ]
}
```

## Registering with a client

```sh
claude mcp add oura -- oura mcp serve
claude mcp add oura-sandbox -- oura mcp serve --sandbox
```

or in `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "oura": {
      "command": "oura",
      "args": ["mcp", "serve"]
    }
  }
}
```
