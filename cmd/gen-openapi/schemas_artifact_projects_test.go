package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestArtifactProjectContractsAreNamedAndClosed(t *testing.T) {
	cases := []struct{ method, path, status, response, request string }{
		{"POST", "/api/v1/pages/artifacts", "201", "ArtifactProjectCreateResponse", "ArtifactProjectCreateRequest"},
		{"GET", "/api/v1/pages/artifacts/availability", "200", "ArtifactProjectAvailability", ""},
		{"POST", "/api/v1/pages/artifacts/consent", "200", "ArtifactProjectConsentResponse", "ArtifactProjectConsentRequest"},
		{"GET", "/api/v1/pages/{slug}/artifact", "200", "ArtifactProjectSnapshot", ""},
		{"GET", "/api/v1/pages/{slug}/artifact/versions", "200", "ArtifactProjectSnapshot", ""},
		{"POST", "/api/v1/pages/{slug}/artifact/versions", "200", "ArtifactProjectMutationResponse", "ArtifactProjectPublishRequest"},
		{"POST", "/api/v1/pages/{slug}/artifact/restore", "200", "ArtifactProjectMutationResponse", "ArtifactProjectVersionRequest"},
		{"POST", "/api/v1/pages/{slug}/artifact/share", "200", "ArtifactProjectShareResponse", "ArtifactProjectVersionRequest"},
		{"GET", "/api/v1/pages/{slug}/artifact/comments", "200", "ArtifactProjectSnapshot", ""},
		{"POST", "/api/v1/pages/{slug}/artifact/comments", "201", "ArtifactProjectComment", "ArtifactProjectCommentRequest"},
		{"POST", "/api/v1/pages/{slug}/artifact/comments/{commentId}/reply", "200", "ArtifactProjectReply", "ArtifactProjectReplyRequest"},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			doc := buildDocument([]route{routeFromKey(tc.method + " " + tc.path)})
			op := doc["paths"].(map[string]any)[tc.path].(map[string]any)[strings.ToLower(tc.method)].(map[string]any)
			response, ok := op["responses"].(map[string]any)[tc.status].(map[string]any)
			if !ok {
				t.Fatalf("missing declared success status %s and concrete artifact schema", tc.status)
			}
			got := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
			if got["$ref"] != "#/components/schemas/"+tc.response {
				t.Fatalf("generic or wrong response: %#v", got)
			}
			components := doc["components"].(map[string]any)["schemas"].(map[string]any)
			if components[tc.response].(map[string]any)["additionalProperties"] != false {
				t.Fatal("response is not closed")
			}
			if tc.request != "" {
				request := op["requestBody"].(map[string]any)
				if request["required"] != true {
					t.Fatal("request body must be required")
				}
				schema := request["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
				if schema["$ref"] != "#/components/schemas/"+tc.request || components[tc.request].(map[string]any)["additionalProperties"] != false {
					t.Fatalf("generic or open request: %#v", schema)
				}
			}
		})
	}
}

func TestArtifactProjectSchemaValidatesSnapshotAndRejectsSpoofedReply(t *testing.T) {
	doc := buildDocument(nil)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := compileAt(t, raw, "/components/schemas/ArtifactProjectSnapshot")
	fixture := map[string]any{"page_id": "page", "name": "Project", "slug": "project", "origin": nil, "enabled": true, "can_write": true, "latest_version": float64(1), "shared_version": float64(0), "manifest": map[string]any{"version": float64(1), "entrypoint": "index.html", "files": []any{map[string]any{"path": "index.html", "content": "<h1 id=hero>Hello</h1>", "media_type": "text/html"}}, "created_at": "2026-10-27T00:00:00Z", "message": "", "restored_from": float64(0)}, "versions": []any{}, "quota": map[string]any{"used_bytes": float64(100), "limit_bytes": float64(1000), "warning": false, "retention": "keep-all"}, "comments": []any{}}
	if err = snapshot.Validate(fixture); err != nil {
		t.Fatal("real wire snapshot rejected", err)
	}
	delete(fixture, "quota")
	if snapshot.Validate(fixture) == nil {
		t.Fatal("snapshot missing visible quota accepted")
	}
	fixture["quota"] = map[string]any{"used_bytes": float64(0), "limit_bytes": float64(0), "warning": false, "retention": "shared-version-only"}
	fixture["can_write"], fixture["shared_version"] = false, float64(1)
	if err = snapshot.Validate(fixture); err != nil {
		t.Fatal("reader hidden-quota snapshot rejected", err)
	}
	reply := compileAt(t, raw, "/components/schemas/ArtifactProjectReplyRequest")
	for _, body := range []map[string]any{{"text": "Changed", "version": float64(2)}, {"text": "Select again", "version": float64(1), "needs_clarification": true}} {
		if err = reply.Validate(body); err != nil {
			t.Fatal(err)
		}
	}
	if reply.Validate(map[string]any{"text": "Changed", "version": float64(2), "author": "forged"}) == nil {
		t.Fatal("caller-supplied reply author accepted")
	}
}
