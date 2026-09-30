package main

// Schemas for Admin › Backups settings, backup keys, off-site destinations,
// incidents and the recovery sheet (internal/api/admin_instance_backup_settings.go,
// internal/backupplan). The shapes are the console's backups-model.ts types.

func backupLimitsSchema() map[string]any {
	return object(map[string]any{
		"concurrency": bpInteger(), "cpu_cores": bpInteger(), "disk_mbps": bpInteger(), "upload_mbps": bpInteger(),
	}, "concurrency", "cpu_cores", "disk_mbps", "upload_mbps")
}

func backupAlertEventsSchema() map[string]any {
	return object(map[string]any{
		"failed": bpBoolean(), "incomplete": bpBoolean(), "stale": bpBoolean(), "offsite": bpBoolean(), "drill": bpBoolean(),
	}, "failed", "incomplete", "stale", "offsite", "drill")
}

func backupSettingsSchema() map[string]any {
	return object(map[string]any{
		"limits": backupLimitsSchema(), "heartbeat_url": bpNullable(bpStr()), "heartbeat_last_at": bpNullable(bpStr()),
		"heartbeat_last_ok": bpNullable(bpBoolean()), "heartbeat_last_error": bpNullable(bpStr()),
		"recovery_kit_enabled": bpBoolean(), "channels": bpStringArray(), "events": backupAlertEventsSchema(),
		"stale_alert_hours": bpInteger(), "drill_reminder": bpStr(), "updated_at": bpNullable(bpStr()),
		"destinations": array(object(map[string]any{
			"id": bpStr(), "kind": bpStr(), "label": bpStr(), "path": bpNullable(bpStr()), "used_bytes": bpNullable(bpInteger()),
			"verified": bpBoolean(), "available": bpBoolean(),
		}, "id", "kind", "label", "path", "used_bytes", "verified", "available")),
		"instance_admins": bpInteger(), "local_path": bpNullable(bpStr()),
	}, "limits", "heartbeat_url", "recovery_kit_enabled", "channels", "events", "stale_alert_hours", "drill_reminder",
		"destinations", "instance_admins", "local_path")
}

// backupSettingsRequest: every field optional; what is left out keeps its
// value. heartbeat_url null (or "") clears it.
func backupSettingsRequest() map[string]any {
	return object(map[string]any{
		"limits": object(map[string]any{
			"concurrency": bpInteger(), "cpu_cores": bpInteger(), "disk_mbps": bpInteger(), "upload_mbps": bpInteger(),
		}),
		"heartbeat_url": bpNullable(bpStr()), "recovery_kit_enabled": bpBoolean(), "channels": bpStringArray(),
		"events": object(map[string]any{
			"failed": bpBoolean(), "incomplete": bpBoolean(), "stale": bpBoolean(), "offsite": bpBoolean(), "drill": bpBoolean(),
		}),
		"stale_alert_hours": bpInteger(), "drill_reminder": bpStr(),
	})
}

func backupRecipientSchema() map[string]any {
	return object(map[string]any{
		"id": bpStr(), "name": bpStr(), "public_key": bpStr(), "holder": bpStr(), "fingerprint": bpStr(),
		"created_by": bpNullable(bpStr()), "created_at": bpStr(), "used_by": bpStringArray(),
	}, "id", "name", "public_key", "holder", "fingerprint", "created_by", "created_at", "used_by")
}

func backupDestinationSchema() map[string]any {
	return object(map[string]any{
		"id": bpStr(), "name": bpStr(), "kind": bpStr(), "endpoint": bpStr(), "region": bpStr(), "bucket": bpStr(), "prefix": bpStr(),
		"access_key_id": bpStr(), "path_style": bpBoolean(), "allow_private_network": bpBoolean(),
		"last_test_at": bpNullable(bpStr()), "last_test_error": bpNullable(bpStr()), "created_by": bpNullable(bpStr()),
		"created_at": bpStr(), "copies": bpInteger(), "copy_bytes": bpInteger(), "last_verified_at": bpNullable(bpStr()),
		"used_by": bpStringArray(),
	}, "id", "name", "kind", "endpoint", "region", "bucket", "prefix", "access_key_id", "path_style", "allow_private_network",
		"last_test_at", "last_test_error", "created_at", "copies", "copy_bytes", "last_verified_at", "used_by")
}

func backupDestinationTestSchema() map[string]any {
	return object(map[string]any{"ok": bpBoolean(), "error": bpNullable(bpStr()), "tested_at": bpStr()}, "ok", "error", "tested_at")
}

func backupIncidentSchema() map[string]any {
	return object(map[string]any{
		"id": bpStr(), "plan_id": bpNullable(bpStr()), "plan_name": bpNullable(bpStr()), "kind": bpStr(), "state": bpStr(),
		"count": bpInteger(), "first_at": bpStr(), "last_at": bpStr(), "resolved_at": bpNullable(bpStr()), "message": bpStr(),
		"run_id": bpNullable(bpStr()),
	}, "id", "plan_id", "plan_name", "kind", "state", "count", "first_at", "last_at", "resolved_at", "message", "run_id")
}

func backupSettingsSchemaCatalog() map[string]DomainSchema {
	return map[string]DomainSchema{
		"GET /api/v1/admin/instance/backups/settings": {Response: backupSettingsSchema()},
		"PUT /api/v1/admin/instance/backups/settings": {Request: backupSettingsRequest(), Response: backupSettingsSchema()},
		"GET /api/v1/admin/instance/backups/recipients": {Response: object(map[string]any{
			"data": array(backupRecipientSchema()),
		}, "data")},
		"POST /api/v1/admin/instance/backups/recipients": {
			SuccessStatuses: []string{"201"},
			Request: object(map[string]any{
				"name": bpStr(), "public_key": bpStr(), "holder": bpStr(),
			}, "name", "public_key"),
			Response: backupRecipientSchema(),
		},
		"DELETE /api/v1/admin/instance/backups/recipients/{id}": {SuccessStatuses: []string{"204"}},
		"GET /api/v1/admin/instance/backups/destinations": {Response: object(map[string]any{
			"data": array(backupDestinationSchema()),
		}, "data")},
		// The secret access key goes in once and is sealed with the vault
		// key; it is never returned. The connection is tested first unless
		// skip_test, and a failed test (422) stores nothing.
		"POST /api/v1/admin/instance/backups/destinations": {
			SuccessStatuses: []string{"201"},
			Request: object(map[string]any{
				"name": bpStr(), "kind": bpStr(), "endpoint": bpStr(), "region": bpStr(), "bucket": bpStr(), "prefix": bpStr(),
				"access_key_id": bpStr(), "secret_access_key": bpStr(), "path_style": bpBoolean(), "allow_private_network": bpBoolean(),
				"skip_test": bpBoolean(),
			}, "endpoint", "bucket", "access_key_id", "secret_access_key"),
			Response: object(map[string]any{
				"destination": backupDestinationSchema(), "test": bpNullable(backupDestinationTestSchema()), "warning": bpNullable(bpStr()),
			}, "destination", "test", "warning"),
		},
		"POST /api/v1/admin/instance/backups/destinations/{id}/test": {Request: ref("EmptyRequest"), Response: backupDestinationTestSchema()},
		"DELETE /api/v1/admin/instance/backups/destinations/{id}":    {SuccessStatuses: []string{"204"}},
		"GET /api/v1/admin/instance/backups/incidents": {Response: object(map[string]any{
			"data": array(backupIncidentSchema()),
		}, "data")},
		"GET /api/v1/admin/instance/backups/recovery-sheet": {Response: bpStr(), ResponseMedia: []string{"text/markdown"}},
		// Restore from an off-site copy (admin_instance_backup_copies.go).
		"GET /api/v1/admin/instance/backups/copies": {Response: object(map[string]any{
			"destination_id": bpStr(), "destination_name": bpStr(),
			"copies": array(object(map[string]any{
				"key": bpStr(), "size": bpInteger(), "modified": bpStr(), "scope": bpStr(), "workspace_id": bpNullable(bpStr()),
				"local": bpBoolean(), "local_path": bpNullable(bpStr()),
			}, "key", "size", "modified", "scope", "workspace_id", "local", "local_path")),
		}, "destination_id", "destination_name", "copies")},
		// 202 at once with the job; the download runs on the server.
		"POST /api/v1/admin/instance/backups/copies/fetch": {
			SuccessStatuses: []string{"202"},
			Request:         object(map[string]any{"destination_id": bpStr(), "key": bpStr()}, "destination_id", "key"),
			Response:        backupCopyFetchSchema(),
		},
		"GET /api/v1/admin/instance/backups/copies/fetch/{id}": {Response: backupCopyFetchSchema()},
	}
}

// backupCopyFetchSchema is one off-site fetch job: status running, done
// (path is the local bundle) or failed (error says why).
func backupCopyFetchSchema() map[string]any {
	return object(map[string]any{
		"id": bpStr(), "destination_id": bpStr(), "key": bpStr(), "status": bpStr(), "path": bpNullable(bpStr()),
		"size": bpInteger(), "layers": bpInteger(), "error": bpNullable(bpStr()), "started_at": bpStr(), "ended_at": bpNullable(bpStr()),
	}, "id", "destination_id", "key", "status", "path", "size", "layers", "error", "started_at", "ended_at")
}
