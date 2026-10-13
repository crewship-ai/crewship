package main

func routineOriginBindingSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	origin := func() map[string]any { return map[string]any{"type": "string", "nullable": true} }
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	schedule := obj(map[string]any{"id": str(), "origin_id": origin()}, "id", "origin_id")
	response := obj(map[string]any{"pipeline_id": str(), "origin_id": origin(), "schedules": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/RoutineOriginScheduleBinding"}}}, "pipeline_id", "origin_id", "schedules")
	request := obj(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"clear"}}, "reason": map[string]any{"type": "string", "minLength": 1, "maxLength": 2000}, "expected_origin_id": origin(), "schedules": map[string]any{"type": "array", "maxItems": 100, "items": obj(map[string]any{"id": str(), "expected_origin_id": origin()}, "id", "expected_origin_id")}}, "action", "reason", "expected_origin_id")
	ref := map[string]any{"$ref": "#/components/schemas/RoutineOriginBinding"}
	route := "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/origin-binding"
	return map[string]DomainSchema{"GET " + route: {Response: ref, SuccessStatuses: []string{"200"}}, "POST " + route: {RequestRequired: true, Request: map[string]any{"$ref": "#/components/schemas/RoutineOriginClearRequest"}, Response: ref, SuccessStatuses: []string{"200"}}}, map[string]any{"RoutineOriginBinding": response, "RoutineOriginScheduleBinding": schedule, "RoutineOriginClearRequest": request}
}
