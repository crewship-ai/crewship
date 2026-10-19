package main

// routineOrganisationSchemaCatalog types the routine folders (ODCH-RT-01),
// templates (ODCH-RT-22), the Health aggregate (ODCH-RT-07) and the checkup
// (ODCH-RT-06). They are the Routines panel's organisation and health
// surfaces; each handler writes a fixed struct, so these are transcriptions
// of internal/api/pipeline_folders.go, pipeline_templates.go,
// pipelines_health.go and pipeline_checkup.go.
func routineOrganisationSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	nullableString := func() map[string]any { return map[string]any{"type": "string", "nullable": true} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	number := func() map[string]any { return map[string]any{"type": "number"} }
	enum := func(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	anyObject := func() map[string]any { return map[string]any{"type": "object", "additionalProperties": true} }
	object := func(properties map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	base := "/api/v1/workspaces/{workspaceId}"

	components := map[string]any{
		"RoutineFolder": object(map[string]any{
			"id": str(), "name": str(), "color": str(), "routine_count": integer(), "created_at": str(), "updated_at": str(),
		}, "id", "name", "routine_count", "created_at", "updated_at"),
		"RoutineFolderList": array(ref("RoutineFolder")),
		"RoutineFolderCreateRequest": object(map[string]any{
			"name":  map[string]any{"type": "string", "minLength": 1, "maxLength": 80},
			"color": map[string]any{"type": "string", "maxLength": 64},
		}, "name"),
		"RoutineFolderUpdateRequest": object(map[string]any{
			"name":  map[string]any{"type": "string", "minLength": 1, "maxLength": 80, "description": "Omit to keep the name."},
			"color": map[string]any{"type": "string", "maxLength": 64, "description": "Omit to keep the colour; \"\" clears it."},
		}),
		"RoutineFolderMoveRequest": object(map[string]any{
			"slugs":     map[string]any{"type": "array", "items": str(), "minItems": 1, "maxItems": 200},
			"folder_id": map[string]any{"type": "string", "nullable": true, "description": "The folder to file the routines in; null or empty takes them out of any folder."},
		}, "slugs"),
		"RoutineFolderMoveResult": object(map[string]any{"moved": integer(), "folder_id": nullableString()}, "moved", "folder_id"),

		"RoutineTemplate": object(map[string]any{
			"id": str(), "name": str(), "description": str(), "category": str(), "step_count": integer(),
			"definition": map[string]any{"type": "object", "additionalProperties": true, "description": "The routine definition; present on get and create, absent from the list."},
			"created_at": str(), "updated_at": str(),
		}, "id", "name", "step_count", "created_at", "updated_at"),
		"RoutineTemplateList": array(ref("RoutineTemplate")),
		"RoutineTemplateCreateRequest": object(map[string]any{
			"name":         map[string]any{"type": "string", "minLength": 1, "maxLength": 80},
			"description":  map[string]any{"type": "string", "maxLength": 500},
			"category":     map[string]any{"type": "string", "maxLength": 40},
			"definition":   anyObject(),
			"from_routine": map[string]any{"type": "string", "description": "Slug of a routine whose definition becomes the template. Exactly one of definition and from_routine."},
		}, "name"),
		"RoutineTemplateUseRequest": object(map[string]any{
			"slug":           map[string]any{"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,62}$"},
			"name":           str(),
			"author_crew_id": str(),
		}, "slug"),

		"RoutineHealthRow": object(map[string]any{
			"slug":          str(),
			"last_outcomes": map[string]any{"type": "array", "items": enum("completed", "failed", "stopped", "waiting", "running"), "maxItems": 20, "description": "The newest runs, oldest first."},
			"finished":      integer(), "succeeded": integer(),
			"success_pct": map[string]any{"type": "integer", "description": "Over the finished runs among last_outcomes; absent when none finished."},
			"runs":        integer(), "spend_usd": number(),
		}, "slug", "last_outcomes", "finished", "succeeded", "runs", "spend_usd"),
		"RoutineHealth": object(map[string]any{"window_days": integer(), "routines": array(ref("RoutineHealthRow"))}, "window_days", "routines"),

		"RoutineCheckupCheck": object(map[string]any{
			"key": str(), "level": enum("ok", "warn", "bad"), "title": str(), "detail": str(),
			"tab": map[string]any{"type": "string", "description": "The tab of the routine page that fixes it, when one does."},
		}, "key", "level", "title", "detail"),
		"RoutineCheckup": object(map[string]any{
			"slug": str(), "checked_at": str(), "passed": integer(), "warned": integer(), "failed": integer(), "total": integer(),
			"checks": array(ref("RoutineCheckupCheck")),
		}, "slug", "checked_at", "passed", "warned", "failed", "total", "checks"),
	}

	routes := map[string]DomainSchema{
		"GET " + base + "/pipeline-folders":             {Response: ref("RoutineFolderList"), SuccessStatuses: []string{"200"}},
		"POST " + base + "/pipeline-folders":            {Request: ref("RoutineFolderCreateRequest"), RequestRequired: true, Response: ref("RoutineFolder"), SuccessStatuses: []string{"201"}, ErrorStatuses: []string{"400", "409"}},
		"POST " + base + "/pipeline-folders/move":       {Request: ref("RoutineFolderMoveRequest"), RequestRequired: true, Response: ref("RoutineFolderMoveResult"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "404"}},
		"PATCH " + base + "/pipeline-folders/{id}":      {Request: ref("RoutineFolderUpdateRequest"), RequestRequired: true, Response: ref("RoutineFolder"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "404", "409"}},
		"DELETE " + base + "/pipeline-folders/{id}":     {SuccessStatuses: []string{"204"}, ErrorStatuses: []string{"404"}},
		"GET " + base + "/pipeline-templates":           {Response: ref("RoutineTemplateList"), SuccessStatuses: []string{"200"}},
		"GET " + base + "/pipeline-templates/{id}":      {Response: ref("RoutineTemplate"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"404"}},
		"POST " + base + "/pipeline-templates":          {Request: ref("RoutineTemplateCreateRequest"), RequestRequired: true, Response: ref("RoutineTemplate"), SuccessStatuses: []string{"201"}, ErrorStatuses: []string{"400", "404", "409", "422"}},
		"DELETE " + base + "/pipeline-templates/{id}":   {SuccessStatuses: []string{"204"}, ErrorStatuses: []string{"404"}},
		"POST " + base + "/pipeline-templates/{id}/use": {Request: ref("RoutineTemplateUseRequest"), RequestRequired: true, Response: ref("RoutineDraft"), SuccessStatuses: []string{"201"}, ErrorStatuses: []string{"400", "404", "409", "422"}},
		"GET " + base + "/pipeline-health":              {Response: ref("RoutineHealth"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "503"}},
		"GET " + base + "/pipelines/{slug}/checkup":     {Response: ref("RoutineCheckup"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"404"}},
	}
	return routes, components
}
