package main

func artifactDashboardDataSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	counter := func(min int) map[string]any {
		return map[string]any{"type": "integer", "format": "int64", "minimum": min, "maximum": 9007199254740991}
	}
	text := func() map[string]any { return map[string]any{"type": "string"} }
	identifier := func() map[string]any {
		return map[string]any{"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$"}
	}
	array := func(item string, max int) map[string]any {
		return map[string]any{"type": "array", "maxItems": max, "items": ref(item)}
	}
	value := map[string]any{"type": "object", "additionalProperties": true, "description": "At most 256 KiB of JSON; duplicate keys and nesting beyond 64 are rejected. Numbers are preserved without a floating-point conversion."}
	writeCounter := counter(0)
	writeCounter["maximum"] = 9007199254740990
	writes := array("ArtifactDataWrite", 50)
	writes["minItems"] = 1
	schemas := map[string]any{
		"ArtifactDataWrite":       object(map[string]any{"id": identifier(), "ifVersion": writeCounter, "value": value}, "id", "ifVersion", "value"),
		"ArtifactDataBatch":       object(map[string]any{"base_version": counter(1), "documents": writes}, "base_version", "documents"),
		"ArtifactDataDocument":    object(map[string]any{"id": identifier(), "version": counter(1), "value": value, "updated_at": map[string]any{"type": "string", "format": "date-time"}, "source_label": text(), "source_kind": map[string]any{"type": "string", "enum": []string{"human", "agent", "unknown"}}}, "id", "version", "value", "updated_at", "source_label", "source_kind"),
		"ArtifactDataResponse":    object(map[string]any{"collection": identifier(), "code_version": counter(1), "documents": array("ArtifactDataDocument", 4096)}, "collection", "code_version", "documents"),
		"ArtifactDataCollection":  object(map[string]any{"name": identifier(), "documents": map[string]any{"type": "integer", "minimum": 0, "maximum": 4096}, "bytes": map[string]any{"type": "integer", "minimum": 0, "maximum": 8388608}}, "name", "documents", "bytes"),
		"ArtifactDataCollections": object(map[string]any{"code_version": counter(1), "collections": array("ArtifactDataCollection", 32)}, "code_version", "collections"),
	}
	routes := map[string]DomainSchema{}
	for _, entry := range []struct{ method, path, response string }{
		{"GET", "/api/v1/pages/{slug}/artifact/data", "ArtifactDataCollections"},
		{"GET", "/api/v1/pages/{slug}/artifact/data/{collection}", "ArtifactDataResponse"},
		{"POST", "/api/v1/pages/{slug}/artifact/data/{collection}", "ArtifactDataResponse"},
	} {
		schema := DomainSchema{NoRequest: entry.method == "GET", Response: ref(entry.response), SuccessStatuses: []string{"200"}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "413", "500", "503"}}
		if entry.method == "POST" {
			schema.Request = ref("ArtifactDataBatch")
			schema.RequestRequired = true
		}
		routes[entry.method+" "+entry.path] = schema
	}
	return routes, schemas
}
