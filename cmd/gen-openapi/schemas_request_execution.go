package main

func requestExecutionProperties() map[string]any {
	return map[string]any{
		"retry_of_request_id": map[string]any{"type": "string", "maxLength": 128},
		"retry_warning":       map[string]any{"type": "string"},
		"execution_state":     map[string]any{"type": "string", "enum": []string{"observed", "awaiting_confirmation", "unknown", "deadline_exceeded"}},
		"heartbeat_at":        map[string]any{"type": "string", "format": "date-time", "nullable": true, "description": "Actual work attempt heartbeat only; null when no such evidence exists."},
		"last_activity_at":    map[string]any{"type": "string", "format": "date-time", "nullable": true},
		"deadline_at":         map[string]any{"type": "string", "format": "date-time", "nullable": true},
		"deadline_source":     map[string]any{"type": "string", "enum": []string{"recorded", "legacy_derived"}},
		"run_status":          map[string]any{"type": "string", "enum": []string{"COMPLETED", "FAILED", "CANCELLED", "TIMEOUT"}, "description": "Confirmed terminal run outcome. Omitted when unconfirmed; independent of the request result."},
	}
}

func pageRequestResultSchema() DomainSchema {
	return DomainSchema{Response: map[string]any{"type": "object", "properties": map[string]any{"page_slug": map[string]any{"type": "string"}, "revision": map[string]any{"type": "integer", "minimum": 1}}, "required": []string{"page_slug"}, "additionalProperties": false}}
}
