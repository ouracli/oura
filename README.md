# ouracli

An agent-first CLI for the [Oura Ring API v2](https://cloud.ouraring.com/v2/docs), in Go. Binary name: `oura`.

> **Unofficial project.** `ouracli` is an independent, community-built CLI for
> the Oura API. It is not affiliated with, endorsed by, or sponsored by Oura
> Health Oy. "Oura" and related marks are trademarks of Oura Health Oy; this
> project uses them only to describe interoperability with the Oura API.

"Agent-first" means the primary caller is a language model, not a human at a
terminal: **stdout carries JSON, always** — a single document, an NDJSON
stream, or a typed error envelope, never prose. Every subcommand is
self-describing via `oura schema`, so an agent loads the manifest once instead
of scraping `--help` text. Failures come back as
`{"error":{"kind","code","reason","message","hint"}}` with a stable exit-code
enum, so an agent can branch on `.error.kind` instead of grepping stderr.
Secrets never touch the agent's context: tokens live in the OS keyring (or an
AES-256-GCM encrypted file where no keyring exists), resolved transparently
per request. And because agents increasingly *are* MCP clients, the whole
tool doubles as an MCP server (`oura mcp serve`) with zero extra code — the
same endpoint registry drives the CLI, the schema manifest, and the MCP tool
list, so all three can never drift apart.

Humans are welcome too: pass `--pretty` anywhere for indented JSON, and
`oura sleep --sandbox` works with **zero credentials** — Oura publishes a
sandbox environment that mirrors every data endpoint with fake data.

## Install

**Homebrew (macOS & Linux)** — three commands, copy them one at a time:

```sh
brew tap ouracli/oura
brew trust ouracli/oura   # Homebrew asks you to trust third-party taps once
brew install oura
```

Check it worked:

```sh
oura version
```

You should see something like `{"version":"0.1.0",...}`.

<details>
<summary>Other ways to install (Go users)</summary>

```sh
# go install
go install github.com/ouracli/oura/cmd/oura@latest

# or build from source
git clone https://github.com/ouracli/oura && cd oura && make build
```

</details>

## Quickstart in 60 seconds

```sh
oura sleep --sandbox --pretty    # real API shape, fake data, zero credentials
```

That's it — no account, no API key. `--sandbox` routes to Oura's public
sandbox environment, which returns realistic fake data for every endpoint
except `profile`. It's the fastest way to see the shape of the JSON before
wiring up real credentials.

```json
{
  "data": [
    {
      "id": "daily_sleep-0-2026-6-29",
      "contributors": {
        "deep_sleep": 70,
        "efficiency": 80,
        "latency": 90,
        "rem_sleep": 60,
        "restfulness": 70,
        "timing": 80,
        "total_sleep": 90
      },
      "day": "2026-06-29",
      "score": 80,
      "timestamp": "2026-06-29T00:00:00.000+00:00"
    }
  ],
  "next_token": null
}
```

### Connecting your real ring

You need a free "OAuth app" on Oura's website — think of it as the key that
lets this tool read your data. You create it once; it takes about two
minutes. (Oura retired the old Personal Access Tokens in December 2025, so
this is now the only way in. If you still have an old token that works,
skip all of this: `printf %s "$TOKEN" | oura auth login --token-stdin`.)

**Step 1 — create your app on Oura's site.**
Go to <https://cloud.ouraring.com/oauth/applications>, sign in with your
normal Oura account, and click **New Application**. Fill it in like this:

| Field | What to enter |
|---|---|
| Application name | anything, e.g. `my oura cli` |
| Website / description | anything, e.g. `personal use` |
| Redirect URI | `http://localhost:8989/callback` — **copy it exactly** (this address points back at your own computer; nothing is exposed to the internet) |

Save it. Oura shows you a **Client ID** and **Client Secret** — keep that
page open for the next step.

**Step 2 — log in.**

```sh
oura auth login
```

It asks for the Client ID and Client Secret (the secret stays hidden as you
paste it), then opens your browser to Oura's "allow access?" page. Click
**Accept**. The browser tab will say *"Authorization complete — you can close
this tab"*, and the terminal prints `"stored": true`.

Your tokens are now in your operating system's keychain — not in a file, not
in your shell history — and refresh themselves automatically from here on.

**Step 3 — check everything, then pull your data.**

```sh
oura doctor     # every check should say "ok": true
oura sleep --pretty
oura workouts --pretty
oura readiness --pretty
```

Each data command defaults to the last 7 days; add `--start 2026-06-01
--end 2026-06-30` for other ranges.

**If something goes wrong**, the error itself tells you what to do — read the
`hint` field. The common ones:

| You see | What it means | Fix |
|---|---|---|
| `redirect_uri_not_registered` | The redirect URI on your Oura app doesn't match | Edit your app at cloud.ouraring.com/oauth/applications and set it to exactly `http://localhost:8989/callback` |
| `authorization_timeout` | You didn't click Accept in the browser in time | Run `oura auth login` again and approve the browser prompt |
| `authorization_denied` | You clicked Deny | Run `oura auth login` again and click Accept |
| `token_rejected` | Token expired or was revoked | Run `oura auth login` again |
| `subscription_required` / a 403 | Your Oura membership lapsed, or you unticked a data type on the consent screen | Renew membership, or re-login and leave all scopes ticked |

And `oura doctor` diagnoses the rest: it's a JSON checklist (config dir,
keyring backend, credentials, token validity, network), each failing check
carrying its own `hint`.

## Command tour

Every data command follows the same shape: `--start`/`--end` (default: the
last 7 days), `--fields` for a server-side projection (where the endpoint
supports it; the valid names are listed per command in `oura schema` and
validated client-side, because Oura silently ignores unknown ones),
`--next-token` to resume pagination by hand, and `--all` to
follow pagination automatically and stream NDJSON. Run `oura schema` for the
full, current list — the table below is illustrative, not exhaustive.

| command | Oura endpoint | notes |
|---|---|---|
| `oura sleep` | `daily_sleep` | daily sleep score + contributors |
| `oura sleep-periods` | `sleep` | detailed per-period sleep sessions |
| `oura sleep-time` | `sleep_time` | recommended bedtime window |
| `oura activity` | `daily_activity` | steps, calories, MET minutes |
| `oura readiness` | `daily_readiness` | readiness score + contributors |
| `oura stress` | `daily_stress` | high-stress/recovery time |
| `oura resilience` | `daily_resilience` | resilience level + contributors |
| `oura spo2` | `daily_spo2` | blood-oxygen % during sleep |
| `oura cardio-age` | `daily_cardiovascular_age` | estimated vascular age |
| `oura vo2max` | `vO2_max` | estimated VO2 max |
| `oura heartrate` | `heartrate` | time-series bpm (datetime range) |
| `oura battery` | `ring_battery_level` | time-series battery % (datetime range) |
| `oura workouts` | `workout` | logged workouts |
| `oura sessions` | `session` | guided/logged sessions |
| `oura tags` | `enhanced_tag` | user-annotated events |
| `oura rest-mode` | `rest_mode_period` | Rest Mode periods |
| `oura ring` | `ring_configuration` | ring hardware config |
| `oura profile` | `personal_info` | user id, age, sex, height, weight, email — **no sandbox route** |
| `oura auth login\|status\|logout` | — | credential management |
| `oura doctor` | — | onboarding diagnostics |
| `oura schema` | — | JSON tool manifest |
| `oura mcp serve` | — | MCP server on stdio |

A few real examples, run against the sandbox:

```sh
$ oura activity --sandbox --start 2026-07-01 --end 2026-07-02 --pretty
{
  "data": [
    {
      "id": "daily_activity-0-2026-7-1",
      "active_calories": 100,
      "average_met_minutes": 10.0,
      "contributors": {
        "meet_daily_targets": 74,
        "move_every_hour": 90,
        "recovery_time": 70,
        "stay_active": 80,
        "training_frequency": 60,
        "training_volume": 50
      },
      "day": "2026-07-01",
      ...
    }
  ],
  "next_token": null
}

$ oura heartrate --sandbox --latest
{"data":[{"timestamp":"2026-07-05T00:00:00.000Z","bpm":60,"producer_timestamp":null,"source":"awake"}],"next_token":null}

$ oura workouts --sandbox --start 2026-07-01 --end 2026-07-02 --pretty
{
  "data": [
    {
      "id": "workout-2-2026-7-1",
      "activity": "swimming",
      "calories": 1000.0,
      "day": "2026-07-01",
      "distance": 200.0,
      "end_datetime": "2026-07-01T00:00:00.000+00:00",
      "intensity": "moderate",
      "label": null,
      "source": "manual",
      "start_datetime": "2026-07-01T00:00:00.000+00:00"
    }
  ],
  "next_token": null
}
```

A command that takes a document ID (every endpoint with a per-document route)
short-circuits straight to the single document, ignoring the range flags:

```sh
oura sleep daily_sleep-0-2026-6-29
```

## Agent integration

### `oura schema`

Load this once. It reflects the live cobra command tree, so it can never
drift from what the binary actually does: every command's flags (with type,
default, and description), positional args, stdout shape, and exit codes,
plus the global flags and a full exit-code reference.

```sh
oura schema            # everything
oura schema sleep      # just one command
```

```json
{
  "name": "sleep",
  "short": "Daily sleep score (0-100) with contributor breakdown: deep sleep, REM, latency, efficiency, restfulness, and timing.",
  "flags": [
    {"name": "all", "type": "bool", "default": "false", "description": "follow next_token and stream every document to stdout as NDJSON..."},
    {"name": "end", "type": "string", "default": "", "description": "end date, YYYY-MM-DD (default: today)"},
    {"name": "fields", "type": "string", "default": "", "description": "comma-separated field projection to request from Oura; valid: contributors, day, id, score, timestamp"},
    {"name": "next-token", "type": "string", "default": "", "description": "resume pagination from this token"},
    {"name": "start", "type": "string", "default": "", "description": "start date, YYYY-MM-DD (default: 7 days before --end)"}
  ],
  "args": [{"name": "document_id", "required": false}],
  "fields": ["contributors", "day", "id", "score", "timestamp"],
  "stdout": "json|ndjson(--all)",
  "exit_codes": [0, 2, 3, 5, 6, 7, 8]
}
```

### Error envelope

Every failure — bad flags, network errors, Oura rejecting the request — is
the same shape on stdout, and nothing else is written to stdout for that
invocation:

```json
{"error":{"kind":"usage","code":3,"reason":"bad_start_date","message":"invalid --start \"bogus\": parsing time \"bogus\" as \"2006-01-02\": cannot parse \"bogus\" as \"2006\"","hint":"dates must be YYYY-MM-DD, e.g. 2026-06-29"}}
```

`kind` and the process exit code always agree — branch on either one:

| code | kind | meaning | agent action |
|------|------|---------|---------------|
| 0 | `ok` | success | continue |
| 1 | `internal` | bug / unexpected | surface to caller |
| 2 | `auth` | missing/invalid/expired credentials, 401/403 | run `oura auth login` |
| 3 | `usage` | bad flags, args, or dates | fix the invocation via `oura schema` |
| 4 | `config` | config file or keyring problem | run `oura doctor` |
| 5 | `api` | Oura API rejected the request (other than auth) | inspect `.error.reason` |
| 6 | `network` | timeout, DNS, connection failure | retry with backoff |
| 7 | `ratelimit` | HTTP 429 | wait, then retry (see `.hint` for `Retry-After`) |
| 8 | `subscription` | HTTP 426 — data requires an active Oura membership | inform the user |

### NDJSON `--all` contract

Every data command accepts `--all`, which switches stdout from a single JSON
document to an NDJSON stream: one raw document per line, following
`next_token` until it's exhausted, terminated by exactly one summary line —
`{"type":"summary","count":N,"ok":bool,...}`. The terminator is how a
consumer distinguishes "the stream ended because pagination finished" from
"the process was killed mid-stream": a well-formed stream is never truncated.

```sh
$ oura sleep --sandbox --all
{"id":"daily_sleep-0-2026-6-29","contributors":{...},"day":"2026-06-29","score":80,...}
{"id":"daily_sleep-1-2026-6-30","contributors":{...},"day":"2026-06-30","score":73,...}
...
{"count":7,"endpoint":"sleep","ok":true,"type":"summary"}
```

If pagination fails partway through, the stream still ends with exactly one
summary line — `ok:false` plus an embedded `"error"` envelope — rather than
emitting a second, competing stdout artifact after the stream has already
committed to the NDJSON shape:

```json
{"count":3,"endpoint":"sleep","ok":false,"error":{"kind":"network","code":6,"reason":"timeout","message":"...","hint":"retry, or raise --timeout"},"type":"summary"}
```

## MCP

`oura mcp serve` runs an MCP server on stdio (via the official
[Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk)) that exposes
every entry in the endpoint registry as a tool (`oura_sleep`,
`oura_heartrate`, `oura_workouts`, ...), plus `oura_auth_status`. See
[docs/MCP.md](docs/MCP.md) for the full tool list and argument reference.

Register it with the Claude Code CLI:

```sh
claude mcp add oura -- oura mcp serve
```

Or point Claude Desktop at it directly in `claude_desktop_config.json`:

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

Try it with zero credentials by adding `--sandbox`, which routes every tool
call to Oura's fake-data sandbox:

```sh
claude mcp add oura-sandbox -- oura mcp serve --sandbox
```

```json
{
  "mcpServers": {
    "oura-sandbox": {
      "command": "oura",
      "args": ["mcp", "serve", "--sandbox"]
    }
  }
}
```

Tool results carry the raw Oura JSON as a text block; failures carry the
same `{"error":{...}}` envelope the CLI emits on stdout, with
`CallToolResult.IsError` set — so a model driving the MCP tools sees exactly
the same failure shape it would from the CLI and can react the same way.

## Auth details

- **OAuth2 authorization-code flow**, loopback redirect only (no PKCE, no
  device flow). Two quirks worth knowing: the *authorize* endpoint lives on
  `cloud.ouraring.com` but the *token* endpoint lives on `api.ouraring.com`
  (ouracli handles the split transparently), and Oura's **refresh tokens are
  single-use and rotate on every exchange** — ouracli persists the newly
  rotated refresh token to the store before ever using the new access token,
  so a crash mid-refresh can't strand the account.
- **Scopes** (space-separated; pass a subset via `--scopes`, or leave empty
  to request all): `email personal daily heartrate workout tag session
  spo2`. Oura lets a user untick individual scopes on the consent screen, so
  the granted set recorded by `oura auth login`/`oura auth status` may be
  narrower than what was requested.
- **`OURA_TOKEN`** is an escape hatch: set it and every command uses that
  bearer token directly, bypassing the keyring entirely (useful for CI, or
  for using a legacy PAT that predates the December 2025 deprecation).
  Precedence is `OURA_TOKEN` > stored credentials > (none, error).
- **Storage**: credentials go to the OS keyring — macOS Keychain, Windows
  Credential Manager, or libsecret/Secret Service on Linux — via
  `zalando/go-keyring`. Where no keyring is reachable (headless Linux, most
  CI), ouracli falls back to an AES-256-GCM encrypted file under the config
  dir, with a per-install random key at `0600`. This is obfuscation-at-rest,
  not a substitute for a real keyring, and `oura auth status` / `oura
  doctor` both report which backend is active (`"backend":"keyring"` or
  `"backend":"encrypted-file"`) so you always know which one you're on. Set
  `OURA_KEYRING_BACKEND=file` to force the file fallback (useful for tests
  and containers), and `OURA_CONFIG_DIR` to relocate the whole config
  directory.
