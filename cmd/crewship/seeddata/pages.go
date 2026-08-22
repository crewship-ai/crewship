package seeddata

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// PageDef is one demo page: the spec a human would author, plus a payload per
// panel so the seeded page arrives with something on it.
//
// The payloads live beside the spec rather than in a second file because they
// are the same decision: a demo panel and the number it shows are authored
// together, and splitting them is how one gets renamed without the other.
type PageDef struct {
	Slug        string `yaml:"slug"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// Owner hands the page to a crew ("crew/<slug>"). Empty means the seeding
	// account keeps it.
	//
	// This is the page's owner, which is a different question from a panel's
	// owner: the panel's owner decides who may SEE it, and the page's decides
	// who may hand-write a script-produced panel and edit the spec. Naming a
	// crew here is what makes the demo a team's board rather than one account's
	// — isPageOwner counts every member of the owning crew
	// (internal/api/pages_authz.go), so anyone in that crew can correct a panel
	// by hand without a grant and without waiting for the producer.
	//
	// It is NOT part of the page document a human authors: ownership is decided
	// once, at creation, and moving it afterwards is a transfer with its own
	// rules. It rides on the create request, exactly as `crewship page create
	// --owner` does.
	Owner  string         `yaml:"owner,omitempty"`
	Panels []PagePanelDef `yaml:"panels"`
}

// PagePanelDef is a panel's spec with its demo payload attached.
//
// `Demo` is deliberately `any`: it is the producer's payload, judged by the
// panel's own schema on the server. Typing it here would mean a second,
// weaker copy of five JSON Schemas that could disagree with them — and the
// disagreement would surface as a seed that fails against a schema it claims
// to satisfy.
type PagePanelDef struct {
	ID       string `yaml:"id"`
	Schema   string `yaml:"schema"`
	Title    string `yaml:"title,omitempty"`
	Icon     string `yaml:"icon,omitempty"`
	Tab      string `yaml:"tab,omitempty"`
	Owner    string `yaml:"owner"`
	Producer string `yaml:"producer"`
	SLA      string `yaml:"sla"`
	Span     int    `yaml:"span,omitempty"`

	// The authored half — the sensor, the buttons and the publication flag.
	//
	// All three are `any` for the reason `Demo` is: they are judged by the
	// server, which owns the grammar. A wake predicate is checked against the
	// panel's own schema, an action's routine has to exist, and `public` is
	// governed by the publication rules. Typing them here would be a second,
	// weaker copy of that grammar, and the copy that drifts is always the one
	// nobody runs.
	//
	// They are carried at all because a catalogue that could only declare the
	// display half would demo half the feature: PRD §0 says the panel is a
	// sensor, and a seeded page with no gate never shows that.
	Wake      any  `yaml:"wake,omitempty"`
	OnFailure any  `yaml:"on_failure,omitempty"`
	Actions   any  `yaml:"actions,omitempty"`
	Public    bool `yaml:"public,omitempty"`

	Demo any `yaml:"demo,omitempty"`
}

// Pages is the demo catalogue, loaded at package init like every other one.
var Pages = mustLoadPages()

func mustLoadPages() []PageDef {
	data, err := builtinFS.ReadFile("builtin/pages.yaml")
	if err != nil {
		panic(fmt.Sprintf("seeddata: read builtin/pages.yaml: %v", err))
	}
	var doc struct {
		Pages []PageDef `yaml:"pages"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		panic(fmt.Sprintf("seeddata: parse builtin/pages.yaml: %v", err))
	}
	// Same fail-fast as the other catalogues: a renamed top-level key would
	// otherwise disable page seeding silently, and "the demo has no pages" is
	// indistinguishable from "the feature is broken" to whoever opens it.
	if len(doc.Pages) == 0 {
		panic("seeddata: builtin/pages.yaml decoded to zero pages — schema drift?")
	}
	return doc.Pages
}
