package main

// providerLoginSchemaCatalog documents administrative pool definitions and
// device-code sign-in to a model
// provider (docs/prd/provider-logins.md §10.3). The frontend is built
// against these two shapes in parallel with the backend, so they are
// spelled out rather than left to the generator's generic envelope: a
// client must be able to read from the spec that `interval_s` is how often
// to poll and that `status` has exactly four values.
func providerLoginSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	dateTime := func() map[string]any { return map[string]any{"type": "string", "format": "date-time"} }
	nullable := func(s map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range s {
			out[k] = v
		}
		out["nullable"] = true
		return out
	}
	object := func(p map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": p}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	ref := func(n string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + n} }

	components := map[string]any{
		"ProviderPoolMember": object(map[string]any{
			"credential_id": str(), "priority": integer(),
		}, "credential_id"),
		"ProviderPool": object(map[string]any{
			"revision": map[string]any{"type": "integer", "minimum": 1},
			"id":       str(), "name": str(), "provider": str(), "mode": str(),
			"allow_cross_owner": map[string]any{"type": "boolean"},
			"created_by":        nullable(str()), "member_count": integer(),
			"members": map[string]any{"type": "array", "items": ref("ProviderPoolMember")},
		}, "id", "name", "provider", "mode", "allow_cross_owner", "created_by", "member_count", "revision"),
		"ProviderPoolPage": object(map[string]any{
			"items":       map[string]any{"type": "array", "items": ref("ProviderPool"), "maxItems": 100},
			"next_cursor": nullable(str()),
		}, "items", "next_cursor"),
		"ProviderPoolCreateRequest": object(map[string]any{
			"name":     map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
			"provider": str(), "mode": map[string]any{"type": "string", "enum": []string{"subscription", "api_key"}},
			"allow_cross_owner": map[string]any{"type": "boolean", "default": false},
			"members":           map[string]any{"type": "array", "items": ref("ProviderPoolMember"), "minItems": 1, "maxItems": 100},
		}, "name", "provider", "mode", "members"),
		"ProviderPoolUpdateRequest": object(map[string]any{
			"name":              map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
			"allow_cross_owner": map[string]any{"type": "boolean", "default": false},
			"members":           map[string]any{"type": "array", "items": ref("ProviderPoolMember"), "minItems": 1, "maxItems": 100},
		}, "name", "members"),
		"ProviderLoginDeviceStartRequest": object(map[string]any{
			"provider": map[string]any{"type": "string", "enum": []any{"OPENAI"}},
			"mode":     map[string]any{"type": "string", "enum": []any{"subscription"}},
		}, "provider"),
		"ProviderLoginDeviceStart": object(map[string]any{
			"device_id":        str(),
			"user_code":        str(),
			"verification_url": str(),
			"expires_at":       dateTime(),
			"interval_s":       integer(),
		}, "device_id", "user_code", "verification_url", "expires_at", "interval_s"),
		"ProviderLoginDeviceStatus": object(map[string]any{
			"status":           map[string]any{"type": "string", "enum": []any{"pending", "complete", "expired", "denied"}},
			"credential_id":    nullable(str()),
			"user_code":        str(),
			"verification_url": str(),
			"expires_at":       dateTime(),
			"error":            nullable(str()),
		}, "status"),
	}
	components["ProviderPoolMember"].(map[string]any)["additionalProperties"] = false
	components["ProviderPoolCreateRequest"].(map[string]any)["additionalProperties"] = false
	components["ProviderPoolUpdateRequest"].(map[string]any)["additionalProperties"] = false
	precondition := []map[string]any{{"name": "If-Match", "in": "header", "required": true, "description": "Strong ETag from pool get, e.g. \"1\". Missing: 428; stale: 412.", "schema": map[string]any{"type": "string", "pattern": `^"[1-9][0-9]*"$`}}}

	routes := map[string]DomainSchema{
		"PUT /api/v1/provider-logins/pools/{poolId}":    {Request: ref("ProviderPoolUpdateRequest"), RequestRequired: true, Parameters: precondition, SuccessStatuses: []string{"204"}},
		"DELETE /api/v1/provider-logins/pools/{poolId}": {Parameters: precondition, SuccessStatuses: []string{"204"}},
		"POST /api/v1/provider-logins/pools":            {Request: ref("ProviderPoolCreateRequest"), RequestRequired: true, Response: ref("ProviderPool")},
		"GET /api/v1/provider-logins/pools":             {Response: ref("ProviderPoolPage")},
		"GET /api/v1/provider-logins/pools/{poolId}":    {Response: ref("ProviderPool")},
		"POST /api/v1/provider-logins/device":           {Request: ref("ProviderLoginDeviceStartRequest"), Response: ref("ProviderLoginDeviceStart")},
		"GET /api/v1/provider-logins/device/{deviceId}": {Response: ref("ProviderLoginDeviceStatus")},
	}
	etag := map[string]any{"ETag": map[string]any{"description": "Committed definition revision. Send unchanged in If-Match for update/removal.", "schema": map[string]any{"type": "string", "pattern": `^"[1-9][0-9]*"$`}}}
	for _, key := range []string{"POST /api/v1/provider-logins/pools", "GET /api/v1/provider-logins/pools/{poolId}", "PUT /api/v1/provider-logins/pools/{poolId}"} {
		schema := routes[key]
		schema.SuccessHeaders = etag
		routes[key] = schema
	}
	return routes, components
}
