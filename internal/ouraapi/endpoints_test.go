package ouraapi

import (
	"strings"
	"testing"
)

func TestEndpointsCLINamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, ep := range Endpoints {
		if seen[ep.CLI] {
			t.Errorf("duplicate CLI name %q", ep.CLI)
		}
		seen[ep.CLI] = true
	}
}

func TestEndpointsMCPToolNamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, ep := range Endpoints {
		if seen[ep.MCPTool] {
			t.Errorf("duplicate MCPTool name %q", ep.MCPTool)
		}
		seen[ep.MCPTool] = true
	}
}

func TestEndpointsPathsStartWithUsercollection(t *testing.T) {
	for _, ep := range Endpoints {
		if !strings.HasPrefix(ep.Path, "/usercollection/") {
			t.Errorf("endpoint %q has Path %q, want it to start with /usercollection/", ep.CLI, ep.Path)
		}
	}
}

func TestEndpointsExactlyOneSingleObjectStyle(t *testing.T) {
	count := 0
	var which []string
	for _, ep := range Endpoints {
		if ep.Style == StyleSingleObject {
			count++
			which = append(which, ep.CLI)
		}
	}
	if count != 1 {
		t.Errorf("found %d StyleSingleObject endpoints (%v), want exactly 1 (personal_info)", count, which)
	}
}

func TestEndpointsVO2MaxPathCapitalO(t *testing.T) {
	ep, ok := FindEndpoint("vo2max")
	if !ok {
		t.Fatal("no endpoint registered with CLI name \"vo2max\"")
	}
	if ep.Path != "/usercollection/vO2_max" {
		t.Errorf("vo2max Path = %q, want %q (Oura's path capitalizes the O)", ep.Path, "/usercollection/vO2_max")
	}
}

func TestEndpointsDeprecatedImpliesTagsLegacy(t *testing.T) {
	for _, ep := range Endpoints {
		if ep.Deprecated && ep.CLI != "tags-legacy" {
			t.Errorf("endpoint %q is marked Deprecated but only tags-legacy is expected to be", ep.CLI)
		}
	}
	ep, ok := FindEndpoint("tags-legacy")
	if !ok {
		t.Fatal("no endpoint registered with CLI name \"tags-legacy\"")
	}
	if !ep.Deprecated {
		t.Error("tags-legacy should be marked Deprecated")
	}
}

func TestEndpointsEveryEntryHasCoreFields(t *testing.T) {
	for _, ep := range Endpoints {
		if ep.CLI == "" {
			t.Errorf("endpoint with Path %q has an empty CLI name", ep.Path)
		}
		if ep.MCPTool == "" {
			t.Errorf("endpoint %q has an empty MCPTool name", ep.CLI)
		}
		if !strings.HasPrefix(ep.MCPTool, "oura_") {
			t.Errorf("endpoint %q has MCPTool %q, want it prefixed with oura_", ep.CLI, ep.MCPTool)
		}
		if ep.Short == "" {
			t.Errorf("endpoint %q has an empty Short description", ep.CLI)
		}
	}
}

func TestFindEndpointUnknown(t *testing.T) {
	if _, ok := FindEndpoint("does-not-exist"); ok {
		t.Error("FindEndpoint(\"does-not-exist\") should return ok=false")
	}
}
