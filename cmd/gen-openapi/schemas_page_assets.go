package main

func pageAssetSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func(max int) map[string]any { return map[string]any{"type": "integer", "minimum": 1, "maximum": max} }
	metadata := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "page_id", "sha256", "content_type", "width", "height", "byte_size", "state", "created_at", "expires_at"}, "properties": map[string]any{
		"id": str(), "page_id": str(), "sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}, "content_type": map[string]any{"type": "string", "enum": []string{"image/png", "image/jpeg"}}, "width": integer(2048), "height": integer(2048), "byte_size": integer(8 << 20), "state": map[string]any{"type": "string", "enum": []string{"staged", "retained"}}, "created_at": map[string]any{"type": "string", "format": "date-time"}, "expires_at": map[string]any{"type": "string", "format": "date-time"}}}
	binary := map[string]any{"type": "string", "format": "binary", "maxLength": 5 << 20}
	catalog := map[string]DomainSchema{
		"POST /api/v1/pages/{slug}/assets":                {Request: binary, RequestRequired: true, RequestMedia: []string{"application/octet-stream", "image/png", "image/jpeg"}, Response: metadata, SuccessStatuses: []string{"200", "201"}, ErrorStatuses: []string{"400", "401", "403", "404", "408", "409", "410", "413", "422", "500", "503"}, Parameters: []map[string]any{{"name": "mutation_id", "in": "query", "required": true, "schema": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,128}$"}}}},
		"GET /api/v1/pages/{slug}/assets/{assetId}":       {Response: metadata, SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"401", "403", "404", "408", "500", "503"}},
		"GET /api/v1/pages/{slug}/assets/{assetId}/bytes": {Response: map[string]any{"type": "string", "format": "binary", "maxLength": 8 << 20}, ResponseMedia: []string{"image/png", "image/jpeg"}, SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"401", "403", "404", "408", "500", "503"}, SuccessHeaders: map[string]any{"Cache-Control": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"no-store"}}}, "X-Content-Type-Options": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"nosniff"}}}}},
	}
	for key, contract := range catalog {
		contract.Parameters = append(contract.Parameters, map[string]any{"name": "workspace_id", "in": "query", "required": false, "schema": str()}, map[string]any{"name": "X-Workspace-ID", "in": "header", "required": false, "schema": str()})
		catalog[key] = contract
	}
	return catalog

}
