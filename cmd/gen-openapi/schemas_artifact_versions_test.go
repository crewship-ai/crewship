package main

import (
	"strings"
	"testing"
)

func TestArtifactProjectVersionOperationsUseNamedSnapshotsAndManifests(t *testing.T) {
	for _, tc := range []struct{ key, response, request string }{
		{"GET /api/v1/pages/{slug}/artifact", "ArtifactProjectSnapshot", ""},
		{"GET /api/v1/pages/{slug}/artifact/versions", "ArtifactProjectSnapshot", ""},
		{"POST /api/v1/pages/{slug}/artifact/versions", "ArtifactProjectMutationResponse", "ArtifactProjectPublishRequest"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			doc := buildDocument([]route{routeFromKey(tc.key)})
			parts := strings.SplitN(tc.key, " ", 2)
			op := doc["paths"].(map[string]any)[parts[1]].(map[string]any)[strings.ToLower(parts[0])].(map[string]any)
			response := op["responses"].(map[string]any)["200"].(map[string]any)
			schema := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
			if schema["$ref"] != "#/components/schemas/"+tc.response {
				t.Fatalf("wrong response %#v", schema)
			}
			if tc.request != "" {
				body := op["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
				if body["$ref"] != "#/components/schemas/"+tc.request {
					t.Fatalf("wrong request %#v", body)
				}
			}
		})
	}
}
