package seeddata

// MirrorE8hySlug is the seeded copy of the owner's claude.ai artifact
// "Template Crewship Showcase" (E8HYX1pSVKSAbLqgvTCZ8Z): a single-page
// interactive app with a three.js ship, a Mermaid diagram, a colour and type
// pairing tool, task filters and an e-mail draft. The page is carried
// byte for byte except its three external resources (three.js, Mermaid,
// fonts), which are made local; showcase/mirror/e8hy/SOURCE.md says how.
const MirrorE8hySlug = "ref-crewship-showcase"

func init() {
	ShowcaseArtifacts = append(ShowcaseArtifacts, ShowcaseArtifactDef{
		Slug: MirrorE8hySlug,
		Name: "Template Crewship Showcase",
		File: "mirror/e8hy/m-e8hy.json",
		Kind: "interactive",
	})
}
