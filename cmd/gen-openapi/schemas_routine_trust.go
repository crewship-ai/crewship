package main

// routineTrustSchemaCatalog documents the standing approval grant
// surface. These three operations are the audit trail for "this gate no
// longer asks a human", so their shapes are worth spelling out rather
// than letting the generator emit a generic envelope — a client reading
// the spec should be able to see that a grant carries who granted it,
// what definition it is pinned to, and how many times it has fired.
func routineTrustSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	dateTime := func() map[string]any { return map[string]any{"type": "string", "format": "date-time"} }
	obj := func(properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": properties}
	}
	array := func(item map[string]any) map[string]any {
		return map[string]any{"type": "array", "items": item}
	}

	// grant mirrors pipeline.TrustGrant plus the server-computed `live`
	// flag. Live is derived here rather than left to the client so the
	// CLI and the dashboard cannot disagree about whether a gate is
	// currently disarmed.
	grant := obj(map[string]any{
		"id":                 str(),
		"workspace_id":       str(),
		"pipeline_id":        str(),
		"step_id":            str(),
		"definition_hash":    str(),
		"granted_by_user_id": str(),
		"granted_at":         dateTime(),
		"reason":             str(),
		"prior_approvals":    integer(),
		"max_uses":           integer(),
		"uses":               integer(),
		"expires_at":         dateTime(),
		"revoked_at":         dateTime(),
		"revoked_by_user_id": str(),
		"revoke_reason":      str(),
		"live":               boolean(),
	})

	return map[string]DomainSchema{
		"GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/trust": {
			// definition_hash is the routine's CURRENT hash: the value a
			// client must echo back on POST to prove it is granting trust
			// for the body it was shown.
			Response: obj(map[string]any{
				"slug":            str(),
				"definition_hash": str(),
				"grants":          array(grant),
			}),
		},
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/trust": {
			Request: map[string]any{
				"type":     "object",
				"required": []any{"step_id"},
				"properties": map[string]any{
					"step_id":         str(),
					"definition_hash": str(),
					"reason":          str(),
					"prior_approvals": integer(),
					"max_uses":        integer(),
					"expires_at":      dateTime(),
				},
			},
			Response: obj(map[string]any{
				"id":              str(),
				"slug":            str(),
				"step_id":         str(),
				"definition_hash": str(),
			}),
			SuccessStatuses: []string{"201"},
		},
		"DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}/trust/{grantId}": {
			// Revoke marks the row rather than deleting it, so this
			// returns the revoked grant's id rather than 204 — the caller
			// gets a receipt it can quote in an audit.
			Response: obj(map[string]any{
				"id":     str(),
				"status": str(),
			}),
		},
	}
}
