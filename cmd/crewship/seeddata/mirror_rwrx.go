package seeddata

// The reference dashboard "Template Crewship Dashboard", mirrored 1:1 from the
// owner's claude.ai Dashboard artifact (showcase/mirror/rwrx/SOURCE.md). It is
// an interactive dashboard project: the original page verbatim, the host
// tokens and `dash` API it reads, and its rows in the `tasks` collection.
func init() {
	ShowcaseArtifacts = append(ShowcaseArtifacts, ShowcaseArtifactDef{
		Slug: MirrorRwrxSlug, Name: "Template Crewship Dashboard", File: "mirror/rwrx/manifest.json", Kind: "dashboard",
		Data: []ShowcaseDataDef{{Collection: "tasks", File: "mirror/rwrx/data-tasks.json"}},
	})
}

// MirrorRwrxSlug is the seeded Page of the mirrored reference dashboard.
const MirrorRwrxSlug = "ref-dashboard-template"
