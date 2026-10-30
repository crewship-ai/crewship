package main

func artifactSlidesSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	anchor := map[string]any{"type": "object", "properties": map[string]any{"slide_id": text(), "path": text(), "selector": text(), "quote": text()}, "required": []string{"slide_id", "path", "selector", "quote"}, "additionalProperties": false}
	schemas := map[string]any{
		"ArtifactSlideAnchor":      anchor,
		"ArtifactSlidesProjection": map[string]any{"type": "object", "properties": map[string]any{"deck": map[string]any{"type": "object", "additionalProperties": true, "description": "Slides deck derived from deck.json and one file per slide: schema version 1, a 1920×1080 canvas and the Go Regular font, with at most 100 slides, 1000 elements and 2 MiB."}, "anchors": map[string]any{"type": "object", "maxProperties": 1000, "additionalProperties": ref("ArtifactSlideAnchor")}}, "required": []string{"deck", "anchors"}, "additionalProperties": false},
	}
	routes := map[string]DomainSchema{}
	for _, path := range []string{"slides", "export.pdf"} {
		schema := DomainSchema{NoRequest: true, Parameters: []map[string]any{{"name": "version", "in": "query", "required": true, "schema": map[string]any{"type": "integer", "format": "int64", "minimum": 1, "maximum": 9007199254740991}, "description": "Exact immutable code version to project; required for every caller. The owner may select any version, an editor only the latest version and other readers only the shared version."}}, Response: ref("ArtifactSlidesProjection"), SuccessStatuses: []string{"200"}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"400", "401", "403", "404", "413", "422", "500", "503"}}
		if path == "export.pdf" {
			schema.Response = map[string]any{"type": "string", "format": "binary"}
			schema.ResponseMedia = []string{"application/pdf"}
		}
		routes["GET /api/v1/pages/{slug}/artifact/"+path] = schema
	}
	return routes, schemas
}
