package main

// remainingAuthIntegrationsSchemaCatalog contains the last handler-audited
// contracts for identity-adjacent and user preference surfaces. It is kept in
// its own module so later domain work cannot accidentally weaken these shapes.
func remainingAuthIntegrationsSchemaCatalog() (map[string]DomainSchema, map[string]any) {
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
	object := func(p map[string]any) map[string]any { return map[string]any{"type": "object", "properties": p} }
	array := func(i map[string]any) map[string]any { return map[string]any{"type": "array", "items": i} }
	anyObject := func() map[string]any { return map[string]any{"type": "object", "additionalProperties": true} }
	ref := func(n string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + n} }
	request := func(p map[string]any, required ...string) map[string]any {
		s := object(p)
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}

	status := object(map[string]any{"status": str()})
	label := object(map[string]any{"id": str(), "name": str(), "color": str(), "label_group": nullable(str())})
	feedback := object(map[string]any{
		"id": str(), "message_id": str(), "chat_id": nullable(str()), "trace_id": nullable(str()),
		"signal": map[string]any{"type": "string", "enum": []string{"helpful", "not_helpful", "inaccurate", "unsafe", "edit", "regenerate"}},
		"reason": nullable(str()), "user_id": nullable(str()), "created_at": str(),
	})
	hook := object(map[string]any{
		"id": str(), "workspace_id": str(), "crew_id": nullable(str()), "event": str(), "handler_kind": str(),
		"handler_config": anyObject(), "matcher": anyObject(), "enabled": boolean(), "blocking": boolean(),
		"created_by": nullable(str()), "created_at": str(), "updated_at": str(),
	})
	savedView := object(map[string]any{
		"id": str(), "name": str(), "filters_json": str(), "sort_json": nullable(str()), "view_type": str(),
		"is_default": boolean(), "shared": boolean(), "created_at": str(),
	})
	session := object(map[string]any{"id": str(), "created_at": str(), "last_used_at": str(), "user_agent": str(), "ip": str(), "is_current": boolean()})
	credentialAudit := object(map[string]any{"id": str(), "event_type": str(), "agent_id": nullable(str()), "ip_address": nullable(str()), "metadata": anyObject(), "occurred_at": str()})
	credentialBinding := object(map[string]any{"id": str(), "credential_id": str(), "credential_name": str(), "scope": str(), "crew_id": nullable(str()), "agent_id": nullable(str()), "slot": str(), "created_at": str()})
	field := object(map[string]any{"key": str(), "is_secret": boolean(), "ordinal": integer(), "value": nullable(str()), "created_at": str(), "updated_at": str()})
	pair := object(map[string]any{"code": str(), "expires_at": str()})
	pairPoll := object(map[string]any{"status": str(), "adapter_hint": nullable(str()), "expires_at": str()})

	components := map[string]any{
		"RemainingLabel": label, "RemainingFeedback": feedback,
		"RemainingHook": hook, "RemainingSavedView": savedView, "RemainingSession": session,
		"RemainingCredentialAudit": credentialAudit, "RemainingCredentialBinding": credentialBinding,
		"RemainingCredentialField": field, "RemainingPairStart": pair, "RemainingPairPoll": pairPoll,
		"RemainingFeedbackCreateRequest":        request(map[string]any{"message_id": str(), "chat_id": str(), "trace_id": str(), "signal": feedback["properties"].(map[string]any)["signal"], "reason": str()}, "message_id", "signal"),
		"RemainingLabelCreateRequest":           request(map[string]any{"name": str(), "color": str(), "label_group": nullable(str())}, "name", "color"),
		"RemainingLabelUpdateRequest":           object(map[string]any{"name": nullable(str()), "color": nullable(str()), "label_group": nullable(str())}),
		"RemainingSavedViewCreateRequest":       request(map[string]any{"name": str(), "filters_json": str(), "sort_json": nullable(str()), "view_type": str(), "shared": boolean()}, "name", "filters_json"),
		"RemainingSavedViewUpdateRequest":       object(map[string]any{"name": nullable(str()), "filters_json": nullable(str()), "sort_json": nullable(str()), "view_type": nullable(str()), "is_default": nullable(boolean()), "shared": nullable(boolean())}),
		"RemainingPairStartRequest":             object(map[string]any{"adapter_hint": str()}),
		"RemainingPairRedeemRequest":            request(map[string]any{"code": str(), "adapter_hint": str()}, "code"),
		"RemainingPairRedeemResponse":           object(map[string]any{"cli_token": str(), "user_id": str(), "email": str()}),
		"RemainingCredentialFieldRequest":       request(map[string]any{"key": str(), "label": str(), "value": str(), "secret": boolean()}, "key", "label", "value"),
		"RemainingCredentialSensitivityRequest": request(map[string]any{"sensitivity": str()}, "sensitivity"),
		"RemainingRevealPolicyRequest":          object(map[string]any{"enabled": nullable(boolean())}),
	}

	routes := map[string]DomainSchema{
		"GET /api/v1/labels":                                          {Response: array(ref("RemainingLabel"))},
		"POST /api/v1/labels":                                         {Request: ref("RemainingLabelCreateRequest"), Response: ref("RemainingLabel")},
		"PATCH /api/v1/labels/{labelId}":                              {Request: ref("RemainingLabelUpdateRequest"), Response: ref("RemainingLabel")},
		"DELETE /api/v1/labels/{labelId}":                             {Response: map[string]any{"type": "object", "properties": map[string]any{}}},
		"POST /api/v1/feedback":                                       {Request: ref("RemainingFeedbackCreateRequest"), Response: object(map[string]any{"id": str()})},
		"GET /api/v1/feedback":                                        {Response: object(map[string]any{"feedback": array(ref("RemainingFeedback"))})},
		"DELETE /api/v1/feedback":                                     {Response: map[string]any{"type": "object", "properties": map[string]any{}}},
		"GET /api/v1/hooks":                                           {Response: object(map[string]any{"rows": array(ref("RemainingHook")), "count": integer()})},
		"POST /api/v1/hooks/{id}/enable":                              {Response: object(map[string]any{"id": str(), "enabled": boolean()})},
		"POST /api/v1/hooks/{id}/disable":                             {Response: object(map[string]any{"id": str(), "enabled": boolean()})},
		"GET /api/v1/saved-views":                                     {Response: array(ref("RemainingSavedView"))},
		"POST /api/v1/saved-views":                                    {Request: ref("RemainingSavedViewCreateRequest"), Response: ref("RemainingSavedView")},
		"PATCH /api/v1/saved-views/{viewId}":                          {Request: ref("RemainingSavedViewUpdateRequest"), Response: ref("RemainingSavedView")},
		"DELETE /api/v1/saved-views/{viewId}":                         {Response: map[string]any{"type": "object", "properties": map[string]any{}}},
		"GET /api/v1/auth/sessions":                                   {Response: array(ref("RemainingSession"))},
		"POST /api/v1/auth/sessions/{id}/revoke":                      {Response: status},
		"POST /api/v1/auth/pair/start":                                {Request: ref("RemainingPairStartRequest"), Response: ref("RemainingPairStart")},
		"GET /api/v1/auth/pair/poll":                                  {Response: ref("RemainingPairPoll")},
		"POST /api/v1/auth/pair/redeem":                               {Request: ref("RemainingPairRedeemRequest"), Response: ref("RemainingPairRedeemResponse")},
		"GET /api/v1/users/me/peer-consent":                           {Response: object(map[string]any{"user_id": str(), "workspace_id": str(), "opted_out": boolean(), "opted_out_at": nullable(str())})},
		"PUT /api/v1/users/me/peer-consent":                           {Request: request(map[string]any{"opted_out": boolean()}, "opted_out"), Response: anyObject()},
		"GET /api/v1/users/me/peer-cards":                             {Response: object(map[string]any{"user_id": str(), "peers": array(anyObject())})},
		"DELETE /api/v1/users/me/peer-cards":                          {Response: object(map[string]any{"user_id": str(), "purged": integer()})},
		"GET /api/v1/users/me/user-model":                             {Response: anyObject()},
		"DELETE /api/v1/users/me/user-model":                          {Response: object(map[string]any{"user_id": str(), "purged": integer()})},
		"DELETE /api/v1/users/me/user-model/facts/{key}":              {Response: object(map[string]any{"user_id": str(), "forgot": str(), "exists": boolean(), "remaining": array(anyObject())})},
		"GET /api/v1/credentials/{credentialId}/audit":                {Response: array(ref("RemainingCredentialAudit"))},
		"GET /api/v1/credentials/{credentialId}/rotations":            {Response: array(ref("CredentialRotation"))},
		"DELETE /api/v1/credential-rotations/{rotationId}":            {Response: status},
		"GET /api/v1/agents/{agentId}/credential-bindings":            {Response: object(map[string]any{"bindings": array(ref("RemainingCredentialBinding"))})},
		"DELETE /api/v1/credentials/bindings/{bindingId}":             {Response: status},
		"PUT /api/v1/credentials/{credentialId}/fields/{fieldKey}":    {Request: ref("RemainingCredentialFieldRequest"), Response: ref("RemainingCredentialField")},
		"DELETE /api/v1/credentials/{credentialId}/fields/{fieldKey}": {Response: status},
		"GET /api/v1/credentials/default-env-var":                     {Response: object(map[string]any{"env_var": nullable(str()), "testable": boolean()})},
		"GET /api/v1/credentials/reveal-policy":                       {Response: object(map[string]any{"workspace_id": str(), "enabled": boolean()})},
		"PUT /api/v1/credentials/reveal-policy":                       {Request: ref("RemainingRevealPolicyRequest"), Response: object(map[string]any{"workspace_id": str(), "enabled": boolean()})},
		"PUT /api/v1/credentials/{credentialId}/sensitivity":          {Request: ref("RemainingCredentialSensitivityRequest"), Response: object(map[string]any{"credential_id": str(), "sensitivity": str(), "previous": str()})},
	}
	return routes, components
}
