// Package envelope defines the typed error contract for ouracli.
//
// Every failure the CLI reports is a single JSON object on stdout:
//
//	{"error":{"kind":"auth","code":2,"reason":"token_invalid","message":"...","hint":"..."}}
//
// and the process exits with the code matching the kind. Agents branch on
// .error.kind or the exit code; .hint tells them (or the user) what to do next.
package envelope

import (
	"encoding/json"
	"fmt"
	"io"
)

// Kind classifies an error. Kinds map 1:1 to exit codes.
type Kind string

const (
	KindOK           Kind = "ok"
	KindInternal     Kind = "internal"
	KindAuth         Kind = "auth"
	KindUsage        Kind = "usage"
	KindConfig       Kind = "config"
	KindAPI          Kind = "api"
	KindNetwork      Kind = "network"
	KindRateLimit    Kind = "ratelimit"
	KindSubscription Kind = "subscription"
)

// Exit codes, one per kind. Stable: agents key retry/escalation off these.
const (
	ExitOK           = 0
	ExitInternal     = 1
	ExitAuth         = 2
	ExitUsage        = 3
	ExitConfig       = 4
	ExitAPI          = 5
	ExitNetwork      = 6
	ExitRateLimit    = 7
	ExitSubscription = 8
)

var exitCodes = map[Kind]int{
	KindOK:           ExitOK,
	KindInternal:     ExitInternal,
	KindAuth:         ExitAuth,
	KindUsage:        ExitUsage,
	KindConfig:       ExitConfig,
	KindAPI:          ExitAPI,
	KindNetwork:      ExitNetwork,
	KindRateLimit:    ExitRateLimit,
	KindSubscription: ExitSubscription,
}

// ExitCode returns the exit code for a kind (ExitInternal for unknown kinds).
func ExitCode(k Kind) int {
	if c, ok := exitCodes[k]; ok {
		return c
	}
	return ExitInternal
}

// Error is a typed CLI error. It implements the error interface so it can
// flow through ordinary Go error returns and be rendered once at the top.
type Error struct {
	Kind    Kind   `json:"kind"`
	Code    int    `json:"code"`
	Reason  string `json:"reason"`         // stable machine-readable slug, e.g. "token_invalid"
	Message string `json:"message"`        // human-readable detail
	Hint    string `json:"hint,omitempty"` // what to do next
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s/%s: %s", e.Kind, e.Reason, e.Message)
}

// New builds an Error with the exit code derived from kind.
func New(kind Kind, reason, message, hint string) *Error {
	return &Error{Kind: kind, Code: ExitCode(kind), Reason: reason, Message: message, Hint: hint}
}

// wire is the on-stdout shape: {"error": {...}}.
type wire struct {
	Err *Error `json:"error"`
}

// Write renders the envelope as one line of JSON to w.
func (e *Error) Write(w io.Writer) {
	b, err := json.Marshal(wire{Err: e})
	if err != nil {
		// Marshaling a flat struct of strings cannot realistically fail;
		// emit a hand-built envelope rather than nothing.
		fmt.Fprintf(w, `{"error":{"kind":"internal","code":1,"reason":"envelope_marshal","message":%q}}`+"\n", err.Error())
		return
	}
	w.Write(append(b, '\n'))
}

// From coerces any error into an *Error, defaulting to KindInternal.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return e
	}
	return New(KindInternal, "unexpected", err.Error(), "")
}
