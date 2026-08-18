package pages

import (
	"errors"
	"fmt"
	"io"
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

	// Icon is the panel's glyph, from the closed set in icons.go. Optional:
	// a panel that declares none keeps the icon its schema implies. It exists
	// because a page with three status.v1 panels on it had three identical
	// headers, and the schema is the wrong thing to derive identity from —
	// "is it running" and "who is on call" are the same SHAPE and not the same
	// subject. Closed for the reason PanelSchema is: an open string is a name
	// the client cannot draw, and a blank header is a quieter failure than an
	// unknown schema, which at least renders a fallback that says so.
	Icon PanelIcon `json:"icon,omitempty" yaml:"icon,omitempty"`

	// Tab is the name on the tab bar this panel appears under. Optional: a page
	// where no panel declares one has no bar and renders exactly as it did
	// before tabs existed.
	//
	// One key on the panel and no `tabs:` block, so adding a tab is one word
	// rather than a new section — and so there is no second list that can
	// disagree with the panels about which of them exists. Bar order is first
	// appearance; a panel with no tab lands on the first one. The whole rule,
	// and the reason a tab is more than a layout choice, is in tabs.go.
	Tab string `json:"tab,omitempty" yaml:"tab,omitempty"`

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

	// Actions are the buttons this panel offers (§8b.1). They are DECLARED
	// here, by a human editing the page, and that is the whole security
	// property: the click posts an action id, the server resolves it against
	// this stored list, and the wire format has no field in which a caller
	// could name a routine of its own. An agent may write a narrative onto a
	// panel; it can never author the button underneath it (§8 rule 4).
	Actions []PanelAction `json:"actions,omitempty" yaml:"actions,omitempty"`

	// OnFailure says what happens when this panel stops reporting (§4 rule 4).
	// A page that quietly goes stale must generate work for a human rather
	// than sit there looking plausible.
	OnFailure *PanelOnFailure `json:"on_failure,omitempty" yaml:"on_failure,omitempty"`

	// Refresh names the event that RUNS this panel's producer (§12 v1.1, and
	// the worked example at §6). It is a TRIGGER declaration, not a hint: a
	// page holds no query and no datasource, so the only way a panel's
	// contents change is a producer pushing to it, and a `refresh:` that does
	// not run the producer cannot refresh anything.
	//
	// Closed, and refused at save time with the vocabulary named — the whole
	// reasoning, and the four things it refuses, are in refresh.go. Like the
	// gates it rides on, it is compiled into an `automations` row rather than
	// into a second eventing path.
	Refresh PanelRefresh `json:"refresh,omitempty" yaml:"refresh,omitempty"`

	// Wake gates turn this panel from a display into a sensor (§5, §0.1): a
	// threshold on the pushed payload wakes an agent, which writes its
	// analysis back onto the same page. Each gate compiles to an `automations`
	// row — the journal-event matcher that already exists — rather than to a
	// second eventing path.
	Wake []PanelWake `json:"wake,omitempty" yaml:"wake,omitempty"`
}

// PanelAction is one button (§8b.1).
//
// The vocabulary is the small set Block Kit, Adaptive Cards and amis arrived at
// independently: an id, a label, a semantic style, an optional confirm step,
// and a distinction between "this calls the server" and "this only affects the
// client". Adaptive Cards' Action.Execute — a named remote operation — is the
// direct analogue of "run this routine".
type PanelAction struct {
	// ID is unique within the page. It is what a click posts; see §8b.2.
	ID string `json:"id" yaml:"id"`

	// Kind is closed. "custom" resolves to a handler registered in our own
	// client at build time — never to user-supplied code — and exists from day
	// one on Airbnb's advice, because an extension point retrofitted later is
	// an extension point bolted onto a shape that never expected it.
	Kind PanelActionKind `json:"kind" yaml:"kind"`

	Label string           `json:"label" yaml:"label"`
	Style PanelActionStyle `json:"style,omitempty" yaml:"style,omitempty"`

	// Confirm is drawn by host chrome, never by panel content (§8 rule 5), so
	// an injected panel cannot fake or skip it.
	Confirm *PanelActionConfirm `json:"confirm,omitempty" yaml:"confirm,omitempty"`

	// Routine is the named operation a "call" runs. It is read from HERE at
	// dispatch time and never from the request.
	Routine string `json:"routine,omitempty" yaml:"routine,omitempty"`

	// Params are fixed and author-controlled; Inputs are collected from the
	// user and validated server-side against this declaration.
	Params map[string]any `json:"params,omitempty" yaml:"params,omitempty"`
	Inputs []PanelInput   `json:"inputs,omitempty" yaml:"inputs,omitempty"`

	// Target is the panel ids a "toggle" shows or hides. Local only.
	Target []string `json:"target,omitempty" yaml:"target,omitempty"`

	// Ref is the internal entity a "link" points at — never a URL (§8 rule 3).
	// The renderer builds the address. Slack AI's private-channel leak was a
	// rendered link, and CamoLeak proved a trusted first-party proxy is not a
	// defence, so the schema simply has nowhere to put one.
	Ref *PanelEntityRef `json:"ref,omitempty" yaml:"ref,omitempty"`
}

// PanelActionKind is closed: a new kind is a server release.
type PanelActionKind string

const (
	ActionCall   PanelActionKind = "call"
	ActionLink   PanelActionKind = "link"
	ActionToggle PanelActionKind = "toggle"
	ActionCustom PanelActionKind = "custom"
)

// PanelActionStyle is the semantic triad all three mature formats converged on.
type PanelActionStyle string

const (
	ActionStyleDefault PanelActionStyle = "default"
	ActionStylePrimary PanelActionStyle = "primary"
	ActionStyleDanger  PanelActionStyle = "danger"
)

// PanelActionConfirm is the confirm step. Friction is calibrated to blast
// radius (§8 rule 7): a read-only or reversible action declares none, because
// Anthropic's own containment data shows ~93% of prompts get approved and a
// universal dialog is a rubber stamp rather than a control.
type PanelActionConfirm struct {
	Title        string `json:"title" yaml:"title"`
	Body         string `json:"body" yaml:"body"`
	ConfirmLabel string `json:"confirm_label,omitempty" yaml:"confirm_label,omitempty"`
	CancelLabel  string `json:"cancel_label,omitempty" yaml:"cancel_label,omitempty"`
}

// PanelInput is one parameter collected before a "call" dispatches. The shape
// mirrors the server-declared form schema SlashActionModal already renders, so
// the surface has one field switch rather than two.
type PanelInput struct {
	Name     string   `json:"name" yaml:"name"`
	Label    string   `json:"label,omitempty" yaml:"label,omitempty"`
	Type     string   `json:"type,omitempty" yaml:"type,omitempty"`
	Required bool     `json:"required,omitempty" yaml:"required,omitempty"`
	Default  string   `json:"default,omitempty" yaml:"default,omitempty"`
	Options  []string `json:"options,omitempty" yaml:"options,omitempty"`
}

// PanelEntityRef names an internal entity. Kind is closed and there is no URL
// field anywhere in it, deliberately.
type PanelEntityRef struct {
	Kind string `json:"kind" yaml:"kind"`
	ID   string `json:"id" yaml:"id"`
}

// PanelOnFailure routes a panel's failure to somewhere a human will see it.
type PanelOnFailure struct {
	// Issue is "crew/<slug>" — the crew whose board gets the issue.
	Issue string `json:"issue,omitempty" yaml:"issue,omitempty"`
}

// PanelWake is one threshold that wakes an agent (§5).
type PanelWake struct {
	// When is the predicate over the pushed payload, e.g.
	// `any(state == "critical")`. Deliberately not a general expression
	// language: it is matched against a payload, and a predicate nobody can
	// read is a predicate nobody can audit.
	When string `json:"when" yaml:"when"`

	// For requires the condition to hold this long before firing, so a single
	// bad scrape does not wake anybody.
	For string `json:"for,omitempty" yaml:"for,omitempty"`

	// Agent is "crew/<slug>" — who gets woken.
	Agent string `json:"agent" yaml:"agent"`

	// Writes is the panel id the woken agent is expected to write. It is a
	// declaration, not a grant: the agent still needs produce authority on it.
	Writes string `json:"writes,omitempty" yaml:"writes,omitempty"`
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

	// A YAML stream can carry more than one document, and decoding the first
	// while ignoring the rest is the quietest way this function could lose
	// somebody's work: `crewship page create --file` would exit zero, print
	// nothing, and create one page out of four.
	//
	// That is reachable, and by our own hand — `crewship export page` with no
	// slug emits every page in the workspace as one `---`-separated stream, and
	// this is the documented way back in. Refusing costs nothing anyone wanted:
	// nobody hands `page create` four pages and means one.
	//
	// A LEADING `---` is not a second document. yaml reads it as the start of
	// the first, so a single-page export — which is also `---`-prefixed — still
	// parses. Only a genuine second document reaches this branch.
	var trailing Document
	err := dec.Decode(&trailing)
	switch {
	case err == nil && !trailingIsEmpty(trailing):
		// A real second document. An EMPTY one is not: yaml.v3 hands back a
		// zero Document with a nil error for the nothing after a trailing
		// `---`, and refusing that would reject a perfectly ordinary one-page
		// file — including one `crewship export page` wrote, since it ends
		// every document with its own separator.
		return nil, newError(CodeInvalidSpec, "",
			"this file carries more than one YAML document and a page spec is one page; "+
				"split the stream and create each page separately")
	case err == nil:
		// Empty trailing document: treated as end of stream.
	case !errors.Is(err, io.EOF):
		// Not EOF and not a document either: the bytes after the first document
		// are malformed. Saying so beats accepting the first half of a file the
		// author believes was read whole.
		return nil, newError(CodeInvalidSpec, "", "after the page document: %v", err)
	}

	if err := doc.Validate(); err != nil {
		return nil, err
	}
	return &doc, nil
}

// trailingIsEmpty reports whether a decoded trailing document carries nothing.
//
// Document holds a slice, so it is not comparable and `== Document{}` will not
// build. Checking the four fields a real page always has is enough and is more
// honest than reflect.DeepEqual: what we are asking is "did the author write a
// second PAGE here", and a document with no apiVersion, no kind, no name and no
// panels is not one.
func trailingIsEmpty(d Document) bool {
	return d.APIVersion == "" && d.Kind == "" &&
		d.Metadata == (Metadata{}) && len(d.Spec.Panels) == 0
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
		// The icon is normalised before it is checked, and checked before it is
		// stored: what validates is exactly what the client will be asked to
		// resolve. Trimming only — no case folding — because `icon: Memory`
		// silently becoming `memory` teaches an author a spelling that is not
		// the vocabulary, and the next name they guess will not be forgiven.
		p.Icon = PanelIcon(strings.TrimSpace(string(p.Icon)))
		if p.Icon != "" && !p.Icon.Known() {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q declares icon %q; the set is closed — one of: %s",
				p.ID, p.Icon, PanelIconList())
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

	// Tabs before actions, and page-scoped for the same kind of reason: how many
	// distinct tabs a page declares, and whether two of them differ only by
	// case, are not questions the per-panel loop above can answer. A tab hides
	// panels (tabs.go), so a bar that draws a blank word, or the same word
	// twice, is a page whose reader cannot tell what they are not being shown.
	if err := validatePageTabs(d.Spec.Panels); err != nil {
		return err
	}

	// Actions last, because two of their rules are page-scoped: ids are unique
	// within the PAGE (§8b.1) and a toggle may only target a panel that exists
	// on it, neither of which is answerable inside the per-panel loop above.
	// The stored spec is the dispatch allow-list (§8b.2), so an action that does
	// not validate here is an action that can never be clicked.
	if err := validatePageActions(d.Spec.Panels); err != nil {
		return err
	}
	// Gates last, and here rather than only in the handler: ParseDocument and
	// the manifest kind both call Validate, so a gate that names a panel which
	// is not on the page has to fail at parse time. Otherwise `crewship apply`
	// and the editor accept it and the server refuses it later, which is the
	// same document being valid in one door and invalid in the next.
	if err := ValidateGates(d); err != nil {
		return err
	}
	// Refresh AFTER the gates, and not by accident: `refresh: on:wake` is a
	// declaration ABOUT the gates — it is refused on a page that declares none,
	// and refused on a panel that declares its own — so it can only be checked
	// once the gates on this document are known to compile (refresh.go).
	return ValidateRefresh(d)
}
