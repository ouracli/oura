package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Unicode code points used to build adversarial test inputs. Built from
// \uXXXX escapes rather than embedding literal invisible/control characters
// in the source file, which upsets tooling (e.g. a raw BOM anywhere but byte
// 0 of a Go source file is a hard compile error).
const (
	rlo  = "\u202e" // RIGHT-TO-LEFT OVERRIDE
	lro  = "\u202d" // LEFT-TO-RIGHT OVERRIDE
	lre  = "\u202a" // LEFT-TO-RIGHT EMBEDDING
	lri  = "\u2066" // LEFT-TO-RIGHT ISOLATE
	rli  = "\u2067" // RIGHT-TO-LEFT ISOLATE
	fsi  = "\u2068" // FIRST STRONG ISOLATE
	pdi  = "\u2069" // POP DIRECTIONAL ISOLATE
	zwsp = "\u200b" // ZERO WIDTH SPACE
	zwnj = "\u200c" // ZERO WIDTH NON-JOINER
	zwj  = "\u200d" // ZERO WIDTH JOINER
	lrm  = "\u200e" // LEFT-TO-RIGHT MARK
	rlm  = "\u200f" // RIGHT-TO-LEFT MARK
	bomC = "\ufeff" // BOM / ZERO WIDTH NO-BREAK SPACE
	nel  = "\u0085" // C1 control (NEXT LINE)
	csiC = "\u009b" // C1 control (CSI as a bare codepoint, not an escape sequence)
)

func TestSanitize(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain ascii is untouched", "hello world", "hello world"},
		{"keeps newline and tab", "line1\n\tindented", "line1\n\tindented"},
		{"keeps normal unicode", "héllo wörld 日本語 \U0001F389", "héllo wörld 日本語 \U0001F389"},
		{
			"strips CSI ANSI color codes",
			"\x1b[31mred\x1b[0m text",
			"red text",
		},
		{
			"strips CSI cursor movement",
			"a\x1b[2Jb\x1b[1;1Hc",
			"abc",
		},
		{
			"strips OSC sequence terminated by BEL",
			"before\x1b]0;window title\x07after",
			"beforeafter",
		},
		{
			"strips OSC sequence terminated by ST",
			"before\x1b]8;;http://example.com\x1b\\after",
			"beforeafter",
		},
		{
			"strips lone two-char escape",
			"a\x1bMb",
			"ab",
		},
		{
			"strips bidi embedding/override characters",
			"a" + rlo + "b" + lro + "c" + lre + "d",
			"abcd",
		},
		{
			"strips bidi isolates",
			"a" + lri + "b" + rli + "c" + fsi + "d" + pdi + "e",
			"abcde",
		},
		{
			"strips zero-width characters and LRM/RLM",
			"a" + zwsp + "b" + zwnj + "c" + zwj + "d" + lrm + "e" + rlm + "f",
			"abcdef",
		},
		{
			"strips BOM / zero-width no-break space",
			"a" + bomC + "b",
			"ab",
		},
		{
			"strips C0 controls except newline/tab",
			"a\x00b\x01c\x07d\x1fe",
			"abcde",
		},
		{
			"strips C1 controls",
			"a" + nel + "b" + csiC + "c",
			"abc",
		},
		{
			"combined attack: ansi + bidi + zero-width",
			"\x1b[1msafe" + rlo + zwsp + "looking\x1b[0m",
			"safelooking",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sanitize(tc.input); got != tc.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestStderrWriteSanitizes(t *testing.T) {
	var buf bytes.Buffer
	sw := Stderr{W: &buf}
	input := []byte("\x1b[31mred\x1b[0m\n")
	n, err := sw.Write(input)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Write must report the length of the input it was given, not the
	// sanitized/shorter output, or callers using io.Copy will retry/error.
	if n != len(input) {
		t.Errorf("Write returned n=%d, want len(input)=%d", n, len(input))
	}
	if got := buf.String(); got != "red\n" {
		t.Errorf("sanitized output = %q, want %q", got, "red\n")
	}
}

func TestStderrWriteFixesInvalidUTF8(t *testing.T) {
	var buf bytes.Buffer
	sw := Stderr{W: &buf}
	invalid := []byte{'a', 0xff, 'b'}
	if _, err := sw.Write(invalid); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "a") || !strings.Contains(got, "b") {
		t.Errorf("expected valid bytes preserved, got %q", got)
	}
}

func TestJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, map[string]any{"a": 1, "b": "<html>"}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	// SetEscapeHTML(false) means angle brackets must NOT be escaped.
	if !strings.Contains(buf.String(), "<html>") {
		t.Errorf("expected literal <html> unescaped, got %q", buf.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
}

func TestPrettyJSONIsIndented(t *testing.T) {
	var buf bytes.Buffer
	if err := PrettyJSON(&buf, map[string]any{"a": 1}); err != nil {
		t.Fatalf("PrettyJSON: %v", err)
	}
	if !strings.Contains(buf.String(), "\n") {
		t.Errorf("expected indented (multi-line) output, got %q", buf.String())
	}
}

func TestNDJSONEmitAndSummary(t *testing.T) {
	var buf bytes.Buffer
	nd := NewNDJSON(&buf)
	docs := []map[string]any{
		{"id": "1"},
		{"id": "2"},
		{"id": "3"},
	}
	for _, d := range docs {
		if err := nd.Emit(d); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}
	if err := nd.Summary(map[string]any{"endpoint": "sleep"}); err != nil {
		t.Fatalf("Summary: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != len(docs)+1 {
		t.Fatalf("got %d lines, want %d (docs + summary)", len(lines), len(docs)+1)
	}
	for i, d := range docs {
		var got map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &got); err != nil {
			t.Fatalf("line %d not valid JSON: %v", i, err)
		}
		if got["id"] != d["id"] {
			t.Errorf("line %d id = %v, want %v", i, got["id"], d["id"])
		}
	}

	var summary map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &summary); err != nil {
		t.Fatalf("summary line not valid JSON: %v", err)
	}
	if summary["type"] != "summary" {
		t.Errorf("summary.type = %v, want %q", summary["type"], "summary")
	}
	if count, ok := summary["count"].(float64); !ok || int(count) != len(docs) {
		t.Errorf("summary.count = %v, want %d", summary["count"], len(docs))
	}
	if summary["ok"] != true {
		t.Errorf("summary.ok = %v, want true", summary["ok"])
	}
	if summary["endpoint"] != "sleep" {
		t.Errorf("summary.endpoint = %v, want %q (extras must merge)", summary["endpoint"], "sleep")
	}
}

func TestNDJSONSummaryReflectsEmitError(t *testing.T) {
	var buf bytes.Buffer
	nd := NewNDJSON(&buf)
	// Force an emit error by writing an unmarshalable value (a channel).
	if err := nd.Emit(make(chan int)); err == nil {
		t.Fatal("expected Emit of an unmarshalable value to error")
	}
	if err := nd.Summary(nil); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	var summary map[string]any
	if err := json.Unmarshal(buf.Bytes(), &summary); err != nil {
		t.Fatalf("summary not valid JSON: %v", err)
	}
	if summary["ok"] != false {
		t.Errorf("summary.ok = %v, want false after a failed Emit", summary["ok"])
	}
	if count, ok := summary["count"].(float64); !ok || int(count) != 0 {
		t.Errorf("summary.count = %v, want 0 (failed emit should not increment count)", summary["count"])
	}
}

func TestProgressfSanitizesAndWritesLine(t *testing.T) {
	var buf bytes.Buffer
	Progressf(&buf, "hint: %s\x1b[31m!!!\x1b[0m", "run 'oura doctor'")
	want := "hint: run 'oura doctor'!!!\n"
	if got := buf.String(); got != want {
		t.Errorf("Progressf output = %q, want %q", got, want)
	}
}
