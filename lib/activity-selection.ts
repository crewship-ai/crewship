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
 * One place the reader can be standing.
 *
 * Structurally an `ActivitySelection` with an open `kind`, because the kinds a
 * reader can walk INTO are not the four the rail offers: a chain graph node is
 * clicked as "kind:ref" (chain-canvas) and the walker emits issue, routine,
 * run, assignment, agent, inbox and automation — a list that has already grown
 * twice. An open kind means a kind added on the server appears here as itself,
 * narrowing to `<kind>_id`, rather than arriving as a shape this module has to
 * reject.
 */
export interface ActivityStop {
  kind: string
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

/**
 * Which of the three main columns the page draws.
 *
 * One selection, one column — they are alternatives, never a stack. The page
 * used to glue a workflow's graph ABOVE the global overview, so picking a
 * workflow changed the picture at the top and nothing under it: two different
 * workflows produced two screens identical below the graph, both still headed
 * "every crew, agent, routine and issue in one place". That is two pages, and
 * the second one was answering a question nobody had asked.
 *
 *   overview → what is happening across everything, optionally narrowed by an
 *              entity focus (an issue, a routine, a crew — a LENS over the
 *              same feed, which is why it keeps the overview rather than
 *              replacing it)
 *   workflow → that one chain, the whole column
 *   node     → one node out of a chain — an agent, a run, an assignment —
 *              as the slice of the window that mentions it
 */
export type ActivityMainColumn = "overview" | "workflow" | "node"

export interface ActivitySurface {
  /** Which column the shell renders. Exactly one. */
  main: ActivityMainColumn
  /** The rail's focus, and the narrowing the journal query applies. */
  focus: ActivityEntityFocus | null
  /** What the topology card is anchored on, or null when no graph is shown. */
  chainAnchor: string | null
  /** The heading over that graph. Null exactly when `chainAnchor` is. */
  chainLabel: string | null
  /** The node being read, set exactly when `main` is "node". */
  node: ActivityStop | null
  /** The one chip that names the selection, or null when nothing is selected. */
  chip: ActivitySelectionChip | null
}

const EMPTY: ActivitySurface = {
  main: "overview",
  focus: null,
  chainAnchor: null,
  chainLabel: null,
  node: null,
  chip: null,
}

const ENTITY_KINDS: ReadonlySet<string> = new Set<ActivityEntityKind>(["issue", "routine", "crew"])

function isEntityKind(kind: string): kind is ActivityEntityKind {
  return ENTITY_KINDS.has(kind)
}

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
export function activitySurface(selection: ActivityStop | null): ActivitySurface {
  if (!selection) return EMPTY

  if (selection.kind === "workflow") {
    return {
      main: "workflow",
      focus: null,
      chainAnchor: selection.id,
      chainLabel: selection.label,
      node: null,
      chip: { label: `workflow: ${selection.label} (graph only)`, narrows: false },
    }
  }

  if (!isEntityKind(selection.kind)) {
    // A node out of a chain: not one of the rail's four, so there is no focus
    // the journal query can express and no graph to anchor. It narrows the
    // loaded window to the rows that mention it — see `stopMatcher` — and it
    // DOES narrow, so the empty-result banner counts it and stays true.
    return {
      main: "node",
      focus: null,
      chainAnchor: null,
      chainLabel: null,
      node: selection,
      chip: { label: `${selection.kind}: ${selection.label}`, narrows: true },
    }
  }

  return {
    main: "overview",
    focus: { kind: selection.kind, id: selection.id, label: selection.label },
    chainAnchor: null,
    chainLabel: null,
    node: null,
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

/* ------------------------------------------------------------------ *
 *  The walk
 * ------------------------------------------------------------------ */

/**
 * How many stops the walk remembers, the overview aside.
 *
 * A bound is not decoration: `openStop` is driven by clicks on a graph, and a
 * reader bouncing between two nodes would otherwise grow the array — and the
 * breadcrumb rendered from it — for as long as they kept clicking. Six is deep
 * enough for the walks the graph actually offers (workflow → run → assignment
 * → agent → issue) and short enough that the trail still fits on one line.
 */
export const ACTIVITY_MAX_DEPTH = 6

/**
 * Where the reader is, and how they got there.
 *
 * The LAST stop decides the main column; everything before it exists so that
 * back has somewhere to go. It is one value, extending the one selection this
 * module already owned rather than sitting beside it — a "current view" state
 * and a separate "history" state are two sources of truth that must be kept in
 * sync, which is the exact bug the single selection replaced.
 *
 * `dropped` counts the stops that fell off the front at the depth cap. It is
 * carried rather than inferred because the trail must be able to say the walk
 * is longer than what it shows; a breadcrumb that silently begins in the
 * middle claims the reader started there.
 */
export interface ActivityPath {
  stops: readonly ActivityStop[]
  dropped: number
}

/** The overview: nothing selected, nothing to go back to. */
export const ACTIVITY_HOME: ActivityPath = Object.freeze({
  stops: Object.freeze([]) as readonly ActivityStop[],
  dropped: 0,
})

/** The stop that decides the column, or null at the overview. */
export function currentStop(path: ActivityPath): ActivityStop | null {
  return path.stops.length > 0 ? path.stops[path.stops.length - 1] : null
}

/**
 * Start a new walk — what the rail does.
 *
 * A rail pick REPLACES the path rather than pushing onto it. The rail is not a
 * step in a walk: it is how you choose which walk you are on, so keeping the
 * previous workflow behind an issue picked from the rail would make back lead
 * to a place the reader had already left.
 */
export function selectStop(stop: ActivityStop | null): ActivityPath {
  if (!stop) return ACTIVITY_HOME
  return { stops: [stop], dropped: 0 }
}

function sameStop(a: ActivityStop, b: ActivityStop): boolean {
  return a.kind === b.kind && a.id === b.id
}

/**
 * Walk one level down: a step, an issue, an agent, out of what is on screen.
 *
 * Three cases, in order:
 *
 *  - it is where you already are → nothing happens, so a double click on a
 *    node does not add a step you then have to press back through;
 *  - it is somewhere you have been → the path truncates back to it, so a loop
 *    between two nodes reads as returning rather than as descending forever;
 *  - otherwise it is pushed, and the oldest stop falls off at the cap. The
 *    OLDEST, never the newest: dropping the top instead would leave back at
 *    the boundary landing on a place the reader was never at.
 */
export function openStop(path: ActivityPath, stop: ActivityStop): ActivityPath {
  const current = currentStop(path)
  if (current && sameStop(current, stop)) return path

  const seen = path.stops.findIndex((s) => sameStop(s, stop))
  if (seen >= 0) return { stops: path.stops.slice(0, seen + 1), dropped: path.dropped }

  const next = [...path.stops, stop]
  const over = next.length - ACTIVITY_MAX_DEPTH
  if (over <= 0) return { stops: next, dropped: path.dropped }
  return { stops: next.slice(over), dropped: path.dropped + over }
}

/**
 * One level back — to where you came FROM, not to a fixed home.
 *
 * `dropped` survives a pop on purpose: those stops are gone and cannot be
 * walked back to, so the trail must keep saying the walk is longer than it
 * shows until the reader starts a new one.
 */
export function backFrom(path: ActivityPath): ActivityPath {
  // Emptying the walk IS being home, dropped stops included: there is nothing
  // left to say the trail is longer than it shows.
  if (path.stops.length <= 1) return ACTIVITY_HOME
  return { stops: path.stops.slice(0, -1), dropped: path.dropped }
}

/**
 * Jump straight to a crumb, by the depth it reports.
 *
 * Clamped rather than throwing: the argument comes from a rendered breadcrumb,
 * which can outlive the path it was rendered from by one click.
 */
export function jumpTo(path: ActivityPath, depth: number): ActivityPath {
  if (depth <= 0) return ACTIVITY_HOME
  if (depth >= path.stops.length) return path
  return { stops: path.stops.slice(0, depth), dropped: path.dropped }
}

/**
 * The workflow the reader is INSIDE, however deep the walk has gone.
 *
 * The rail highlights this rather than the current stop. A reader three levels
 * into a workflow is still in that workflow, and a rail that deselects the
 * moment they open a node is the screen giving two answers to "what am I
 * looking at" again — the highlight saying nothing is picked while the trail
 * says "workflow: on-close-file-followup › agent: ada".
 *
 * Derived from the same path, not tracked beside it.
 */
export function workflowAnchor(path: ActivityPath): string | null {
  const workflow = path.stops.find((s) => s.kind === "workflow")
  return workflow ? workflow.id : null
}

export interface ActivityCrumb {
  /** What the reader sees, and what tells them what they are looking at. */
  label: string
  /** The path length this crumb stands for — feed it back to `jumpTo`. */
  depth: number
  /** This is where they are now. Exactly one crumb has it. */
  current: boolean
}

export interface ActivityTrail {
  crumbs: ActivityCrumb[]
  /** Stops fell off the front; the walk is longer than these crumbs. */
  truncated: boolean
}

/** How a stop names itself in the trail. Kind first, because "ada" alone does not say what ada is. */
export function stopLabel(stop: ActivityStop): string {
  return `${stop.kind}: ${stop.label}`
}

/**
 * The trail, rooted at the overview.
 *
 * The root is always there, even at depth 0, because it is the answer to "what
 * am I looking at" — and at every other depth it is the one click back to
 * everything.
 */
export function activityTrail(path: ActivityPath): ActivityTrail {
  const crumbs: ActivityCrumb[] = [
    { label: "Overview", depth: 0, current: path.stops.length === 0 },
  ]
  path.stops.forEach((stop, i) => {
    crumbs.push({ label: stopLabel(stop), depth: i + 1, current: i === path.stops.length - 1 })
  })
  return { crumbs, truncated: path.dropped > 0 }
}

/* ------------------------------------------------------------------ *
 *  Narrowing the window to one node
 * ------------------------------------------------------------------ */

/** As much of a journal row as deciding "does this mention that node" needs. */
export interface NarrowableEntry {
  agent_id?: string | null
  crew_id?: string | null
  mission_id?: string | null
  payload?: Record<string, unknown> | null
  refs?: Record<string, unknown> | null
}

/** Kinds the journal carries as a COLUMN — the payload does not repeat them. */
const COLUMN_BY_KIND: Record<string, "agent_id" | "crew_id" | "mission_id"> = {
  agent: "agent_id",
  crew: "crew_id",
  issue: "mission_id",
}

/** Kinds whose payload key is not simply `<kind>_id`. */
const KEYS_BY_KIND: Record<string, string[]> = {
  // Both spellings, because the producers disagree — the executor writes
  // `pipeline_slug`, the newer surfaces write `routine_slug`, and both reach
  // the journal. Matching one silently drops half a routine's rows.
  routine: ["routine_slug", "pipeline_slug", "routine_id", "pipeline_id"],
  // The spine reads a step under either name (lib/activity-stream), so a step
  // opened FROM the spine must be findable under either too.
  step: ["step_id", "step"],
}

/**
 * Does this row belong to that node?
 *
 * The default is `<kind>_id`, which is the convention every producer follows
 * and the reason a kind added to the walker later still narrows to SOMETHING.
 * A kind that matches nothing yields an empty result the filter banner can
 * explain; a kind that matched everything would quietly show the reader the
 * whole workspace under a heading naming one agent.
 */
export function stopMatcher(stop: ActivityStop): (entry: NarrowableEntry) => boolean {
  const column = COLUMN_BY_KIND[stop.kind]
  const keys = KEYS_BY_KIND[stop.kind] ?? [`${stop.kind}_id`]
  return (entry) => {
    if (column && entry[column] === stop.id) return true
    const payload = entry.payload
    const refs = entry.refs
    return keys.some((k) => payload?.[k] === stop.id || refs?.[k] === stop.id)
  }
}
