package main

func guestProjectMilestoneSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	}
	item := object(map[string]any{"id": str(), "project_id": str(), "name": str(), "description": str(), "target_date": str(), "status": str(), "position": integer(), "version": str(), "can_manage": boolean(), "can_delete": boolean()}, "id", "project_id", "name", "description", "target_date", "status", "position", "version", "can_manage", "can_delete")
	create := object(map[string]any{"name": str(), "description": str(), "target_date": str(), "status": map[string]any{"type": "string", "enum": []string{"active", "completed", "cancelled"}}}, "name", "description", "target_date")
	update := object(map[string]any{"name": str(), "description": str(), "target_date": str(), "status": str(), "position": map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000}, "version": str()}, "name", "description", "target_date", "status", "position", "version")
	path := "/api/v1/workspaces/{workspaceId}/restricted-projects/{projectId}/milestones"
	return map[string]DomainSchema{
		"GET " + path:                       {Response: map[string]any{"type": "array", "items": item}, SuccessStatuses: []string{"200"}, SuccessHeaders: map[string]any{"X-Can-Manage": map[string]any{"description": "Current mutation eligibility for this credential", "schema": str()}, "X-Total-Count": map[string]any{"description": "Total milestones in this authorized project", "schema": integer()}}},
		"GET " + path + "/{milestoneId}":    {Response: item, SuccessStatuses: []string{"200"}},
		"POST " + path:                      {Request: create, RequestRequired: true, Response: item, SuccessStatuses: []string{"201"}},
		"PUT " + path + "/{milestoneId}":    {Request: update, RequestRequired: true, Response: item, SuccessStatuses: []string{"200"}},
		"DELETE " + path + "/{milestoneId}": {Request: object(map[string]any{"version": str()}, "version"), RequestRequired: true, SuccessStatuses: []string{"204"}},
	}
}
