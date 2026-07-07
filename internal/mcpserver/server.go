// Package mcpserver exposes every Oura endpoint as an MCP tool over stdio.
//
// It is the third surface generated from ouraapi.Endpoints (alongside the
// cobra data commands and the `oura schema` manifest), so the tool list can
// never drift from the CLI. Each tool builds a query from its typed arguments,
// calls the shared ouraapi.Client, and returns the raw Oura JSON as a single
// text block. API failures are surfaced as the standard error envelope with
// CallToolResult.IsError set, so an agent can branch on .error.kind/.hint
// exactly as it would from the CLI.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ouracli/oura/internal/cliauth"
	"github.com/ouracli/oura/internal/envelope"
	"github.com/ouracli/oura/internal/ouraapi"
)

// dateLayout is Oura's start_date/end_date format.
const dateLayout = "2006-01-02"

// dateRangeArgs are the parameters for StyleDateRange endpoints.
type dateRangeArgs struct {
	StartDate string `json:"start_date,omitempty" jsonschema:"start date YYYY-MM-DD (default: 7 days ago)"`
	EndDate   string `json:"end_date,omitempty" jsonschema:"end date YYYY-MM-DD (default: today)"`
	NextToken string `json:"next_token,omitempty" jsonschema:"pagination cursor: pass a previous response's next_token to fetch the next page"`
	Fields    string `json:"fields,omitempty" jsonschema:"comma-separated field projection; valid names are listed in this tool's description"`
}

// datetimeRangeArgs are the parameters for StyleDatetimeRange endpoints
// (heartrate, ring battery level).
type datetimeRangeArgs struct {
	StartDatetime string `json:"start_datetime,omitempty" jsonschema:"start datetime RFC3339, e.g. 2026-07-01T00:00:00Z"`
	EndDatetime   string `json:"end_datetime,omitempty" jsonschema:"end datetime RFC3339, e.g. 2026-07-06T00:00:00Z"`
	Latest        bool   `json:"latest,omitempty" jsonschema:"return only the single most recent sample instead of a range"`
	NextToken     string `json:"next_token,omitempty" jsonschema:"pagination cursor: pass a previous response's next_token to fetch the next page"`
	Fields        string `json:"fields,omitempty" jsonschema:"comma-separated field projection; valid names are listed in this tool's description"`
}

// tokenOnlyArgs are the parameters for StyleTokenOnly endpoints
// (ring_configuration): no date window.
type tokenOnlyArgs struct {
	NextToken string `json:"next_token,omitempty" jsonschema:"pagination cursor: pass a previous response's next_token to fetch the next page"`
	Fields    string `json:"fields,omitempty" jsonschema:"comma-separated field projection; valid names are listed in this tool's description"`
}

// emptyArgs is the parameter type for tools that take no input
// (StyleSingleObject and oura_auth_status).
type emptyArgs struct{}

// New builds an MCP server registering one tool per Oura endpoint plus an
// oura_auth_status diagnostic tool. makeClient resolves credentials (honoring
// --sandbox/OURA_TOKEN/keyring) and is called fresh on each tool invocation so
// credential errors surface as tool errors rather than at startup. When sandbox
// is true, an endpoint with no sandbox route (only personal_info today) is
// still registered but returns the same no_sandbox_route usage error the CLI
// emits, so the two surfaces do not diverge on that condition.
func New(version string, sandbox bool, makeClient func(ctx context.Context) (*ouraapi.Client, error)) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "oura", Version: version}, nil)

	for _, ep := range ouraapi.Endpoints {
		ep := ep // capture per endpoint for the closures below
		desc := toolDescription(ep)
		if sandbox && !ep.Sandbox {
			// Mirror the CLI's guard (cmd_data.go): the tool exists but errors
			// with the clean no_sandbox_route envelope instead of hitting a
			// nonexistent sandbox route and returning a misleading HTTP 404.
			addTool(server, ep.MCPTool, desc, func(_ context.Context, _ emptyArgs) *mcp.CallToolResult {
				return noSandboxRouteResult(ep)
			})
			continue
		}
		switch ep.Style {
		case ouraapi.StyleDateRange:
			addTool(server, ep.MCPTool, desc, func(ctx context.Context, in dateRangeArgs) *mcp.CallToolResult {
				client, errRes := resolveClient(ctx, makeClient)
				if errRes != nil {
					return errRes
				}
				q := url.Values{}
				start := in.StartDate
				if start == "" {
					start = time.Now().AddDate(0, 0, -7).Format(dateLayout)
				}
				end := in.EndDate
				if end == "" {
					end = time.Now().Format(dateLayout)
				}
				q.Set("start_date", start)
				q.Set("end_date", end)
				if in.NextToken != "" {
					q.Set("next_token", in.NextToken)
				}
				if errRes := setFields(q, ep, in.Fields); errRes != nil {
					return errRes
				}
				return listResult(ctx, client, ep, q)
			})
		case ouraapi.StyleDatetimeRange:
			addTool(server, ep.MCPTool, desc, func(ctx context.Context, in datetimeRangeArgs) *mcp.CallToolResult {
				client, errRes := resolveClient(ctx, makeClient)
				if errRes != nil {
					return errRes
				}
				q := url.Values{}
				if in.StartDatetime != "" {
					q.Set("start_datetime", in.StartDatetime)
				}
				if in.EndDatetime != "" {
					q.Set("end_datetime", in.EndDatetime)
				}
				if in.Latest {
					q.Set("latest", "true")
				}
				if in.NextToken != "" {
					q.Set("next_token", in.NextToken)
				}
				if errRes := setFields(q, ep, in.Fields); errRes != nil {
					return errRes
				}
				return listResult(ctx, client, ep, q)
			})
		case ouraapi.StyleTokenOnly:
			addTool(server, ep.MCPTool, desc, func(ctx context.Context, in tokenOnlyArgs) *mcp.CallToolResult {
				client, errRes := resolveClient(ctx, makeClient)
				if errRes != nil {
					return errRes
				}
				q := url.Values{}
				if in.NextToken != "" {
					q.Set("next_token", in.NextToken)
				}
				if errRes := setFields(q, ep, in.Fields); errRes != nil {
					return errRes
				}
				return listResult(ctx, client, ep, q)
			})
		case ouraapi.StyleSingleObject:
			addTool(server, ep.MCPTool, desc, func(ctx context.Context, _ emptyArgs) *mcp.CallToolResult {
				client, errRes := resolveClient(ctx, makeClient)
				if errRes != nil {
					return errRes
				}
				raw, err := client.Object(ctx, ep)
				if err != nil {
					return errorResult(err)
				}
				return textResult(string(raw))
			})
		}
	}

	addTool(server, "oura_auth_status",
		"Report whether oura has stored Oura credentials and how (method, backend, granted scopes, token expiry). No secrets are returned. Call this first to diagnose auth before invoking data tools.",
		func(_ context.Context, _ emptyArgs) *mcp.CallToolResult {
			b, err := json.Marshal(authStatus())
			if err != nil {
				return errorResult(err)
			}
			return textResult(string(b))
		})

	return server
}

// addTool registers a typed tool whose handler returns a CallToolResult
// directly; tool-level failures ride inside the result (IsError), never as an
// MCP protocol error, so the model always sees them and can self-correct.
func addTool[In any](s *mcp.Server, name, desc string, run func(ctx context.Context, in In) *mcp.CallToolResult) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
			return run(ctx, in), nil, nil
		})
}

// resolveClient builds the API client, converting a credential error into an
// error result so the agent sees the same envelope it would from the CLI.
func resolveClient(ctx context.Context, makeClient func(ctx context.Context) (*ouraapi.Client, error)) (*ouraapi.Client, *mcp.CallToolResult) {
	client, err := makeClient(ctx)
	if err != nil {
		return nil, errorResult(err)
	}
	return client, nil
}

// setFields validates a requested fields projection against ep's registry
// (the API silently ignores unknown names, so this is the only place a typo
// surfaces) and sets it on q. A validation failure comes back as the usual
// usage-envelope tool error; nil means q is ready.
func setFields(q url.Values, ep ouraapi.Endpoint, fields string) *mcp.CallToolResult {
	norm, err := ep.NormalizeFields(fields)
	if err != nil {
		return errorResult(err)
	}
	if norm != "" {
		q.Set("fields", norm)
	}
	return nil
}

// listResult fetches one page and returns the {"data",...,"next_token"}
// envelope as raw JSON text.
func listResult(ctx context.Context, c *ouraapi.Client, ep ouraapi.Endpoint, q url.Values) *mcp.CallToolResult {
	lr, err := c.List(ctx, ep, q)
	if err != nil {
		return errorResult(err)
	}
	b, err := json.Marshal(lr)
	if err != nil {
		return errorResult(err)
	}
	return textResult(string(b))
}

// textResult wraps text as a single successful text-content result.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// noSandboxRouteResult reports that ep has no sandbox equivalent, matching the
// CLI's noSandboxRouteErr byte-for-byte so an agent gets the same envelope from
// either surface.
func noSandboxRouteResult(ep ouraapi.Endpoint) *mcp.CallToolResult {
	return errorResult(envelope.New(envelope.KindUsage, "no_sandbox_route",
		fmt.Sprintf("%s has no sandbox route", ep.CLI),
		fmt.Sprintf("%s has no sandbox equivalent; use real credentials", ep.CLI)))
}

// errorResult renders any error as the standard {"error":{...}} envelope and
// marks the result as an error so agents can branch on .error.kind/.hint.
func errorResult(err error) *mcp.CallToolResult {
	var buf bytes.Buffer
	envelope.From(err).Write(&buf)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: strings.TrimRight(buf.String(), "\n")}},
		IsError: true,
	}
}

// toolDescription is ep.Short followed by a short parameter guide tailored to
// the endpoint's style.
func toolDescription(ep ouraapi.Endpoint) string {
	desc := ep.Short
	switch ep.Style {
	case ouraapi.StyleDateRange:
		desc += " Params: start_date/end_date (YYYY-MM-DD, default last 7 days), next_token to paginate"
		if ep.HasFields {
			desc += ", fields to project columns"
		}
		desc += "."
	case ouraapi.StyleDatetimeRange:
		desc += " Params: start_datetime/end_datetime (RFC3339), latest for only the most recent sample, next_token to paginate"
		if ep.HasFields {
			desc += ", fields to project columns"
		}
		desc += "."
	case ouraapi.StyleTokenOnly:
		desc += " Params: next_token to paginate"
		if ep.HasFields {
			desc += ", fields to project columns"
		}
		desc += "."
	case ouraapi.StyleSingleObject:
		desc += " Takes no parameters; returns a single object."
	}
	if ep.HasFields {
		desc += " Valid fields: " + strings.Join(ep.Fields, ", ") + "."
	}
	if ep.Deprecated {
		desc += " (deprecated)"
	}
	return desc
}

// authStatus re-derives the same object as `oura auth status` from cliauth,
// without touching the network or exposing any secret material.
func authStatus() map[string]any {
	envOverride := os.Getenv("OURA_TOKEN") != ""
	store := cliauth.Open(cliauth.ConfigDir())
	creds, loadErr := store.Load()

	out := map[string]any{
		"authenticated": false,
		"method":        nil,
		"backend":       store.Backend(),
		"scopes":        []string{},
		"expires_at":    nil,
		"expired":       false,
		"env_override":  envOverride,
	}
	if envOverride {
		out["authenticated"] = true
		out["method"] = "env"
	}
	if loadErr == nil && creds.AccessToken != "" {
		out["authenticated"] = true
		if !envOverride {
			out["method"] = string(creds.Method)
		}
		if creds.Scopes != nil {
			out["scopes"] = creds.Scopes
		}
		if !creds.Expiry.IsZero() {
			out["expires_at"] = creds.Expiry.UTC().Format(time.RFC3339)
			out["expired"] = time.Now().After(creds.Expiry)
		}
	}
	return out
}
