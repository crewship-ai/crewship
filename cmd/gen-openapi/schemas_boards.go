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
		"id": str, "placement_rev": integer, "mode": map[string]any{"type": "string", "enum": []string{"fixed", "follow_latest"}}, "source": obj(map[string]any{"page_slug": str, "revision": integer, "pinned_revision": integer, "title": str}, "page_slug", "revision", "pinned_revision", "title"),
	}), "id", "x", "y", "width", "height", "mode", "placement_rev")
	placement["description"] = "Source is omitted unless the viewer can read both the Board and the artifact. Geometry remains as an unavailable placeholder."
	snapshot := obj(map[string]any{
		"format": map[string]any{"type": "string", "enum": []string{"crewship-board-excalidraw-0.18.1-v1"}},
		"seq":    integer, "can_write": boolean, "write_scope": map[string]any{"type": "string", "enum": []string{"all-elements", "own-elements"}}, "placements": array(placement),
		"placement_tombstones": array(obj(map[string]any{"id": str, "placement_rev": integer, "deleted": boolean}, "id", "placement_rev", "deleted")),
		"records":              array(obj(map[string]any{"element_id": str, "element_rev": integer, "deleted": boolean, "author_user_id": str, "author_agent_id": str, "author_run_id": str, "element": mergeProps(element, map[string]any{"nullable": true})}, "element_id", "element_rev", "deleted", "element")),
	}, "format", "seq", "can_write", "placements", "records")
	receipt := obj(map[string]any{"seq": integer, "result_seq": integer, "placement_id": str, "placement_revs": map[string]any{"type": "object", "additionalProperties": integer}, "element_revs": map[string]any{"type": "object", "additionalProperties": integer}, "undo_expires_at": str}, "seq", "result_seq")
	mutation := map[string]any{"mutation_id": str, "base_seq": integer}
	batch := obj(mergeProps(mutation, map[string]any{
		"upserts":               array(obj(map[string]any{"element": element, "expected_rev": integer}, "element", "expected_rev")),
		"deletes":               array(obj(map[string]any{"id": str, "expected_rev": integer}, "id", "expected_rev")),
		"placement_updates":     array(obj(mergeProps(geometry, map[string]any{"id": str, "expected_rev": integer}), "id", "x", "y", "width", "height")),
		"placement_deletes":     array(obj(map[string]any{"id": str, "expected_rev": integer}, "id")),
		"placement_modes":       array(obj(map[string]any{"id": str, "expected_rev": integer, "mode": map[string]any{"type": "string", "enum": []string{"fixed", "follow_latest"}}, "source_revision": map[string]any{"type": "integer", "minimum": 1}}, "id", "expected_rev", "mode", "source_revision")),
		"element_permission_id": str,
		"placement_restores":    array(obj(map[string]any{"id": str, "expected_rev": integer}, "id", "expected_rev")),
	}), "mutation_id", "base_seq")
	createPlacement := obj(mergeProps(mergeProps(mutation, geometry), map[string]any{"source_page_slug": str, "source_revision": integer}), "mutation_id", "base_seq", "source_page_slug", "source_revision", "x", "y", "width", "height")
	positive := map[string]any{"type": "integer", "minimum": 1}
	literal := func(value string) map[string]any { return map[string]any{"type": "string", "enum": []string{value}} }
	anchorFields := map[string]any{"schema_version": map[string]any{"type": "integer", "enum": []int{1}}, "board_seq": integer}
	wholeAnchor := obj(mergeProps(anchorFields, map[string]any{"type": literal("board")}), "schema_version", "type", "board_seq")
	elementAnchorFields := mergeProps(anchorFields, map[string]any{"type": literal("board-element"), "element_id": str, "element_rev": positive})
	elementAnchor := obj(elementAnchorFields, "schema_version", "type", "board_seq", "element_id", "element_rev")
	inputAnchor := map[string]any{"oneOf": []any{wholeAnchor, elementAnchor}}
	capturedAnchor := obj(mergeProps(elementAnchorFields, map[string]any{"snapshot": obj(mergeProps(geometry, map[string]any{"angle": number, "element_type": str}), "x", "y", "width", "height", "angle", "element_type")}), "schema_version", "type", "board_seq", "element_id", "element_rev", "snapshot")
	commentBody := map[string]any{"type": "string", "minLength": 1, "description": "Non-whitespace UTF-8 text, at most 4096 bytes. Comments grant no agent authority."}
	comment := obj(map[string]any{"id": str, "body": str, "author_user_id": str, "author_name": str, "created_at": str, "version": positive, "edited_at": str, "deleted_at": str, "can_edit": boolean, "can_delete": boolean, "can_view_history": boolean, "can_react": boolean, "reactions": array(obj(map[string]any{"emoji": map[string]any{"type": "string", "enum": []string{"👍", "❤️", "👀", "✅"}}, "count": integer, "reacted_by_me": boolean}, "emoji", "count", "reacted_by_me"))}, "id", "body", "author_user_id", "author_name", "created_at", "version", "can_edit", "can_delete", "can_view_history", "can_react", "reactions")
	thread := obj(map[string]any{"id": str, "anchor": map[string]any{"oneOf": []any{wholeAnchor, capturedAnchor}}, "status": map[string]any{"type": "string", "enum": []string{"open", "resolved"}}, "version": positive, "created_by_user_id": str, "created_by_name": str, "created_at": str, "updated_at": str, "resolved_by_user_id": str, "resolved_at": str, "comments": array(comment), "applied_version": positive}, "id", "anchor", "status", "version", "created_by_user_id", "created_by_name", "created_at", "updated_at", "comments")
	threadList := obj(map[string]any{"threads": array(thread), "can_comment": boolean, "can_resolve": boolean, "next_cursor": str}, "threads", "can_comment", "can_resolve")
	permissionTarget := obj(map[string]any{"element_id": str, "expected_rev": positive}, "element_id", "expected_rev")
	permissionFields := map[string]any{"run_id": str, "elements": array(permissionTarget), "methods": array(map[string]any{"type": "string", "enum": []string{"update", "delete"}}), "purpose": str}
	permission := obj(mergeProps(permissionFields, map[string]any{"id": str, "agent_id": str, "agent_name": str, "expires_at": str, "created_at": str, "used": boolean, "revoked": boolean}), "id", "run_id", "agent_id", "agent_name", "elements", "methods", "purpose", "expires_at", "created_at", "used", "revoked")
	permissionList := obj(map[string]any{"permissions": array(permission), "eligible_runs": array(obj(map[string]any{"run_id": str, "agent_id": str, "agent_name": str, "expires_at": str, "needs_board_delegation": boolean}, "run_id", "agent_id", "agent_name", "expires_at"))}, "permissions", "eligible_runs")
	catalog := map[string]DomainSchema{
		"PATCH /api/v1/pages/{slug}/board/threads/{threadId}/comments/{commentId}":                 {Request: obj(map[string]any{"base_version": positive, "mutation_id": str, "body": commentBody}, "base_version", "mutation_id", "body"), RequestRequired: true, Response: thread},
		"DELETE /api/v1/pages/{slug}/board/threads/{threadId}/comments/{commentId}":                {Request: obj(map[string]any{"base_version": positive, "mutation_id": str}, "base_version", "mutation_id"), RequestRequired: true, Response: thread},
		"PUT /api/v1/pages/{slug}/board/threads/{threadId}/comments/{commentId}/reactions/{emoji}": {Request: obj(map[string]any{"base_version": positive, "mutation_id": str, "active": boolean}, "base_version", "mutation_id", "active"), RequestRequired: true, Response: thread},
		"GET /api/v1/pages/{slug}/board/threads/{threadId}/comments/{commentId}/history":           {Response: obj(map[string]any{"history": array(obj(map[string]any{"version": positive, "body": str, "action": map[string]any{"type": "string", "enum": []string{"create", "edit", "delete"}}, "actor_user_id": str, "created_at": str}, "version", "body", "action", "actor_user_id", "created_at"))}, "history")},
		"GET /api/v1/pages/{slug}/board/element-permissions":                                       {Response: permissionList},
		"POST /api/v1/pages/{slug}/board/element-permissions":                                      {Request: obj(mergeProps(permissionFields, map[string]any{"mutation_id": str, "expires_in_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 3600}}), "run_id", "elements", "methods", "purpose", "mutation_id", "expires_in_seconds"), RequestRequired: true, Response: permission, SuccessStatuses: []string{"200", "201"}},
		"DELETE /api/v1/pages/{slug}/board/element-permissions/{permissionId}":                     {Response: obj(map[string]any{}), SuccessStatuses: []string{"204"}},
		"GET /api/v1/pages/{slug}/board/threads":                                                   {Response: threadList, Parameters: discussionPaginationParameters(false), ErrorStatuses: []string{"400", "401", "403", "404", "500"}},
		"POST /api/v1/pages/{slug}/board/threads":                                                  {Request: obj(map[string]any{"anchor": inputAnchor, "body": commentBody, "mutation_id": str}, "anchor", "body", "mutation_id"), RequestRequired: true, Response: thread, SuccessStatuses: []string{"200", "201"}},
		"POST /api/v1/pages/{slug}/board/threads/{threadId}/comments":                              {Request: obj(map[string]any{"body": commentBody, "base_version": positive, "mutation_id": str}, "body", "base_version", "mutation_id"), RequestRequired: true, Response: thread, SuccessStatuses: []string{"200", "201"}},
		"POST /api/v1/pages/{slug}/board/threads/{threadId}/resolve":                               {Request: obj(map[string]any{"resolved": boolean, "base_version": positive, "mutation_id": str}, "resolved", "base_version", "mutation_id"), RequestRequired: true, Response: thread},
		"POST /api/v1/pages/{slug}/board/delegations":                                              {Request: obj(map[string]any{"run_id": str}, "run_id"), RequestRequired: true, Response: obj(map[string]any{"run_id": str, "agent_id": str, "expires_at": str, "scope": str}, "run_id", "agent_id", "expires_at", "scope"), SuccessStatuses: []string{"201"}},
		"DELETE /api/v1/pages/{slug}/board/delegations/{runId}":                                    {Response: obj(map[string]any{}), SuccessStatuses: []string{"204"}},
		"POST /api/v1/pages/{slug}/board/files":                                                    {Request: map[string]any{"type": "string", "format": "binary"}, RequestRequired: true, RequestMedia: []string{"image/png", "image/jpeg"}, Response: obj(map[string]any{"file_id": str, "content_type": str, "width": integer, "height": integer, "bytes": integer, "seq": integer, "result_seq": integer}, "file_id", "content_type", "width", "height", "bytes", "seq", "result_seq"), SuccessStatuses: []string{"200", "201"}},
		"GET /api/v1/pages/{slug}/board/files/{fileId}":                                            {Response: map[string]any{"type": "string", "format": "binary"}, ResponseMedia: []string{"image/png", "image/jpeg"}},
		"GET /api/v1/pages/{slug}/board":                                                           {Response: snapshot},
		"POST /api/v1/pages/{slug}/board":                                                          {Request: obj(map[string]any{"mutation_id": str}, "mutation_id"), RequestRequired: true, Response: snapshot, SuccessStatuses: []string{"200"}},
		"POST /api/v1/pages/{slug}/board/batches":                                                  {Request: batch, RequestRequired: true, Response: receipt, SuccessStatuses: []string{"200", "201"}},
		"POST /api/v1/pages/{slug}/board/placements":                                               {Request: createPlacement, RequestRequired: true, Response: obj(map[string]any{"seq": integer, "result_seq": integer, "placement_id": str}, "seq", "result_seq", "placement_id"), SuccessStatuses: []string{"200", "201"}},
		"DELETE /api/v1/pages/{slug}/board/placements/{placementId}":                               {Request: obj(mutation, "mutation_id", "base_seq"), RequestRequired: true, Response: receipt},
	}
	for route, schema := range boardRequestSchemaCatalog() {
		catalog[route] = schema
	}
	return catalog
}
