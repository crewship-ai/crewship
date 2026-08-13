package pages

// Tabs — one page, several screens (PRD §9, §2.3, §3, §4).
//
// A page is a fixed structure a reader returns to. Once it holds more than
// about six panels it stops being glanceable and becomes a scroll, and the
// thing a reader came for is below the fold. Every ordinary website solved this
// with a tab bar, and that is all this is: a bar under the breadcrumb, one
// screen at a time.
//
// ## The format decision
//
// One optional key on the panel, and no second list:
//
//	- id: latence
//	  schema: metric.v1
//	  tab: Odezva
//
// The alternative — a `tabs:` block declaring order and membership — was
// rejected for the property this format is worth having at all: adding a tab is
// ONE WORD on the panel that needs it, not a new section plus an entry in it.
// It also removes a whole class of bug, because there is no second list to
// disagree with the panels. A tabs block can name a tab with no panels, list a
// panel twice, or omit one; none of those states is representable here.
//
// Bar order is FIRST APPEARANCE in the panel list, for the same reason panel
// order is the layout (§9): the spec is read top to bottom and the page is
// drawn the way it reads. A panel that names no tab lands on the FIRST tab —
// the tab of the first panel that names one — so declaring a tab on one panel
// of an existing page produces a working page rather than an error, which is
// what "one word" has to mean to be true.
//
// A page where no panel names a tab has no tabs at all: Tabs returns nil, no
// bar is drawn, and the grid is exactly what it was before this file existed.
//
// ## Why a tab is not only a layout feature
//
// A tab HIDES panels, and §4 is the reason this product exists: a panel that
// stops reporting says so. Today that is free — everything is on screen at
// once, so a failed panel is visible whether or not anyone was looking for it.
// With tabs, a critical panel can sit on the third tab while the page looks
// fine, and a panel gone stale where nobody can see it is perfectly silent,
// which is the Grafana failure mode §2.3 exists to prevent.
//
// So the renderer carries two obligations that this file's shape has to make
// cheap to meet, and both are asserted in the client's tests:
//
//  1. Every tab in the bar carries the WORST state of its own panels — failed
//     over stale over never_produced over fresh — as a glyph and not as colour
//     alone (§3: state is never carried by colour by itself).
//  2. The page header's freshness summary is computed over ALL tabs, never the
//     visible one. Switching tabs must not change it: a page that reads FRESH
//     while a hidden tab is failing is the silent-old-numbers failure with an
//     extra click in front of it.
//
// And one obligation this file meets directly: a tab is a property of the PAGE,
// not of the viewer. A tab whose panels are all sealed to a given reader still
// appears in that reader's bar, carrying its sealed placeholders — hiding it
// would leak the fact that everything on it is foreign, and reflowing the bar
// per viewer breaks the "same shape for everyone" property §2.3 argues for at
// length. That is why `tab` rides on the sealed placeholder too
// (internal/api/pages_handler.go).

import (
	"strings"
	"unicode"
)

// Structural limits on the tab bar. Shape constants like MaxPanelsPerPage, not
// tunables: they are properties of a bar a human can read at a glance, and of a
// name that has to fit on one at the narrow breakpoint.
const (
	// MaxTabNameRunes — a tab is named by a word, not by a sentence. The bar
	// scrolls horizontally on a phone rather than wrapping into a stack, so a
	// long name does not break the layout; it just pushes every other tab off
	// the screen, which is the same as not having a bar.
	MaxTabNameRunes = 32

	// MaxTabsPerPage — beyond this the reader is navigating a site rather than
	// reading a page, and the bar cannot be scanned at the narrow breakpoint.
	// MaxPanelsPerPage is 24, so this still admits three panels a tab.
	MaxTabsPerPage = 8
)

// PageTab is one tab of a page: the name drawn on the bar, and the ids of the
// panels that render under it, in spec order.
type PageTab struct {
	// Name is the tab name exactly as authored, minus surrounding whitespace.
	// Never case-folded — see normaliseTab.
	Name string
	// PanelIDs are this tab's panels, in the order the spec declares them.
	PanelIDs []string
}

// Tabs returns the page's tabs in bar order, with every panel assigned to
// exactly one of them.
//
// Bar order is first appearance. A panel declaring no tab lands on the first
// tab. A page where no panel declares a tab returns nil — there is no bar, and
// the caller renders the flat grid it always did.
//
// This is the one authority for both rules; the client's derivePageTabs
// (hooks/use-pages.ts) mirrors it, and the tests on both sides are written in
// the same terms so the two cannot drift silently.
func Tabs(panels []PanelSpec) []PageTab {
	var order []string
	index := map[string]int{}
	for i := range panels {
		name := strings.TrimSpace(panels[i].Tab)
		if name == "" {
			continue
		}
		if _, seen := index[name]; seen {
			continue
		}
		index[name] = len(order)
		order = append(order, name)
	}
	if len(order) == 0 {
		return nil
	}

	out := make([]PageTab, len(order))
	for i, name := range order {
		out[i] = PageTab{Name: name}
	}
	for i := range panels {
		name := strings.TrimSpace(panels[i].Tab)
		at := 0 // no tab declared: the first tab, not a tab of its own.
		if name != "" {
			at = index[name]
		}
		out[at].PanelIDs = append(out[at].PanelIDs, panels[i].ID)
	}
	return out
}

// normaliseTab trims a declared tab name.
//
// Trimming only — no case folding — for the reason the icon is not folded
// either (spec.go): `tab: Odezva` silently becoming `odezva` teaches an author
// a spelling that is not what they wrote, and the bar would then draw a word
// nobody typed. Two names that differ ONLY by case are refused below instead,
// because a bar with "Odezva" and "odezva" on it is two tabs a reader sees as
// one.
func normaliseTab(raw string) string { return strings.TrimSpace(raw) }

// validatePageTabs normalises every declared tab in place and checks the page's
// tab bar as a whole.
//
// Page-scoped, like validatePageActions, because two of its rules cannot be
// answered inside a per-panel loop: how many distinct tabs the page declares,
// and whether two of them collide.
func validatePageTabs(panels []PanelSpec) error {
	// firstSpelling maps the folded name to the spelling that claimed it, so a
	// collision can name both.
	firstSpelling := map[string]string{}
	var distinct int

	for i := range panels {
		p := &panels[i]
		raw := p.Tab
		name := normaliseTab(raw)
		p.Tab = name

		if raw != "" && name == "" {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q declares a blank tab; a tab is a word on a bar, and a blank one draws "+
					"nothing while still hiding the panels under it — omit the key to leave the "+
					"panel on the first tab", p.ID)
		}
		if name == "" {
			continue
		}
		for _, r := range name {
			if unicode.IsControl(r) {
				return newError(CodeInvalidSpec, p.Schema,
					"panel %q declares tab %q, which contains a control character; a tab name is "+
						"one line of text on a bar", p.ID, name)
			}
		}
		if n := len([]rune(name)); n > MaxTabNameRunes {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q declares a tab name of %d characters; the cap is %d — a tab is named by "+
					"a word, and a longer one pushes every other tab off a phone screen",
				p.ID, n, MaxTabNameRunes)
		}

		folded := strings.ToLower(name)
		claimed, seen := firstSpelling[folded]
		switch {
		case !seen:
			firstSpelling[folded] = name
			distinct++
		case claimed != name:
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q declares tab %q, and this page already has a tab %q; two names that "+
					"differ only in case are two tabs a reader sees as one", p.ID, name, claimed)
		}
	}

	if distinct > MaxTabsPerPage {
		return newError(CodeInvalidSpec, "",
			"%d tabs; the cap is %d — beyond that the bar cannot be scanned, and the page is a "+
				"site rather than a page", distinct, MaxTabsPerPage)
	}
	return nil
}
