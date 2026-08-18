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
	Slug        string         `yaml:"slug"`
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Panels      []PagePanelDef `yaml:"panels"`
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
	Demo     any    `yaml:"demo,omitempty"`
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
