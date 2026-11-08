package seeddata

// The owner's claude.ai design system "Template Crewship Design System",
// mirrored as a Design in Library mode: an artifact project whose manifest
// holds design-system.json (see showcase/mirror/ds216/SOURCE.md). It is seeded
// through the same artifact-project path as the other showcase projects.
func init() {
	ShowcaseArtifacts = append(ShowcaseArtifacts, ShowcaseArtifactDef{
		Slug: "ref-design-system",
		Name: "Template Crewship Design System",
		File: "mirror/ds216/manifest.json",
		Kind: "design system",
	})
}
