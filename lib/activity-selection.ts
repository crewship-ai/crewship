// What /activity-new is currently pointed at — one value, one answer.
//
// The page can be aimed at four things: a workflow (one chain, drawn as a
// graph), or an issue / routine / crew (a lens over the same activity feed).
// They were two independent useStates in activity-stream-view: an `EntityFocus`
// for the rail and a `selectedChain` for the graph. Neither cleared the other,
// so the chips row could read "routine: Normalize dates to ISO 8601" while the
// card below it still drew the on-close-file-followup chain — two answers to
// "what am I looking at", and the graph was the stale one.
//
// The fix is not to make the two states notify each other; two sources of truth
// that must be kept in sync IS the bug. There is one selection, and everything
// on screen — the rail's highlight, the journal query's narrowing, the graph's
// anchor, the chip that clears it — is DERIVED from it here. A stale graph is
// then not a bug to be fixed but a state that cannot be represented.
//
// Pure and DOM-free on purpose: the selection rules are the part worth testing,
// and they should not require mounting a page with an SSE stream in it.

/** Entity kinds that narrow the activity feed itself. */
export type ActivityEntityKind = "issue" | "routine" | "crew"

/**
 * The rail's entity focus.
 *
 * Structurally the sidebar's `EntityFocus`, restated rather than imported: this
 * module is pure and must not depend on a component. The same reason
 * `lib/activity-stream.ts` restates `FocusRef`.
 */
export interface ActivityEntityFocus {
  kind: ActivityEntityKind
  id: string
  label: string
}

export type ActivitySelectionKind = ActivityEntityKind | "workflow"

/**
 * One selection. `id` is whatever that kind is keyed by — a chain origin for a
 * workflow, a mission id for an issue, a slug for a routine, a crew id for a
 * crew — and `label` is what the reader saw when they clicked it.
 */
export interface ActivitySelection {
  kind: ActivitySelectionKind
  id: string
  label: string
}

/**
 * The chip that names the current selection in the chips row.
 *
 * `narrows` separates a filter from a view. An issue, routine or crew narrows
 * what the feed holds; a workflow does not — the journal carries no
 * `chain_origin` (it lives on `pipeline_runs`), so a chain cannot be expressed
 * as a filter over journal rows, and picking one re-points the GRAPH only. The
 * empty-result banner counts filters to say "the window holds N events and none
 * satisfies all M filters at once"; counting a workflow there would make that
 * sentence false.
 */
export interface ActivitySelectionChip {
  label: string
  narrows: boolean
}

export interface ActivitySurface {
  /** The rail's focus, and the narrowing the journal query applies. */
  focus: ActivityEntityFocus | null
  /** What the topology card is anchored on, or null when no graph is shown. */
  chainAnchor: string | null
  /** The heading over that graph. Null exactly when `chainAnchor` is. */
  chainLabel: string | null
  /** The one chip that names the selection, or null when nothing is selected. */
  chip: ActivitySelectionChip | null
}

const EMPTY: ActivitySurface = { focus: null, chainAnchor: null, chainLabel: null, chip: null }

/**
 * Everything the screen shows about the current selection, derived in one place.
 *
 * A workflow and an entity focus are different KINDS of answer — a workflow is
 * one specific chain, a routine focus is a filter over a loaded window — so
 * picking either supersedes the other rather than intersecting with it. The
 * supersession is visible: the chips row shows the one selection that is in
 * effect, so a graph on screen always has a chip naming it, and a chip that
 * names an entity never has a graph under it.
 */
export function activitySurface(selection: ActivitySelection | null): ActivitySurface {
  if (!selection) return EMPTY

  if (selection.kind === "workflow") {
    return {
      focus: null,
      chainAnchor: selection.id,
      chainLabel: selection.label,
      chip: { label: `workflow: ${selection.label} (graph only)`, narrows: false },
    }
  }

  return {
    focus: { kind: selection.kind, id: selection.id, label: selection.label },
    chainAnchor: null,
    chainLabel: null,
    chip: {
      // A routine slug is not indexed by the journal, so its narrowing only
      // covers what was fetched. The chip has always said so; keep saying it.
      label:
        selection.kind === "routine"
          ? `routine: ${selection.label} (loaded window)`
          : `${selection.kind}: ${selection.label}`,
      narrows: true,
    },
  }
}

/**
 * What to call a chain in the chip and over its graph.
 *
 * `undefined` is reachable: the chains index is fetched once and not streamed,
 * so a row can be clicked and then swept from the list before the label is
 * read. An empty heading over a graph reads as a rendering fault, so it falls
 * back to naming the thing generically instead.
 */
export function workflowLabel(
  chain: { routine_slug?: string; started_by?: string } | undefined,
): string {
  return chain?.routine_slug || chain?.started_by || "this workflow"
}
