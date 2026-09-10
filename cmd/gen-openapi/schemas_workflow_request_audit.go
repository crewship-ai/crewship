package main

// workflowRequestSchemaCatalog contains request bodies audited against the
// public workflow handlers and their request-focused tests. Keep this catalog
// separate from response/domain catalogs: these payloads are intentionally
// keyed by route, while their named components make the generated contract
// readable and reusable.
func workflowRequestSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	number := func() map[string]any { return map[string]any{"type": "number"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	anyObject := func() map[string]any { return map[string]any{"type": "object", "additionalProperties": true} }
	anyValue := func() map[string]any { return map[string]any{} }
	arr := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	obj := func(properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": properties}
	}
	nullable := func(schema map[string]any) map[string]any {
		copy := map[string]any{}
		for k, v := range schema {
			copy[k] = v
		}
		copy["nullable"] = true
		return copy
	}
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	empty := obj(map[string]any{})

	draft := obj(map[string]any{
		"id": str(), "workspace_id": str(), "slug": str(), "revision": integer(),
		"base_pipeline_id": str(), "base_revision": integer(), "document": anyObject(),
		"updated_by": str(), "created_at": str(), "updated_at": str(),
	})
	draft["required"] = []string{"id", "workspace_id", "slug", "revision", "base_pipeline_id", "base_revision", "document", "updated_by", "created_at", "updated_at"}
	draftSave := obj(map[string]any{
		"id": str(), "slug": str(), "revision": integer(), "base_pipeline_id": str(),
		"base_revision": integer(), "document": anyObject(),
	})
	draftSave["required"] = []string{"slug", "document"}
	draftRevision := obj(map[string]any{"id": str(), "revision": integer()})
	draftRevision["required"] = []string{"id", "revision"}
	publication := obj(map[string]any{"id": str(), "revision": integer(), "save_token": str(), "approve_risk": boolean()})
	publication["required"] = []string{"id", "revision", "save_token"}

	pipelineRun := obj(map[string]any{
		"inputs": anyObject(), "tier_override": str(), "triggered_via": str(), "triggered_by_id": str(),
		"tags": arr(str()), "metadata": anyObject(), "delay_seconds": integer(), "ttl_seconds": integer(),
		"debounce_key": str(), "debounce_window_seconds": integer(), "debounce_max_seconds": integer(),
		"priority": integer(), "idempotency_key_ttl_seconds": integer(),
	})
	batchItem := obj(map[string]any{"inputs": anyObject(), "tags": arr(str()), "metadata": anyObject()})
	batch := obj(map[string]any{"items": arr(batchItem), "tags": arr(str()), "tier_override": str()})
	testRun := obj(map[string]any{"definition": anyValue(), "author_crew_id": str(), "sample_inputs": anyObject()})
	stepRun := obj(map[string]any{"step_id": str(), "inputs": anyObject(), "step_outputs": map[string]any{"type": "object", "additionalProperties": str()}, "tier_override": str()})
	// routineTrigger is B8's atomic-authoring extension (#2359): an optional
	// trigger created in the SAME transaction as the routine + version.
	// Only "schedule" and "manual" are supported today.
	routineTrigger := obj(map[string]any{
		"kind": str(), "cron": str(), "timezone": str(), "catchup_policy": str(),
		"max_consecutive_failures": integer(), "inputs": anyObject(),
	})
	save := obj(map[string]any{
		"slug": str(), "name": str(), "description": str(), "definition": anyValue(), "author_crew_id": str(),
		"author_agent_id":  str(),
		"last_test_run_at": str(), "last_test_run_passed": boolean(), "skip_test_gate": boolean(),
		"skip_governance_gate": boolean(), "save_token": str(), "change_summary": str(),
		"trigger": routineTrigger, "activation": str(),
	})
	importBody := obj(map[string]any{
		"format": str(), "pipeline": obj(map[string]any{"name": str(), "description": str(), "slug": str(), "dsl_version": str(), "definition": anyValue()}),
		"metadata": anyObject(), "author_crew_id": str(),
	})
	schedule := obj(map[string]any{
		"name": str(), "target_pipeline_slug": str(), "target_pipeline_id": str(), "target_pipeline_version": integer(),
		"cron_expr": str(), "timezone": str(), "inputs": anyObject(), "enabled": boolean(),
		"wake_pipeline_slug": nullable(str()), "wake_pipeline_id": nullable(str()), "wake_inputs": anyObject(),
		"wake_fail_closed": boolean(), "catchup_policy": str(), "max_consecutive_failures": integer(),
	})
	replay := obj(map[string]any{"pinned_version": integer()})
	metadata := obj(map[string]any{"set": anyObject(), "increment": anyObject(), "append": anyObject()})
	signal := obj(map[string]any{"event_type": str(), "payload": str()})
	checkpoint := obj(map[string]any{"label": str()})
	missionCreate := obj(map[string]any{"title": str(), "description": nullable(str()), "lead_agent_id": str(), "workflow_template": nullable(str())})
	missionUpdate := obj(map[string]any{"status": nullable(str()), "title": nullable(str()), "description": nullable(str()), "plan": nullable(str())})
	taskCreate := obj(map[string]any{"title": str(), "description": nullable(str()), "assigned_agent_id": nullable(str()), "task_order": integer(), "depends_on": arr(str()), "max_iterations": nullable(integer())})
	taskUpdate := obj(map[string]any{"status": nullable(str()), "title": nullable(str()), "description": nullable(str()), "depends_on": nullable(str()), "assigned_agent_id": nullable(str()), "result_summary": nullable(str()), "error_message": nullable(str()), "output_path": nullable(str()), "token_count": nullable(integer()), "estimated_cost": nullable(number()), "max_iterations": nullable(integer())})
	recurringCreate := obj(map[string]any{"crew_id": str(), "title": str(), "description": nullable(str()), "priority": str(), "project_id": nullable(str()), "milestone_id": nullable(str()), "assignee_type": nullable(str()), "assignee_id": nullable(str()), "labels_json": nullable(str()), "cron_expression": str()})
	recurringUpdate := obj(map[string]any{"crew_id": nullable(str()), "title": nullable(str()), "description": nullable(str()), "priority": nullable(str()), "project_id": nullable(str()), "milestone_id": nullable(str()), "assignee_type": nullable(str()), "assignee_id": nullable(str()), "labels_json": nullable(str()), "cron_expression": nullable(str()), "enabled": nullable(boolean())})
	triageCreate := obj(map[string]any{"name": str(), "pattern": str(), "match_type": str(), "crew_id": nullable(str()), "assignee_id": nullable(str()), "priority": nullable(str()), "project_id": nullable(str()), "labels_json": nullable(str())})
	triageUpdate := obj(map[string]any{"name": nullable(str()), "pattern": nullable(str()), "match_type": nullable(str()), "crew_id": nullable(str()), "assignee_id": nullable(str()), "priority": nullable(str()), "project_id": nullable(str()), "labels_json": nullable(str()), "position": nullable(integer()), "enabled": nullable(boolean())})
	milestoneCreate := obj(map[string]any{"name": str(), "description": nullable(str()), "target_date": nullable(str()), "status": str()})
	milestoneUpdate := obj(map[string]any{"name": nullable(str()), "description": nullable(str()), "target_date": nullable(str()), "status": nullable(str()), "position": nullable(integer())})
	escalationResolve := obj(map[string]any{"resolution": str(), "action": str(), "redirect_to": str()})
	// Supplying is the one route a human-typed secret takes (#2376): the
	// value, plus name/type/tier for a free-text ask that staged no credential.
	escalationSupply := obj(map[string]any{"value": str(), "name": str(), "type": str(), "security_level": integer()})
	// Cancelling is not resolving with a different verb: it withdraws the
	// question rather than answering it, so it carries only a reason.
	escalationCancel := obj(map[string]any{"reason": str()})
	// Legacy decisions default approved to false. Typed decisions also carry
	// that flag, so action_id/data presence must distinguish the variants.
	legacyDecision := obj(map[string]any{"approved": boolean(), "comment": str()})
	legacyDecision["title"] = "Legacy decision"
	legacyDecision["not"] = map[string]any{"anyOf": []any{
		map[string]any{"required": []string{"action_id"}},
		map[string]any{"required": []string{"data"}},
	}}
	typedDecision := obj(map[string]any{
		"approved": boolean(), "comment": str(), "data": anyObject(),
		"action_id": map[string]any{"type": "string", "minLength": 1},
	})
	typedDecision["title"] = "Typed decision"
	typedDecision["required"] = []string{"action_id"}
	waitpoint := map[string]any{"oneOf": []any{legacyDecision, typedDecision}}

	publishResponse := obj(map[string]any{"head_version": integer(), "last_recorded_run_id": str(), "last_run_outcome": str(), "id": str(), "slug": str(), "name": str(), "description": str(), "dsl_version": str(), "definition_hash": str(), "ephemeral": boolean(), "workspace_visible": boolean(), "invocation_count": integer(), "last_invoked_at": str(), "last_invocation_status": str(), "author_crew_id": str(), "author_agent_id": str(), "author_agent_name": str(), "author_user_id": str(), "authored_via": str(), "status": str(), "icon": str(), "color": str(), "risk_reasons": arr(str()), "inbox_item_id": str(), "created_at": str(), "updated_at": str(), "linked_issue_count": integer(), "linked_issues": arr(str()), "integrations_required": arr(str()), "manifest": anyObject(), "definition": anyObject(), "trigger": anyObject()})
	publishResponse["required"] = []string{"id", "slug", "name", "dsl_version", "definition_hash", "ephemeral", "workspace_visible", "invocation_count", "authored_via", "status", "created_at", "updated_at", "linked_issue_count"}
	draftListEntry := obj(map[string]any{"slug": str(), "revision": integer(), "updated_at": str()})
	draftListEntry["required"] = []string{"slug", "revision", "updated_at"}
	fixtureTest := obj(map[string]any{"definition": anyObject(), "step_id": str(), "inputs": anyObject(), "step_outputs": map[string]any{"type": "object", "additionalProperties": str()}, "fixture_output": str(), "env": map[string]any{"type": "object", "additionalProperties": str()}, "metadata": anyObject(), "secrets": map[string]any{"type": "object", "additionalProperties": str()}})
	fixtureTest["required"] = []string{"definition", "step_id"}
	fixtureResult := obj(map[string]any{"execution_mode": str(), "step_id": str(), "step_type": str(), "definition_hash": str(), "fixture_hash": str(), "output_source": str(), "output": str(), "valid": boolean(), "validation_declared": boolean(), "validation_reason": str(), "limitations": arr(str())})
	fixtureResult["required"] = []string{"execution_mode", "step_id", "step_type", "definition_hash", "fixture_hash", "output_source", "output", "valid", "validation_declared", "limitations"}
	components := map[string]any{
		"WorkflowFixtureTestRequest":  fixtureTest,
		"RoutineFixtureResult":        fixtureResult,
		"RoutineDraft":                draft,
		"RoutineDraftSaveRequest":     draftSave,
		"RoutineDraftRevisionRequest": draftRevision,
		"RoutineDraftList":            arr(draftListEntry),
		"RoutinePublishRequest":       publication,
		"RoutinePublishResponse":      publishResponse,
		"WorkflowPipelineRunRequest":  pipelineRun, "WorkflowBatchRunRequest": batch, "WorkflowTestRunRequest": testRun,
		"WorkflowStepRunRequest": stepRun, "WorkflowPipelineSaveRequest": save, "WorkflowPipelineImportRequest": importBody,
		"WorkflowScheduleRequest": schedule, "WorkflowReplayRequest": replay, "WorkflowRunMetadataRequest": metadata,
		"WorkflowSignalRequest": signal, "WorkflowCheckpointRequest": checkpoint, "WorkflowMissionCreateRequest": missionCreate,
		"WorkflowMissionUpdateRequest": missionUpdate, "WorkflowTaskCreateRequest": taskCreate, "WorkflowTaskUpdateRequest": taskUpdate,
		"WorkflowRecurringIssueCreateRequest": recurringCreate, "WorkflowRecurringIssueUpdateRequest": recurringUpdate,
		"WorkflowTriageRuleCreateRequest": triageCreate, "WorkflowTriageRuleUpdateRequest": triageUpdate,
		"WorkflowMilestoneCreateRequest": milestoneCreate, "WorkflowMilestoneUpdateRequest": milestoneUpdate,
		"WorkflowEscalationResolveRequest": escalationResolve, "WorkflowEscalationCancelRequest": escalationCancel,
		"WorkflowEscalationSupplyRequest":  escalationSupply,
		"WorkflowWaitpointApprovalRequest": waitpoint,
		"WorkflowEmptyRequest":             empty, "WorkflowBudgetRequest": obj(map[string]any{"monthly_budget_usd": number()}),
		"WorkflowTagsRequest": obj(map[string]any{"tags": arr(str())}), "WorkflowStepOverrideRequest": obj(map[string]any{"prompt": str(), "model_override": str()}),
		"WorkflowRollbackRequest":          obj(map[string]any{"target_version": integer(), "version": integer()}),
		"WorkflowWaitpointCallbackRequest": obj(map[string]any{"approved": nullable(boolean()), "payload": anyValue()}),
	}
	request := func(name string) DomainSchema { return DomainSchema{Request: ref(name)} }
	routes := map[string]DomainSchema{
		"GET /api/v1/workspaces/{workspaceId}/pipelines/drafts":          {Response: ref("RoutineDraftList")},
		"POST /api/v1/workspaces/{workspaceId}/pipelines/drafts":         {Request: ref("RoutineDraftSaveRequest"), Response: ref("RoutineDraft")},
		"GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/draft":    {Response: ref("RoutineDraft")},
		"DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}/draft": {Request: ref("RoutineDraftRevisionRequest"), SuccessStatuses: []string{"204"}},
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/publish": {Request: ref("RoutinePublishRequest"), Response: ref("RoutinePublishResponse"), SuccessStatuses: []string{"201"}},
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run":     request("WorkflowPipelineRunRequest"), "POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run_batch": request("WorkflowBatchRunRequest"),
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/dry_run": request("WorkflowPipelineRunRequest"), "POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/step_run": request("WorkflowStepRunRequest"),
		"POST /api/v1/workspaces/{workspaceId}/pipelines/fixture_test": {Request: ref("WorkflowFixtureTestRequest"), RequestRequired: true, Response: ref("RoutineFixtureResult"), SuccessStatuses: []string{"200"}},
		"POST /api/v1/workspaces/{workspaceId}/pipelines/test_run":     request("WorkflowTestRunRequest"), "POST /api/v1/workspaces/{workspaceId}/pipelines/save": request("WorkflowPipelineSaveRequest"), "POST /api/v1/workspaces/{workspaceId}/pipelines/import": request("WorkflowPipelineImportRequest"),
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/rollback": request("WorkflowRollbackRequest"), "PATCH /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/metadata": request("WorkflowRunMetadataRequest"), "POST /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/signal": request("WorkflowSignalRequest"),
		"POST /api/v1/workspaces/{workspaceId}/pipeline-schedules/{scheduleId}/run": request("WorkflowEmptyRequest"), "POST /api/v1/workspaces/{workspaceId}/pipeline-schedules": request("WorkflowScheduleRequest"), "PATCH /api/v1/workspaces/{workspaceId}/pipeline-schedules/{scheduleId}": request("WorkflowScheduleRequest"),
		// B8 (#2359): activate a draft trigger — no request body.
		"POST /api/v1/workspaces/{workspaceId}/pipeline-schedules/{scheduleId}/activate": request("WorkflowEmptyRequest"),
		"PUT /api/v1/workspaces/{workspaceId}/pipelines/{slug}/tags":                     request("WorkflowTagsRequest"), "PUT /api/v1/workspaces/{workspaceId}/pipelines/{slug}/steps/{stepId}/override": request("WorkflowStepOverrideRequest"), "PATCH /api/v1/workspaces/{workspaceId}/pipelines/{slug}/budget": request("WorkflowBudgetRequest"),
		"POST /api/v1/workspaces/{workspaceId}/pipelines/runs/{runId}/replay": request("WorkflowReplayRequest"), "POST /api/v1/workspaces/{workspaceId}/pipelines/waitpoints/{token}/approve": request("WorkflowWaitpointApprovalRequest"),
		"POST /api/v1/crews/{crewId}/missions": request("WorkflowMissionCreateRequest"), "PATCH /api/v1/crews/{crewId}/missions/{missionId}": request("WorkflowMissionUpdateRequest"), "POST /api/v1/crews/{crewId}/missions/{missionId}/tasks": request("WorkflowTaskCreateRequest"), "PATCH /api/v1/crews/{crewId}/missions/{missionId}/tasks/{taskId}": request("WorkflowTaskUpdateRequest"),
		"POST /api/v1/missions/{missionId}/checkpoints": request("WorkflowCheckpointRequest"), "POST /api/v1/checkpoints/{id}/fork": request("WorkflowCheckpointRequest"), "POST /api/v1/checkpoints/{id}/restore": request("WorkflowEmptyRequest"),
		"POST /api/v1/recurring-issues": request("WorkflowRecurringIssueCreateRequest"), "PATCH /api/v1/recurring-issues/{recurringId}": request("WorkflowRecurringIssueUpdateRequest"),
		"POST /api/v1/triage-rules": request("WorkflowTriageRuleCreateRequest"), "PATCH /api/v1/triage-rules/{ruleId}": request("WorkflowTriageRuleUpdateRequest"), "POST /api/v1/triage/process": request("WorkflowEmptyRequest"),
		"POST /api/v1/projects/{projectId}/milestones": request("WorkflowMilestoneCreateRequest"), "PATCH /api/v1/milestones/{milestoneId}": request("WorkflowMilestoneUpdateRequest"),
		"PATCH /api/v1/escalations/{escalationId}/resolve":       request("WorkflowEscalationResolveRequest"),
		"POST /api/v1/escalations/{escalationId}/supply":         request("WorkflowEscalationSupplyRequest"),
		"POST /api/v1/escalations/{escalationId}/cancel":         request("WorkflowEscalationCancelRequest"),
		"POST /api/v1/escalations/sweep-expired":                 request("WorkflowEmptyRequest"),
		"POST /api/v1/waitpoint-tokens/{token}":                  request("WorkflowWaitpointCallbackRequest"),
		"POST /api/v1/crews/{crewId}/missions/{missionId}/start": request("WorkflowEmptyRequest"), "POST /api/v1/crews/{crewId}/missions/{missionId}/restart": request("WorkflowEmptyRequest"), "POST /api/v1/crews/{crewId}/missions/{missionId}/resume": request("WorkflowEmptyRequest"), "POST /api/v1/crews/{crewId}/missions/{missionId}/clone": request("WorkflowEmptyRequest"),
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/approve": request("WorkflowEmptyRequest"), "POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/reject": request("WorkflowEmptyRequest"), "POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/disable": request("WorkflowEmptyRequest"), "POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/enable": request("WorkflowEmptyRequest"),
		"POST /api/v1/workspaces/{workspaceId}/pipelines/pending/{pendingId}/cancel": request("WorkflowEmptyRequest"), "POST /api/v1/workspaces/{workspaceId}/pipelines/runs/{runId}/cancel": request("WorkflowEmptyRequest"),
	}
	return routes, components
}
