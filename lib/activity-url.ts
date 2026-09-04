// /activity — the URL is the state.
//
// The page reads its position (the walk: a workflow, an issue, a run…), its
// lens (Workflows / Issues / Agents / Routines), its status bucket and the
// opened record from the query string, and writes every change back. Before
// this it read the legacy deep link once at mount and never wrote anything:
// a reload lost the walk, Back left the page, and no drill-down had a URL a
// person could send.
//
// Two spellings coexist on purpose:
//
//   ?run=<id> · ?pipeline=<slug>&run=<id> · ?mission=<id> · ?status=<scope>
//       the vocabulary fourteen places across the product already link with
//       (lib/activity-deeplink). A single stop of one of those kinds — or the
//       routine→run pair — is written in it, so a URL copied off this page
//       reads the same as the one the routine card built.
//   ?walk=kind:id/kind:id
//       everything else — a workflow, an agent, a crew, or a walk deeper than
//       the legacy params can spell. Ids only; the shell re-labels stops as
//       the entities they name load.
//
// Pure, like activity-deeplink: the round trip is the part worth testing, and
// it should not need a mounted page with an SSE stream in it.

import { activityDeepLink } from "@/lib/activity-deeplink"
import { ACTIVITY_HOME, type ActivityPath, type ActivityStop } from "@/lib/activity-selection"

export type ActivityLens = "workflows" | "issues" | "agents" | "routines"

export interface ActivityUrlState {
  path: ActivityPath
  lens: ActivityLens
  /** A status bucket; undefined means "all". */
  scope?: string
  /** The opened journal record, by id. */
  entryId?: string
}

const LENSES: ReadonlySet<string> = new Set<ActivityLens>(["workflows", "issues", "agents", "routines"])

/** Legacy param for a stop kind, when the deep-link vocabulary has one. */
const LEGACY_PARAM: Record<string, string> = { issue: "mission", routine: "pipeline", run: "run" }

/** Stop kinds whose id can safely travel in a path segment. */
function encodeStop(s: ActivityStop): string {
  return `${encodeURIComponent(s.kind)}:${encodeURIComponent(s.id)}`
}

function decodeStop(segment: string): ActivityStop | null {
  const i = segment.indexOf(":")
  if (i <= 0) return null
  let kind: string
  let id: string
  try {
    kind = decodeURIComponent(segment.slice(0, i))
    id = decodeURIComponent(segment.slice(i + 1))
  } catch {
    // A hand-edited or truncated `%` sequence is not a stop; the walk simply
    // does not include it rather than the page failing to mount.
    return null
  }
  if (!kind || !id) return null
  // The id is the honest label until the shell resolves a name for it.
  return { kind, id, label: id }
}

/**
 * Is this walk spellable in the legacy vocabulary?
 *
 * One stop of a legacy kind, or the routine→run pair the routine card builds.
 * Anything else — two runs, a workflow, an agent — needs `walk=`.
 */
function legacyParams(path: ActivityPath): Record<string, string> | null {
  const stops = path.stops
  if (path.dropped > 0) return null
  if (stops.length === 1 && LEGACY_PARAM[stops[0].kind]) {
    return { [LEGACY_PARAM[stops[0].kind]]: stops[0].id }
  }
  if (stops.length === 2 && stops[0].kind === "routine" && stops[1].kind === "run") {
    return { pipeline: stops[0].id, run: stops[1].id }
  }
  return null
}

/**
 * The query string for a state. Empty for the overview with nothing set, so
 * the home URL stays `/activity`.
 */
export function activityUrlParams(state: ActivityUrlState): URLSearchParams {
  const params = new URLSearchParams()
  const legacy = legacyParams(state.path)
  if (legacy) {
    for (const [k, v] of Object.entries(legacy)) params.set(k, v)
  } else if (state.path.stops.length > 0) {
    params.set("walk", state.path.stops.map(encodeStop).join("/"))
  }
  if (state.lens !== "workflows") params.set("lens", state.lens)
  if (state.scope && state.scope !== "all") params.set("status", state.scope)
  if (state.entryId) params.set("entry", state.entryId)
  return params
}

/** `pathname?query`, or the bare pathname when there is nothing to say. */
export function activityUrl(pathname: string, state: ActivityUrlState): string {
  const qs = activityUrlParams(state).toString()
  return qs ? `${pathname}?${qs}` : pathname
}

/**
 * Read a URL back into state. `walk=` wins over the legacy params when both
 * are present (this page never writes both); a legacy-only URL goes through
 * the deep-link mapping so every existing inbound link still lands.
 */
export function parseActivityUrl(params: URLSearchParams): ActivityUrlState {
  let path: ActivityPath = ACTIVITY_HOME
  let scope: string | undefined

  const walk = params.get("walk")
  if (walk) {
    const stops = walk.split("/").map(decodeStop).filter((s): s is ActivityStop => s !== null)
    if (stops.length > 0) path = { stops, dropped: 0 }
    const status = params.get("status")?.trim()
    scope = status && status !== "all" ? status : undefined
  } else {
    const legacy = activityDeepLink(params)
    if (legacy?.path) path = legacy.path
    scope = legacy?.scope
  }

  const lensRaw = params.get("lens")
  const lens: ActivityLens = lensRaw && LENSES.has(lensRaw) ? (lensRaw as ActivityLens) : "workflows"
  const entry = params.get("entry")?.trim()
  return { path, lens, scope, entryId: entry ? entry : undefined }
}

/** Two states that would write the same URL. */
export function sameActivityUrlState(a: ActivityUrlState, b: ActivityUrlState): boolean {
  return activityUrlParams(a).toString() === activityUrlParams(b).toString()
}
