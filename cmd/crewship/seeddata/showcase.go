package seeddata

import "embed"

// The showcase is the part of the demo that is opened rather than read about:
// native Word and Sheet documents, a Slides deck, a dashboard with data, two
// interactive artifacts and a Design canvas. Every file is authored outside Go
// (see showcase/README.md for how each one was produced) and posted verbatim;
// the server is what judges the bytes.
//
//go:embed showcase/word/*.json showcase/sheet/*.json showcase/art/*.json
var showcaseFS embed.FS

// ShowcaseFile returns one embedded showcase file. Panics on a missing path:
// the files ship inside the binary, so a missing one is a build-time mistake,
// and TestShowcaseCatalogue_FilesExist catches it before a seed does.
func ShowcaseFile(path string) []byte {
	data, err := showcaseFS.ReadFile("showcase/" + path)
	if err != nil {
		panic("missing showcase file: " + path)
	}
	return data
}

// ShowcaseCraftDef is one native Craft document: an empty Page initialized
// with native WordCraft or GridCraft bytes.
type ShowcaseCraftDef struct {
	Slug        string
	Name        string
	Application string // "wordcraft" or "gridcraft"
	File        string
}

// ShowcaseDataDef is one data batch written into a dashboard collection.
type ShowcaseDataDef struct {
	Collection string
	File       string
}

// ShowcaseArtifactDef is one multi-file artifact project (dashboard,
// interactive app or Slides deck) created from a complete manifest.
type ShowcaseArtifactDef struct {
	Slug string
	Name string
	File string
	// Kind names the capability the artifact needs, for the one-line skip
	// message when the server refuses it.
	Kind string
	Data []ShowcaseDataDef
}

// ShowcaseDesignDef is one Design canvas built from a list of operations and
// retained as a snapshot.
type ShowcaseDesignDef struct {
	Slug string
	Name string
	File string
}

// ShowcaseFeatureFlags are turned on for the seeded workspace so every surface
// the showcase fills is also visible.
var ShowcaseFeatureFlags = []string{
	"artifact_projects", "artifact_slides", "artifact_interactive",
	"artifact_dashboard_data", "artifacts_design", "gridcraft_editor",
	"craft_version_history",
}

// ShowcaseFeatureFlagsCreateFirst may have no instance-wide definition on a
// fresh install, so the seed creates them before enabling them.
var ShowcaseFeatureFlagsCreateFirst = []string{"craft_version_history"}

// ShowcaseCrafts are the native documents, in the order the seed creates them.
var ShowcaseCrafts = []ShowcaseCraftDef{
	{"obchodni-nabidka", "Obchodní nabídka – Acme Company", "wordcraft", "word/01-nabidka.json"},
	{"zapis-z-porady", "Zápis z porady vedení 7. 11.", "wordcraft", "word/02-zapis-z-porady.json"},
	{"shrnuti-smlouvy", "Shrnutí smlouvy o službách", "wordcraft", "word/03-shrnuti-smlouvy.json"},
	{"playbook-onboarding", "Playbook: první týden obchodníka", "wordcraft", "word/04-playbook-onboarding.json"},
	{"cenova-nabidka", "Cenová nabídka (kalkulace)", "gridcraft", "sheet/nabidka.json"},
	{"rozpocet-2027", "Rozpočet 2027", "gridcraft", "sheet/rozpocet.json"},
	{"pipeline-q4", "Obchodní pipeline Q4", "gridcraft", "sheet/pipeline.json"},
}

// ShowcaseArtifacts are the artifact projects, in creation order.
var ShowcaseArtifacts = []ShowcaseArtifactDef{
	{Slug: "obchod-dashboard", Name: "Dashboard: Obchod Q4 2026", File: "art/m-dash.json", Kind: "dashboard",
		Data: []ShowcaseDataDef{{Collection: "pipeline", File: "art/data-pipeline.json"}}},
	{Slug: "hlasovani-akce", Name: "Hlasování: vánoční akce", File: "art/m-poll.json", Kind: "interactive",
		Data: []ShowcaseDataDef{{Collection: "votes", File: "art/data-votes.json"}}},
	{Slug: "kalkulacka-roi", Name: "Kalkulačka návratnosti", File: "art/m-roi.json", Kind: "interactive"},
	{Slug: "prodejni-prezentace", Name: "Prezentace: Crewship pro vaši firmu", File: "art/m-deck.json", Kind: "slides"},
}

// ShowcaseDesigns are the Design canvases.
var ShowcaseDesigns = []ShowcaseDesignDef{
	{Slug: "landing-page", Name: "Design: Landing page Crewship", File: "art/design-landing.json"},
}
