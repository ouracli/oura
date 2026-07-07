package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

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
