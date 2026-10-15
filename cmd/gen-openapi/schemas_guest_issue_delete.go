package main

func guestIssueDeletionSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	item := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "identifier", "title", "project_id", "status", "version", "can_delete"}, "properties": map[string]any{"id": str(), "identifier": str(), "title": str(), "project_id": str(), "status": str(), "version": str(), "can_delete": map[string]any{"type": "boolean"}}}
	body := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"project_id", "version", "confirm"}, "properties": map[string]any{"project_id": str(), "version": map[string]any{"type": "string", "minLength": 64, "maxLength": 64}, "confirm": map[string]any{"type": "boolean", "enum": []bool{true}}}}
	path := "/api/v1/workspaces/{workspaceId}/restricted-issues/{issueId}"
	return map[string]DomainSchema{"GET " + path + "/deletion": {Response: item, SuccessStatuses: []string{"200"}}, "DELETE " + path: {Request: body, RequestRequired: true, SuccessStatuses: []string{"204"}}}
}
