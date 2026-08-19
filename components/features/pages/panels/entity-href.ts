/**
 * Entity ref → route, the ONE resolver on the Pages surface.
 *
 * PRD `docs/prd/pages.md` §8 rule 3: *"No free-form links. A narrative block may
 * reference an internal Crewship entity by id (issue, run, page, agent) and the
 * renderer builds the URL. It may not carry a URL."* §8b.1 says the same thing
 * about `kind: "link"` actions — a link action names an entity, never a
 * destination.
 *
 * Both consumers therefore need the same three properties, and a second copy of
 * them is a second chance to get one wrong:
 *
 *  1. the kind is narrowed through a Set, so an inherited key (`__proto__`,
 *     `constructor`) can never select a route,
 *  2. the id is refused unless it is a plain identifier — no scheme, no slash,
 *     no `..` — before it is interpolated, and encoded after,
 *  3. an unresolvable ref returns `null` rather than a half-built path, so the
 *     caller renders text instead of a link to somewhere it guessed.
 *
 * Slack AI's private-channel exfiltration was a rendered link; this file is the
 * reason there is nowhere for one to come from.
 */

import { ENTITY_REF_KINDS, type EntityRef, type EntityRefKind } from "./types"

export const ENTITY_ROUTES: Record<EntityRefKind, (id: string) => string> = {
  issue: (id) => `/issues/${id}`,
  run: (id) => `/activity?run=${id}`,
  page: (id) => `/pages/${id}`,
  // Both of these land on plain /crews on purpose, and dropping the id is the
  // point rather than a shortcut.
  //
  // The selection-driven /crews redesign deleted the whole /crews/agents
  // subtree and never had /crews/<id>: app/(dashboard)/crews/ is a single
  // page.tsx, and selection is carried in the query string as ?agent=<slug> /
  // ?crew=<slug> (hooks/use-crews-selection.tsx). Both are keyed on the SLUG.
  //
  // An EntityRef carries only an id, and per the rule the dead-route scan
  // states in full (app/(onboarding)/onboarding/__tests__/dead-agent-routes.ts)
  // passing an id where a slug is expected is worse than passing nothing: the
  // stale-selection watcher clears it and the reader lands on an empty canvas.
  // So a panel's entity link opens the roster, which is a real page, instead of
  // a URL that promises a record and 404s.
  agent: () => `/crews`,
  crew: () => `/crews`,
}

const ENTITY_REF_KIND_SET: ReadonlySet<string> = new Set<string>(ENTITY_REF_KINDS)

export function isEntityRefKind(value: unknown): value is EntityRefKind {
  return typeof value === "string" && ENTITY_REF_KIND_SET.has(value)
}

/**
 * The id is checked here as well as at the API boundary, and that is not
 * belt-and-braces theatre: a payload stored before this file existed, or one
 * arriving from a build whose server is older, must not be able to grow a
 * relative id into a path.
 */
export const SAFE_ENTITY_ID = /^[A-Za-z0-9][A-Za-z0-9_.:-]*$/

/** A ref this build can address, as an app-relative path — or `null`. */
export function entityHref(ref: EntityRef | null | undefined): string | null {
  if (!ref || typeof ref !== "object") return null
  const kind = ref.kind
  const id = typeof ref.id === "string" ? ref.id.trim() : ""
  if (!isEntityRefKind(kind) || !id || !SAFE_ENTITY_ID.test(id) || id.includes("..")) return null
  return ENTITY_ROUTES[kind](encodeURIComponent(id))
}
