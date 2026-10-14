package main

func guestIssueContributionSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	}
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	intake := object(map[string]any{"project_id": str(), "crew_id": str(), "crew_name": str(), "revision": map[string]any{"type": "integer", "minimum": 0}}, "project_id", "crew_id", "crew_name", "revision")
	project := object(map[string]any{"id": str(), "name": str(), "can_contribute": boolean(), "intake_ready": boolean()}, "id", "name", "can_contribute", "intake_ready")
	comment := object(map[string]any{"id": str(), "body": str(), "author": map[string]any{"type": "string", "enum": []string{"You", "Workspace member"}}, "created_at": map[string]any{"type": "string", "format": "date-time"}}, "id", "body", "author", "created_at")
	title := map[string]any{"type": "string", "minLength": 1, "description": "Nonblank title, at most 4096 UTF-8 bytes"}
	description := map[string]any{"type": "string", "nullable": true, "description": "At most 65536 UTF-8 bytes"}
	body := map[string]any{"type": "string", "minLength": 1, "description": "Nonblank shared comment, at most 32768 UTF-8 bytes"}
	prefix := "/api/v1/workspaces/{workspaceId}/"
	return map[string]DomainSchema{
		"GET " + prefix + "projects/{projectId}/guest-intake":     {Response: intake, SuccessStatuses: []string{"200"}},
		"PUT " + prefix + "projects/{projectId}/guest-intake":     {Request: object(map[string]any{"crew_id": map[string]any{"type": "string", "minLength": 1}, "revision": map[string]any{"type": "integer", "minimum": 0}}, "crew_id", "revision"), RequestRequired: true, Response: intake, SuccessStatuses: []string{"200"}},
		"GET " + prefix + "restricted-issue-projects":             {Response: array(project), SuccessStatuses: []string{"200"}},
		"POST " + prefix + "restricted-issues":                    {Request: object(map[string]any{"project_id": map[string]any{"type": "string", "minLength": 1}, "title": title, "description": description}, "project_id", "title"), RequestRequired: true, Response: restrictedIssueSchema(), SuccessStatuses: []string{"201"}},
		"PATCH " + prefix + "restricted-issues/{issueId}":         {Request: object(map[string]any{"status": map[string]any{"type": "string", "enum": []string{"BACKLOG", "TODO"}}}, "status"), RequestRequired: true, Response: restrictedIssueSchema(), SuccessStatuses: []string{"200"}},
		"GET " + prefix + "restricted-issues/{issueId}/comments":  {Response: array(comment), SuccessStatuses: []string{"200"}},
		"POST " + prefix + "restricted-issues/{issueId}/comments": {Request: object(map[string]any{"body": body}, "body"), RequestRequired: true, Response: comment, SuccessStatuses: []string{"201"}},
	}
}
