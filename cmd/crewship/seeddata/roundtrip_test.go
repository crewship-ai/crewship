package seeddata

import (
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSeeddataYAML_RoundTripStable walks every YAML file under the
// builtin/ embed.FS, unmarshals it to a generic structure, marshals
// it back, and re-unmarshals the result. A semantic round-trip
// mismatch means yaml struct tags or field types are losing data
// somewhere — a `crewship export` that round-tripped the seed via
// our typed loaders would silently drop those bits.
//
// Bug-hunt iter 3 (area 3). Complements the per-loader unit tests:
// those verify the typed loader respects every field in the YAML;
// this one verifies the YAML itself is round-trippable end to end.
func TestSeeddataYAML_RoundTripStable(t *testing.T) {
	t.Parallel()
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		t.Fatalf("read builtin/: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("builtin/ is empty — embed.FS regression")
	}
	for _, e := range entries {
		e := e
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			t.Parallel()
			path := "builtin/" + e.Name()
			raw, err := fs.ReadFile(builtinFS, path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var first any
			if err := yaml.Unmarshal(raw, &first); err != nil {
				t.Fatalf("first unmarshal of %s: %v", path, err)
			}
			marshalled, err := yaml.Marshal(first)
			if err != nil {
				t.Fatalf("remarshal of %s: %v", path, err)
			}
			var second any
			if err := yaml.Unmarshal(marshalled, &second); err != nil {
				t.Fatalf("second unmarshal of %s: %v\nmarshalled output:\n%s", path, err, marshalled)
			}
			if !reflect.DeepEqual(first, second) {
				t.Errorf("%s: round-trip not stable — yaml.Unmarshal -> Marshal -> Unmarshal produced a different tree.\n"+
					"first len: %d bytes, marshalled len: %d bytes",
					path, len(raw), len(marshalled))
			}
		})
	}
}

// TestSeeddataLoaders_RoundTripThroughTypedStructs is the stricter
// follow-up: for each catalogue we have a typed loader, marshal the
// typed value back to YAML, unmarshal again, and assert reflect
// equality. This catches the failure mode "typed struct loses a
// field on remarshal" (missing yaml tag on a new field, json-only
// tag, omitempty hiding legitimate zero values).
func TestSeeddataLoaders_RoundTripThroughTypedStructs(t *testing.T) {
	t.Parallel()

	roundtripTyped := func(t *testing.T, name string, original, target any) {
		t.Helper()
		out, err := yaml.Marshal(original)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if err := yaml.Unmarshal(out, target); err != nil {
			t.Fatalf("unmarshal %s: %v\nyaml:\n%s", name, err, out)
		}
		if !reflect.DeepEqual(original, target) {
			t.Errorf("%s: typed round-trip lost data.\nintermediate YAML:\n%s", name, out)
		}
	}

	t.Run("Skills", func(t *testing.T) {
		t.Parallel()
		got := &[]SkillDef{}
		roundtripTyped(t, "Skills", &Skills, got)
	})
	t.Run("Agents", func(t *testing.T) {
		t.Parallel()
		got := &[]AgentDef{}
		roundtripTyped(t, "Agents", &Agents, got)
	})
	t.Run("Crews", func(t *testing.T) {
		t.Parallel()
		got := &[]CrewDef{}
		roundtripTyped(t, "Crews", &Crews, got)
	})
	t.Run("Integrations", func(t *testing.T) {
		t.Parallel()
		got := &[]IntegrationDef{}
		roundtripTyped(t, "Integrations", &Integrations, got)
	})
	t.Run("Labels", func(t *testing.T) {
		t.Parallel()
		got := &[]LabelDef{}
		roundtripTyped(t, "Labels", &Labels, got)
	})
	t.Run("Projects", func(t *testing.T) {
		t.Parallel()
		got := &[]ProjectDef{}
		roundtripTyped(t, "Projects", &Projects, got)
	})
	t.Run("Issues", func(t *testing.T) {
		t.Parallel()
		got := &[]IssueDef{}
		roundtripTyped(t, "Issues", &Issues, got)
	})
}

// TestSeeddataYAML_AllExpectedFilesPresent pins the file list so a
// future cleanup that drops one of the 5 catalogues without updating
// the loaders surfaces here. Better to fail loud at test time than
// to ship a binary that panics on first init.
func TestSeeddataYAML_AllExpectedFilesPresent(t *testing.T) {
	t.Parallel()
	want := map[string]bool{
		"skills.yaml":       false,
		"agents.yaml":       false,
		"crews.yaml":        false,
		"integrations.yaml": false,
		"issues.yaml":       false,
	}
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		t.Fatalf("read builtin/: %v", err)
	}
	for _, e := range entries {
		if _, ok := want[e.Name()]; ok {
			want[e.Name()] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing builtin/%s — loader expects this file", name)
		}
	}
}
