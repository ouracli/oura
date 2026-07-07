# ouracli — Design

An agent-first CLI for the Oura Ring API v2, in Go. Binary name: `oura`.

"Agent-first" means the primary caller is an LLM, not a human:

- **JSON by default on stdout.** Every command emits machine-parseable JSON.
  Humans opt in to pretty output with `--pretty`; agents never have to parse prose.
- **Schema manifest.** `oura schema` emits a JSON description of every
  subcommand: flags, args, stdout shape, exit codes. An agent loads it once and
  drives the tool without guessing at `--help` text.
- **Typed error envelopes.** Every failure prints
  `{"error":{"kind","code","reason","message","hint"}}` on stdout and exits with
  a code mapped 1:1 to `kind`. Agents branch on `.error.kind`.
- **Exit codes as an enum.** Deterministic retry/escalate decisions.
- **NDJSON streaming with terminator.** Paginated fetches with `--all` stream one
  document per line, ending with `{"type":"summary",...}`.
- **Secrets out-of-band.** Tokens live in the OS keyring (macOS Keychain,
  Windows Credential Manager, libsecret) with an AES-256-GCM encrypted file
  fallback. Nothing sensitive flows through an agent's context.
- **Sanitized stderr.** Human progress on stderr, stripped of ANSI/bidi/zero-width
  characters, safe to feed back into a model.
- **MCP server built in.** `oura mcp serve` exposes the same operations as MCP
  tools over stdio via the official Go SDK.

## Oura API v2 facts (validated by live probe + OpenAPI 1.35)

- Base URL: `https://api.ouraring.com/v2`. OpenAPI spec:
  https://cloud.ouraring.com/v2/static/json/openapi-1.35.json (the spec's own
  `servers` field is buggy — hardcode api.ouraring.com).
- Auth: `Authorization: Bearer <token>`. **Personal Access Tokens were
  deprecated December 2025 and can no longer be created** — OAuth2
  authorization-code is the only supported flow. We still accept raw tokens
  via `OURA_TOKEN` / `auth login --token-stdin` for legacy tokens.
- OAuth2 quirks ("the strange auth"):
  - Authorize: `https://cloud.ouraring.com/oauth/authorize` (cloud host);
    token exchange: `https://api.ouraring.com/oauth/token` (api host).
  - App registration at cloud.ouraring.com/oauth/applications (client id +
    secret; 10-user cap until Oura approves the app).
  - Scopes, space-separated: email personal daily heartrate workout tag
    session spo2. Empty scope = request ALL. Users can untick individual
    scopes on consent — the callback's `scope` param is the granted set.
  - No PKCE, no device flow. Loopback redirect (http://localhost:PORT/callback)
    is accepted; the redirect_uri must exactly match a registered one, and
    must be symmetric between authorize and token calls.
  - Auth code: single-use, 10-minute validity.
  - **Refresh tokens are single-use and rotate** — persist the new refresh
    token immediately after every refresh.
  - Revoke: POST https://api.ouraring.com/oauth/revoke?access_token=...
- Success envelope: `{"data":[...], "next_token": string|null}`; personal_info
  returns a bare object.
- Error shape: `{"detail": "..."}` where detail is a string, or an array of
  Pydantic validation objects on 422. 403 = missing scope consent OR lapsed
  Oura membership. 426 is legacy; treat as subscription/app-version error.
- Rate limit: 5000 req / 5 min, per-token AND per-app; 429 carries
  Retry-After, X-RateLimit-{Limit,Window,Reset,Tier}.
- Sandbox: `https://api.ouraring.com/v2/sandbox/usercollection/...` mirrors
  every data endpoint EXCEPT personal_info; requires any non-empty
  Authorization header; shares the real rate limit.
- Pagination: `next_token` response field → query param; loop until null.
- Two endpoint families:
  - date-range: `start_date`/`end_date` (YYYY-MM-DD, default yesterday→today),
    plus `fields` projection and `/{document_id}`.
  - datetime series (`heartrate`, `ring_battery_level`): `start_datetime`/
    `end_datetime` (RFC3339), `latest` bool, no document routes. As of
    OpenAPI 1.35 these also support `fields` (live-verified 2026-07-07).
  - Outliers: `personal_info` (no params, bare object), `ring_configuration`
    (next_token/fields only), path `vO2_max` has a capital O.
- **Date-window semantics are inconsistent per endpoint** (live-probed
  2026-07-07 on a UTC-6 account; the OpenAPI spec is silent on all of this):
  - **End-inclusive** (start=end=D returns day D): `daily_sleep`,
    `sleep_time`, `daily_readiness`, `daily_stress`, `daily_resilience`,
    `daily_spo2`, `daily_cardiovascular_age` — the midnight-anchored dailies.
  - **End-exclusive** (start=end=D returns NOTHING; day D needs end=D+1):
    `daily_activity` (4am-anchored timestamps) and `sleep` periods (keyed to
    bedtime end — so `end_date=today` misses last night's sleep).
  - **`workout` filters on the workout's UTC start time, end-exclusive, not
    on `day`**: a `day=D` workout that started the previous local evening
    (or after D's UTC midnight for negative-offset zones) falls outside
    `[D, D+1)` entirely. To reliably capture day D, widen a day each side
    and filter client-side on `day`.
  - **The sandbox is end-exclusive on every endpoint**, including the
    dailies — it does NOT reproduce the real API's inclusive behavior, so
    don't use it to verify window semantics.
  - Unprobed for lack of account data: `vO2_max`, `session`, `enhanced_tag`,
    `tag`, `rest_mode_period`. Assume exclusive until shown otherwise.
  - ouracli's response: the default window is 7 days ago through **tomorrow**
    (`ouraapi.DefaultDateWindow`) so no-flag calls never silently drop
    today/last night, and every `--end`/`end_date` help string carries the
    exclusivity warning. The universal safe recipe surfaced to agents: to
    get day D, query `[D-1, D+1]` and filter client-side on `day`.
- `fields` projection facts (live-probed 2026-07-07):
  - Unknown names are **silently ignored**; if nothing valid remains the API
    returns FULL documents with HTTP 200. A typo'd projection therefore
    succeeds with the wrong payload — the CLI/MCP validate client-side
    against the registry's per-endpoint Fields list before sending.
  - Some anchor fields are always included even when not requested: `id` +
    `timestamp` on dailies, `bedtime_start` on sleep periods, `timestamp` on
    heartrate/battery, `id` on ring_configuration.
  - `daily_resilience` declares `fields` in the spec but the live API
    accepts and **ignores** it (identical full documents either way), so the
    registry keeps HasFields false there.
  - The **sandbox ignores `fields` entirely** on every endpoint — do not use
    it to verify projection behavior.
- Spec-vs-live drift (probed 2026-07-07): `heartrate` and
  `ring_battery_level` documents carry `producer_timestamp`; the spec's
  `timestamp_unix` does not exist in live responses. The registry follows
  the live shape; the exception is pinned in
  internal/ouraapi/fields_test.go so a future spec revision that fixes it
  fails the test and prompts a re-probe.

## Endpoint registry contract (single source of truth)

`internal/ouraapi/endpoints.go` defines:

```go
type Style int
const (
    StyleDateRange Style = iota  // start_date/end_date + next_token + fields
    StyleDatetimeRange           // start_datetime/end_datetime + latest + next_token
    StyleTokenOnly               // next_token + fields only (ring_configuration)
    StyleSingleObject            // no params, bare object (personal_info)
)

type Endpoint struct {
    CLI        string // CLI command name, e.g. "sleep"
    MCPTool    string // MCP tool name, e.g. "oura_sleep"
    Path       string // "/usercollection/daily_sleep"
    Short      string // one-line description
    Style      Style
    HasDocID   bool     // GET .../{document_id} exists
    HasFields  bool     // the fields query param actually projects
    Sandbox    bool     // available under /v2/sandbox
    Deprecated bool
    Fields     []string // sorted top-level document field names — the valid
                        // fields-projection values when HasFields is true
}

var Endpoints []Endpoint // every v2 usercollection endpoint

func (c *Client) List(ctx context.Context, ep Endpoint, q url.Values) (*ListResponse, error)
func (c *Client) Doc(ctx context.Context, ep Endpoint, id string, fields string) (json.RawMessage, error)
func (c *Client) Object(ctx context.Context, ep Endpoint) (json.RawMessage, error)
```

The cobra data commands, the MCP tool list, and the schema manifest are ALL
generated by iterating `Endpoints`, so the three surfaces cannot drift.

## Command tree

```
oura auth login          # OAuth2 browser flow (loopback) or --token-stdin → keyring
oura auth status         # {"authenticated":true,"method":"pat","scopes":[...]}
oura auth logout         # remove from keyring
oura doctor              # onboarding diagnostics: keyring, network, token validity
oura schema              # agent tool manifest (JSON)
oura sleep [--start --end --all]            # daily_sleep
oura sleep sessions                          # sleep (detailed periods)
oura activity            # daily_activity
oura readiness           # daily_readiness
oura stress              # daily_stress
oura resilience          # daily_resilience
oura spo2                # daily_spo2
oura cardio-age          # daily_cardiovascular_age
oura vo2max              # vO2_max
oura heartrate           # heartrate (datetime params)
oura workouts            # workout
oura sessions            # session
oura tags                # enhanced_tag
oura rest-mode           # rest_mode_period
oura ring                # ring_configuration
oura profile             # personal_info
oura mcp serve           # MCP server on stdio
oura version
```

Global flags: `--sandbox`, `--pretty`, `--config`, `--timeout`, `--token`
(explicit token override, discouraged; env `OURA_TOKEN` also honored).

Every list command supports `--start`/`--end` (default: 7 days ago through
tomorrow — see the date-window semantics above),
`--all` (follow next_token, NDJSON stream), `--next-token`.

## Package layout

```
cmd/oura/            # package main, flat cmd_*.go files (higgs style)
  main.go
  root.go
  cmd_auth.go  cmd_doctor.go  cmd_schema.go  cmd_mcp.go
  cmd_data.go        # generated-ish: one registration per endpoint
internal/ouraapi/    # typed API client: types.go, client.go, endpoints.go
internal/cliauth/    # keyring + AES-GCM fallback + OAuth2 localhost flow
internal/envelope/   # error envelope, exit codes, kinds
internal/output/     # JSON/NDJSON writers, stderr sanitizer, pretty renderer
internal/schema/     # manifest types + generation from command registry
internal/mcpserver/  # MCP tool definitions bridging to ouraapi
```

## Error kinds → exit codes

| code | kind      | meaning                                   | agent action    |
|------|-----------|-------------------------------------------|-----------------|
| 0    | ok        |                                           |                 |
| 1    | internal  | bug / unexpected                          | surface         |
| 2    | auth      | missing/invalid/expired token, 401/403    | run auth login  |
| 3    | usage     | bad flags/args/dates                      | fix invocation  |
| 4    | config    | config file/keyring problems              | run doctor      |
| 5    | api       | Oura 4xx/5xx other than auth              | inspect .reason |
| 6    | network   | timeouts, DNS, connection                 | retry w/ backoff|
| 7    | ratelimit | 429                                       | wait, retry     |
| 8    | subscription | 426 — data requires active Oura sub    | inform user     |

## Onboarding

`oura` with no args and no credentials prints a JSON welcome object with
`next_steps` (agents) and a friendly guide on stderr (humans): try
`oura sleep --sandbox` instantly with zero credentials, then
`oura auth login` for real data. `oura doctor` checks keyring availability,
network reachability, token validity, and subscription status, emitting
pass/fail JSON checks with hints.

## MCP

`oura mcp serve` uses github.com/modelcontextprotocol/go-sdk/mcp with
StdioTransport. Tools mirror the read endpoints (typed args: start_date,
end_date, next_token, sandbox), plus `oura_auth_status` and `oura_version`
(build version/commit/date + sandbox flag, so an agent can always answer
"which version is running?"). Tool results return
the raw API JSON. Errors surface the envelope as tool errors.
