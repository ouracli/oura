// Command oura is an agent-first CLI for the Oura Ring API v2.
package main

import (
	"os"
	"strings"

	"github.com/ouracli/oura/internal/envelope"
)

// Set by -ldflags "-X main.version=..." at release time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	root := newRootCmd()
	err := root.Execute()
	if err == nil {
		return
	}
	// stderrOnlyError signals that stdout must be left untouched: the failure is
	// either already on stdout (the --all NDJSON summary line) or stdout is
	// owned by another protocol (the MCP transport). Report it only on stderr
	// and exit with the mapped code.
	if se, ok := err.(*stderrOnlyError); ok {
		fprintStderrSummary(se.env)
		os.Exit(se.env.Code)
	}
	e := envelope.From(classifyCobraError(err))
	e.Write(os.Stdout)
	fprintStderrSummary(e)
	os.Exit(e.Code)
}

// stderrOnlyError wraps an envelope whose JSON must NOT be written to stdout.
// It is used where stdout has already committed to another shape for the
// invocation: the NDJSON stream emitted by `--all` (whose terminating summary
// line already carries the error) and `mcp serve` (whose stdout belongs to the
// MCP transport). main() prints the human summary to stderr and exits with the
// kind's code, leaving stdout exactly as the command left it.
type stderrOnlyError struct{ env *envelope.Error }

func (e *stderrOnlyError) Error() string { return e.env.Error() }

// classifyCobraError maps cobra's own usage errors into the typed envelope so
// agents get {"error":{"kind":"usage",...}} + exit 3 even for a bad flag.
func classifyCobraError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*envelope.Error); ok {
		return err
	}
	msg := err.Error()
	for _, prefix := range []string{
		"unknown flag", "unknown command", "unknown shorthand flag",
		"invalid argument", "flag needs an argument", "accepts",
		"requires", "bad flag syntax",
	} {
		if strings.HasPrefix(msg, prefix) {
			return envelope.New(envelope.KindUsage, "bad_invocation", msg,
				"run 'oura schema' for the full command manifest")
		}
	}
	return err
}
