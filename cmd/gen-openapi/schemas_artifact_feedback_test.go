package main

import (
	"encoding/json"
	"testing"
)

func TestArtifactProjectFeedbackSchemasAcceptOwnerAndSharedSnapshots(t *testing.T) {
	doc := buildDocument(nil)
	components := artifactProjectFeedbackComponents()
	fixtureSchemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for name, schema := range components {
		fixtureSchemas[name] = schema
	}
	for _, name := range []string{"ArtifactProjectOrigin", "ArtifactProjectReply", "ArtifactProjectComment", "ArtifactProjectSnapshot", "ArtifactProjectCommentRequest", "ArtifactProjectReplyRequest"} {
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
	snapshot := compileAt(t, raw, "/components/schemas/ArtifactProjectSnapshot")
	fixture := map[string]any{"page_id": "page", "name": "Project", "slug": "project", "origin": nil, "enabled": true, "can_write": true, "latest_version": float64(1), "shared_version": float64(0), "manifest": map[string]any{"version": float64(1), "entrypoint": "index.html", "files": []any{map[string]any{"path": "index.html", "content": "<h1 id=artifact-hero>Hello</h1>", "media_type": "text/html"}}, "created_at": "2026-10-27T00:00:00Z", "message": "", "restored_from": float64(0)}, "versions": []any{}, "quota": map[string]any{"used_bytes": float64(100), "limit_bytes": float64(1000), "warning": false, "retention": "keep-all"}, "comments": []any{}}
	if err := snapshot.Validate(fixture); err != nil {
		t.Fatal("owner snapshot rejected", err)
	}
	delete(fixture, "quota")
	if snapshot.Validate(fixture) == nil {
		t.Fatal("missing quota accepted")
	}
	fixture["quota"] = map[string]any{"used_bytes": float64(0), "limit_bytes": float64(0), "warning": false, "retention": "shared-version-only"}
	fixture["can_write"], fixture["shared_version"] = false, float64(1)
	if err := snapshot.Validate(fixture); err != nil {
		t.Fatal("reader hidden-quota snapshot rejected", err)
	}
	reply := compileAt(t, raw, "/components/schemas/ArtifactProjectReplyRequest")
	for _, body := range []map[string]any{{"text": "Changed", "version": float64(2)}, {"text": "Select again", "version": float64(1), "needs_clarification": true}} {
		if err := reply.Validate(body); err != nil {
			t.Fatal(err)
		}
	}
	if reply.Validate(map[string]any{"text": "Changed", "version": float64(2), "author": "forged"}) == nil {
		t.Fatal("caller-supplied reply author accepted")
	}
}

func TestArtifactFeedbackAuthorKindIsOptionalAndClosed(t *testing.T) {
	doc := buildDocument(nil)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ArtifactProjectReply", "ArtifactProjectComment"} {
		t.Run(name, func(t *testing.T) {
			schema := compileAt(t, raw, "/components/schemas/"+name)
			body := map[string]any{"text": "Comment", "version": float64(1), "author": "erased", "created_at": "2026-10-27T00:00:00Z"}
			if name == "ArtifactProjectComment" {
				for k, v := range map[string]any{"id": "comment", "path": "index.html", "selector": "#hero", "quote": "Hello", "status": "comment", "replies": []any{}} {
					body[k] = v
				}
			}
			if err := schema.Validate(body); err != nil {
				t.Fatal("unattributed erased identity refused", err)
			}
			for _, kind := range []string{"human", "agent"} {
				body["author_kind"] = kind
				if err := schema.Validate(body); err != nil {
					t.Fatal(kind, err)
				}
			}
			body["author_kind"] = "erased"
			if schema.Validate(body) == nil {
				t.Fatal("unknown attribution accepted")
			}
		})
	}
	for _, name := range []string{"ArtifactProjectCommentRequest", "ArtifactProjectReplyRequest"} {
		props := artifactProjectFeedbackComponents()[name].(map[string]any)["properties"].(map[string]any)
		if _, ok := props["author_kind"]; ok {
			t.Fatal("request permits caller attribution", name)
		}
	}
}
