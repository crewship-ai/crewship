package main

import "testing"

func TestGuestContributionSchemasExcludeAuthoritySelectors(t *testing.T) {
	catalog := guestIssueContributionSchemaCatalog()
	for _, key := range []string{
		"POST /api/v1/workspaces/{workspaceId}/restricted-issues",
		"POST /api/v1/workspaces/{workspaceId}/restricted-issues/{issueId}/comments",
		"PATCH /api/v1/workspaces/{workspaceId}/restricted-issues/{issueId}",
	} {
		request := catalog[key].Request
		if request["additionalProperties"] != false {
			t.Fatalf("%s accepts undeclared fields", key)
		}
		props := request["properties"].(map[string]any)
		for _, forbidden := range []string{"crew_id", "agent_id", "author_id", "created_by_user_id", "assignee_id", "routine_id", "guest_visible", "guest_project_id"} {
			if props[forbidden] != nil {
				t.Errorf("%s exposes authority selector %s", key, forbidden)
			}
		}
	}
	thread := catalog["GET /api/v1/workspaces/{workspaceId}/restricted-issues/{issueId}/comments"].Response
	props := thread["items"].(map[string]any)["properties"].(map[string]any)
	for _, forbidden := range []string{"author_id", "email", "crew_id", "agent_id", "mission_id"} {
		if props[forbidden] != nil {
			t.Errorf("Guest thread exposes %s", forbidden)
		}
	}
	doc := buildDocument([]route{{method: "POST", path: "/api/v1/workspaces/{workspaceId}/restricted-issues"}})
	op := doc["paths"].(map[string]any)["/api/v1/workspaces/{workspaceId}/restricted-issues"].(map[string]any)["post"].(map[string]any)
	if op["responses"].(map[string]any)["201"] == nil {
		t.Fatal("creation lacks documented 201")
	}
}
