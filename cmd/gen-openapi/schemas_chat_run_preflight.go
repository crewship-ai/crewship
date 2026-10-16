package main

func chatRunPreflightSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	num := func() map[string]any { return map[string]any{"type": "number"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	budget := obj(map[string]any{"id": str(), "scope": str(), "window": str(), "mode": str(), "limit_usd": num(), "spent_usd": num(), "remaining_usd": num(), "state": str()}, "id", "scope", "window", "mode", "limit_usd", "spent_usd", "remaining_usd", "state")
	payer := obj(map[string]any{"state": str(), "credential_name": str(), "credential_id": str(), "source": str(), "delivery": str(), "provider": str()}, "state")
	gate := obj(map[string]any{"configured_mode": str(), "predicted_mode": str(), "rule_matches": boolean(), "blocking": boolean(), "reason": str()}, "configured_mode", "predicted_mode", "rule_matches", "blocking", "reason")
	body := obj(map[string]any{"scope": map[string]any{"type": "string", "enum": []string{"configured_chat_agent"}}, "agent_id": str(), "model": str(), "payer": payer, "budgets": map[string]any{"type": "array", "items": budget}, "gate": gate, "estimate_usd": map[string]any{"type": "number", "nullable": true}, "estimate_state": map[string]any{"type": "string", "enum": []string{"unknown"}}, "constraints": map[string]any{"type": "array", "items": str()}}, "scope", "agent_id", "model", "payer", "budgets", "gate", "estimate_usd", "estimate_state", "constraints")
	route := DomainSchema{Response: map[string]any{"$ref": "#/components/schemas/ChatRunPreflight"}, SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"404", "503"}, Parameters: []map[string]any{{"name": "chatId", "in": "path", "required": true, "schema": str()}}, SuccessHeaders: map[string]any{"Cache-Control": map[string]any{"schema": str(), "description": "no-store; authorization-sensitive advisory snapshot, neither reservation nor approval"}}}
	return map[string]DomainSchema{"GET /api/v1/chats/{chatId}/run-preflight": route}, map[string]any{"ChatRunPreflight": body}
}
