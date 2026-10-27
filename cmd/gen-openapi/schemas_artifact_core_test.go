package main

import (
	"encoding/json"
	"testing"
)

func TestArtifactProjectCoreComponentsAreClosedAndRetainEmptyFiles(t *testing.T) {
	doc := buildDocument(nil)
	components := artifactProjectCoreComponents()
	for _, name := range []string{"ArtifactProjectFileInput", "ArtifactProjectFile", "ArtifactProjectManifest", "ArtifactProjectVersion", "ArtifactProjectQuota", "ArtifactProjectCreateRequest", "ArtifactProjectPublishRequest", "ArtifactProjectVersionRequest", "ArtifactProjectCreateResponse", "ArtifactProjectMutationResponse", "ArtifactProjectShareResponse", "ArtifactProjectError", "ArtifactProjectAvailability", "ArtifactProjectConsentRequest", "ArtifactProjectConsentResponse"} {
		schema, ok := components[name].(map[string]any)
		if !ok {
			t.Fatalf("missing prepared component %s", name)
		}
		if schema["additionalProperties"] != false {
			t.Errorf("open component %s", name)
		}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	file := compileAt(t, raw, "/components/schemas/ArtifactProjectFileInput")
	valid := map[string]any{"path": "index.html", "content": "", "media_type": "text/html"}
	if err := file.Validate(valid); err != nil {
		t.Fatalf("empty content refused: %v", err)
	}
	for _, field := range []string{"path", "content", "media_type"} {
		body := map[string]any{"path": "index.html", "content": "", "media_type": "text/html"}
		delete(body, field)
		if file.Validate(body) == nil {
			t.Errorf("missing %s accepted", field)
		}
		body[field] = nil
		if file.Validate(body) == nil {
			t.Errorf("null %s accepted", field)
		}
	}
	valid["actor"] = "forged"
	if file.Validate(valid) == nil {
		t.Fatal("unknown file identity accepted")
	}
}
