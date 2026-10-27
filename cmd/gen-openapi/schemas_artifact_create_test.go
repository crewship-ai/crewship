package main

import (
	"strings"
	"testing"
)

func TestArtifactProjectCreateOperationsUseClosedSchemas(t *testing.T) {
	for _, tc := range []struct{ key, status, response, request string }{
		{"POST /api/v1/pages/artifacts", "201", "ArtifactProjectCreateResponse", "ArtifactProjectCreateRequest"},
		{"GET /api/v1/pages/artifacts/availability", "200", "ArtifactProjectAvailability", ""},
	} {
		t.Run(tc.key, func(t *testing.T) {
			doc := buildDocument([]route{routeFromKey(tc.key)})
			parts := strings.SplitN(tc.key, " ", 2)
			op := doc["paths"].(map[string]any)[parts[1]].(map[string]any)[strings.ToLower(parts[0])].(map[string]any)
			responses := op["responses"].(map[string]any)
			response, ok := responses[tc.status].(map[string]any)
			if !ok {
				t.Fatalf("missing success %s", tc.status)
			}
			schema := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
			if schema["$ref"] != "#/components/schemas/"+tc.response {
				t.Fatalf("wrong response %#v", schema)
			}
			if tc.request != "" {
				request := op["requestBody"].(map[string]any)
				body := request["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
				if request["required"] != true || body["$ref"] != "#/components/schemas/"+tc.request {
					t.Fatalf("wrong request %#v", request)
				}
			}
		})
	}
}
