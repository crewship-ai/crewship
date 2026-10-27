package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestArtifactFeedbackHTTPContractsNamedAndClosed(t *testing.T) {
	for _, tc := range []struct{ method, suffix, status, response, request string }{
		{"GET", "/comments", "200", "ArtifactProjectSnapshot", ""},
		{"POST", "/restore", "200", "ArtifactProjectMutationResponse", "ArtifactProjectVersionRequest"},
		{"POST", "/share", "200", "ArtifactProjectShareResponse", "ArtifactProjectVersionRequest"},
		{"POST", "/comments", "201", "ArtifactProjectComment", "ArtifactProjectCommentRequest"},
		{"POST", "/comments/{commentId}/reply", "200", "ArtifactProjectReply", "ArtifactProjectReplyRequest"},
	} {
		t.Run(tc.method+tc.suffix, func(t *testing.T) {
			path := "/api/v1/pages/{slug}/artifact" + tc.suffix
			doc := buildDocument([]route{routeFromKey(tc.method + " " + path)})
			op := doc["paths"].(map[string]any)[path].(map[string]any)[strings.ToLower(tc.method)].(map[string]any)
			response, ok := op["responses"].(map[string]any)[tc.status].(map[string]any)
			if !ok {
				t.Fatalf("missing success status %s", tc.status)
			}
			schema := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
			if schema["$ref"] != "#/components/schemas/"+tc.response {
				t.Fatalf("wrong response: %#v", schema)
			}
			components := doc["components"].(map[string]any)["schemas"].(map[string]any)
			if components[tc.response].(map[string]any)["additionalProperties"] != false {
				t.Fatal("open response")
			}
			if tc.request == "" {
				return
			}
			body := op["requestBody"].(map[string]any)
			if body["required"] != true || body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"] != "#/components/schemas/"+tc.request {
				t.Fatal("missing named required request")
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			compiled := compileAt(t, raw, "/components/schemas/"+tc.request)
			valid := map[string]any{"base_version": float64(1), "version": float64(1)}
			switch tc.request {
			case "ArtifactProjectCommentRequest":
				valid = map[string]any{"version": float64(1), "path": "index.html", "selector": "#hero", "quote": "Title", "text": "Change"}
			case "ArtifactProjectReplyRequest":
				valid = map[string]any{"version": float64(2), "text": "Changed"}
			}
			if err := compiled.Validate(valid); err != nil {
				t.Fatalf("valid request rejected: %v", err)
			}
			valid["author"], valid["page_id"] = "forged", "foreign"
			if compiled.Validate(valid) == nil {
				t.Fatal("caller-supplied authority accepted")
			}
		})
	}
}
