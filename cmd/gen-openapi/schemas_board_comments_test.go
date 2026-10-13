package main

import "testing"

func TestBoardCommentLifecycleSchemasKeepCapturedCASAndHumanRights(t *testing.T) {
	catalog := boardSchemaCatalog()
	for _, tc := range []struct {
		route    string
		required []string
	}{
		{"PATCH /api/v1/pages/{slug}/board/threads/{threadId}/comments/{commentId}", []string{"base_version", "mutation_id", "body"}},
		{"DELETE /api/v1/pages/{slug}/board/threads/{threadId}/comments/{commentId}", []string{"base_version", "mutation_id"}},
		{"PUT /api/v1/pages/{slug}/board/threads/{threadId}/comments/{commentId}/reactions/{emoji}", []string{"base_version", "mutation_id", "active"}},
	} {
		entry, ok := catalog[tc.route]
		if !ok || !entry.RequestRequired || entry.Request["additionalProperties"] != false {
			t.Fatalf("missing typed contract %s", tc.route)
		}
		assertRequired(t, entry.Request, tc.required...)
		props := entry.Response["properties"].(map[string]any)
		comment := props["comments"].(map[string]any)["items"].(map[string]any)
		assertRequired(t, comment, "version", "can_edit", "can_delete", "can_react", "can_view_history", "reactions")
		if comment["properties"].(map[string]any)["body"].(map[string]any)["minLength"] != nil {
			t.Fatal("tombstone body must permit empty text")
		}
	}
	history := catalog["GET /api/v1/pages/{slug}/board/threads/{threadId}/comments/{commentId}/history"].Response
	assertRequired(t, history, "history")
	item := history["properties"].(map[string]any)["history"].(map[string]any)["items"].(map[string]any)
	assertRequired(t, item, "version", "body", "action", "actor_user_id", "created_at")
}
