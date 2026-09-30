package main

// Schemas for Admin › Backups plans, runs and overview
// (internal/api/admin_instance_backup_plans.go, internal/backupplan). The
// shapes are the console's backups-model.ts types.

func bpStr() map[string]any         { return map[string]any{"type": "string"} }
func bpInteger() map[string]any     { return map[string]any{"type": "integer"} }
func bpBoolean() map[string]any     { return map[string]any{"type": "boolean"} }
func bpStringArray() map[string]any { return array(bpStr()) }
func bpNullable(schema map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range schema {
		out[k] = v
	}
	out["nullable"] = true
	return out
}

func backupPlanSchema() map[string]any {
	return object(map[string]any{
		"id": bpStr(), "name": bpStr(), "preset": bpStr(), "scope": bpStr(), "workspace_ids": bpStringArray(), "contents": bpStringArray(),
		"env_mode": bpStr(), "cadence": bpStr(), "time_of_day": bpStr(), "weekday": bpNullable(bpInteger()), "monthday": bpNullable(bpInteger()),
		"cron_expr": bpNullable(bpStr()), "timezone": bpStr(), "env_cadence": bpStr(), "keep_min": bpInteger(), "keep_daily": bpInteger(),
		"keep_weekly": bpInteger(), "keep_monthly": bpInteger(), "destinations": bpStringArray(), "recipient_ids": bpStringArray(),
		"busy_wait_minutes": bpInteger(), "busy_retry_minutes": bpInteger(), "hold_cap_minutes": bpInteger(), "stale_alert_hours": bpInteger(),
		"enabled": bpBoolean(), "next_run_at": bpNullable(bpStr()), "last_run_at": bpNullable(bpStr()), "counts_as_protection": bpBoolean(),
		"created_at": bpStr(), "updated_at": bpStr(),
	}, "id", "name", "preset", "scope", "workspace_ids", "contents", "env_mode", "cadence", "time_of_day", "weekday", "monthday",
		"cron_expr", "timezone", "env_cadence", "keep_min", "keep_daily", "keep_weekly", "keep_monthly", "destinations",
		"recipient_ids", "busy_wait_minutes", "busy_retry_minutes", "hold_cap_minutes", "stale_alert_hours", "enabled",
		"next_run_at", "last_run_at", "counts_as_protection")
}

// backupPlanRequest: every field optional; POST fills the rest from the
// preset's defaults, PUT keeps the stored value.
func backupPlanRequest() map[string]any {
	return object(map[string]any{
		"name": bpStr(), "preset": bpStr(), "scope": bpStr(), "workspace_ids": bpStringArray(), "contents": bpStringArray(),
		"env_mode": bpStr(), "cadence": bpStr(), "time_of_day": bpStr(), "weekday": bpNullable(bpInteger()), "monthday": bpNullable(bpInteger()),
		"cron_expr": bpNullable(bpStr()), "timezone": bpStr(), "env_cadence": bpStr(), "keep_min": bpInteger(), "keep_daily": bpInteger(),
		"keep_weekly": bpInteger(), "keep_monthly": bpInteger(), "destinations": bpStringArray(), "recipient_ids": bpStringArray(),
		"busy_wait_minutes": bpInteger(), "busy_retry_minutes": bpInteger(), "hold_cap_minutes": bpInteger(), "stale_alert_hours": bpInteger(),
		"enabled": bpBoolean(),
	})
}

func backupRunPhaseSchema() map[string]any {
	return object(map[string]any{
		"name": bpStr(), "started_at": bpNullable(bpStr()), "ended_at": bpNullable(bpStr()), "status": bpStr(), "detail": bpNullable(bpStr()),
	}, "name", "started_at", "ended_at", "status", "detail")
}

func backupRunSchema() map[string]any {
	return object(map[string]any{
		"id": bpStr(), "plan_id": bpNullable(bpStr()), "plan_name": bpNullable(bpStr()), "trigger": bpStr(), "note": bpNullable(bpStr()),
		"scope": bpStr(), "workspace_id": bpNullable(bpStr()), "workspace_name": bpNullable(bpStr()), "kind": bpStr(), "status": bpStr(),
		"retried_at": bpNullable(bpStr()), "phases": array(backupRunPhaseSchema()), "bundle_path": bpNullable(bpStr()),
		"size_bytes": bpNullable(bpInteger()), "error": bpNullable(bpStr()),
		"incomplete": array(object(map[string]any{
			"kind": bpStr(), "count": bpInteger(), "workspace_id": bpNullable(bpStr()), "detail": bpStr(),
		}, "kind", "count", "workspace_id", "detail")),
		"started_at": bpStr(), "ended_at": bpNullable(bpStr()), "proof_level": bpInteger(), "drill_result": bpNullable(bpStr()),
		"drill_at": bpNullable(bpStr()), "drill_note": bpNullable(bpStr()), "pinned": bpBoolean(), "recipients": bpStringArray(),
		"format_version": bpNullable(bpInteger()), "restorable": bpNullable(bpStr()), "legacy": bpBoolean(),
		"phase": bpStr(), "environments": bpBoolean(), "due_at": bpNullable(bpStr()), "retry_of": bpNullable(bpStr()),
		"categories": bpStringArray(), "catalog_id": bpNullable(bpStr()), "hold_ms": bpNullable(bpInteger()),
	}, "id", "plan_id", "plan_name", "trigger", "scope", "workspace_id", "workspace_name", "kind", "status", "phases",
		"bundle_path", "size_bytes", "incomplete", "started_at", "ended_at", "proof_level", "pinned", "recipients", "legacy")
}

func backupStatusRow() map[string]any {
	return object(map[string]any{"value": bpStr(), "detail": bpNullable(bpStr()), "detail_tone": bpNullable(bpStr())}, "value", "detail", "detail_tone")
}

func backupOverviewSchema() map[string]any {
	return object(map[string]any{
		"status": object(map[string]any{
			"label": bpStr(), "verdict": bpStr(), "summary": bpNullable(bpStr()), "protects": backupStatusRow(), "how_often": backupStatusRow(),
			"where": backupStatusRow(), "how_long": backupStatusRow(), "really_restored": backupStatusRow(), "offsite_verified": bpBoolean(),
		}, "label", "verdict", "summary", "protects", "how_often", "where", "how_long", "really_restored", "offsite_verified"),
		"needs_attention": array(object(map[string]any{
			"id": bpStr(), "severity": bpStr(), "title": bpStr(), "detail": bpStr(),
			"action": bpNullable(object(map[string]any{
				"kind": bpStr(), "label": bpStr(), "workspace_id": bpNullable(bpStr()), "run_id": bpNullable(bpStr()),
			}, "kind", "label", "workspace_id", "run_id")),
		}, "id", "severity", "title", "detail", "action")),
		"nights": array(object(map[string]any{"date": bpStr(), "status": bpStr(), "proof": bpInteger(), "detail": bpNullable(bpStr())},
			"date", "status", "proof", "detail")),
		"space": object(map[string]any{
			"backups_bytes": bpInteger(), "free_bytes": bpInteger(), "total_bytes": bpInteger(), "staging_need_bytes": bpInteger(),
			"restore_need_bytes": bpInteger(),
		}, "backups_bytes", "free_bytes", "total_bytes", "staging_need_bytes", "restore_need_bytes"),
		"workspaces": array(object(map[string]any{
			"workspace_id": bpStr(), "name": bpStr(), "last_backup_at": bpNullable(bpStr()), "status": bpStr(), "plan": bpNullable(bpStr()), "proof": bpInteger(),
		}, "workspace_id", "name", "last_backup_at", "status", "plan", "proof")),
		"instance_summary": bpNullable(bpStr()),
	}, "status", "needs_attention", "nights", "space", "instance_summary")
}

func backupPlanSchemaCatalog() map[string]DomainSchema {
	return map[string]DomainSchema{
		"GET /api/v1/admin/instance/backups/plans":         {Response: object(map[string]any{"data": array(backupPlanSchema())}, "data")},
		"POST /api/v1/admin/instance/backups/plans":        {Request: backupPlanRequest(), Response: backupPlanSchema()},
		"DELETE /api/v1/admin/instance/backups/plans/{id}": {SuccessStatuses: []string{"204"}},
		"GET /api/v1/admin/instance/backups/plans/{id}":    {Response: backupPlanSchema()},
		"PUT /api/v1/admin/instance/backups/plans/{id}":    {Request: backupPlanRequest(), Response: backupPlanSchema()},
		"GET /api/v1/admin/instance/backups/plans/{id}/next": {Response: object(map[string]any{
			"runs": array(object(map[string]any{"at": bpStr(), "environments": bpBoolean()}, "at", "environments")),
		}, "runs")},
		"GET /api/v1/admin/instance/backups/plans/{id}/calendar": {Response: object(map[string]any{
			"days": array(object(map[string]any{
				"date":    bpStr(),
				"entries": array(object(map[string]any{"at": bpStr(), "kind": bpStr(), "status": bpStr()}, "at", "kind", "status")),
			}, "date", "entries")),
		}, "days")},
		"POST /api/v1/admin/instance/backups/plans/preview-contents": {
			Request: object(map[string]any{"preset": bpStr(), "contents": bpStringArray(), "env_mode": bpStr()}, "preset"),
			Response: object(map[string]any{
				"included": bpStringArray(),
				"required": array(object(map[string]any{"key": bpStr(), "because": bpStringArray()}, "key", "because")),
				"excluded": bpStringArray(),
			}, "included", "required", "excluded")},
		"GET /api/v1/admin/instance/backups/runs": {Response: object(map[string]any{"data": array(backupRunSchema())}, "data")},
		// One run system: scheduled, catch-up and manual runs of every scope
		// are backup_runs rows. POST answers 202 at once (one run per
		// workspace for scope=workspaces); recipients is another name for
		// recipient_ids; a passphrase is held in memory until the run starts.
		"POST /api/v1/admin/instance/backups/run": {
			SuccessStatuses: []string{"202"},
			Request: object(map[string]any{
				"plan_id": bpStr(), "scope": bpStr(), "workspace_ids": bpStringArray(), "preset": bpStr(), "contents": bpStringArray(),
				"env_mode": bpStr(), "recipient_ids": bpStringArray(), "recipients": bpStringArray(), "passphrase": bpStr(), "note": bpStr(),
			}),
			Response: object(map[string]any{"id": bpStr(), "run_id": bpStr(), "run_ids": bpStringArray(), "status": bpStr()}, "id", "run_id", "run_ids", "status")},
		"GET /api/v1/admin/instance/backups/run/{runId}": {Response: backupRunSchema()},
		"GET /api/v1/admin/instance/backups/overview":    {Response: backupOverviewSchema()},
	}
}
