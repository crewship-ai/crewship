package main

// finalWorkflowIssueSchemaCatalog is the last response-contract audit for the
// workflow/issues surface. Properties mirror the private response DTOs in
// internal/api and stay separate from the earlier domain catalogs.
func finalWorkflowIssueSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	nullable := func(s map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range s {
			out[k] = v
		}
		out["nullable"] = true
		return out
	}
	arr := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	obj := func(properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": properties}
	}
	open := func() map[string]any { return map[string]any{"type": "object", "additionalProperties": true} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }

	recurringIssue := obj(map[string]any{"id": str(), "crew_id": str(), "crew_name": str(), "title": str(), "description": nullable(str()), "priority": str(), "project_id": nullable(str()), "milestone_id": nullable(str()), "assignee_type": nullable(str()), "assignee_id": nullable(str()), "labels_json": nullable(str()), "cron_expression": str(), "enabled": boolean(), "next_run": nullable(str()), "last_run": nullable(str()), "run_count": integer(), "created_at": str()})
	triageRule := obj(map[string]any{"id": str(), "name": str(), "pattern": str(), "match_type": str(), "crew_id": nullable(str()), "assignee_id": nullable(str()), "priority": nullable(str()), "project_id": nullable(str()), "labels_json": nullable(str()), "position": integer(), "enabled": boolean(), "match_count": integer(), "created_at": str()})
	milestone := obj(map[string]any{"id": str(), "project_id": str(), "name": str(), "description": nullable(str()), "target_date": nullable(str()), "status": str(), "position": integer(), "issue_count": integer(), "done_count": integer(), "created_at": str(), "updated_at": str()})
	relation := obj(map[string]any{"id": str(), "source_id": str(), "target_id": str(), "relation_type": str(), "target_identifier": nullable(str()), "target_title": str(), "target_status": str(), "created_at": str()})
	comment := obj(map[string]any{"id": str(), "mission_id": str(), "author_type": str(), "author_id": str(), "author_name": str(), "body": str(), "created_at": str(), "updated_at": str()})
	activity := obj(map[string]any{"id": str(), "mission_id": str(), "actor_type": str(), "actor_id": str(), "actor_name": nullable(str()), "action": str(), "details": nullable(str()), "created_at": str()})
	// mission_id and source (#2313, item 3) tell a client WHY a run is
	// attributed to the issue, not just that it is: "task" (the issue's own
	// mission_tasks plan), "mention" (an @mention dispatch, via
	// mission_comment_mentions), or "delegation" (a sub-agent's own further
	// /assign call mid-mission, found only via assignments.mission_id).
	issueRun := obj(map[string]any{"id": str(), "status": str(), "agent_name": str(), "task": str(), "started_at": str(), "ended_at": str(), "duration_ms": integer(), "result_summary": str(), "error_message": str(), "mission_id": nullable(str()), "source": str()})
	checkpointState := obj(map[string]any{"agent_memory": map[string]any{"type": "object", "additionalProperties": str()}, "pending_tasks": arr(str()), "open_assignments": arr(str()), "crew_container_id": str(), "meta": open()})
	checkpoint := obj(map[string]any{"id": str(), "workspace_id": str(), "crew_id": str(), "mission_id": str(), "label": str(), "journal_cursor": str(), "state": checkpointState, "fork_of": str(), "created_by": str(), "created_at": str()})
	runInsights := obj(map[string]any{"window": str(), "totals": obj(map[string]any{"total": integer(), "succeeded": integer(), "failed": integer(), "running": integer()}), "duration": obj(map[string]any{"p50_ms": integer(), "p95_ms": integer()}), "by_trigger": arr(obj(map[string]any{"key": str(), "total": integer(), "failed": integer()})), "by_model": arr(obj(map[string]any{"key": str(), "total": integer(), "failed": integer()})), "by_crew": arr(obj(map[string]any{"id": str(), "name": str(), "total": integer(), "failed": integer()})), "top_agents": arr(obj(map[string]any{"id": str(), "name": str(), "crew_name": str(), "total": integer(), "failed": integer()})), "truncated": boolean()})
	escalation := obj(map[string]any{"id": str(), "type": str(), "from_name": str(), "from_slug": str(), "reason": str(), "context": nullable(str()), "metadata": nullable(str()), "peer_conversation_id": nullable(str()), "status": str(), "resolution": nullable(str()), "action": nullable(str()), "redirect_to": nullable(str()), "resolved_by": nullable(str()), "resolved_at": nullable(str()), "created_at": str(), "credential_id": nullable(str()), "deadline_at": nullable(str()), "answer_deadline_at": nullable(str()), "agent_gave_up_at": nullable(str()), "second_approver_required": boolean(), "second_approver_by_workspace": boolean(), "second_approver_by_tier": boolean(), "security_level_label": str()})
	issue := ref("Issue")
	// A pull request or merge request attached to an issue. Every fetched field
	// is nullable on purpose: a link exists from the moment it is pasted, and
	// stays readable when the forge is unreachable — so the row keeps the state
	// it last had rather than vanishing, and last_sync_error says why.
	codeLink := obj(map[string]any{
		"id": str(), "mission_id": str(), "workspace_id": str(),
		"provider": str(), "host": str(), "owner": str(), "repo": str(),
		"number": integer(), "kind": str(), "url": str(),
		"title": nullable(str()), "state": nullable(str()), "author": nullable(str()),
		"source_branch": nullable(str()), "target_branch": nullable(str()),
		"remote_created_at": nullable(str()), "remote_updated_at": nullable(str()),
		"remote_merged_at": nullable(str()), "remote_closed_at": nullable(str()),
		"credential_id": nullable(str()), "last_synced_at": nullable(str()),
		"last_sync_error": nullable(str()),
	})
	// A file attached to an issue (#1768 item 7). sha256 is part of the
	// contract, not an implementation leak: it is how a client verifies what it
	// downloaded, how a backup verify pass tells "blob missing" from "blob
	// corrupt", and how an agent decides it has already read this exact file.
	// storage_key is deliberately absent — publishing the on-disk layout invites
	// clients to construct paths instead of asking for resources.
	//
	// The uploader is TWO nullable columns rather than one polymorphic pair,
	// because exactly one of them is set and a client rendering "who attached
	// this" needs to know which kind it is looking at.
	attachment := obj(map[string]any{
		"id": str(), "workspace_id": str(), "owner_type": str(), "owner_id": str(),
		"filename": str(), "content_type": str(), "size_bytes": integer(), "sha256": str(),
		"uploaded_by_user_id": nullable(str()), "uploaded_by_agent_id": nullable(str()),
		"uploaded_by_name": nullable(str()),
		"created_at":       str(),
	})
	return map[string]DomainSchema{
		"GET /api/v1/recurring-issues": {Response: arr(recurringIssue)}, "POST /api/v1/recurring-issues": {Response: recurringIssue}, "PATCH /api/v1/recurring-issues/{recurringId}": {Response: recurringIssue},
		"GET /api/v1/triage-rules": {Response: arr(triageRule)}, "POST /api/v1/triage-rules": {Response: triageRule}, "PATCH /api/v1/triage-rules/{ruleId}": {Response: triageRule}, "POST /api/v1/triage/process": {Response: obj(map[string]any{"processed": integer(), "matched": integer()})},
		"GET /api/v1/projects/{projectId}/milestones": {Response: arr(milestone)}, "POST /api/v1/projects/{projectId}/milestones": {Response: milestone}, "PATCH /api/v1/milestones/{milestoneId}": {Response: milestone},
		"GET /api/v1/crews/{crewId}/issues/{identifier}/code-links":                   {Response: arr(codeLink)},
		"POST /api/v1/crews/{crewId}/issues/{identifier}/code-links":                  {Request: obj(map[string]any{"url": str()}), Response: codeLink},
		"POST /api/v1/crews/{crewId}/issues/{identifier}/code-links/{linkId}/refresh": {Request: obj(map[string]any{}), Response: codeLink},
		"DELETE /api/v1/crews/{crewId}/issues/{identifier}/code-links/{linkId}":       {Response: obj(map[string]any{"status": str()})},
		// Attachments. The upload is multipart/form-data with one `file` part —
		// declaring that rather than letting it default to application/json is
		// the difference between a spec a client can drive and one it cannot.
		// The download's response is binary and its media type is whatever the
		// server RESOLVED from the extension, so the list enumerates the
		// allowlist's families rather than claiming one.
		"GET /api/v1/crews/{crewId}/issues/{identifier}/attachments": {Response: arr(attachment)},
		"POST /api/v1/crews/{crewId}/issues/{identifier}/attachments": {
			Request:      obj(map[string]any{"file": map[string]any{"type": "string", "format": "binary"}}),
			RequestMedia: []string{"multipart/form-data"},
			Response:     attachment,
		},
		"GET /api/v1/crews/{crewId}/issues/{identifier}/attachments/{attachmentId}": {
			Response:      map[string]any{"type": "string", "format": "binary"},
			ResponseMedia: []string{"application/octet-stream", "text/plain", "application/json", "image/png", "image/jpeg", "application/pdf"},
		},
		"DELETE /api/v1/crews/{crewId}/issues/{identifier}/attachments/{attachmentId}": {Response: obj(map[string]any{"status": str()})},
		"GET /api/v1/crews/{crewId}/issues/{identifier}/relations":                     {Response: arr(relation)}, "POST /api/v1/crews/{crewId}/issues/{identifier}/relations": {Response: obj(map[string]any{"id": str(), "status": str()})}, "DELETE /api/v1/relations/{relationId}": {Response: obj(map[string]any{"status": str()})},
		"GET /api/v1/crews/{crewId}/issues/{identifier}/comments": {Response: arr(comment)}, "POST /api/v1/crews/{crewId}/issues/{identifier}/comments": {Response: comment}, "GET /api/v1/crews/{crewId}/issues/{identifier}/activity": {Response: arr(activity)}, "GET /api/v1/crews/{crewId}/issues/{identifier}/runs": {Response: arr(issueRun)}, "GET /api/v1/crews/{crewId}/issues/{identifier}/subtasks": {Response: arr(issue)},
		"POST /api/v1/crews/{crewId}/issues": {Response: ref("Issue")}, "GET /api/v1/crews/{crewId}/issues/{identifier}": {Response: ref("Issue")}, "PATCH /api/v1/crews/{crewId}/issues/{identifier}": {Response: ref("Issue")},
		"POST /api/v1/crews/{crewId}/issues/{identifier}/review": {Response: obj(map[string]any{"status": str(), "action": str()})}, "POST /api/v1/crews/{crewId}/issues/{identifier}/start": {Response: obj(map[string]any{"status": str(), "identifier": str()})}, "POST /api/v1/crews/{crewId}/issues/{identifier}/stop": {Response: obj(map[string]any{"status": str(), "identifier": str(), "runs_stopped": integer()})}, "PATCH /api/v1/issues/bulk": {Response: obj(map[string]any{"updated": integer()})},
		// B1 (#2332): an issue's agent sessions — one row per (issue, agent),
		// only 'pending' is ever written until B2/B3 land.
		"GET /api/v1/crews/{crewId}/issues/{identifier}/sessions": {Response: arr(obj(map[string]any{"id": str(), "mission_id": str(), "agent_id": str(), "agent_name": str(), "state": str(), "last_consumed_seq": integer(), "active_run_id": str(), "agent_version": integer(), "last_activity_at": str(), "created_at": str(), "updated_at": str()}))},
		// B5 (#2345, §9.5): a session's checkpoint history — NOT the same
		// table as the `checkpoint` schema above (mission save-points,
		// /api/v1/missions/{missionId}/checkpoints): agent_session_checkpoints
		// is the §11.1 "latest checkpoint" a woken agent resumes from.
		"GET /api/v1/crews/{crewId}/issues/{identifier}/sessions/{sessionId}/checkpoints": {Response: arr(obj(map[string]any{"id": str(), "session_id": str(), "run_id": str(), "seq_at_write": integer(), "done": str(), "plan": str(), "facts": str(), "blockers": str(), "next_step": str(), "confidence": str(), "parsed": boolean(), "created_at": str()}))},
		"GET /api/v1/runs": {Response: ref("RunList")}, "GET /api/v1/runs/{id}": {Response: ref("Run")}, "GET /api/v1/runs/insights": {Response: runInsights},
		"GET /api/v1/missions/{missionId}/checkpoints": {Response: obj(map[string]any{"checkpoints": arr(checkpoint), "count": integer(), "mission_id": str()})}, "POST /api/v1/missions/{missionId}/checkpoints": {Response: checkpoint}, "GET /api/v1/checkpoints/{id}": {Response: checkpoint}, "POST /api/v1/checkpoints/{id}/restore": {Response: obj(map[string]any{"checkpoint": checkpoint, "journal_cursor": str(), "warn_divergence": arr(str())})}, "POST /api/v1/checkpoints/{id}/fork": {Response: obj(map[string]any{"new_mission_id": str(), "new_checkpoint_id": str()})},
		"GET /api/v1/crews/{crewId}/escalations": {Response: arr(escalation)}, // agent_still_waiting / agent_gave_up_at / note: an escalation may be
		// resolved AFTER the agent's wait window closed (the two clocks —
		// internal/api/escalation_lifecycle.go). The resolve succeeds, but the
		// run that asked will not receive the answer, so the response says so
		// rather than letting a caller assume it unblocked something.
		"PATCH /api/v1/escalations/{escalationId}/resolve": {Response: obj(map[string]any{"id": str(), "status": str(), "action": str(), "agent_still_waiting": boolean(), "agent_gave_up_at": str(), "note": str()})}, "GET /api/v1/escalations/pending-count": {Response: obj(map[string]any{"count": integer()})}, "DELETE /api/v1/crews/{crewId}/escalations": {Response: obj(map[string]any{"deleted": integer()})},
		// Escalation lifecycle beyond "a human decided": withdrawing a question
		// that stopped mattering, and forcing the deadline sweep.
		"POST /api/v1/escalations/{escalationId}/cancel": {Response: obj(map[string]any{"id": str(), "status": str()})},
		"POST /api/v1/escalations/sweep-expired":         {Response: obj(map[string]any{"expired": integer()})},
		"GET /api/v1/issues":                             {Response: ref("IssueList")}, "GET /api/v1/issues/{identifier}": {Response: ref("Issue")},
	}
}
