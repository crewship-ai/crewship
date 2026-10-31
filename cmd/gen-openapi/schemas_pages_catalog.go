package main

// The catalog helpers validate these options outside the route body scanner.
// Multi-value facets accept repeated parameters and comma-separated values.
func pageCatalogQueryParameters() []map[string]any {
	text := func(name, description string) map[string]any {
		return map[string]any{"name": name, "in": "query", "required": false,
			"description": description, "schema": map[string]any{"type": "string"}}
	}
	facet := func(name, description string, values ...string) map[string]any {
		items := map[string]any{"type": "string"}
		if len(values) != 0 {
			items["enum"] = values
		}
		return map[string]any{"name": name, "in": "query", "required": false,
			"description": description + " Repeat the parameter or separate values with commas; values are ORed within this facet.",
			"style":       "form", "explode": true, "schema": map[string]any{"type": "array", "items": items}}
	}
	sort := text("sort", "Stable order: updated descending (default), case-folded name ascending, or library kind order. Ties keep the index order: updated descending, then slug ascending.")
	sort["schema"].(map[string]any)["enum"] = []string{"updated", "name", "kind"}
	return []map[string]any{
		text("q", "Case-insensitive literal substring of the Page name or slug; query operators are not interpreted."),
		facet("kind", "Caller-visible kind, derived from the same selected metadata as the index row.", "project", "dashboard", "page", "word", "sheet", "slides", "design"),
		facet("access", "Explicit private_audience classification, independent of ownership or folder.", "private", "open"),
		facet("version", "Version-store state; legacy storage without a typed version head is unversioned.", "shared", "unshared", "unversioned"),
		facet("owner", "Canonical index owner reference user/ID or crew/slug; me means the authenticated human's user reference."),
		text("folder", "Exact folder slug, or ~unfiled. An omitted parameter includes every folder."),
		func() map[string]any {
			empty := text("empty", "Pages with nothing to open yet: no typed kind, no Word, Sheet or Slides storage and no panels. Omitted includes them.")
			empty["schema"].(map[string]any)["enum"] = []string{"exclude", "only", "include"}
			return empty
		}(),
		sort,
	}
}
