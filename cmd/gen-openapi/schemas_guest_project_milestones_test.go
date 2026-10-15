package main

import "testing"

func TestGuestMilestoneSchemasBoundEditsToReviewedMetadata(t *testing.T) {
	catalog := guestProjectMilestoneSchemaCatalog()
	path := "/api/v1/workspaces/{workspaceId}/restricted-projects/{projectId}/milestones"
	for _, key := range []string{"POST " + path, "PUT " + path + "/{milestoneId}", "DELETE " + path + "/{milestoneId}"} {
		body := catalog[key].Request
		if !catalog[key].RequestRequired || body["additionalProperties"] != false {
			t.Fatalf("%s permits undeclared metadata", key)
		}
		fields := body["properties"].(map[string]any)
		for _, forbidden := range []string{"project_id", "workspace_id", "issue_ids", "mission_ids", "created_by", "agent_id"} {
			if fields[forbidden] != nil {
				t.Fatalf("%s exposes authority/work mutation %s", key, forbidden)
			}
		}
	}
	deletion := catalog["DELETE "+path+"/{milestoneId}"]
	fields := deletion.Request["properties"].(map[string]any)
	if len(fields) != 1 || fields["version"] == nil || len(deletion.SuccessStatuses) != 1 || deletion.SuccessStatuses[0] != "204" {
		t.Fatal("delete must require only a reviewed version and return no body")
	}
	if catalog["GET "+path].SuccessHeaders["X-Can-Manage"] == nil {
		t.Fatal("empty directory lacks credential-specific management eligibility")
	}
}
