package pages

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The page spec — layer 1 of PRD §6.
//
// A human writes YAML, a machine writes JSON, both validated, no third DSL.
// The envelope is the one internal/manifest already parses (apiVersion / kind /
// metadata / spec), because a Page is the 21st kind in a system that exists,
// not a new framework. Nobody defends hand-written dashboard JSON as an
// authoring format, and every ecosystem that chose the Kubernetes envelope for
// dashboards — Perses, Grafana's Schema v2, Rill, Lightdash, Cube — arrived
// here independently.
//
// What this file does NOT do is resolve anything. The authoring gate (§10b.1)
// has two halves: validate the shape, then check that every declared producer
// and owner exists. The second half needs the database and belongs to the
// handler. What is here is what stops a page being saved before anything has to
// be looked up.

const (
	// DocumentAPIVersion is the manifest envelope every Crewship kind carries.
	DocumentAPIVersion = "crewship/v1"
	// DocumentKind is the one noun, everywhere: kind: Page, route /pages, CLI
	// `crewship page`, table `pages`. The design artefact used three names and
	// none of them survives here.
	DocumentKind = "Page"

	// DefaultSpan is what a panel that declares no span gets: the full width of
	// the 12-column grid. Zero would render a panel with no width at all.
	DefaultSpan = 12
)

// slugRE is the shape the rest of Crewship uses for anything addressable.
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Document is a page as authored.
type Document struct {
	APIVersion string   `json:"apiVersion" yaml:"apiVersion"`
	Kind       string   `json:"kind" yaml:"kind"`
	Metadata   Metadata `json:"metadata" yaml:"metadata"`
	Spec       Spec     `json:"spec" yaml:"spec"`
}

// Metadata is the page's identity.
type Metadata struct {
	Name        string `json:"name" yaml:"name"`
	Slug        string `json:"slug" yaml:"slug"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// Spec is the ordered list of panels. Layout is declared, never dragged: a
// dashboard does not drag on a phone anywhere, it reflows to one column.
type Spec struct {
	Panels []PanelSpec `json:"panels" yaml:"panels"`
}

// PanelSpec is one panel's declaration. Four of these fields are the contract
// the whole feature rests on — schema (what shape), owner (who may see it),
// producer (who may write it), sla (when silence becomes a fault).
type PanelSpec struct {
	// ID is the author-chosen panel id. It is the address a producer pushes to
	// (`crewship page set <page>/<panel>`), so it is stable across edits.
	ID string `json:"id" yaml:"id"`

	Schema PanelSchema `json:"schema" yaml:"schema"`
	Title  string      `json:"title,omitempty" yaml:"title,omitempty"`

	// Owner is the permission anchor, not a label: "crew/<slug>". A panel the
	// viewer may not see is filtered server-side before serialisation and
	// leaves a sealed placeholder in its grid slot, so the page has the same
	// shape for everyone (§2.3).
	Owner string `json:"owner" yaml:"owner"`

	// Producer is "<kind>/<ref>" — the routine or script permitted to write
	// this panel's data. Producer authority is separate from viewer authority:
	// a crew member who can see a panel cannot write it.
	Producer string `json:"producer" yaml:"producer"`

	// SLA is a Go duration string ("30s", "1h"). Required: a panel without one
	// does not validate, and there is no default that means "never mind".
	SLA string `json:"sla" yaml:"sla"`

	// Span is the panel's width on the 12-column grid.
	Span int `json:"span,omitempty" yaml:"span,omitempty"`

	// Public opts this panel into a published page (§7.3.2 rule 2). Default
	// deny, per panel and never per page: publishing must never be a bulk
	// action over panels the author has not looked at. Only a human may set it
	// — an agent can build the page but cannot widen its reach.
	Public bool `json:"public,omitempty" yaml:"public,omitempty"`
}

// SLADuration parses the declared SLA.
func (p PanelSpec) SLADuration() (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(p.SLA))
	if err != nil {
		return 0, fmt.Errorf("sla %q is not a duration (try 30s, 5m, 1h): %w", p.SLA, err)
	}
	return d, nil
}

// OwnerCrewSlug parses the owner reference and returns the crew slug.
//
// The owner is always a crew and never a user. That is the Directus shape, not
// the Retool one: visibility is a property of the panel row, resolved by the
// ordinary membership check, rather than a condition a page author writes into
// each component — which is the documented road to permission sprawl, rules
// scattered across component properties and discovered at audit time.
func (p PanelSpec) OwnerCrewSlug() (string, error) {
	kind, ref, ok := strings.Cut(strings.TrimSpace(p.Owner), "/")
	if !ok || kind != "crew" {
		return "", fmt.Errorf("owner %q must be crew/<slug>: a panel's permission anchor is a crew", p.Owner)
	}
	if !slugRE.MatchString(ref) {
		return "", fmt.Errorf("owner %q: %q is not a crew slug", p.Owner, ref)
	}
	return ref, nil
}

// ProducerParts splits the producer reference into its kind and ref.
func (p PanelSpec) ProducerParts() (ProducerKind, string, error) {
	kind, ref, ok := strings.Cut(strings.TrimSpace(p.Producer), "/")
	if !ok {
		return "", "", fmt.Errorf("producer %q must be <kind>/<ref>", p.Producer)
	}
	pk := ProducerKind(kind)
	if !pk.Known() {
		return "", "", fmt.Errorf("producer kind %q is not one of routine, script, agent, webhook — "+
			"a page holds no query and no datasource", kind)
	}
	if strings.TrimSpace(ref) == "" {
		return "", "", fmt.Errorf("producer %q names no %s", p.Producer, kind)
	}
	return pk, ref, nil
}

// ParseDocument parses and validates an authored page document. YAML is the
// authoring format and JSON is a subset of it, so both are accepted.
func ParseDocument(raw []byte) (*Document, error) {
	if len(raw) > MaxSpecBytes {
		return nil, newError(CodeTooLarge, "", "spec is %d bytes; the cap is %d", len(raw), MaxSpecBytes)
	}

	var doc Document
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, newError(CodeInvalidSpec, "", "%v", err)
	}
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	return &doc, nil
}

// Validate checks the document's shape and every declared limit. It resolves
// nothing: whether crew/lookout exists is the handler's question.
func (d *Document) Validate() error {
	if d.APIVersion != DocumentAPIVersion {
		return newError(CodeInvalidSpec, "", "apiVersion %q; want %q", d.APIVersion, DocumentAPIVersion)
	}
	if d.Kind != DocumentKind {
		return newError(CodeInvalidSpec, "", "kind %q; want %q", d.Kind, DocumentKind)
	}
	if strings.TrimSpace(d.Metadata.Name) == "" {
		return newError(CodeInvalidSpec, "", "metadata.name is required")
	}
	if !slugRE.MatchString(d.Metadata.Slug) {
		return newError(CodeInvalidSpec, "",
			"metadata.slug %q is not a slug; a page is slug-addressable and the slug goes in a URL",
			d.Metadata.Slug)
	}

	switch {
	case len(d.Spec.Panels) == 0:
		return newError(CodeInvalidSpec, "",
			"a page needs at least one panel; there is nothing to render and nothing to push to")
	case len(d.Spec.Panels) > MaxPanelsPerPage:
		return newError(CodeInvalidSpec, "",
			"%d panels; the cap is %d", len(d.Spec.Panels), MaxPanelsPerPage)
	}

	seen := make(map[string]bool, len(d.Spec.Panels))
	for i := range d.Spec.Panels {
		p := &d.Spec.Panels[i]
		if !slugRE.MatchString(p.ID) {
			return newError(CodeInvalidSpec, "",
				"panel %d: id %q is not a slug; it is the address a producer pushes to", i, p.ID)
		}
		if seen[p.ID] {
			return newError(CodeInvalidSpec, "",
				"panel id %q appears twice; one of the two could never be pushed to", p.ID)
		}
		seen[p.ID] = true

		if !p.Schema.Producible() {
			if p.Schema.Known() {
				return newError(CodeInvalidSpec, p.Schema,
					"panel %q declares %s, which is reserved but not yet implemented", p.ID, p.Schema)
			}
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q declares unknown schema %q; the set is closed", p.ID, p.Schema)
		}
		if _, err := p.OwnerCrewSlug(); err != nil {
			return newError(CodeInvalidSpec, p.Schema, "panel %q: %v", p.ID, err)
		}
		if _, _, err := p.ProducerParts(); err != nil {
			return newError(CodeInvalidSpec, p.Schema, "panel %q: %v", p.ID, err)
		}

		sla, err := p.SLADuration()
		if err != nil {
			return newError(CodeInvalidSpec, p.Schema, "panel %q: %v", p.ID, err)
		}
		if sla <= 0 {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q declares sla %q; an SLA of zero is the default that means 'never mind', "+
					"and §4 says there is not one", p.ID, p.SLA)
		}

		if p.Span == 0 {
			p.Span = DefaultSpan
		}
		if p.Span < 1 || p.Span > 12 {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q declares span %d; the grid has 12 columns", p.ID, p.Span)
		}
	}
	return nil
}
