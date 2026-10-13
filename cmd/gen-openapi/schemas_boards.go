package main

// Boards use server sequence numbers, never Excalidraw's client versions,
// as write authority. Artifact placement identities are server controlled.
func boardSchemaCatalog() map[string]DomainSchema {
	str := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer", "minimum": 0}
	number := map[string]any{"type": "number"}
	boolean := map[string]any{"type": "boolean"}
	obj := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	element := map[string]any{"type": "object", "additionalProperties": true, "description": "Validated Excalidraw 0.18.1 drawing element. Images reference Board-owned immutable files; URLs, arbitrary embeddables and customData are forbidden; see the versioned Board profile."}
	geometry := map[string]any{"x": number, "y": number, "width": number, "height": number}
	placement := obj(mergeProps(geometry, map[string]any{
		"id": str, "source": obj(map[string]any{"page_slug": str, "revision": integer, "title": str}, "page_slug", "revision", "title"),
	}), "id", "x", "y", "width", "height")
	placement["description"] = "Source is omitted unless the viewer can read both the Board and the artifact. Geometry remains as an unavailable placeholder."
	snapshot := obj(map[string]any{
		"format": map[string]any{"type": "string", "enum": []string{"crewship-board-excalidraw-0.18.1-v1"}},
		"seq":    integer, "can_write": boolean, "placements": array(placement),
		"records": array(obj(map[string]any{"element_id": str, "element_rev": integer, "deleted": boolean, "author_user_id": str, "author_agent_id": str, "author_run_id": str, "element": map[string]any{"oneOf": []any{element, map[string]any{"type": "null"}}}}, "element_id", "element_rev", "deleted", "element")),
	}, "format", "seq", "can_write", "placements", "records")
	receipt := obj(map[string]any{"seq": integer, "result_seq": integer, "placement_id": str}, "seq", "result_seq")
	mutation := map[string]any{"mutation_id": str, "base_seq": integer}
	batch := obj(mergeProps(mutation, map[string]any{
		"upserts":           array(obj(map[string]any{"element": element, "expected_rev": integer}, "element", "expected_rev")),
		"deletes":           array(obj(map[string]any{"id": str, "expected_rev": integer}, "id", "expected_rev")),
		"placement_updates": array(obj(mergeProps(geometry, map[string]any{"id": str}), "id", "x", "y", "width", "height")),
		"placement_deletes": array(obj(map[string]any{"id": str}, "id")),
	}), "mutation_id", "base_seq")
	createPlacement := obj(mergeProps(mergeProps(mutation, geometry), map[string]any{"source_page_slug": str, "source_revision": integer}), "mutation_id", "base_seq", "source_page_slug", "source_revision", "x", "y", "width", "height")
	return map[string]DomainSchema{
		"POST /api/v1/pages/{slug}/board/delegations":                {Request: obj(map[string]any{"run_id": str}, "run_id"), RequestRequired: true, Response: obj(map[string]any{"run_id": str, "agent_id": str, "expires_at": str, "scope": str}, "run_id", "agent_id", "expires_at", "scope"), SuccessStatuses: []string{"201"}},
		"DELETE /api/v1/pages/{slug}/board/delegations/{runId}":      {Response: obj(map[string]any{}), SuccessStatuses: []string{"204"}},
		"POST /api/v1/pages/{slug}/board/files":                      {Request: map[string]any{"type": "string", "format": "binary"}, RequestRequired: true, RequestMedia: []string{"image/png", "image/jpeg"}, Response: obj(map[string]any{"file_id": str, "content_type": str, "width": integer, "height": integer, "bytes": integer, "seq": integer, "result_seq": integer}, "file_id", "content_type", "width", "height", "bytes", "seq", "result_seq"), SuccessStatuses: []string{"200", "201"}},
		"GET /api/v1/pages/{slug}/board/files/{fileId}":              {Response: map[string]any{"type": "string", "format": "binary"}, ResponseMedia: []string{"image/png", "image/jpeg"}},
		"GET /api/v1/pages/{slug}/board":                             {Response: snapshot},
		"POST /api/v1/pages/{slug}/board":                            {Request: obj(map[string]any{"mutation_id": str}, "mutation_id"), RequestRequired: true, Response: snapshot, SuccessStatuses: []string{"200"}},
		"POST /api/v1/pages/{slug}/board/batches":                    {Request: batch, RequestRequired: true, Response: receipt, SuccessStatuses: []string{"200", "201"}},
		"POST /api/v1/pages/{slug}/board/placements":                 {Request: createPlacement, RequestRequired: true, Response: obj(map[string]any{"seq": integer, "result_seq": integer, "placement_id": str}, "seq", "result_seq", "placement_id"), SuccessStatuses: []string{"200", "201"}},
		"DELETE /api/v1/pages/{slug}/board/placements/{placementId}": {Request: obj(mutation, "mutation_id", "base_seq"), RequestRequired: true, Response: receipt},
	}
}
