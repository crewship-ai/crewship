package seeddata

// Mirror of the owner's claude.ai Slides deck "Template Crewship Slides"
// (https://claude.ai/artifact/CBBoNZhbaHvUUUz2aQ2Ccv) as a Crewship Slides
// artifact project. Content and conversion notes: showcase/mirror/cbbo/SOURCE.md.

// MirrorCbboSlug is the seeded Page holding the mirrored deck.
const MirrorCbboSlug = "ref-slides-template"

func init() {
	ShowcaseArtifacts = append(ShowcaseArtifacts, ShowcaseArtifactDef{
		Slug: MirrorCbboSlug,
		Name: "Template Crewship Slides",
		File: "mirror/cbbo/m-deck.json",
		Kind: "slides",
	})
}
