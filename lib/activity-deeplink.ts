// The URLs /activity has always accepted.
//
// This route was the run-trace canvas for its whole life, and fourteen places
// across the product still link into it with a query string it defined:
//
//   ?run=<id>            an inbox waitpoint, a routine's run rows, the bell
//   ?pipeline=<slug>     a routine's "view runs"
//   ?pipeline=&run=      routine-card-detail, so back lands on the routine
//   ?mission=<id>        an issue card's "activity"
//   ?status=active       the bell's "view all"
//   ?run=&step=          the canvas's own selection, copied out of the URL bar
//
// The canvas is gone and the stream replaced it. Those links were written
// against the page that no longer exists, and the failure mode of ignoring them
// is the quiet one: every URL still returns 200, still renders, and simply does
// not go where it said. Nothing logs it, and the reader concludes the run they
// clicked has no trace.
//
// So the parameters are translated rather than dropped. The vocabularies line
// up almost exactly, because both pages are about the same nouns: a run, a
// routine, an issue. Where they do not line up this module says so instead of
// approximating — see `step` below.
//
// Pure, and separate from the view, because these are the mappings worth
// testing. Asserting them through a mounted page would need a router.

import { ACTIVITY_HOME, openStop, type ActivityPath, type ActivityStop } from "@/lib/activity-selection"

/**
 * What a deep link asked for: somewhere to be, or a narrowing, or both.
 *
 * Null for a bare `/activity`. Not "the overview" — the caller has to tell "no
 * deep link" from "a deep link that asked for the overview", or arriving with
 * no query string would clear state the reader came with.
 */
export interface ActivityDeepLink {
  /** Where to land, already shaped as a path so Back has somewhere to go. */
  path: ActivityPath | null
  /** Which status segment to select, when the link named one. */
  scope?: string
}

/**
 * The status values the segments can draw.
 *
 * A closed set, matched exactly. The bell only ever writes `active`, but a URL
 * is user-editable and a link written against some other vocabulary should land
 * on everything rather than on nothing — a page that renders empty because it
 * did not recognise a word is indistinguishable from a page with no data.
 */
const SCOPES = new Set(["active", "waiting", "failed", "done"])

/**
 * The parameters that name a place, from least to most specific.
 *
 * Order is the whole design. `?pipeline=<slug>&run=<id>` is one link, built by
 * routine-card-detail so a reader arrives at the run WITHOUT losing the routine
 * they came from — pushing the routine first and the run second is what makes
 * Back land on the routine rather than on the overview. Read in the other
 * order, the same URL would strand them.
 */
const PLACES: readonly { param: string; kind: ActivityStop["kind"] }[] = [
  { param: "mission", kind: "issue" },
  { param: "pipeline", kind: "routine" },
  { param: "run", kind: "run" },
] as const

/** A parameter value that names something, or undefined. */
function named(params: URLSearchParams, key: string): string | undefined {
  const raw = params.get(key)?.trim()
  // `?run=` is what a builder emits when its id was undefined. Treating that as
  // a destination lands the reader on a page headed by an empty string.
  return raw ? raw : undefined
}

/**
 * Reads the legacy query string into a place and a narrowing.
 *
 * The label on each stop is the id itself. The shell re-labels a stop the
 * moment it can resolve one — the routine's human name, the issue's identifier
 * — and an id is what the reader pasted, so it is the honest placeholder in the
 * meantime. Inventing "Run" or "Loading…" would put a word on the breadcrumb
 * that no link and no row ever said.
 *
 * `step` is deliberately unread. The canvas focused one step in a side panel;
 * the stream has no such object, because a run's steps ARE its drill-down.
 * Landing on the run and ignoring the step is the closest true answer;
 * pretending to honour it, or refusing the whole link over it, are both worse.
 */
export function activityDeepLink(params: URLSearchParams): ActivityDeepLink | null {
  let path: ActivityPath | null = null
  for (const { param, kind } of PLACES) {
    const id = named(params, param)
    if (!id) continue
    path = openStop(path ?? ACTIVITY_HOME, { kind, id, label: id })
  }

  const status = named(params, "status")
  const scope = status && SCOPES.has(status) ? status : undefined

  if (!path && !scope) return null
  return scope ? { path, scope } : { path }
}
