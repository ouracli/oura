package ouraapi

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/ouracli/oura/internal/envelope"
)

// The fields registry is cross-checked against Oura's own OpenAPI document
// (testdata/openapi-1.35.json, downloaded 2026-07-07 from
// https://cloud.ouraring.com/v2/static/json/openapi-1.35.json) so it cannot
// silently drift from the API contract. Where the spec and the live API
// disagree, the live shape wins and the disagreement is pinned in
// specExceptions below so a future spec that fixes it fails this test and
// prompts a re-probe.

// specExceptions records live-verified deviations from OpenAPI 1.35, keyed by
// the trailing path segment. Probed 2026-07-07 against both the sandbox and
// the real API: heartrate and ring_battery_level documents carry
// producer_timestamp, and the spec's timestamp_unix does not exist.
var specExceptions = map[string]struct{ add, remove []string }{
	"heartrate":          {add: []string{"producer_timestamp"}, remove: []string{"timestamp_unix"}},
	"ring_battery_level": {add: []string{"producer_timestamp"}, remove: []string{"timestamp_unix"}},
}

// fieldsIgnoredByAPI lists endpoints whose spec declares a fields query param
// that the live API accepts but ignores (verified 2026-07-07: identical full
// documents with and without a projection), so the registry keeps
// HasFields false for them.
var fieldsIgnoredByAPI = map[string]bool{
	"daily_resilience": true,
}

// openapiDoc is the slice of the OpenAPI document this test needs.
type openapiDoc struct {
	Paths      map[string]map[string]openapiOperation `json:"paths"`
	Components struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	} `json:"components"`
}

type openapiOperation struct {
	Parameters []struct {
		Name string `json:"name"`
		In   string `json:"in"`
	} `json:"parameters"`
	Responses map[string]struct {
		Content map[string]struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"content"`
	} `json:"responses"`
}

type openapiSchema struct {
	Ref        string                     `json:"$ref"`
	AnyOf      []json.RawMessage          `json:"anyOf"`
	Properties map[string]json.RawMessage `json:"properties"`
	Items      json.RawMessage            `json:"items"`
}

func loadSpec(t *testing.T) *openapiDoc {
	t.Helper()
	raw, err := os.ReadFile("testdata/openapi-1.35.json")
	if err != nil {
		t.Fatalf("reading spec: %v", err)
	}
	var doc openapiDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing spec: %v", err)
	}
	return &doc
}

// deref follows $ref chains into components.schemas.
func (d *openapiDoc) deref(t *testing.T, raw json.RawMessage) openapiSchema {
	t.Helper()
	for {
		// Fresh struct each hop: Unmarshal leaves absent keys untouched, so a
		// reused struct would keep the previous hop's $ref and loop forever.
		var s openapiSchema
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("parsing schema: %v", err)
		}
		if s.Ref == "" {
			return s
		}
		name := s.Ref[strings.LastIndex(s.Ref, "/")+1:]
		next, ok := d.Components.Schemas[name]
		if !ok {
			t.Fatalf("unresolvable $ref %q", s.Ref)
		}
		raw = next
	}
}

// documentFields extracts the sorted top-level field names of the document
// model behind a collection (or single-object) 200 response schema.
func (d *openapiDoc) documentFields(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	s := d.deref(t, raw)
	// Collection responses are anyOf(MultiDocumentResponse_X_, ...): pick the
	// variant that actually types its data items.
	for _, alt := range s.AnyOf {
		if r := d.deref(t, alt); r.Properties["data"] != nil {
			s = r
			break
		}
	}
	if data, ok := s.Properties["data"]; ok {
		arr := d.deref(t, data)
		s = d.deref(t, arr.Items)
	}
	fields := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		fields = append(fields, name)
	}
	sort.Strings(fields)
	return fields
}

// TestFieldsMatchOpenAPISpec asserts every registry entry's Fields equals the
// spec's document properties, modulo the pinned live-API exceptions.
func TestFieldsMatchOpenAPISpec(t *testing.T) {
	doc := loadSpec(t)
	for _, ep := range Endpoints {
		item, ok := doc.Paths["/v2"+ep.Path]
		if !ok {
			t.Errorf("%s: path /v2%s not in spec", ep.CLI, ep.Path)
			continue
		}
		get, ok := item["get"]
		if !ok {
			t.Errorf("%s: no GET in spec", ep.CLI)
			continue
		}
		schema := get.Responses["200"].Content["application/json"].Schema
		if schema == nil {
			t.Errorf("%s: no 200 application/json schema in spec", ep.CLI)
			continue
		}
		want := doc.documentFields(t, schema)
		if exc, ok := specExceptions[ep.Path[strings.LastIndex(ep.Path, "/")+1:]]; ok {
			kept := want[:0]
			for _, f := range want {
				removed := false
				for _, r := range exc.remove {
					if f == r {
						removed = true
					}
				}
				if !removed {
					kept = append(kept, f)
				}
			}
			want = append(kept, exc.add...)
			sort.Strings(want)
		}
		got := append([]string(nil), ep.Fields...)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: Fields drift from spec\n got: %v\nwant: %v", ep.CLI, got, want)
		}
	}
}

// TestHasFieldsMatchesOpenAPISpec asserts HasFields agrees with the spec's
// declared fields query param, except where the live API ignores it.
func TestHasFieldsMatchesOpenAPISpec(t *testing.T) {
	doc := loadSpec(t)
	for _, ep := range Endpoints {
		get := doc.Paths["/v2"+ep.Path]["get"]
		specHas := false
		for _, p := range get.Parameters {
			if p.In == "query" && p.Name == "fields" {
				specHas = true
			}
		}
		want := specHas && !fieldsIgnoredByAPI[ep.Path[strings.LastIndex(ep.Path, "/")+1:]]
		if ep.HasFields != want {
			t.Errorf("%s: HasFields = %v, want %v (spec declares fields: %v, live API ignores: %v)",
				ep.CLI, ep.HasFields, want, specHas, fieldsIgnoredByAPI[ep.Path[strings.LastIndex(ep.Path, "/")+1:]])
		}
	}
}

// TestFieldsSortedUniqueNonEmpty pins the registry invariants Fields relies
// on: present for every endpoint, sorted, and free of duplicates.
func TestFieldsSortedUniqueNonEmpty(t *testing.T) {
	for _, ep := range Endpoints {
		if len(ep.Fields) == 0 {
			t.Errorf("%s: empty Fields", ep.CLI)
			continue
		}
		if !sort.StringsAreSorted(ep.Fields) {
			t.Errorf("%s: Fields not sorted: %v", ep.CLI, ep.Fields)
		}
		seen := map[string]bool{}
		for _, f := range ep.Fields {
			if seen[f] {
				t.Errorf("%s: duplicate field %q", ep.CLI, f)
			}
			seen[f] = true
		}
	}
}

func TestNormalizeFields(t *testing.T) {
	sleep, ok := FindEndpoint("sleep")
	if !ok {
		t.Fatal("no sleep endpoint")
	}
	resilience, ok := FindEndpoint("resilience")
	if !ok {
		t.Fatal("no resilience endpoint")
	}

	tests := []struct {
		name    string
		ep      Endpoint
		in      string
		want    string
		errPart string // substring of the error message; empty = no error
	}{
		{"empty is a no-op", sleep, "", "", ""},
		{"blank is a no-op", sleep, "  ", "", ""},
		{"valid single", sleep, "day", "day", ""},
		{"valid multiple", sleep, "day,score", "day,score", ""},
		{"whitespace trimmed", sleep, " day , score ", "day,score", ""},
		{"trailing comma dropped", sleep, "day,", "day", ""},
		{"unknown field rejected", sleep, "bogus", "", "unknown field bogus"},
		{"unknown among valid rejected", sleep, "day,bogus", "", "unknown field bogus"},
		{"unsupported endpoint rejected", resilience, "day", "", "does not support the fields projection"},
		{"unsupported endpoint empty ok", resilience, "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.ep.NormalizeFields(tc.in)
			if tc.errPart == "" {
				if err != nil {
					t.Fatalf("NormalizeFields(%q) error: %v", tc.in, err)
				}
				if got != tc.want {
					t.Errorf("NormalizeFields(%q) = %q, want %q", tc.in, got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("NormalizeFields(%q) = %q, want error containing %q", tc.in, got, tc.errPart)
			}
			if !strings.Contains(err.Error(), tc.errPart) {
				t.Errorf("NormalizeFields(%q) error %q, want it to contain %q", tc.in, err.Error(), tc.errPart)
			}
		})
	}
}

// TestNormalizeFieldsErrorListsValidNames pins that the unknown-field hint
// enumerates the valid names, since that hint is what an agent acts on.
func TestNormalizeFieldsErrorListsValidNames(t *testing.T) {
	ep, ok := FindEndpoint("stress")
	if !ok {
		t.Fatal("no stress endpoint")
	}
	_, err := ep.NormalizeFields("typo")
	if err == nil {
		t.Fatal("want error for unknown field")
	}
	env := envelope.From(err)
	if env.Reason != "unknown_field" {
		t.Errorf("reason = %q, want unknown_field", env.Reason)
	}
	for _, f := range ep.Fields {
		if !strings.Contains(env.Hint, f) {
			t.Errorf("hint %q missing valid field %q", env.Hint, f)
		}
	}
}
