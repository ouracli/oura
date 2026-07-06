package envelope

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestExitCode(t *testing.T) {
	cases := []struct {
		kind Kind
		want int
	}{
		{KindOK, 0},
		{KindInternal, 1},
		{KindAuth, 2},
		{KindUsage, 3},
		{KindConfig, 4},
		{KindAPI, 5},
		{KindNetwork, 6},
		{KindRateLimit, 7},
		{KindSubscription, 8},
		{Kind("bogus"), ExitInternal}, // unknown kind falls back to internal
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			if got := ExitCode(tc.kind); got != tc.want {
				t.Errorf("ExitCode(%q) = %d, want %d", tc.kind, got, tc.want)
			}
		})
	}
}

func TestNew(t *testing.T) {
	e := New(KindAuth, "token_invalid", "the token is bad", "run 'oura auth login'")
	if e.Kind != KindAuth {
		t.Errorf("Kind = %q, want %q", e.Kind, KindAuth)
	}
	if e.Code != ExitAuth {
		t.Errorf("Code = %d, want %d", e.Code, ExitAuth)
	}
	if e.Reason != "token_invalid" {
		t.Errorf("Reason = %q, want %q", e.Reason, "token_invalid")
	}
	if e.Message != "the token is bad" {
		t.Errorf("Message = %q, want %q", e.Message, "the token is bad")
	}
	if e.Hint != "run 'oura auth login'" {
		t.Errorf("Hint = %q, want %q", e.Hint, "run 'oura auth login'")
	}
}

func TestErrorString(t *testing.T) {
	e := New(KindUsage, "bad_flag", "unknown flag: --foo", "")
	want := "usage/bad_flag: unknown flag: --foo"
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestFrom(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if From(nil) != nil {
			t.Error("From(nil) should be nil")
		}
	})
	t.Run("already an *Error is passed through unchanged", func(t *testing.T) {
		orig := New(KindNetwork, "timeout", "boom", "retry")
		got := From(orig)
		if got != orig {
			t.Errorf("From(*Error) should return the same pointer, got a new one: %+v", got)
		}
	})
	t.Run("plain error coerces to KindInternal", func(t *testing.T) {
		got := From(errors.New("something broke"))
		if got.Kind != KindInternal {
			t.Errorf("Kind = %q, want %q", got.Kind, KindInternal)
		}
		if got.Code != ExitInternal {
			t.Errorf("Code = %d, want %d", got.Code, ExitInternal)
		}
		if got.Reason != "unexpected" {
			t.Errorf("Reason = %q, want %q", got.Reason, "unexpected")
		}
		if got.Message != "something broke" {
			t.Errorf("Message = %q, want %q", got.Message, "something broke")
		}
	})
}

func TestErrorWriteShape(t *testing.T) {
	e := New(KindRateLimit, "rate_limited", "HTTP 429", "wait and retry")
	var buf bytes.Buffer
	e.Write(&buf)

	// Must be exactly one line of JSON.
	out := buf.String()
	if n := bytes.Count(buf.Bytes(), []byte("\n")); n != 1 {
		t.Fatalf("Write produced %d newlines, want exactly 1; output: %q", n, out)
	}

	var decoded struct {
		Error struct {
			Kind    string `json:"kind"`
			Code    int    `json:"code"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
			Hint    string `json:"hint"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v (output: %q)", err, out)
	}
	if decoded.Error.Kind != "ratelimit" {
		t.Errorf("error.kind = %q, want %q", decoded.Error.Kind, "ratelimit")
	}
	if decoded.Error.Code != ExitRateLimit {
		t.Errorf("error.code = %d, want %d", decoded.Error.Code, ExitRateLimit)
	}
	if decoded.Error.Reason != "rate_limited" {
		t.Errorf("error.reason = %q, want %q", decoded.Error.Reason, "rate_limited")
	}
	if decoded.Error.Message != "HTTP 429" {
		t.Errorf("error.message = %q, want %q", decoded.Error.Message, "HTTP 429")
	}
	if decoded.Error.Hint != "wait and retry" {
		t.Errorf("error.hint = %q, want %q", decoded.Error.Hint, "wait and retry")
	}
}

// TestErrorWriteOmitsEmptyHint checks the `omitempty` on Hint is honored so
// agents parsing the envelope don't see a spurious empty hint field.
func TestErrorWriteOmitsEmptyHint(t *testing.T) {
	e := New(KindInternal, "oops", "boom", "")
	var buf bytes.Buffer
	e.Write(&buf)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal outer: %v", err)
	}
	var inner map[string]json.RawMessage
	if err := json.Unmarshal(raw["error"], &inner); err != nil {
		t.Fatalf("unmarshal inner: %v", err)
	}
	if _, ok := inner["hint"]; ok {
		t.Errorf("expected no \"hint\" key when Hint is empty, got %v", inner)
	}
}
