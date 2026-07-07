package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/ouracli/oura/internal/envelope"
	"github.com/ouracli/oura/internal/mcpserver"
	"github.com/ouracli/oura/internal/output"
)

// newMCPCmd builds the `oura mcp` command group. Its serve subcommand turns
// the CLI into an MCP server so an agent runtime can call every Oura endpoint
// as a tool over stdio.
func newMCPCmd() *cobra.Command {
	mcpCmd := &cobra.Command{
		Use:   "mcp",
		Short: "Model Context Protocol server exposing every Oura endpoint as a tool",
	}
	mcpCmd.AddCommand(newMCPServeCmd())
	return mcpCmd
}

func newMCPServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Serve every Oura endpoint as an MCP tool over stdio",
		Long: "Runs an MCP server on stdio using the official Go SDK. Every entry in the\n" +
			"endpoint registry becomes a tool (oura_sleep, oura_heartrate, ...), plus\n" +
			"oura_auth_status for diagnosing credentials and oura_version for reporting\n" +
			"the build serving the session. Tool results carry the raw Oura JSON;\n" +
			"failures carry the same error envelope the CLI emits. --sandbox serves\n" +
			"fake data with no credentials. stdout is owned by the MCP protocol; human\n" +
			"progress goes to stderr.",
		Args: cobra.NoArgs,
		// stdout is owned by the MCP transport, so it has no JSON/NDJSON shape.
		Annotations: map[string]string{annStdout: "", annExitCodes: "0,1"},
		RunE:        runMCPServe,
	}
}

func runMCPServe(cmd *cobra.Command, args []string) error {
	output.Progressf(os.Stderr, "oura MCP server on stdio")
	server := mcpserver.New(mcpserver.BuildInfo{Version: version, Commit: commit, Date: date}, globals.sandbox, apiClient)
	err := server.Run(cmd.Context(), &mcp.StdioTransport{})
	if err == nil || isCleanShutdown(err) {
		// The host closing stdin (EOF) or cancelling the context is the normal
		// end of an MCP session: exit 0 without printing anything, since stdout
		// belongs to the transport.
		return nil
	}
	// stdout belongs to the MCP transport for the whole session; surfacing the
	// envelope as an stderrOnlyError keeps the failure off the protocol channel
	// (where a client could mis-parse it as a JSON-RPC message) while still
	// exiting non-zero. The error goes to stderr instead.
	return &stderrOnlyError{env: envelope.New(envelope.KindInternal, "mcp_serve", err.Error(),
		"restart 'oura mcp serve'; run 'oura doctor' if it persists")}
}

// isCleanShutdown reports whether err is the ordinary end of an MCP session.
// The SDK signals a closed stdio stream with an unexported "server is closing"
// error that carries the underlying EOF only in its message, so match on that
// text in addition to the context/EOF sentinels.
func isCleanShutdown(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) {
		return true
	}
	return strings.Contains(err.Error(), "server is closing")
}
