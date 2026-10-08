# Oura API coverage

Audited 2026-10-08 against [Oura’s current API documentation](https://cloud.ouraring.com/v2/docs)
and its [OpenAPI 1.41 download](https://cloud.ouraring.com/v2/static/json/openapi-1.41.json).
The unmodified spec is checked into `internal/ouraapi/testdata/openapi-1.41.json`.

## Coverage

The CLI implements all **35 production data GET routes**: 19 collection or
personal-info routes and 16 individual-document routes. It also implements
all **34 sandbox data GET routes**: 18 collections and 16 document routes.
Personal info has no sandbox route. The MCP server exposes all 19 data types
as tools; individual-document lookup is available through the CLI’s optional
`document_id` argument, rather than a separate MCP tool.

| API data type | CLI | MCP tool |
|---|---|---|
| `daily_sleep` | `oura sleep` | `oura_sleep` |
| `sleep` | `oura sleep-periods` | `oura_sleep_periods` |
| `sleep_time` | `oura sleep-time` | `oura_sleep_time` |
| `daily_activity` | `oura activity` | `oura_activity` |
| `daily_readiness` | `oura readiness` | `oura_readiness` |
| `daily_stress` | `oura stress` | `oura_stress` |
| `daily_resilience` | `oura resilience` | `oura_resilience` |
| `daily_spo2` | `oura spo2` | `oura_spo2` |
| `daily_cardiovascular_age` | `oura cardio-age` | `oura_cardio_age` |
| `vO2_max` | `oura vo2max` | `oura_vo2max` |
| `heartrate` | `oura heartrate` | `oura_heartrate` |
| `ring_battery_level` | `oura battery` | `oura_battery` |
| `workout` | `oura workouts` | `oura_workouts` |
| `session` | `oura sessions` | `oura_sessions` |
| `enhanced_tag` | `oura tags` | `oura_tags` |
| `tag` | `oura tags-legacy` | `oura_tags_legacy` |
| `rest_mode_period` | `oura rest-mode` | `oura_rest_mode` |
| `ring_configuration` | `oura ring` | `oura_ring` |
| `personal_info` | `oura profile` | `oura_profile` |

The six webhook subscription operations are **not implemented**: list, create,
get, update, delete, and renew. They use application client credentials and
manage subscriptions to a hosted webhook receiver. The CLI is a read-only
data client and does not host a receiver or manage webhook subscriptions.
Coverage claims refer to user data, rather than the entire management API.

## Changes since OpenAPI 1.35

- No data paths or document fields were added or removed.
- The server URL is now correctly `https://api.ouraring.com`.
- OAuth scopes now include `heart_health`; the spec uses `spo2` instead of
  its previous `spo2Daily` spelling. Default login omits the scope parameter
  to request all available scopes, so no hardcoded scope list blocks them.
  Re-run `oura auth login` if an earlier consent omitted heart-health access.
- OAuth authorization and token URLs remain
  `https://cloud.ouraring.com/oauth/authorize` and
  `https://api.ouraring.com/oauth/token`. Application registration moved to
  the [Oura Developer Portal](https://developer.ouraring.com/).
- Validation errors can include `ctx` and `input`; the client already
  preserves structured API error details. Rate-limit guidance emphasizes
  the per-token and per-application response headers.

## Verification and limits

- `TestDataRoutesMatchOpenAPISpec` compares the complete production and
  sandbox data route sets in both directions, including document lookup,
  date/datetime filters, pagination, and query parameters. A new upstream
  data route fails the test until the registry is updated.
- Existing field-contract tests pass against 1.41. Live-verified exceptions
  remain: heart-rate and battery samples contain `producer_timestamp` rather
  than the spec’s `timestamp_unix`, and resilience ignores field projection.
  The sandbox does not implement projection or latest-sample filtering.
- All 18 sandbox collections returned valid JSON in live probes.
- All 16 sandbox document routes returned upstream HTTP 500 when given IDs
  returned by the corresponding sandbox collections. The CLI surfaced the
  errors in its JSON error envelope. Use sandbox collections to explore
  data; sandbox document lookup was not verified working.
- Live production reads could not be verified: the installed CLI reported
  `no_credentials`. Signing into the developer portal does not store CLI
  credentials; run `oura auth login` to connect it. No private health data
  or credentials are included in this report.
- CLI, MCP, pagination, field validation, and error-handling unit tests pass
  with the race detector.
