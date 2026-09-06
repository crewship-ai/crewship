package main

// providerLoginSchemaCatalog documents the device-code sign-in to a model
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

	routes := map[string]DomainSchema{
		"POST /api/v1/credentials/{credentialId}/refresh": {Request: object(map[string]any{}), Response: object(map[string]any{"login": ref("ProviderLogin")}, "login")},
		"POST /api/v1/provider-logins/device":             {Request: ref("ProviderLoginDeviceStartRequest"), Response: ref("ProviderLoginDeviceStart")},
		"GET /api/v1/provider-logins/device/{deviceId}":   {Response: ref("ProviderLoginDeviceStatus")},
	}
	return routes, components
}
