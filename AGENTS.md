# AGENTS.md — operating manual for driving `oura`

You are an LLM (or an agent runtime acting on one) calling the `oura` CLI, or
its MCP twin (`oura mcp serve`). This file is the terse version of the
contract; `README.md` has the prose and examples.

## The contract

- **stdout is JSON, and only JSON.** Exactly one of: a single JSON document,
  an NDJSON stream terminated by a `{"type":"summary",...}` line, or an
  error envelope `{"error":{...}}`. Never prose, never a partial line you
  need to guess the end of.
- **stderr is for humans.** Progress messages, warnings, onboarding hints.
  It is sanitized (no ANSI/bidi/zero-width characters) and safe to read if
  you want extra context, but you should never need to parse it to know
  whether a call succeeded — the exit code and stdout already tell you that.
- **Load `oura schema` once, at the start of a session**, instead of
  shelling out to `--help`. It gives you every command's flags (name, type,
  default, description), positional args, stdout shape (`"json"` or
  `"json|ndjson(--all)"`), and exit codes, reflected live off the actual
  command tree — it cannot go stale relative to the binary you're running.
  `oura schema <command>` scopes it to one command.

## Reacting to failure

Every non-zero exit prints exactly one JSON object to stdout:

```json
{"error":{"kind":"auth","code":2,"reason":"token_rejected","message":"HTTP 401: ...","hint":"run 'oura auth login' to store a valid token, or check OURA_TOKEN"}}
```

`kind`/`code` are 1:1 and stable — branch on `.error.kind` (or the exit
code, they always agree) to decide what to do next. `.error.hint` is always
an actionable next command or fact, not filler; read it before improvising.

| code | kind | what happened | what you should do |
|------|------|----------------|---------------------|
| 0 | `ok` | success | proceed |
| 1 | `internal` | a bug in ouracli | report it verbatim; don't retry blindly |
| 2 | `auth` | missing/invalid/expired credentials, HTTP 401/403 | run `oura auth login`, or check `OURA_TOKEN` if you set it; **do not loop retrying** — it will not fix itself |
| 3 | `usage` | you passed something invalid (bad flag, bad date, unknown command) | fix the invocation; consult `oura schema` if unsure of a flag name or type |
| 4 | `config` | keyring/config-file problem, independent of any one request | run `oura doctor` and act on its hints before retrying |
| 5 | `api` | Oura rejected or failed the request for a reason other than auth (4xx/5xx) | inspect `.error.reason` and `.error.message`; often not retryable without changing the request |
| 6 | `network` | timeout, DNS failure, connection refused | transient — retry with backoff; consider raising `--timeout` |
| 7 | `ratelimit` | HTTP 429 | back off; `.error.hint` carries the `Retry-After` seconds when Oura sent one |
| 8 | `subscription` | HTTP 426 — this data requires an active Oura membership | not fixable by retrying or by you; tell the user |

A cobra-level usage error (unknown flag, wrong arg count) is coerced into the
same `{"error":{"kind":"usage",...}}` shape and exit 3 — you never have to
special-case cobra's own error text.

## Sandbox mode

Every data command accepts `--sandbox`, which routes to Oura's public
sandbox environment (`/v2/sandbox/usercollection/...`) instead of the real
API. It requires **no credentials at all** and returns realistic fake data
for every endpoint except `profile` (which has no sandbox route and returns
a `usage`/`no_sandbox_route` error if you pass `--sandbox` to it). Use it to
verify a query shape or explore the JSON before touching real credentials,
or whenever you have none.

`oura mcp serve --sandbox` runs the same sandbox routing for every MCP tool
call in that server session.

## Fields projection

Most collection endpoints accept `--fields` (`fields` over MCP), a
comma-separated projection that shrinks each document to just the fields you
name (plus a per-endpoint anchor like `id`/`timestamp` that Oura always
includes). The valid names for each command are listed in `oura schema`
(the command's `fields` array and the `--fields` flag description) and in
each MCP tool's description.

Unknown names are validated **client-side** and rejected with an
`unknown_field` usage error whose hint lists the valid names — the Oura API
itself silently ignores unknown fields and would return full documents, so
without this check a typo'd projection would "succeed" with the wrong,
much larger payload. Note the sandbox ignores projections entirely; don't
use `--sandbox` to check what a projection returns.

## Pagination pattern

Collection endpoints return `{"data":[...],"next_token":string|null}`. Two
ways to consume it:

1. **Manual paging** (CLI or MCP): call once, and if `next_token` is
   non-null, call again passing it back as `--next-token`
   (`next_token` as an MCP tool argument). Stop when it's null.
2. **`--all`** (CLI only): pass `--all` and the CLI follows every page for
   you, streaming one raw document per line to stdout as NDJSON, ending with
   exactly one `{"type":"summary","count":N,"ok":bool}` line. If pagination
   fails partway through, you still get exactly one summary line, with
   `"ok":false` and an embedded `"error"` object — check `ok` before
   trusting a `--all` run's `count`. There is no MCP equivalent of `--all`;
   MCP tool calls always return a single page, so page manually there.

## MCP alternative

Everything above applies identically over MCP: `oura mcp serve` exposes one
tool per endpoint (`oura_sleep`, `oura_heartrate`, ...) plus
`oura_auth_status`. Tool arguments mirror the CLI flags (`start_date`/
`end_date` or `start_datetime`/`end_datetime`, `next_token`, `fields`,
`latest`). A failed tool call returns the identical `{"error":{...}}`
envelope as text content with `isError` set — same `.kind`/`.hint`
semantics as the CLI table above. There is no schema-manifest tool; read
each tool's `description` (generated from the same endpoint registry as
`oura schema`) for its parameters, or shell out to `oura schema` once if
you have CLI access alongside MCP. See `docs/MCP.md` for the full tool
list.
