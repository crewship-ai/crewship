package main

// artifactKindGuideSchemaCatalog describes the static per-kind agent guides
// (internal/artifacts/kindguide) served under /api/v1/artifact-kind-guides.
func artifactKindGuideSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	summary := map[string]any{
		"kind":          map[string]any{"type": "string", "enum": []string{"docs", "slides", "design", "library", "dashboard", "app"}},
		"title":         text("Display name of the kind."),
		"version":       text("Semantic version of this kind's guide."),
		"rules_version": text("Semantic version of the shared rules appended to every guide."),
		"sha256":        text("Lowercase hex SHA-256 of content."),
		"bytes":         map[string]any{"type": "integer", "minimum": 1, "maximum": 16384},
		"modes":         map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"ask", "comment", "propose", "edit"}}},
	}
	required := []string{"kind", "title", "version", "rules_version", "sha256", "bytes", "modes"}
	strs := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	full := map[string]any{
		"capabilities": map[string]any{"type": "object", "properties": map[string]any{"supported": strs, "unsupported": strs}, "required": []string{"supported", "unsupported"}, "additionalProperties": false, "description": "What the agent profile supports and, by name, what it does not. Grants nothing."},
		"content":      text("Markdown guide: data shapes and limits, write order, version check, conflicts, agent modes, verification and untrusted content.")}
	for key, value := range summary {
		full[key] = value
	}
	schemas := map[string]any{
		"ArtifactKindGuideSummary": map[string]any{"type": "object", "properties": summary, "required": required, "additionalProperties": false},
		"ArtifactKindGuide":        map[string]any{"type": "object", "properties": full, "required": append(append([]string{}, required...), "capabilities", "content"), "additionalProperties": false},
		"ArtifactKindGuideList":    map[string]any{"type": "object", "properties": map[string]any{"guides": map[string]any{"type": "array", "items": ref("ArtifactKindGuideSummary")}}, "required": []string{"guides"}, "additionalProperties": false},
	}
	routes := map[string]DomainSchema{
		"GET /api/v1/artifact-kind-guides":        {NoRequest: true, Response: ref("ArtifactKindGuideList"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"401"}},
		"GET /api/v1/artifact-kind-guides/{kind}": {NoRequest: true, Response: ref("ArtifactKindGuide"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"401", "404"}},
	}
	return routes, schemas
}
