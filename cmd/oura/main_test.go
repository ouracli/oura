package main

import (
	"errors"
	"testing"

	"github.com/akeemjenkins/ouracli/internal/envelope"
)

func TestClassifyCobraErrorNil(t *testing.T) {
	if err := classifyCobraError(nil); err != nil {
		t.Errorf("classifyCobraError(nil) = %v, want nil", err)
	}
}

func TestClassifyCobraErrorPassesThroughEnvelopeErrors(t *testing.T) {
	orig := envelope.New(envelope.KindAuth, "token_invalid", "bad token", "run 'oura auth login'")
	got := classifyCobraError(orig)
	if got != orig {
		t.Errorf("classifyCobraError should pass an existing *envelope.Error through unchanged, got %v", got)
	}
}

func TestClassifyCobraErrorRecognizedPrefixes(t *testing.T) {
	cases := []string{
		"unknown flag: --bogus",
		"unknown command \"frobnicate\" for \"oura\"",
		"unknown shorthand flag: 'z' in -z",
		"invalid argument \"abc\" for \"--limit\" flag",
		"flag needs an argument: --start",
		"accepts at most 1 arg(s), received 2",
		"requires at least 1 arg(s), only received 0",
		"bad flag syntax: -=",
	}
	for _, msg := range cases {
		t.Run(msg, func(t *testing.T) {
			got := classifyCobraError(errors.New(msg))
			envErr, ok := got.(*envelope.Error)
			if !ok {
				t.Fatalf("classifyCobraError(%q) = %T, want *envelope.Error", msg, got)
			}
			if envErr.Kind != envelope.KindUsage {
				t.Errorf("Kind = %q, want %q", envErr.Kind, envelope.KindUsage)
			}
			if envErr.Code != envelope.ExitUsage {
				t.Errorf("Code = %d, want %d", envErr.Code, envelope.ExitUsage)
			}
			if envErr.Message != msg {
				t.Errorf("Message = %q, want the original error text %q", envErr.Message, msg)
			}
			if envErr.Hint == "" {
				t.Error("expected a non-empty hint pointing at 'oura schema'")
			}
		})
	}
}

func TestClassifyCobraErrorUnrecognizedPassesThroughRaw(t *testing.T) {
	orig := errors.New("some unrelated internal failure")
	got := classifyCobraError(orig)
	if got != orig {
		t.Errorf("classifyCobraError should pass unrecognized errors through unchanged, got %v", got)
	}
}
