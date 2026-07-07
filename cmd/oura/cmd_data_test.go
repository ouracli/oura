package main

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/ouracli/oura/internal/envelope"
	"github.com/ouracli/oura/internal/ouraapi"
)

// TestDataCmdsFieldsSurface pins how the fields registry reaches the CLI
// surface: every HasFields endpoint registers a --fields flag whose help
// enumerates the valid names and carries the annFields annotation the schema
// manifest exposes; endpoints without projection support register neither.
func TestDataCmdsFieldsSurface(t *testing.T) {
	byName := map[string]*cobra.Command{}
	for _, c := range newDataCmds() {
		byName[c.Name()] = c
	}

	for _, ep := range ouraapi.Endpoints {
		cmd := byName[ep.CLI]
		if cmd == nil {
			t.Errorf("no generated command for endpoint %q", ep.CLI)
			continue
		}
		flag := cmd.Flags().Lookup("fields")
		ann := cmd.Annotations[annFields]
		if !ep.HasFields {
			if flag != nil {
				t.Errorf("%s: --fields flag registered but HasFields is false", ep.CLI)
			}
			if ann != "" {
				t.Errorf("%s: fields annotation %q present but HasFields is false", ep.CLI, ann)
			}
			continue
		}
		if flag == nil {
			t.Errorf("%s: HasFields is true but no --fields flag", ep.CLI)
			continue
		}
		if want := strings.Join(ep.Fields, ","); ann != want {
			t.Errorf("%s: fields annotation = %q, want %q", ep.CLI, ann, want)
		}
		for _, f := range ep.Fields {
			if !strings.Contains(flag.Usage, f) {
				t.Errorf("%s: --fields help %q missing valid name %q", ep.CLI, flag.Usage, f)
			}
		}
	}
}

// TestResolveDateRangeDefaults pins the no-flag window at [7 days ago,
// TOMORROW]. Tomorrow is deliberate: Oura's end_date is exclusive on several
// endpoints (probed 2026-07-07), so a default end of today would silently
// omit today's activity and last night's sleep periods — the exact failure
// this default exists to prevent.
func TestResolveDateRangeDefaults(t *testing.T) {
	start, end, err := resolveDateRange("", "")
	if err != nil {
		t.Fatalf("resolveDateRange: %v", err)
	}
	now := time.Now()
	if want := now.AddDate(0, 0, -7).Format("2006-01-02"); start != want {
		t.Errorf("default start = %q, want %q (7 days ago)", start, want)
	}
	if want := now.AddDate(0, 0, 1).Format("2006-01-02"); end != want {
		t.Errorf("default end = %q, want %q (tomorrow — Oura's end_date is exclusive on several endpoints)", end, want)
	}
}

// TestResolveDateRangeExplicitFlags pins that explicit flags pass through
// verbatim (no silent widening) and that --start defaults to 7 days before
// an explicit --end.
func TestResolveDateRangeExplicitFlags(t *testing.T) {
	start, end, err := resolveDateRange("2026-07-01", "2026-07-08")
	if err != nil {
		t.Fatalf("resolveDateRange: %v", err)
	}
	if start != "2026-07-01" || end != "2026-07-08" {
		t.Errorf("explicit flags = [%s, %s], want passed through verbatim", start, end)
	}

	start, end, err = resolveDateRange("", "2026-07-08")
	if err != nil {
		t.Fatalf("resolveDateRange: %v", err)
	}
	if start != "2026-07-01" || end != "2026-07-08" {
		t.Errorf("end-only = [%s, %s], want start 7 days before the explicit end", start, end)
	}
}

func TestResolveDateRangeRejectsMalformedDates(t *testing.T) {
	for _, tc := range []struct{ start, end string }{{"07/01/2026", ""}, {"", "not-a-date"}} {
		if _, _, err := resolveDateRange(tc.start, tc.end); err == nil {
			t.Errorf("resolveDateRange(%q, %q) succeeded, want usage error", tc.start, tc.end)
		} else if envelope.From(err).Kind != envelope.KindUsage {
			t.Errorf("resolveDateRange(%q, %q) error kind = %q, want usage", tc.start, tc.end, envelope.From(err).Kind)
		}
	}
}

// TestEndFlagHelpWarnsAboutExclusiveEnd pins the CLI surfacing of the
// end_date quirk: the --end help (which feeds `oura schema` and --help) must
// carry the warning on every date-range command.
func TestEndFlagHelpWarnsAboutExclusiveEnd(t *testing.T) {
	for _, c := range newDataCmds() {
		ep, ok := ouraapi.FindEndpoint(c.Name())
		if !ok || ep.Style != ouraapi.StyleDateRange {
			continue
		}
		flag := c.Flags().Lookup("end")
		if flag == nil {
			t.Errorf("%s: no --end flag", c.Name())
			continue
		}
		if !strings.Contains(flag.Usage, "EXCLUSIVE") {
			t.Errorf("%s: --end help lacks the EXCLUSIVE warning: %q", c.Name(), flag.Usage)
		}
	}
}

// TestDatetimeRangeEndpointsHaveFields pins the OpenAPI-1.35 upgrade this
// branch verified live: heartrate and battery projections actually work.
func TestDatetimeRangeEndpointsHaveFields(t *testing.T) {
	for _, name := range []string{"heartrate", "battery"} {
		ep, ok := ouraapi.FindEndpoint(name)
		if !ok {
			t.Fatalf("no endpoint %q", name)
		}
		if !ep.HasFields {
			t.Errorf("%s: HasFields = false, want true (verified against the live API 2026-07-07)", name)
		}
	}
}
