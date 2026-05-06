package connectors_test

// Smoke test that exercises the YAML structure of every shipped
// fixture WITHOUT going through our ParseManifest stub. Catches
// (a) malformed YAML that would crash the parser, (b) yaml-tag
// mismatches between fixtures and the Manifest struct definition,
// (c) unknown top-level keys that would be silently ignored.
//
// This test is fast (no DB, no I/O beyond reading the fixture files)
// and is the first signal a developer gets when a fixture's shape
// drifts from the schema.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/connectors"
	"gopkg.in/yaml.v3"
)

func TestFixtures_RawYAMLParsesIntoManifest(t *testing.T) {
	entries, err := os.ReadDir("fixtures")
	if err != nil {
		t.Fatalf("read fixtures dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no fixtures present")
	}

	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("fixtures", e.Name()))
			if err != nil {
				t.Fatalf("read: %v", err)
			}

			// Strict-decode so unknown keys are rejected. If a fixture
			// uses e.g. `display_name:` instead of `name:` we want a
			// loud failure, not a silent zero-value.
			dec := yaml.NewDecoder(bytes.NewReader(data))
			dec.KnownFields(true)

			var m connectors.Manifest
			if err := dec.Decode(&m); err != nil {
				t.Fatalf("yaml decode: %v", err)
			}

			// Smoke-level invariants — full Validate() lives in
			// manifest_test.go and runs once ParseManifest+Validate are
			// implemented. These are just "did the fields land in the
			// expected slots".
			if m.ID == "" {
				t.Error("ID empty after yaml decode")
			}
			if m.Name == "" {
				t.Error("Name empty after yaml decode")
			}
			if m.AuthMode == "" {
				t.Error("AuthMode empty after yaml decode")
			}
			if m.MCP.Transport == "" {
				t.Error("MCP.Transport empty — fixture likely uses wrong yaml key")
			}
			if !connectors.IsValidTransport(m.MCP.Transport) {
				t.Errorf("MCP.Transport = %q, not a recognized transport", m.MCP.Transport)
			}

			count++
		})
	}

	if count == 0 {
		t.Fatal("no .yaml fixtures matched — directory layout drift?")
	}
}
