// Package output enforces ouracli's stdout/stderr discipline.
//
// stdout carries exactly one thing per invocation: a JSON document, an NDJSON
// stream ending in a {"type":"summary"} object, or an error envelope. stderr
// carries human-readable progress, sanitized so it is safe to feed back into
// a model's context (no ANSI escapes, bidi controls, or zero-width runes).
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// JSON writes v to w as a single compact JSON line.
func JSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// PrettyJSON writes v indented, for humans using --pretty.
func PrettyJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// NDJSON streams documents one per line and is closed by a summary object,
// so callers know the stream ended deliberately rather than truncated.
type NDJSON struct {
	w       io.Writer
	count   int
	errored bool
}

func NewNDJSON(w io.Writer) *NDJSON { return &NDJSON{w: w} }

// Emit writes one document line.
func (n *NDJSON) Emit(v any) error {
	if err := JSON(n.w, v); err != nil {
		n.errored = true
		return err
	}
	n.count++
	return nil
}

// Summary terminates the stream. extra keys are merged into the summary object.
func (n *NDJSON) Summary(extra map[string]any) error {
	s := map[string]any{"type": "summary", "count": n.count, "ok": !n.errored}
	for k, v := range extra {
		s[k] = v
	}
	return JSON(n.w, s)
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-_]`)

// Sanitize strips ANSI escapes, C0/C1 controls (except \n and \t), bidi
// overrides, and zero-width characters from s.
func Sanitize(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || (r >= 0x7f && r <= 0x9f): // C0/C1 controls
		case r >= 0x200b && r <= 0x200f: // zero-width + LRM/RLM
		case r >= 0x202a && r <= 0x202e: // bidi embedding/override
		case r >= 0x2066 && r <= 0x2069: // bidi isolates
		case r == 0xfeff: // BOM / zero-width no-break space
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Stderr is a writer that sanitizes everything passing through it.
type Stderr struct{ W io.Writer }

func (s Stderr) Write(p []byte) (int, error) {
	clean := Sanitize(string(bytes.ToValidUTF8(p, []byte("�"))))
	if _, err := io.WriteString(s.W, clean); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Progressf prints a sanitized human progress line to w (normally stderr).
func Progressf(w io.Writer, format string, args ...any) {
	fmt.Fprintln(w, Sanitize(fmt.Sprintf(format, args...)))
}
