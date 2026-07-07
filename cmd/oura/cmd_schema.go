package main

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ouracli/oura/internal/envelope"
)

// The schema manifest is derived reflectively from the cobra tree at runtime,
// so it can never drift from the actual commands. Agents load it once and
// drive the tool from it instead of parsing --help prose.

type schemaFlag struct {
	Name        string `json:"name"`
	Shorthand   string `json:"shorthand,omitempty"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Description string `json:"description"`
}

type schemaArg struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

type schemaCommand struct {
	Name  string       `json:"name"`
	Short string       `json:"short"`
	Long  string       `json:"long,omitempty"`
	Flags []schemaFlag `json:"flags"`
	Args  []schemaArg  `json:"args"`
	// Fields is the endpoint's valid --fields projection values (top-level
	// document field names), present only on data commands that support the
	// projection. Unknown names are rejected client-side because the API
	// would silently ignore them.
	Fields    []string        `json:"fields,omitempty"`
	Stdout    string          `json:"stdout,omitempty"`
	ExitCodes []int           `json:"exit_codes"`
	Commands  []schemaCommand `json:"commands,omitempty"`
}

type schemaExitCodeDoc struct {
	Code        int    `json:"code"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	AgentAction string `json:"agent_action"`
}

type schemaRoot struct {
	Tool         string              `json:"tool"`
	Version      string              `json:"version"`
	Commands     []schemaCommand     `json:"commands"`
	GlobalFlags  []schemaFlag        `json:"global_flags"`
	ExitCodeDocs []schemaExitCodeDoc `json:"exit_code_docs"`
}

var exitCodeDocs = []schemaExitCodeDoc{
	{envelope.ExitOK, string(envelope.KindOK), "success", "continue"},
	{envelope.ExitInternal, string(envelope.KindInternal), "internal error (bug)", "surface to caller"},
	{envelope.ExitAuth, string(envelope.KindAuth), "missing/invalid/expired credentials", "run 'oura auth login'"},
	{envelope.ExitUsage, string(envelope.KindUsage), "bad flags, args, or dates", "fix the invocation via 'oura schema'"},
	{envelope.ExitConfig, string(envelope.KindConfig), "config or keyring problem", "run 'oura doctor'"},
	{envelope.ExitAPI, string(envelope.KindAPI), "Oura API rejected the request", "inspect .error.reason"},
	{envelope.ExitNetwork, string(envelope.KindNetwork), "network failure reaching api.ouraring.com", "retry with backoff"},
	{envelope.ExitRateLimit, string(envelope.KindRateLimit), "rate limited (HTTP 429)", "wait and retry"},
	{envelope.ExitSubscription, string(envelope.KindSubscription), "data requires an active Oura subscription (HTTP 426)", "inform the user"},
}

func newSchemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema [command]",
		Short: "Emit the JSON tool manifest (all commands, flags, exit codes)",
		Args:  cobra.MaximumNArgs(1),
		Annotations: map[string]string{
			annStdout:    "json",
			annExitCodes: "0,3",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			if len(args) == 1 {
				target := findCommand(root, args[0])
				if target == nil {
					return envelope.New(envelope.KindUsage, "unknown_command",
						"no such command: "+args[0], "run 'oura schema' for the full manifest")
				}
				return printResult(describeCommand(target))
			}
			return printResult(schemaRoot{
				Tool:         "oura",
				Version:      version,
				Commands:     describeChildren(root),
				GlobalFlags:  describeFlagSet(root.PersistentFlags()),
				ExitCodeDocs: exitCodeDocs,
			})
		},
	}
}

func findCommand(root *cobra.Command, name string) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func describeChildren(c *cobra.Command) []schemaCommand {
	var out []schemaCommand
	for _, sub := range c.Commands() {
		if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		out = append(out, describeCommand(sub))
	}
	return out
}

func describeCommand(c *cobra.Command) schemaCommand {
	sc := schemaCommand{
		Name:      c.Name(),
		Short:     c.Short,
		Long:      c.Long,
		Flags:     describeFlagSet(c.Flags()),
		Args:      parseUseArgs(c.Use),
		Stdout:    c.Annotations[annStdout],
		ExitCodes: parseExitCodes(c.Annotations[annExitCodes]),
		Commands:  describeChildren(c),
	}
	if ann := c.Annotations[annFields]; ann != "" {
		sc.Fields = strings.Split(ann, ",")
	}
	return sc
}

func describeFlagSet(fs *pflag.FlagSet) []schemaFlag {
	out := []schemaFlag{}
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" || f.Name == "version" {
			return
		}
		out = append(out, schemaFlag{
			Name:        f.Name,
			Shorthand:   f.Shorthand,
			Type:        f.Value.Type(),
			Default:     f.DefValue,
			Description: f.Usage,
		})
	})
	return out
}

// parseUseArgs derives positional args from the cobra Use string:
// <name> is required, [name] is optional.
func parseUseArgs(use string) []schemaArg {
	out := []schemaArg{}
	for _, tok := range strings.Fields(use)[1:] {
		switch {
		case strings.HasPrefix(tok, "<") && strings.HasSuffix(tok, ">"):
			out = append(out, schemaArg{Name: strings.Trim(tok, "<>"), Required: true})
		case strings.HasPrefix(tok, "[") && strings.HasSuffix(tok, "]"):
			out = append(out, schemaArg{Name: strings.Trim(tok, "[]"), Required: false})
		}
	}
	return out
}

func parseExitCodes(s string) []int {
	if s == "" {
		return []int{envelope.ExitOK, envelope.ExitInternal}
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
			out = append(out, n)
		}
	}
	return out
}
