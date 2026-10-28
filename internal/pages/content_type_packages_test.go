package pages

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestArtifactTypePackageMetadata(t *testing.T) {
	for _, id := range []string{"artifact-project", "artifact-dashboard", "artifact-slides"} {
		t.Run(id, func(t *testing.T) {
			d, ok := LookupContentType(id, 1)
			if !ok {
				t.Fatal("missing compiled artifact package")
			}
			body, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			var metadata map[string]any
			if err := json.Unmarshal(body, &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata["release"] != "1.1.0" || metadata["contract_version"] != float64(1) || d.AuthoringSkill != "crewship-artifacts" {
				t.Fatalf("incomplete package identity: %s", body)
			}
			for _, key := range []string{"content_locations", "required_features", "mcp_tools"} {
				if values, ok := metadata[key].([]any); !ok || len(values) == 0 {
					t.Fatalf("missing %s: %s", key, body)
				}
			}
		})
	}
}

func TestArtifactPackageMetadataIsolationAndJSONParity(t *testing.T) {
	original, _ := LookupContentType("artifact-dashboard", 1)
	changed, _ := LookupContentType("artifact-dashboard", 1)
	changed.ContentLocations[0] = "other-store"
	changed.RequiredFeatures[0] = "bypass"
	changed.MCPTools[0] = "arbitrary-command"
	current, _ := LookupContentType("artifact-dashboard", 1)
	if !reflect.DeepEqual(original, current) {
		t.Fatal("caller changed compiled package sources")
	}
	actual, err := json.Marshal(ContentTypes())
	if err != nil {
		t.Fatal(err)
	}
	var goValues, typescriptValues any
	if err := json.Unmarshal(actual, &goValues); err != nil {
		t.Fatal(err)
	}
	// TypeScript imports this exact JSON. Optional metadata must remain absent
	// from legacy descriptors rather than serializing empty Go defaults.
	if err := json.Unmarshal(contentTypeData, &typescriptValues); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(goValues, typescriptValues) {
		t.Fatalf("Go/TypeScript descriptor mismatch: %s", actual)
	}
}
