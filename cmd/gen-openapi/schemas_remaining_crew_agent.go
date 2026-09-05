package main

// remainingCrewAgentSchemaCatalogV1 contains the contracts that were not part
// of the first crew/workspace read audit.  The names and fields intentionally
// mirror the small response structs and writeJSON maps in the corresponding
// handlers; this catalog is kept separate so later audits cannot silently
// change the established component names.
func remainingCrewAgentSchemaCatalogV1() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	anyObject := func() map[string]any { return map[string]any{"type": "object", "additionalProperties": true} }
	object := func(fields map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": fields}
	}
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }

	components := map[string]any{}
	components["WorkspaceMemberResponseV1"] = object(map[string]any{"id": str(), "workspace_id": str(), "user_id": str(), "role": str(), "created_at": str(), "updated_at": str(), "user": anyObject()})
	components["CrewMemberResponseV1"] = object(map[string]any{"id": str(), "crew_id": str(), "user_id": str(), "role": str(), "created_at": str(), "updated_at": str(), "user": anyObject()})
	routes := map[string]DomainSchema{}
	add := func(method, path, name string, response map[string]any) {
		components[name] = response
		routes[method+" "+path] = DomainSchema{Response: ref(name)}
	}
	addRequest := func(method, path, requestName string, request map[string]any) {
		components[requestName] = request
		schema := routes[method+" "+path]
		schema.Request = ref(requestName)
		routes[method+" "+path] = schema
	}
	action := object(map[string]any{"success": boolean(), "status": str(), "message": str()})
	addAction := func(method, path, name string) { add(method, path, name, action) }
	// A 204 has no body, so it gets no component. Declaring one would put a
	// schema in the spec for bytes the handler never writes, which is the same
	// class of untruth this catalog exists to remove.
	addNoContent := func(method, path string) {
		routes[method+" "+path] = DomainSchema{SuccessStatuses: []string{"204"}}
	}

	// Agent read subresources.
	add("GET", "/api/v1/agents/crews-status", "RemainingAgentCrewsStatusV1", object(map[string]any{"crews": array(anyObject()), "agents": array(anyObject())}))
	add("GET", "/api/v1/agent-load", "RemainingAgentLoadV1", object(map[string]any{"agents": array(anyObject()), "total": integer(), "running": integer()}))
	add("GET", "/api/v1/agents/{agentId}/credential-bindings", "RemainingAgentCredentialBindingsV1", array(anyObject()))
	add("GET", "/api/v1/agents/{agentId}/credentials", "RemainingAgentCredentialsV1", array(anyObject()))
	add("GET", "/api/v1/agents/{agentId}/debug", "RemainingAgentDebugV1", object(map[string]any{"agent_id": str(), "status": str(), "details": anyObject()}))
	add("GET", "/api/v1/agents/{agentId}/git-log", "RemainingAgentGitLogV1", object(map[string]any{"commits": array(anyObject()), "branch": str()}))
	add("GET", "/api/v1/agents/{agentId}/integrations/resolved", "RemainingAgentResolvedIntegrationsV1", object(map[string]any{"integrations": array(anyObject())}))
	add("GET", "/api/v1/agents/{agentId}/learning", "RemainingAgentLearningV1", object(map[string]any{"agent_id": str(), "enabled": boolean(), "mode": str(), "updated_at": str()}))
	add("GET", "/api/v1/agents/{agentId}/logs", "RemainingAgentLogsV1", object(map[string]any{"logs": array(str()), "agent_id": str()}))
	add("GET", "/api/v1/agents/{agentId}/notification-channels", "RemainingAgentNotificationChannelsV1", array(anyObject()))
	add("GET", "/api/v1/agents/{agentId}/persona", "RemainingAgentPersonaV1", object(map[string]any{"agent_id": str(), "content": str(), "version": integer(), "created_at": str(), "updated_at": str()}))
	add("GET", "/api/v1/agents/{agentId}/persona/history", "RemainingAgentPersonaHistoryV1", array(anyObject()))
	add("GET", "/api/v1/agents/{agentId}/peers", "RemainingAgentPeersV1", array(anyObject()))
	add("GET", "/api/v1/agents/{agentId}/peers/{userId}", "RemainingAgentPeerV1", object(map[string]any{"agent_id": str(), "user_id": str(), "consent": boolean(), "facts": array(anyObject()), "created_at": str(), "updated_at": str()}))
	add("GET", "/api/v1/agents/{agentId}/runs", "RemainingAgentRunsV1", array(anyObject()))
	add("GET", "/api/v1/agents/{agentId}/skills", "RemainingAgentSkillsV1", array(anyObject()))
	add("GET", "/api/v1/agents/{agentId}/chats", "RemainingAgentChatsV1", array(anyObject()))
	add("GET", "/api/v1/agents/{agentId}/persona", "RemainingAgentPersonaV1", object(map[string]any{"agent_id": str(), "content": str(), "version": integer(), "created_at": str(), "updated_at": str()}))

	// Workspace and crew actions return either a resource or a small status map.
	add("POST", "/api/v1/workspaces", "RemainingWorkspaceCreatedV1", ref("Workspace"))
	add("POST", "/api/v1/workspaces/{workspaceId}/members", "RemainingWorkspaceMemberCreatedV1", ref("WorkspaceMemberResponseV1"))
	add("POST", "/api/v1/workspaces/{workspaceId}/members/provision", "RemainingWorkspaceMemberProvisionedV1", object(map[string]any{"user_id": str(), "email": str(), "setup_token": str(), "setup_url": str()}))
	add("POST", "/api/v1/workspaces/{workspaceId}/invitations", "RemainingWorkspaceInvitationCreatedV1", object(map[string]any{"id": str(), "email": str(), "role": str(), "token": str(), "expires_at": str(), "created_at": str()}))
	add("POST", "/api/v1/crews", "RemainingCrewCreatedV1", ref("Crew"))
	add("PATCH", "/api/v1/crews/{crewId}", "RemainingCrewUpdatedV1", ref("Crew"))
	add("PUT", "/api/v1/crews/{crewId}", "RemainingCrewReplacedV1", ref("Crew"))
	add("POST", "/api/v1/crews/{crewId}/members", "RemainingCrewMemberCreatedV1", ref("CrewMemberResponseV1"))
	add("PATCH", "/api/v1/crews/{crewId}/members/{memberId}", "RemainingCrewMemberUpdatedV1", ref("CrewMemberResponseV1"))
	add("POST", "/api/v1/crews/{crewId}/issues", "RemainingCrewIssueCreatedV1", ref("Issue"))
	add("PATCH", "/api/v1/crews/{crewId}/issues/{identifier}", "RemainingCrewIssueUpdatedV1", ref("Issue"))
	add("POST", "/api/v1/crews/{crewId}/missions", "RemainingCrewMissionCreatedV1", object(map[string]any{"id": str(), "title": str(), "description": str(), "status": str(), "tasks": array(anyObject()), "created_at": str(), "updated_at": str()}))
	add("PATCH", "/api/v1/crews/{crewId}/missions/{missionId}", "RemainingCrewMissionUpdatedV1", object(map[string]any{"id": str(), "title": str(), "description": str(), "status": str(), "tasks": array(anyObject()), "created_at": str(), "updated_at": str()}))
	add("PUT", "/api/v1/crews/{crewId}/persona", "RemainingCrewPersonaUpdatedV1", ref("CrewPersonaResponseV1"))
	add("PUT", "/api/v1/crews/{crewId}/policy", "RemainingCrewPolicyUpdatedV1", ref("CrewPolicyResponseV1"))
	add("GET", "/api/v1/crews/{crewId}/provision", "RemainingCrewProvisionStatusV1", object(map[string]any{"crew_id": str(), "status": str(), "phase": str(), "message": str(), "updated_at": str()}))
	addAction("POST", "/api/v1/crews/{crewId}/provision", "RemainingCrewProvisionTriggeredV1")
	addAction("POST", "/api/v1/crews/{crewId}/rebuild", "RemainingCrewRebuildTriggeredV1")
	addAction("POST", "/api/v1/crews/{crewId}/restart-agents", "RemainingCrewAgentsRestartedV1")
	// #1845 crew image freshness. Given real shapes rather than the shared
	// `action` envelope, because both answers are the whole point of the
	// endpoints: a client that cannot read `behind` and `reason` off the
	// status, or `previous_digest`/`new_digest` off the refresh, has been told
	// only that something happened.
	add("GET", "/api/v1/crews/{crewId}/image-status", "RemainingCrewImageStatusV1", object(map[string]any{
		"crew_id": str(), "image": str(), "container_id": str(), "running": boolean(),
		"running_digest": str(), "resolved_digest": str(), "behind": boolean(), "reason": str(),
	}))
	add("POST", "/api/v1/crews/{crewId}/refresh-image", "RemainingCrewImageRefreshedV1", object(map[string]any{
		"crew_id": str(), "image": str(), "previous_digest": str(), "new_digest": str(),
		"container_removed": boolean(),
	}))
	// container-start gets a real shape rather than the `action` envelope
	// for the same reason refresh-image does: the caller's next step reads
	// `container_id` off it, and `notices` is how it learns the crew came
	// up WITHOUT something it declared — a fact an "ok, something
	// happened" envelope would swallow.
	add("POST", "/api/v1/crews/{crewId}/container-start", "RemainingCrewContainerStartedV1", object(map[string]any{
		"crew_id": str(), "slug": str(), "container_id": str(), "status": str(),
		"notices": array(str()),
	}))
	// Stop carries no container_id: the container it names is gone by the
	// time the caller reads the answer, so returning one would invite a
	// follow-up call against a dead id.
	add("POST", "/api/v1/crews/{crewId}/container-stop", "RemainingCrewContainerStoppedV1", object(map[string]any{
		"crew_id": str(), "slug": str(), "status": str(),
	}))

	// Agent lifecycle and subresource mutations.
	add("POST", "/api/v1/agents", "RemainingAgentCreatedV1", ref("Agent"))
	add("POST", "/api/v1/agents/hire", "RemainingAgentHiredV1", object(map[string]any{"agent": ref("Agent"), "status": str(), "approval_required": boolean(), "message": str()}))
	add("POST", "/api/v1/agents/{agentId}/rehire", "RemainingAgentRehiredV1", ref("Agent"))
	add("POST", "/api/v1/agents/{agentId}/approve-hire", "RemainingAgentHireApprovedV1", action)
	add("PATCH", "/api/v1/agents/{agentId}", "RemainingAgentUpdatedV1", ref("Agent"))
	addAction("DELETE", "/api/v1/agents/{agentId}", "RemainingAgentDeletedV1")
	add("POST", "/api/v1/agents/{agentId}/webhook-secret/rotate", "RemainingAgentWebhookSecretV1", object(map[string]any{"webhook_secret": str(), "rotated_at": str()}))
	addAction("DELETE", "/api/v1/agents/{agentId}/persona", "RemainingAgentPersonaDeletedV1")
	add("PUT", "/api/v1/agents/{agentId}/persona", "RemainingAgentPersonaUpdatedV1", ref("RemainingAgentPersonaV1"))
	add("POST", "/api/v1/agents/{agentId}/persona/suggest", "RemainingAgentPersonaSuggestionV1", object(map[string]any{"content": str(), "rationale": str()}))
	add("PATCH", "/api/v1/agents/{agentId}/learning", "RemainingAgentLearningUpdatedV1", ref("RemainingAgentLearningV1"))
	addAction("DELETE", "/api/v1/agents/{agentId}/peers/{userId}", "RemainingAgentPeerDeletedV1")
	addAction("POST", "/api/v1/agents/{agentId}/skills", "RemainingAgentSkillAddedV1")
	addAction("DELETE", "/api/v1/agents/{agentId}/skills/{skillId}", "RemainingAgentSkillRemovedV1")
	addAction("POST", "/api/v1/agents/{agentId}/credentials", "RemainingAgentCredentialAddedV1")
	addAction("DELETE", "/api/v1/agents/{agentId}/credentials/{assignmentId}", "RemainingAgentCredentialRemovedV1")

	// Request contracts for the handlers whose bodies are not covered by the
	// older domain catalogs. Optional fields are intentionally not marked
	// required: handlers apply defaults and validate presence themselves.
	addRequest("POST", "/api/v1/workspaces/{workspaceId}/members", "RemainingWorkspaceMemberRequestV1", object(map[string]any{"user_id": str(), "role": str()}))
	addRequest("PUT", "/api/v1/agents/{agentId}/persona", "RemainingAgentPersonaRequestV1", object(map[string]any{"content": str()}))
	addRequest("PATCH", "/api/v1/agents/{agentId}/learning", "RemainingAgentLearningRequestV1", object(map[string]any{"enabled": boolean(), "mode": str()}))
	addRequest("PUT", "/api/v1/crews/{crewId}/persona", "RemainingCrewPersonaRequestV1", object(map[string]any{"content": str()}))
	addRequest("PUT", "/api/v1/crews/{crewId}/policy", "RemainingCrewPolicyRequestV1", object(map[string]any{"autonomy": str(), "execution": str(), "approval_required": boolean()}))

	// Remaining subroute actions. These handlers deliberately return compact
	// maps (or a file/task envelope), rather than the parent resource.
	add("GET", "/api/v1/crewshipd", "RemainingCrewshipdStatusV1", object(map[string]any{"status": str(), "version": str(), "message": str()}))
	// The chat attachment triple. The upload's response was catalogued as
	// {id,name,content_type,size,url}; the handler has never answered that —
	// it answers {filename,size,path,agent_path}, and that shape is a contract
	// the composer decodes, so the spec is corrected to it here.
	//
	// `path` is agent-relative (attachments/<chatId>/<attachmentId>/<filename>)
	// and `agent_path` is the same location inside the container. The list adds
	// the row's own identity and checksum, which is what the delete takes.
	add("POST", "/api/v1/agents/{agentId}/chats/{chatId}/attachments", "RemainingAgentAttachmentV1", object(map[string]any{"filename": str(), "size": integer(), "path": str(), "agent_path": str()}))
	add("GET", "/api/v1/agents/{agentId}/chats/{chatId}/attachments", "RemainingAgentAttachmentListV1", array(object(map[string]any{
		"id": str(), "workspace_id": str(), "owner_type": str(), "owner_id": str(),
		"filename": str(), "content_type": str(), "size_bytes": integer(), "sha256": str(),
		"uploaded_by_user_id": str(), "uploaded_by_agent_id": str(), "uploaded_by_name": str(),
		"created_at": str(), "path": str(), "agent_path": str(),
	})))
	addNoContent("DELETE", "/api/v1/agents/{agentId}/chats/{chatId}/attachments/{attachmentId}")
	add("PUT", "/api/v1/agents/{agentId}/files/save", "RemainingAgentFileSavedV1", object(map[string]any{"path": str(), "saved": boolean(), "message": str()}))
	addAction("POST", "/api/v1/agents/{agentId}/stop", "RemainingAgentStoppedV1")
	addAction("DELETE", "/api/v1/crews/{crewId}", "RemainingCrewDeletedV1")
	addAction("POST", "/api/v1/crews/{crewId}/apply-avatar-style", "RemainingCrewAvatarStyleAppliedV1")
	addAction("DELETE", "/api/v1/crews/{crewId}/escalations", "RemainingCrewEscalationsDeletedV1")
	add("DELETE", "/api/v1/crews/{crewId}/files/delete", "RemainingCrewFileDeletedV1", object(map[string]any{"path": str(), "deleted": boolean(), "message": str()}))
	add("PUT", "/api/v1/crews/{crewId}/files/save", "RemainingCrewFileSavedV1", object(map[string]any{"path": str(), "saved": boolean(), "message": str()}))
	addAction("POST", "/api/v1/crews/{crewId}/integrations/{integrationId}/tools/refresh", "RemainingCrewToolsRefreshedV1")
	addAction("DELETE", "/api/v1/crews/{crewId}/issues/{identifier}", "RemainingCrewIssueDeletedV1")
	addAction("POST", "/api/v1/crews/{crewId}/issues/{identifier}/comments", "RemainingCrewIssueCommentCreatedV1")
	addAction("POST", "/api/v1/crews/{crewId}/issues/{identifier}/relations", "RemainingCrewIssueRelationCreatedV1")
	addAction("POST", "/api/v1/crews/{crewId}/issues/{identifier}/review", "RemainingCrewIssueReviewedV1")
	addAction("POST", "/api/v1/crews/{crewId}/issues/{identifier}/start", "RemainingCrewIssueStartedV1")
	addAction("POST", "/api/v1/crews/{crewId}/issues/{identifier}/stop", "RemainingCrewIssueStoppedV1")
	addAction("DELETE", "/api/v1/crews/{crewId}/members/{memberId}", "RemainingCrewMemberDeletedV1")
	addAction("DELETE", "/api/v1/crews/{crewId}/missions/{missionId}", "RemainingCrewMissionDeletedV1")
	addAction("POST", "/api/v1/crews/{crewId}/missions/{missionId}/clone", "RemainingCrewMissionClonedV1")
	addAction("POST", "/api/v1/crews/{crewId}/missions/{missionId}/restart", "RemainingCrewMissionRestartedV1")
	addAction("POST", "/api/v1/crews/{crewId}/missions/{missionId}/resume", "RemainingCrewMissionResumedV1")
	addAction("POST", "/api/v1/crews/{crewId}/missions/{missionId}/start", "RemainingCrewMissionStartedV1")
	add("POST", "/api/v1/crews/{crewId}/missions/{missionId}/tasks", "RemainingCrewMissionTaskCreatedV1", object(map[string]any{"id": str(), "mission_id": str(), "title": str(), "status": str(), "created_at": str()}))
	add("PATCH", "/api/v1/crews/{crewId}/missions/{missionId}/tasks/{taskId}", "RemainingCrewMissionTaskUpdatedV1", object(map[string]any{"id": str(), "mission_id": str(), "title": str(), "status": str(), "updated_at": str()}))
	addAction("DELETE", "/api/v1/crews/{crewId}/persona", "RemainingCrewPersonaDeletedV1")
	addAction("POST", "/api/v1/crews/{crewId}/port-expose/{id}/revoke", "RemainingCrewPortRevokedV1")
	addAction("DELETE", "/api/v1/workspaces/{workspaceId}", "RemainingWorkspaceDeletedV1")
	add("PATCH", "/api/v1/workspaces/{workspaceId}", "RemainingWorkspaceUpdatedV1", ref("Workspace"))
	addAction("DELETE", "/api/v1/workspaces/{workspaceId}/members/{memberId}", "RemainingWorkspaceMemberDeletedV1")
	addAction("PATCH", "/api/v1/workspaces/{workspaceId}/members/{memberId}", "RemainingWorkspaceMemberRoleUpdatedV1")
	addAction("PATCH", "/api/v1/workspaces/{workspaceId}/members/{memberId}/capabilities", "RemainingWorkspaceMemberCapabilitiesUpdatedV1")

	// The exact payloads for these pipeline subroutes are versioned by the
	// pipeline handlers. Keeping named object contracts here documents their
	// stable response envelope and prevents a silent generic fallback.
	for _, route := range []string{
		"PATCH /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/metadata",
		"POST /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/signal",
		"POST /api/v1/workspaces/{workspaceId}/pipeline-schedules",
		"DELETE /api/v1/workspaces/{workspaceId}/pipeline-schedules/{scheduleId}",
		"PATCH /api/v1/workspaces/{workspaceId}/pipeline-schedules/{scheduleId}",
		"POST /api/v1/workspaces/{workspaceId}/pipeline-schedules/{scheduleId}/run",
		"POST /api/v1/workspaces/{workspaceId}/pipeline-schedules/{scheduleId}/activate",
		"DELETE /api/v1/workspaces/{workspaceId}/pipeline-webhooks/{webhookId}",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/import",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/pending/{pendingId}/cancel",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/runs/bulk_replay",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/runs/{runId}/cancel",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/runs/{runId}/replay",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/save",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/test_run",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/waitpoints/{token}/approve",
		"DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/approve",
		"PATCH /api/v1/workspaces/{workspaceId}/pipelines/{slug}/budget",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/disable",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/dry_run",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/enable",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/reject",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/rollback",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run_batch",
		"DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}/state",
		"DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}/state/{key}",
		"PUT /api/v1/workspaces/{workspaceId}/pipelines/{slug}/state/{key}",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/step_run",
		"DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}/steps/{stepId}/override",
		"PUT /api/v1/workspaces/{workspaceId}/pipelines/{slug}/steps/{stepId}/override",
		"PUT /api/v1/workspaces/{workspaceId}/pipelines/{slug}/tags",
		"DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}/tags/{tag}",
		"POST /api/v1/workspaces/{workspaceId}/skills/bulk-import",
		"POST /api/v1/workspaces/{workspaceId}/skills/import",
		"DELETE /api/v1/workspaces/{workspaceId}/skills/{skillId}",
	} {
		method, path, name := splitSchemaRouteName(route)
		add(method, path, "Remaining"+name+"V1", action)
	}

	// Topic-scoped signal delivery is deliberately NOT folded into the
	// action envelope above: the answer to "deliver this event" is which
	// runs it reached, and a generic {success, status, message} would drop
	// exactly the part a caller acts on. delivered=0 is a success, so the
	// count is the only way to tell "nobody was listening" from "two runs
	// resumed".
	add("POST", "/api/v1/workspaces/{workspaceId}/signals", "RemainingWorkspaceTopicSignalV1",
		object(map[string]any{
			"ok": boolean(), "delivered": integer(), "run_ids": array(str()), "truncated": boolean(),
		}))
	addRequest("POST", "/api/v1/workspaces/{workspaceId}/signals", "RemainingWorkspaceTopicSignalRequestV1",
		object(map[string]any{"event_type": str(), "payload": str()}))

	return routes, components
}

func splitSchemaRouteName(route string) (method, path, name string) {
	for i := 0; i < len(route); i++ {
		if route[i] == ' ' {
			method, path = route[:i], route[i+1:]
			break
		}
	}
	name = "PipelineAction"
	for _, part := range []string{"workspaces", "pipeline-runs", "pipeline-schedules", "pipeline-webhooks", "pipelines", "skills"} {
		if len(path) > 0 && containsSchemaPart(path, part) {
			name = part
			break
		}
	}
	return method, path, name
}

func containsSchemaPart(path, part string) bool {
	for i := 0; i+len(part) <= len(path); i++ {
		if path[i:i+len(part)] == part && (i == 0 || path[i-1] == '/') && (i+len(part) == len(path) || path[i+len(part)] == '/') {
			return true
		}
	}
	return false
}
