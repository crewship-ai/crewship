package api

// Pages — `refresh:`, the panel that pulls itself (docs/prd/pages.md §12 v1.1,
// and the worked example at §6).
//
// internal/pages/refresh.go decides what is SAYABLE. This file is what makes it
// true, and the whole of it is one sentence: a `refresh:` declaration compiles
// to an `automations` row whose action runs the panel's producer routine.
//
// WHY THE SAME SUBSTRATE AS `wake:`, AND NOT A SECOND ONE.
// A wake gate compiles to an `automations` row with `action_kind = issue`
// (pages_wake.go). A refresh compiles to one with `action_kind = routine` —
// which is the ORIGINAL action of that table, the one `crewship automation
// create` has always written. So refresh is not an extension of the substrate
// at all; it is the substrate's primary use, reached through a page's YAML
// instead of through the automations API. Everything that path already does —
// in-memory matching off the journal write path, coalescing a storm of matches
// into one enqueue, `debounce_seconds`, `max_per_hour`, and chain-depth pricing
// against pipeline.GuardChainDepth — applies to a refresh for free, and there
// is still exactly one eventing path in the process.
//
// The alternative was a hook in the push path and in the save path that called
// the enqueuer directly. It would have been fewer lines and it would have had
// no debounce, no rate cap and, most importantly, NO LOOP PRICING — which is
// the one thing a "run the producer when the page changes" feature must not be
// missing.
//
// THE TWO EVENTS.
//
//	on:wake            → journal `page.wake.fired`, matched on this page_id.
//	                     Emitted by pages_wake_issue.go when a gate actually
//	                     opens an issue — once per OPEN alert, not once per
//	                     push, because page_panel_alerts arbitrates that. So the
//	                     refresh inherits edge-triggering from the alert table
//	                     rather than re-implementing it.
//	on:panels-changed  → journal `page.spec.changed`, matched on this page_id.
//	                     Emitted below, by the create and update paths, ONLY
//	                     when the panel list actually differs.
//
// Both matchers are `page_id` and nothing else, which is what makes this one
// rule per refreshing panel rather than one per (panel × gate). automation's
// matcher is exact-equality only (deliberately — it runs on the journal write
// path), so "any gate on this page" has to be a property of the EVENT, not a
// disjunction in the rule. `page.wake.fired` already is one: it is emitted per
// firing gate and carries the page it fired on.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/automation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
)

// refreshAutomationID is the deterministic id of one panel's refresh rule.
//
// It shares wakeAutomationPagePrefix with the gates' rules on purpose: the
// reconcile scans that prefix to find everything the page owns, and the delete
// path scans it to remove everything the page owned. A refresh rule under its
// own prefix would be invisible to both, so removing `refresh:` from a spec
// would leave the routine firing forever with nothing in the document to
// explain it.
//
// The `r` before the panel hash is what keeps a refresh rule and a gate rule on
// the same panel from colliding: a gate's suffix is `_<n>` and a refresh has no
// ordinal, so without the marker `panel_1` as a gate and a panel hashing to the
// same first eight hex characters would be one id.
func refreshAutomationID(pageID, panelID string) string {
	return wakeAutomationPagePrefix(pageID) + "r" + shortHash(panelID, 8)
}

// buildRefreshAutomation turns one compiled trigger into the rule that runs it.
//
// The routine is read from the PANEL'S PRODUCER (pages.RefreshTrigger), never
// from a field of its own. A page may only run the routine it has already
// declared writes that panel — otherwise `refresh:` would be a way to run any
// routine in the workspace from a document that never says that routine
// produces anything.
func buildRefreshAutomation(wsID, pageID, pageSlug string, t pages.RefreshTrigger, authorUserID string) automation.Automation {
	eventType := string(journal.EntryPageSpecChanged)
	why := "the page's panels changed"
	// Rendered with pipeline.Render, the same renderer routine steps use — there
	// is deliberately no second templating language.
	//
	// The routine is told WHICH panel it is expected to write and WHY it ran,
	// because a producer that has to infer either from its own configuration is a
	// producer that goes on writing the wrong panel after somebody edits the page.
	inputs := map[string]any{
		"page":    pageSlug,
		"panel":   t.PanelID,
		"refresh": string(t.On),
		"reason":  why,
	}
	if t.On == pages.RefreshOnWake {
		eventType = string(journal.EntryPageWakeFired)
		why = "a wake gate on the page fired"
		inputs["reason"] = why
		// Only on this event, and only because `page.wake.fired` carries them.
		// A template naming a key the event does not have is a render against
		// nothing, and the honest shape for "the page was edited" is an input
		// that is not there rather than one that is empty.
		inputs["trigger_panel"] = "{{ event.payload.panel }}"
		inputs["issue"] = "{{ event.payload.issue_identifier }}"
	}
	return automation.Automation{
		ID:          refreshAutomationID(pageID, t.PanelID),
		WorkspaceID: wsID,
		Name:        fmt.Sprintf("page %s/%s refresh %s", pageSlug, t.PanelID, t.On),
		Enabled:     true,
		EventType:   eventType,
		Matcher: automation.Matcher{
			// page_id and nothing else, for the reason in the file header —
			// and page_id rather than the slug, exactly as the gates do it: a
			// rule keyed on an id cannot be captured by a page created later
			// with a recycled slug.
			PayloadEquals: map[string]any{"page_id": pageID},
		},
		ActionKind: automation.ActionKindRoutine,
		Action: automation.Action{
			RoutineSlug: t.RoutineSlug,
			Inputs:      inputs,
		},
		DebounceSeconds: automation.DefaultDebounceSeconds,
		MaxPerHour:      automation.DefaultMaxPerHour,
		CreatedBy:       authorUserID,
	}
}

// ── The arrangement fingerprint ────────────────────────────────────────────

// pageArrangementFingerprint is a stable digest of the page's PANEL LIST.
//
// It answers exactly one question — "did the arrangement change?" — and it is
// the loop guard for `on:panels-changed`. A routine woken by the event may
// itself hold `page.write` and re-apply a spec; if the spec it applies is the
// same one, the fingerprint is unchanged, no event is emitted, and the circle
// closes after a single lap. Without it, `crewship apply` in a loop would be a
// self-sustaining refresh.
//
// It covers the panel list and NOT the page's metadata, which is the field
// `on:panels-changed` names: renaming a page is not an arrangement change, and
// a producer re-running because somebody fixed a typo in the title is exactly
// the noise this feature would otherwise become.
//
// The whole marshalled panel slice rather than a hand-picked tuple of fields,
// deliberately: a hand-picked list is the fifth instance of the bug this branch
// has already had four of — a field enumerated by hand and forgotten in one
// path. Adding a panel field can make this digest MORE sensitive, which costs a
// spurious refresh; it can never make it silently blind.
func pageArrangementFingerprint(doc *pages.Document) string {
	if doc == nil {
		return ""
	}
	raw, err := json.Marshal(doc.Spec.Panels)
	if err != nil {
		// Unreachable for a document that just round-tripped through
		// spec_json. An empty fingerprint compares unequal to every real one,
		// so the failure mode is "emit the event", which is the safe direction:
		// a refresh that fires when it need not is noise, one that does not
		// fire when it should is the silence this feature exists to end.
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// emitPageSpecChanged writes the journal entry `refresh: on:panels-changed` is
// armed on.
//
// MUST be called AFTER the page transaction has committed AND after
// refreshAutomations() has reloaded the registry. The order is the whole
// correctness of a page created with a refresh on it: the registry matches in
// memory, so an entry emitted before the reload finds no rule and the first
// arrangement change a page ever has would silently fire nothing.
//
// Best effort with respect to the response, like every other Pages journal
// write: the page is saved, and refusing the save now would tell an author
// their edit failed when it did not.
func (h *PageHandler) emitPageSpecChanged(ctx context.Context, wsID string, rec *pageRecord, doc *pages.Document, created bool, fingerprint string) {
	if h.journal == nil {
		return
	}
	verb := "changed"
	if created {
		verb = "created"
	}
	panelIDs := make([]string, 0, len(doc.Spec.Panels))
	for i := range doc.Spec.Panels {
		panelIDs = append(panelIDs, doc.Spec.Panels[i].ID)
	}
	if _, err := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: wsID,
		Type:        journal.EntryPageSpecChanged,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		ActorID:     "pages",
		Summary: fmt.Sprintf("page %s %s: %d panel(s) — %s",
			rec.Slug, verb, len(panelIDs), strings.Join(panelIDs, ", ")),
		Payload: map[string]any{
			"page":    rec.Slug,
			"page_id": rec.ID,
			"panels":  panelIDs,
			"created": created,
			// The digest is carried so "why did this fire" is answerable from
			// two consecutive entries rather than from the spec history.
			"fingerprint": fingerprint,
		},
	}); err != nil && h.logger != nil {
		h.logger.Warn("pages: journal entry for an arrangement change was not written",
			"page", rec.Slug, "error", err)
	}
}
