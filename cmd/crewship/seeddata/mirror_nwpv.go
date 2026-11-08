package seeddata

// MirrorNwpvSlug is the seeded copy of the owner's claude.ai Design canvas
// "Template Crewship Design" (NwpV8rqSjHWDtpZPYUb2ko): thirteen HTML
// artboards on five canvas pages. Crewship's native Design canvas holds
// frame/rect/text/image layers only, so the artboards are carried as an App
// whose screens are the original artboard sources, run by a small stand-in for
// the canvas runtime, with an overview screen for the canvas itself.
// showcase/mirror/nwpv/SOURCE.md lists the conversion and what it cannot carry.
const MirrorNwpvSlug = "ref-design-template"

func init() {
	ShowcaseArtifacts = append(ShowcaseArtifacts, ShowcaseArtifactDef{
		Slug: MirrorNwpvSlug,
		Name: "Template Crewship Design",
		File: "mirror/nwpv/m-nwpv.json",
		Kind: "project",
	})
}
