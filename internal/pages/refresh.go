package pages

import (
	"strings"
)

// `refresh:` — the panel that pulls itself (PRD §12 v1.1, and the worked
// example at §6, docs/prd/pages.md:422).
//
// ## What it MEANS, decided here rather than left open
//
// §12 gives the feature one line — "`refresh: on:wake`, `on:panels-changed`" —
// and the example puts `refresh: on:wake` on a `narrative.v1` panel produced by
// `routine/incident-rozbor`, next to a `status.v1` panel whose gate wakes
// crew/devops. Read together those two say exactly one thing:
//
//	refresh: <event> — RUN THIS PANEL'S PRODUCER when <event> happens.
//
// It is a TRIGGER declaration, not a hint. That is the only reading worth
// implementing, because the alternative — a hint the client uses to decide how
// eagerly to poll — describes a system Pages does not have: there is nothing to
// poll. A page holds no query and no datasource (§1, §5); the only way a
// panel's contents can change is a producer pushing to it. So a `refresh:` that
// does not run the producer cannot refresh anything, and a field the server
// stores and never acts on is worse than no field at all, because the author
// believes it works.
//
// The two events:
//
//   - `on:wake` — a wake gate ANYWHERE ON THIS PAGE fired. That is the §6
//     example: the cheap script notices `critical`, the gate opens an issue on
//     crew/devops, and the narrative panel's routine runs so the analysis is
//     already on the page when the human arrives. Page-scoped rather than
//     gate-scoped because the panel that is refreshed is rarely the panel that
//     is gated — in the PRD's own example they are two different panels.
//   - `on:panels-changed` — the page's ARRANGEMENT changed: a panel was added,
//     removed, reordered, or had its contract edited. A producer that renders
//     something about the page itself ("what is on this board") is stale the
//     moment somebody edits it, and an edit is the one event a page can see
//     without asking anybody.
//
// ## Where it is ENFORCED
//
// Not here. This file decides what is sayable; internal/api/pages_refresh.go
// compiles each declaration into an `automations` row — the same substrate
// `wake:` compiles to (§5), with `action_kind = routine` instead of `issue`.
// That is deliberate and is the whole reason the semantics above are
// implementable in one file: internal/automation already does journal event →
// in-memory matcher → coalesced, debounced, rate-limited, depth-priced enqueue.
// A second eventing path for "run a routine when something happened" would be
// the same feature twice, and only one of the two would have the loop guard.
//
// ## What is REFUSED, and why each refusal exists
//
// All four are refused at SAVE time with a message naming the rule, because
// every one of them describes a declaration that would otherwise be stored,
// believed, and never act:
//
//  1. A value outside the closed set. Same rule as PanelSchema and PanelIcon:
//     an open string means a page saves with `refresh: on:push` and nothing
//     ever happens, forever, silently.
//  2. A producer the server cannot run. Crewship cannot execute somebody's
//     shell script (`script/`), cannot call an inbound-webhook producer
//     (`webhook/` is a door INTO Crewship, not out of it), and does not
//     dispatch an `agent/` — an agent is woken by a gate's `writes:`, which is
//     what §5 is for. Only `routine/` names something the automation substrate
//     can enqueue. Refused rather than accepted as documentary: a `refresh:` a
//     reader can see and the server ignores is the failure this whole file is
//     written against.
//  3. `on:wake` on a page with no wake gate on it. The declaration can never
//     fire; the author has written the second half of a mechanism whose first
//     half is missing, and the honest moment to say so is now.
//  4. `on:wake` on a panel that declares its own `wake:` gates — the loop. Its
//     producer runs, pushes to the panel, the push arms the panel's own gate,
//     the gate fires, and the producer runs again. `page_panel_alerts` blunts
//     it (a gate fires once per open alert, not once per push) and the
//     substrate's `max_per_hour` and chain-depth pricing bound it, but a cycle
//     that is visible in the document should be refused in the document. See
//     ValidateRefresh for the residual cycle that is NOT statically visible.

// PanelRefresh is the closed vocabulary of `refresh:`.
//
// Closed for the reason PanelSchema and PanelIcon are, one step sharper: those
// two fail visibly (an unknown schema renders a fallback that says so), while
// an unrecognised trigger fails as SILENCE — the page saves, the panel renders,
// and the routine the author expected to run never does.
type PanelRefresh string

const (
	// RefreshOnWake runs the panel's producer when a wake gate on this page
	// fires — §5's `page.wake.fired`, which is emitted once per opened alert
	// and not once per push.
	RefreshOnWake PanelRefresh = "on:wake"

	// RefreshOnPanelsChanged runs the panel's producer when the page's panel
	// list changes: added, removed, reordered, or edited. Page metadata — the
	// name, the description — is not an arrangement change.
	RefreshOnPanelsChanged PanelRefresh = "on:panels-changed"
)

// PanelRefreshes is the vocabulary in the order a refusal lists it.
var PanelRefreshes = []PanelRefresh{RefreshOnWake, RefreshOnPanelsChanged}

var knownPanelRefreshes = func() map[PanelRefresh]bool {
	m := make(map[PanelRefresh]bool, len(PanelRefreshes))
	for _, r := range PanelRefreshes {
		m[r] = true
	}
	return m
}()

// Known reports whether the value is in the closed set.
func (r PanelRefresh) Known() bool { return knownPanelRefreshes[r] }

func (r PanelRefresh) String() string { return string(r) }

// PanelRefreshList renders the vocabulary for a refusal. A closed set whose
// error does not name its members is a set the author has to go and look up.
func PanelRefreshList() string {
	names := make([]string, 0, len(PanelRefreshes))
	for _, r := range PanelRefreshes {
		names = append(names, string(r))
	}
	return strings.Join(names, ", ")
}

// RefreshTrigger is one compiled `refresh:` declaration — everything the API
// layer needs to build its `automations` row.
//
// RoutineSlug is resolved out of the panel's `producer:` and never out of a
// second field, because "which routine" is not a question `refresh:` is allowed
// to answer. The panel already names exactly one principal permitted to write
// it (§7.1 rule 4); letting `refresh:` name another would be a way to run a
// routine from a page without the page saying that routine produces anything.
type RefreshTrigger struct {
	// PanelID is the panel whose producer runs.
	PanelID string
	// On is the event that runs it.
	On PanelRefresh
	// RoutineSlug is the panel's producer routine, without its `routine/`
	// prefix — the slug internal/automation resolves to a pipeline.
	RoutineSlug string
}

// RefreshTriggers returns every compiled declaration on the document, in spec
// order.
//
// It assumes the document has been VALIDATED: ValidateRefresh has already
// refused every producer kind that is not a routine, so a panel that reaches
// the append below has a routine to name. A panel whose producer does not split
// is skipped rather than guessed at — the caller of an unvalidated document
// gets nothing rather than a rule pointing at a routine called "".
func RefreshTriggers(d *Document) []RefreshTrigger {
	if d == nil {
		return nil
	}
	var out []RefreshTrigger
	for i := range d.Spec.Panels {
		p := &d.Spec.Panels[i]
		on := PanelRefresh(strings.TrimSpace(string(p.Refresh)))
		if on == "" || !on.Known() {
			continue
		}
		kind, ref, err := p.ProducerParts()
		if err != nil || kind != ProducerRoutine {
			continue
		}
		out = append(out, RefreshTrigger{PanelID: p.ID, On: on, RoutineSlug: ref})
	}
	return out
}

// ValidateRefresh normalises every declared `refresh:` in place and checks
// the page's declarations as a whole.
//
// Page-scoped, like validatePageTabs and validatePageActions, because rule 3
// cannot be answered inside a per-panel loop: whether ANY panel on this page
// declares a wake gate is a property of the page.
//
// ## The residual cycle this cannot see
//
// Rule 4 below closes the cycle that is visible in the document — a panel that
// refreshes on its own gate. It cannot close the one that is not: panel A
// declares a gate, panel B declares `refresh: on:wake`, and B's producer
// routine happens to push to A. Nothing in the spec says what a routine writes
// (a producer is a principal, not a program we can read), so that arrangement
// is unknowable here and is left to the substrate that was built for it —
// `page_panel_alerts` makes a gate fire once per OPEN alert rather than once
// per push, internal/automation prices every hop against
// pipeline.GuardChainDepth, and `max_per_hour` caps the rest. It is stated in
// docs/guides/pages.mdx rather than left to be discovered.
func ValidateRefresh(d *Document) error {
	// Rule 3's evidence, gathered once. ValidateGates has already run by the
	// time Document.Validate reaches here, so a gate counted below is a gate
	// that compiles.
	gated := false
	for i := range d.Spec.Panels {
		if len(d.Spec.Panels[i].Wake) > 0 {
			gated = true
			break
		}
	}

	for i := range d.Spec.Panels {
		p := &d.Spec.Panels[i]
		raw := string(p.Refresh)
		// Trimmed and written back before it is checked, so what validates is
		// exactly what is stored — the same bargain the icon makes in
		// Document.Validate. No case folding, for the same reason: `refresh:
		// On:Wake` silently becoming `on:wake` teaches a spelling that is not
		// the vocabulary, and the next value the author guesses is not forgiven.
		on := PanelRefresh(strings.TrimSpace(raw))
		p.Refresh = on

		if on == "" {
			// Written and then trimmed away — `refresh: "  "`. Refused rather
			// than treated as absent: the author wrote the key, and silently
			// dropping it is how a page ends up with a trigger its author is
			// certain they declared.
			if raw != "" {
				return newError(CodeInvalidSpec, p.Schema,
					"panel %q declares a blank refresh; omit the key rather than writing a trigger "+
						"with no event in it — one of: %s", p.ID, PanelRefreshList())
			}
			continue
		}

		// 1. The closed set.
		if !on.Known() {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q declares refresh %q; the set is closed — one of: %s. "+
					"`refresh:` RUNS the panel's producer when the named event happens, so a value "+
					"the server does not recognise is a routine that never runs and never says why",
				p.ID, on, PanelRefreshList())
		}

		// 2. A producer the server can actually run.
		kind, _, err := p.ProducerParts()
		if err != nil {
			// Unreachable: Document.Validate checked the producer above. The
			// refusal is kept rather than assumed away, because "unreachable"
			// is a claim about today's call order.
			return newError(CodeInvalidSpec, p.Schema, "panel %q: %v", p.ID, err)
		}
		if kind != ProducerRoutine {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q declares refresh %q, but its producer is %s — and `refresh:` RUNS the "+
					"producer, which the server can only do for a routine/<slug>. "+
					"%s. Remove the refresh, or move the work into a routine that pushes this panel",
				p.ID, on, kind, refreshProducerReason(kind))
		}

		if on != RefreshOnWake {
			continue
		}

		// 3. A trigger that can never fire.
		if !gated {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q declares refresh: %s, but no panel on this page declares a `wake:` gate — "+
					"nothing on this page can ever fire it. Add the gate that is supposed to wake "+
					"this producer, or refresh on %s instead",
				p.ID, RefreshOnWake, RefreshOnPanelsChanged)
		}

		// 4. The loop that is visible in the document.
		if len(p.Wake) > 0 {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q declares refresh: %s AND its own `wake:` gate; that is a loop — the "+
					"producer runs, pushes to this panel, the push arms this panel's gate, the gate "+
					"fires, and the producer runs again. Put the gate on the panel being WATCHED and "+
					"the refresh on the panel being WRITTEN, which is what the two are for",
				p.ID, RefreshOnWake)
		}
	}
	return nil
}

// refreshProducerReason says, per producer kind, why the server cannot run it.
// One sentence each, because "not supported" sends the author to the source.
func refreshProducerReason(kind ProducerKind) string {
	switch kind {
	case ProducerScript:
		return "a script/ producer is a path inside somebody's container and Crewship never executes it — " +
			"the script pushes to us, we do not call it"
	case ProducerWebhook:
		return "a webhook/ producer is a door INTO Crewship (§10b.5c) and there is nothing on the other " +
			"side of it to call"
	case ProducerAgent:
		return "an agent/ producer is woken by a `wake:` gate's `writes:` (§5), which is the mechanism " +
			"for asking an agent to write a panel"
	}
	return "only a routine/ producer names something the server can enqueue"
}
