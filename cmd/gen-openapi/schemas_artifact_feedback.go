package main

// Snapshot and feedback components prepare the next HTTP route slices; this
// registry does not publish endpoints or grant caller-selected authority.
func artifactProjectFeedbackComponents() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	integer := func(min int) map[string]any {
		return map[string]any{"type": "integer", "format": "int64", "minimum": min}
	}
	stamp := func() map[string]any { return map[string]any{"type": "string", "format": "date-time"} }
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	object := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	text := func(max int) map[string]any {
		return map[string]any{"type": "string", "maxLength": max, "description": "The server additionally enforces this bound in UTF-8 bytes."}
	}
	path := func() map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "maxLength": 512, "description": "Canonical relative project path, at most512 UTF8 bytes; absolute paths, traversal, backslashes, query strings and fragments are refused."}
	}
	schemas := map[string]any{}
	schemas["ArtifactProjectOrigin"] = object(map[string]any{"chat_id": str(), "agent_id": str()}, "chat_id", "agent_id")
	schemas["ArtifactProjectReply"] = object(map[string]any{
		"text": str(), "version": integer(1), "needs_clarification": boolean(), "author": str(), "author_kind": map[string]any{"type": "string", "enum": []string{"human", "agent"}}, "created_at": stamp(),
	}, "text", "version", "author", "created_at")
	schemas["ArtifactProjectComment"] = object(map[string]any{
		"id": str(), "version": integer(1), "path": path(), "selector": str(), "quote": str(), "text": str(), "author": str(), "author_kind": map[string]any{"type": "string", "enum": []string{"human", "agent"}}, "task_id": str(), "created_at": stamp(),
		"status":           map[string]any{"type": "string", "enum": []string{"comment", "pending", "working", "answered", "needs_clarification"}},
		"answered_version": integer(1), "replies": array(ref("ArtifactProjectReply")),
		"delivery_retryable": map[string]any{"type": "boolean", "description": "Owner-only eligibility for manual retry of the exact undelivered pending task at the selected latest version. False for readers, missing authority, claimed tasks or any already accepted delivery intent. Not an execution grant; fresh read and existing admission are still required."},
	}, "id", "version", "path", "selector", "quote", "text", "author", "created_at", "status", "replies", "delivery_retryable")
	schemas["ArtifactProjectSnapshot"] = object(map[string]any{
		"artifact_kind": map[string]any{"type": "string", "enum": []string{"slides"}},
		"page_id":       str(), "name": str(), "slug": str(), "origin": map[string]any{"oneOf": []any{ref("ArtifactProjectOrigin"), map[string]any{"type": "null"}}},
		"enabled": boolean(), "can_write": boolean(), "can_share": boolean(), "can_restore": boolean(), "can_send_to_agent": boolean(), "can_comment": boolean(), "latest_version": integer(1), "shared_version": integer(0), "manifest": ref("ArtifactProjectManifest"),
		"versions": array(ref("ArtifactProjectVersion")), "quota": ref("ArtifactProjectQuota"), "comments": array(ref("ArtifactProjectComment")),
		"retention_preview": ref("ArtifactProjectRetentionPreview"),
	}, "page_id", "name", "slug", "origin", "enabled", "can_write", "can_share", "can_restore", "can_send_to_agent", "can_comment", "latest_version", "shared_version", "manifest", "versions", "quota", "comments")
	schemas["ArtifactProjectRetentionPreview"] = map[string]any{
		"type": "object", "additionalProperties": false,
		"description": "Owner-only dry run of the proposed history rule. Nothing listed in would_thin is deleted; retention stays keep-all.",
		"required":    []string{"policy", "recent_days", "dry_run", "evaluated_at", "total", "retained", "would_thin"},
		"properties": map[string]any{
			"policy": str(), "recent_days": integer(1), "dry_run": map[string]any{"type": "boolean", "enum": []bool{true}}, "evaluated_at": stamp(),
			"total": integer(0), "retained": integer(0), "would_thin": array(integer(1)),
		},
	}
	schemas["ArtifactProjectCommentRequest"] = object(map[string]any{
		"version": integer(1), "path": path(), "selector": text(2000), "quote": text(2000), "text": text(10000), "send_to_agent": boolean(),
	}, "version", "path", "selector", "quote", "text")
	schemas["ArtifactProjectReplyRequest"] = object(map[string]any{"text": text(10000), "version": integer(1), "needs_clarification": boolean()}, "text", "version")
	return schemas
}
