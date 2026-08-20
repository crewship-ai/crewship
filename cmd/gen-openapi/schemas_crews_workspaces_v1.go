package main

// crewWorkspaceGETSchemaCatalogV1 is the isolated, audited catalog for the
// crew/workspace read sub-resources. These handlers return a mix of DTOs,
// arrays, and envelopes, so they are kept out of the shared schema modules.
func crewWorkspaceGETSchemaCatalogV1() (map[string]map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	number := func() map[string]any { return map[string]any{"type": "number"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	anyObject := func() map[string]any { return map[string]any{"type": "object", "additionalProperties": true} }
	object := func(fields ...map[string]any) map[string]any {
		properties := map[string]any{}
		for _, fieldSet := range fields {
			for name, schema := range fieldSet {
				properties[name] = schema
			}
		}
		return map[string]any{"type": "object", "properties": properties}
	}
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	response := func(name string) DomainSchema { return DomainSchema{Response: ref(name)} }

	schemas := map[string]any{}
	routes := map[string]map[string]DomainSchema{"crew-workspace-get": {}}
	add := func(path, name string, schema map[string]any) {
		schemas[name] = schema
		routes["crew-workspace-get"]["GET "+path] = response(name)
	}
	addList := func(path, name string, item map[string]any) { add(path, name, array(item)) }

	member := object(map[string]any{"user_id": str(), "user_name": str(), "email": str(), "role": str(), "capabilities": array(str()), "created_at": str()})
	issue := object(map[string]any{"id": str(), "identifier": str(), "title": str(), "status": str(), "priority": str(), "description": str(), "created_at": str(), "updated_at": str()})
	pipeline := object(map[string]any{"id": str(), "workspace_id": str(), "name": str(), "slug": str(), "description": str(), "version": integer(), "enabled": boolean(), "created_at": str(), "updated_at": str()})

	add("/api/v1/crews/{crewId}/capabilities", "CrewCapabilitiesResponseV1", object(map[string]any{"crew_id": str(), "crew_slug": str(), "container": anyObject(), "integrations": array(anyObject()), "agents": array(object(map[string]any{"slug": str(), "name": str()})), "runtimes": anyObject(), "schema": anyObject()}))
	add("/api/v1/crews/{crewId}/services", "CrewServicesResponseV1", object(map[string]any{"services": array(object(map[string]any{"name": str(), "image": str(), "type": str(), "status": str(), "ports": array(str())}))}))
	add("/api/v1/crews/{crewId}/containers", "CrewContainersResponseV1", object(map[string]any{"containers": array(object(map[string]any{"name": str(), "image": str(), "kind": str(), "status": str(), "cpu_percent": number(), "memory_mb": integer(), "agent_count": integer()}))}))
	add("/api/v1/crews/{crewId}/credential-readiness", "CrewCredentialReadinessResponseV1", object(map[string]any{"crew_id": str(), "ready": boolean(), "credentials": array(object(map[string]any{"id": str(), "name": str(), "type": str(), "reason": str()}))}))
	add("/api/v1/crews/{crewId}/container-status", "CrewContainerStatusResponseV1", object(map[string]any{"status": str(), "container_id": str(), "crew_id": str(), "running": boolean(), "message": str()}))
	addList("/api/v1/crews/{crewId}/members", "CrewMembersResponseV1", member)
	addList("/api/v1/crews/{crewId}/integrations", "CrewIntegrationsResponseV1", object(map[string]any{"id": str(), "name": str(), "display_name": str(), "enabled": boolean(), "status": str()}))
	addList("/api/v1/crews/{crewId}/integrations/{integrationId}/tools", "CrewIntegrationToolsResponseV1", object(map[string]any{"name": str(), "description": str(), "enabled": boolean(), "input_schema": anyObject()}))
	addList("/api/v1/crews/{crewId}/assignments", "CrewAssignmentsResponseV1", object(map[string]any{"id": str(), "issue_id": str(), "agent_id": str(), "status": str(), "created_at": str(), "updated_at": str()}))
	addList("/api/v1/crews/{crewId}/missions", "CrewMissionsResponseV1", object(map[string]any{"id": str(), "title": str(), "description": str(), "status": str(), "created_at": str(), "updated_at": str()}))
	add("/api/v1/crews/{crewId}/missions/{missionId}", "CrewMissionResponseV1", object(map[string]any{"id": str(), "title": str(), "description": str(), "status": str(), "tasks": array(anyObject()), "created_at": str(), "updated_at": str()}))
	addList("/api/v1/crews/{crewId}/issues/{identifier}/activity", "CrewIssueActivityResponseV1", object(map[string]any{"id": str(), "type": str(), "message": str(), "actor_id": str(), "created_at": str()}))
	addList("/api/v1/crews/{crewId}/issues/{identifier}/runs", "CrewIssueRunsResponseV1", object(map[string]any{"id": str(), "status": str(), "agent_id": str(), "started_at": str(), "finished_at": str(), "error_message": str()}))
	addList("/api/v1/crews/{crewId}/issues/{identifier}/comments", "CrewIssueCommentsResponseV1", object(map[string]any{"id": str(), "body": str(), "author_id": str(), "author_name": str(), "created_at": str(), "updated_at": str()}))
	addList("/api/v1/crews/{crewId}/issues/{identifier}/relations", "CrewIssueRelationsResponseV1", object(map[string]any{"id": str(), "relation_type": str(), "issue_id": str(), "related_issue_id": str()}))
	addList("/api/v1/crews/{crewId}/issues/{identifier}/subtasks", "CrewIssueSubtasksResponseV1", issue)
	add("/api/v1/crews/{crewId}/issues/{identifier}", "CrewIssueResponseV1", issue)
	add("/api/v1/crews/{crewId}/persona", "CrewPersonaResponseV1", object(map[string]any{"crew_id": str(), "content": str(), "version": integer(), "created_at": str(), "updated_at": str()}))
	add("/api/v1/crews/{crewId}/policy", "CrewPolicyResponseV1", object(map[string]any{"crew_id": str(), "autonomy": str(), "execution": str(), "approval_required": boolean(), "updated_at": str()}))
	add("/api/v1/crews/{crewId}/peer-conversations", "CrewPeerConversationsResponseV1", object(map[string]any{"conversations": array(anyObject()), "count": integer()}))
	add("/api/v1/crews/{crewId}/standup", "CrewStandupResponseV1", object(map[string]any{"crew_id": str(), "date": str(), "summary": str(), "items": array(anyObject())}))
	add("/api/v1/crews/{crewId}/escalations", "CrewEscalationsResponseV1", object(map[string]any{"escalations": array(anyObject()), "count": integer()}))
	add("/api/v1/crews/{crewId}/port-expose", "CrewPortExposeResponseV1", object(map[string]any{"ports": array(object(map[string]any{"id": str(), "port": integer(), "host": str(), "status": str(), "created_at": str()}))}))
	add("/api/v1/crews/{crewId}/provision", "CrewProvisionResponseV1", object(map[string]any{"crew_id": str(), "status": str(), "phase": str(), "message": str(), "updated_at": str()}))
	add("/api/v1/crews/{crewId}/git-diff", "CrewGitDiffResponseV1", object(map[string]any{"diff": str(), "base_ref": str(), "head_ref": str(), "files": array(anyObject())}))

	addList("/api/v1/workspaces/{workspaceId}/members", "WorkspaceMembersResponseV1", member)
	addList("/api/v1/workspaces/{workspaceId}/invitations", "WorkspaceInvitationsResponseV1", object(map[string]any{"id": str(), "email": str(), "role": str(), "status": str(), "expires_at": str(), "created_at": str()}))
	add("/api/v1/workspaces/{workspaceId}/members/capabilities", "WorkspaceCapabilitiesResponseV1", object(map[string]any{"members": array(object(map[string]any{"user_id": str(), "role": str(), "capabilities": array(str())}))}))
	add("/api/v1/workspaces/{workspaceId}/members/{memberId}/capabilities", "WorkspaceMemberCapabilitiesResponseV1", object(map[string]any{"user_id": str(), "role": str(), "capabilities": array(str())}))
	addList("/api/v1/workspaces/{workspaceId}/pipelines", "WorkspacePipelinesResponseV1", pipeline)
	add("/api/v1/workspaces/{workspaceId}/pipelines/{slug}", "WorkspacePipelineResponseV1", pipeline)
	addList("/api/v1/workspaces/{workspaceId}/pipelines/{slug}/overrides", "WorkspacePipelineOverridesResponseV1", object(map[string]any{"step_id": str(), "agent_slug": str(), "enabled": boolean(), "config": anyObject()}))
	addList("/api/v1/workspaces/{workspaceId}/pipelines/{slug}/versions", "WorkspacePipelineVersionsResponseV1", object(map[string]any{"version": integer(), "definition": anyObject(), "created_at": str(), "created_by": str()}))
	add("/api/v1/workspaces/{workspaceId}/pipelines/{slug}/versions/{n}", "WorkspacePipelineVersionResponseV1", object(map[string]any{"version": integer(), "definition": anyObject(), "created_at": str(), "created_by": str()}))
	add("/api/v1/workspaces/{workspaceId}/pipelines/{slug}/diff", "WorkspacePipelineDiffResponseV1", object(map[string]any{"from_version": integer(), "to_version": integer(), "diff": str()}))
	add("/api/v1/workspaces/{workspaceId}/pipelines/{slug}/export", "WorkspacePipelineExportResponseV1", object(map[string]any{"name": str(), "slug": str(), "definition": anyObject(), "version": integer()}))
	add("/api/v1/workspaces/{workspaceId}/pipelines/{slug}/budget", "WorkspacePipelineBudgetResponseV1", object(map[string]any{"slug": str(), "has_budget": boolean(), "monthly_budget_usd": number(), "month": str(), "spent_usd": number(), "pct_used": number(), "over_budget": boolean()}))
	add("/api/v1/workspaces/{workspaceId}/pipelines/budget-summary", "WorkspacePipelineBudgetSummaryResponseV1", object(map[string]any{"month": str(), "total_budget_usd": number(), "total_spent_usd": number(), "pipelines": array(anyObject())}))
	addList("/api/v1/workspaces/{workspaceId}/pipelines/pending", "WorkspacePendingRunsResponseV1", object(map[string]any{"id": str(), "pipeline_id": str(), "pipeline_slug": str(), "status": str(), "created_at": str()}))
	addList("/api/v1/workspaces/{workspaceId}/pipeline-webhooks", "WorkspacePipelineWebhooksResponseV1", object(map[string]any{"id": str(), "workspace_id": str(), "name": str(), "target_pipeline_slug": str(), "token": str(), "signing_secret_set": boolean(), "inputs_template": anyObject(), "enabled": boolean(), "rate_limit_per_min": integer(), "created_at": str(), "updated_at": str()}))
	addList("/api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/changes", "WorkspacePipelineRunChangesResponseV1", object(map[string]any{"path": str(), "status": str(), "additions": integer(), "deletions": integer()}))
	addList("/api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/files", "WorkspacePipelineRunFilesResponseV1", object(map[string]any{"path": str(), "size": integer(), "modified_at": str()}))

	return routes, schemas
}
