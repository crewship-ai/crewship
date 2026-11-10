package api

// Artifact profiles: what the selected version of an artifact project IS
// beyond its storage kind, so the catalog, its facets and the Page detail
// name it the same way before anyone opens it (#5152).
const (
	artifactProfileInteractive  = "interactive"
	artifactProfileDesignSystem = "design_system"
)

// artifactProfileSQL derives the profile in SQL from the file names of the
// selected manifest (alias `v`), so the catalog never loads a manifest per row
// to name it. It takes one bind argument: whether Slides is switched on, since
// a deck decides its own kind only then. A design system wins over a runtime
// marker, as in the viewer; an interactive.json marker makes the project an
// App whatever data collections it declares. The marker is the author's
// declaration; whether its runtime may start is decided elsewhere
// (ParseInteractiveArtifact and the feature switches).
func artifactProfileSQL(v, contentType string) string {
	files := func(name string) string {
		return "json_type(" + v + ".manifest_json,'$.files.\"" + name + "\"') IS NOT NULL"
	}
	// Separate CASE arms prevent JSON parsing before the selected-manifest
	// byte limit has been checked (SQL OR does not guarantee short circuiting).
	return "CASE WHEN COALESCE(" + contentType + ",'')<>'artifact_project' OR " + v + ".manifest_json IS NULL OR length(CAST(" + v + ".manifest_json AS BLOB))>131072 THEN ''" +
		" WHEN NOT json_valid(" + v + ".manifest_json) THEN ''" +
		" WHEN ?=1 AND " + files("deck.json") + " THEN ''" +
		" WHEN " + files("design-system.json") + " THEN '" + artifactProfileDesignSystem + "'" +
		" WHEN " + files("interactive.json") + " THEN '" + artifactProfileInteractive + "'" +
		" ELSE '' END"
}
