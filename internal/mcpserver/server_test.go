package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ouracli/oura/internal/ouraapi"
)

// session spins up the server under test and a client connected to it over
// the SDK's in-memory transport. makeClient never touches the network in
// these tests: the handlers under test fail validation before any request.
func session(t *testing.T, build BuildInfo, sandbox bool) *mcp.ClientSession {
	t.Helper()
	server := New(build, sandbox, func(ctx context.Context) (*ouraapi.Client, error) {
		return ouraapi.New("test-token", time.Second), nil
	})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func listTools(t *testing.T, cs *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		tools[tool.Name] = tool
	}
	return tools
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("want exactly 1 content block, got %d", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content block is %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}

// TestToolListCoversRegistryAndDiagnostics pins the tool surface: one tool
// per registry endpoint plus oura_auth_status and oura_version.
func TestToolListCoversRegistryAndDiagnostics(t *testing.T) {
	tools := listTools(t, session(t, BuildInfo{Version: "1.2.3"}, false))
	for _, ep := range ouraapi.Endpoints {
		if tools[ep.MCPTool] == nil {
			t.Errorf("missing tool %q", ep.MCPTool)
		}
	}
	for _, name := range []string{"oura_auth_status", "oura_version"} {
		if tools[name] == nil {
			t.Errorf("missing diagnostic tool %q", name)
		}
	}
}

// TestVersionTool pins the regression from 2026-07-07: an agent asked which
// version the MCP server was running and had no way to find out.
func TestVersionTool(t *testing.T) {
	build := BuildInfo{Version: "1.2.3", Commit: "abc1234", Date: "2026-07-07T00:00:00Z"}
	cs := session(t, build, true)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "oura_version"})
	if err != nil {
		t.Fatalf("tools/call oura_version: %v", err)
	}
	if res.IsError {
		t.Fatalf("oura_version errored: %s", textOf(t, res))
	}
	var got struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Date    string `json:"date"`
		Sandbox bool   `json:"sandbox"`
	}
	if err := json.Unmarshal([]byte(textOf(t, res)), &got); err != nil {
		t.Fatalf("oura_version returned invalid JSON: %v", err)
	}
	if got.Version != build.Version || got.Commit != build.Commit || got.Date != build.Date {
		t.Errorf("oura_version = %+v, want build %+v", got, build)
	}
	if !got.Sandbox {
		t.Error("oura_version sandbox = false, want true for a --sandbox session")
	}
}

// TestDateRangeToolDescriptionsWarnAboutExclusiveEnd pins the surfacing of
// the live-probed end_date exclusivity (2026-07-07): every date-range tool's
// description and its end_date argument schema must carry the warning, so an
// agent that reads either one sees it before issuing a same-day query that
// would silently return nothing.
func TestDateRangeToolDescriptionsWarnAboutExclusiveEnd(t *testing.T) {
	tools := listTools(t, session(t, BuildInfo{Version: "1.2.3"}, false))
	for _, ep := range ouraapi.Endpoints {
		if ep.Style != ouraapi.StyleDateRange {
			continue
		}
		tool := tools[ep.MCPTool]
		if tool == nil {
			t.Fatalf("missing tool %q", ep.MCPTool)
		}
		if !strings.Contains(tool.Description, "EXCLUSIVE") {
			t.Errorf("%s: description lacks the end_date EXCLUSIVE warning: %q", ep.MCPTool, tool.Description)
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("%s: marshal input schema: %v", ep.MCPTool, err)
		}
		if !strings.Contains(string(raw), "EXCLUSIVE") {
			t.Errorf("%s: end_date arg schema lacks the EXCLUSIVE warning", ep.MCPTool)
		}
	}
}

// TestFieldsSurfacedInToolDescriptions pins that every projecting tool
// enumerates its valid field names where the agent reads them.
func TestFieldsSurfacedInToolDescriptions(t *testing.T) {
	tools := listTools(t, session(t, BuildInfo{Version: "1.2.3"}, false))
	for _, ep := range ouraapi.Endpoints {
		if !ep.HasFields {
			continue
		}
		tool := tools[ep.MCPTool]
		if tool == nil {
			t.Fatalf("missing tool %q", ep.MCPTool)
		}
		if !strings.Contains(tool.Description, "Valid fields: "+strings.Join(ep.Fields, ", ")) {
			t.Errorf("%s: description does not enumerate valid fields: %q", ep.MCPTool, tool.Description)
		}
	}
}

// TestUnknownFieldRejectedBeforeAnyRequest pins the client-side projection
// validation over MCP: an unknown field name errors with the usage envelope
// (the API would silently ignore it), and does so before any network call —
// the fake token here would fail any real request.
func TestUnknownFieldRejectedBeforeAnyRequest(t *testing.T) {
	cs := session(t, BuildInfo{Version: "1.2.3"}, false)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "oura_sleep",
		Arguments: map[string]any{"fields": "bogus"},
	})
	if err != nil {
		t.Fatalf("tools/call oura_sleep: %v", err)
	}
	if !res.IsError {
		t.Fatal("want IsError for an unknown field")
	}
	text := textOf(t, res)
	if !strings.Contains(text, "unknown_field") || !strings.Contains(text, "valid fields") {
		t.Errorf("error envelope missing unknown_field reason or valid-fields hint: %s", text)
	}
}

// TestFieldsOnUnsupportedEndpointRejected pins the resilience special case:
// the spec declares a fields param there but the live API ignores it, so the
// tool must reject a projection outright instead of silently no-opping.
func TestFieldsOnUnsupportedEndpointRejected(t *testing.T) {
	cs := session(t, BuildInfo{Version: "1.2.3"}, false)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "oura_resilience",
		Arguments: map[string]any{"fields": "day"},
	})
	if err != nil {
		t.Fatalf("tools/call oura_resilience: %v", err)
	}
	if !res.IsError {
		t.Fatal("want IsError for fields on an endpoint that ignores them")
	}
	if text := textOf(t, res); !strings.Contains(text, "fields_not_supported") {
		t.Errorf("error envelope missing fields_not_supported reason: %s", text)
	}
}
