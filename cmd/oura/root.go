package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/akeemjenkins/ouracli/internal/envelope"
	"github.com/akeemjenkins/ouracli/internal/output"
)

// Annotation keys consumed by the schema generator.
const (
	annStdout    = "stdout_format" // "json" | "ndjson"
	annExitCodes = "exit_codes"    // comma-separated ints
)

// globalOpts are persistent flags shared by every command.
type globalOpts struct {
	sandbox bool
	pretty  bool
	timeout int    // seconds
	token   string // explicit bearer-token override (discouraged; prefer OURA_TOKEN)
	config  string // config directory override; empty uses the platform default
}

var globals globalOpts

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "oura",
		Short: "Agent-first CLI for the Oura Ring API v2",
		Long: "oura is a CLI for the Oura Ring API v2 designed to be driven by a language model.\n" +
			"JSON on stdout, typed error envelopes, a stable exit-code enum, and a schema\n" +
			"manifest (`oura schema`). Try it with zero credentials: oura sleep --sandbox",
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		SilenceUsage:  true,
		SilenceErrors: true,
		// --config is applied before any command runs by exporting it as
		// OURA_CONFIG_DIR, so every cliauth.ConfigDir() caller (including the
		// MCP server and auth commands) honors it without extra plumbing.
		PersistentPreRunE: applyGlobalConfig,
		RunE:              runWelcome,
		Annotations:       map[string]string{annStdout: "json", annExitCodes: "0"},
	}
	root.PersistentFlags().BoolVar(&globals.sandbox, "sandbox", false,
		"use the Oura sandbox API (fake data, no credentials needed)")
	root.PersistentFlags().BoolVar(&globals.pretty, "pretty", false,
		"pretty-print JSON output for humans")
	root.PersistentFlags().IntVar(&globals.timeout, "timeout", 30,
		"HTTP timeout in seconds")
	root.PersistentFlags().StringVar(&globals.token, "token", "",
		"explicit bearer token override (discouraged; prefer the OURA_TOKEN env var)")
	root.PersistentFlags().StringVar(&globals.config, "config", "",
		"config directory holding stored credentials (default: platform config dir)")

	root.AddCommand(
		newAuthCmd(),
		newDoctorCmd(),
		newSchemaCmd(),
		newMCPCmd(),
		newVersionCmd(),
	)
	root.AddCommand(newDataCmds()...)
	return root
}

// applyGlobalConfig honors the --config global flag by exporting it as
// OURA_CONFIG_DIR before the selected command runs, so credential resolution
// throughout the process (cliauth.ConfigDir) points at the chosen directory.
func applyGlobalConfig(cmd *cobra.Command, args []string) error {
	if globals.config != "" {
		if err := os.Setenv("OURA_CONFIG_DIR", globals.config); err != nil {
			return envelope.New(envelope.KindConfig, "config_dir_override", err.Error(),
				"pass a writable --config directory or set OURA_CONFIG_DIR")
		}
	}
	return nil
}

// newVersionCmd emits build metadata as JSON, the agent-readable counterpart to
// the --version flag's human string.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "version",
		Short:       "Print version, commit, and build date as JSON",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{annStdout: "json", annExitCodes: "0"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return printResult(map[string]any{
				"version": version,
				"commit":  commit,
				"date":    date,
			})
		},
	}
}

// fprintStderrSummary prints the one-line human error summary to stderr.
func fprintStderrSummary(e *envelope.Error) {
	output.Progressf(os.Stderr, "error[%s/%s]: %s", e.Kind, e.Reason, e.Message)
	if e.Hint != "" {
		output.Progressf(os.Stderr, "hint: %s", e.Hint)
	}
}

// printResult writes a single JSON document to stdout, honoring --pretty.
func printResult(v any) error {
	if globals.pretty {
		return output.PrettyJSON(os.Stdout, v)
	}
	return output.JSON(os.Stdout, v)
}

// runWelcome handles bare `oura`: a machine-readable orientation object on
// stdout and a short human guide on stderr. This is the onboarding front door.
func runWelcome(cmd *cobra.Command, args []string) error {
	authed, method := storedAuthSummary()
	welcome := map[string]any{
		"tool":          "oura",
		"version":       version,
		"authenticated": authed,
		"next_steps":    welcomeNextSteps(authed),
	}
	if method != "" {
		welcome["auth_method"] = method
	}
	if err := printResult(welcome); err != nil {
		return err
	}
	if !authed {
		output.Progressf(os.Stderr, "Welcome to oura. Try it now with zero credentials:")
		output.Progressf(os.Stderr, "  oura sleep --sandbox --pretty")
		output.Progressf(os.Stderr, "Then connect your ring:")
		output.Progressf(os.Stderr, "  oura auth login    # authorize via Oura's OAuth2 browser flow")
		output.Progressf(os.Stderr, "  # legacy bearer token: printf %%s \"$TOKEN\" | oura auth login --token-stdin")
		output.Progressf(os.Stderr, "Agents: load `oura schema` once and drive every command from it.")
	}
	return nil
}

func welcomeNextSteps(authed bool) []string {
	if authed {
		return []string{
			"oura sleep --start 2026-06-29 --end 2026-07-06",
			"oura doctor",
			"oura schema",
		}
	}
	return []string{
		"oura sleep --sandbox",
		"oura auth login",
		"oura doctor",
		"oura schema",
	}
}
