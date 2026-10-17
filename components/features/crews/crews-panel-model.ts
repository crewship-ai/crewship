/**
 * What the /crews secondary panel shows (Harbor 2): Views that pick crews by
 * what they are doing, the crews grouped by status or A–Z, the open crew's
 * agents under it, and the agents without a crew. Pure functions; the panel
 * only draws what they return.
 */
import { WAITING_STATUSES, type ProvisioningState } from "./explorer-groups"

export type CrewView = "all" | "needs" | "working" | "idle" | "temp"
export type CrewGrouping = "status" | "name"
export type CrewState = "needs" | "working" | "idle"

export interface PanelState {
  view: CrewView
  group: CrewGrouping
}

export const DEFAULT_PANEL_STATE: PanelState = { view: "all", group: "status" }

export const CREW_VIEWS: { id: CrewView; label: string }[] = [
  { id: "all", label: "All crews" },
  { id: "needs", label: "Needs you" },
  { id: "working", label: "Working" },
  { id: "idle", label: "Idle" },
  { id: "temp", label: "Temporary hires" },
]

/** Params the panel writes, and the ones an older link may still carry. */
export const PANEL_PARAM_KEYS = ["status", "group"] as const
const LEGACY_PARAM_KEYS = ["role", "model", "runtime", "hire", "activity", "health", "sort", "details"] as const

/**
 * Reads ?status= and ?group=. Older links keep working: `status=expired`
 * was the "Expired hires" bucket and is now part of Temporary hires;
 * `group=crew` was the crew tree (now the status grouping) and `group=none`
 * the flat A–Z list. Anything unknown falls back to the default.
 */
export function panelStateFromParams(params: URLSearchParams): PanelState {
  const rawView = params.get("status") ?? ""
  const view: CrewView =
    rawView === "expired" ? "temp" : (CREW_VIEWS.some((v) => v.id === rawView) ? (rawView as CrewView) : "all")
  const rawGroup = params.get("group") ?? ""
  const group: CrewGrouping = rawGroup === "name" || rawGroup === "none" ? "name" : "status"
  return { view, group }
}

/** Defaults are left out of the URL; the facets of the old explorer are dropped. */
export function panelStateToParams(state: PanelState): Record<string, string | null> {
  const out: Record<string, string | null> = {
    status: state.view === "all" ? null : state.view,
    group: state.group === "status" ? null : state.group,
  }
  for (const k of LEGACY_PARAM_KEYS) out[k] = null
  return out
}

export interface PanelAgent {
  id: string
  name: string
  slug: string
  status: string
  agent_role: string
  crew_id?: string | null
  role_title?: string | null
  ephemeral?: boolean
  expires_at?: string | null
  expired_at?: string | null
}

export interface PanelCrew {
  id: string
  name: string
  slug: string
  icon: string | null
  color: string | null
  _count?: { agents: number }
}

export interface CrewStatus {
  state: CrewState
  /** How many things wait for a person: agents in error or waiting, a build
   *  that failed or is due, credential gaps. */
  needs: number
}

/** One crew's state: anything waiting for a person wins over work in progress. */
export function crewStatus(
  agents: PanelAgent[],
  provisioning?: ProvisioningState,
  gaps = 0,
): CrewStatus {
  const live = agents.filter((a) => !a.expired_at)
  let needs = 0
  for (const a of live) if (a.status === "ERROR" || WAITING_STATUSES.has(a.status)) needs += 1
  if (provisioning === "failed" || provisioning === "needs_provision") needs += 1
  needs += Math.max(0, gaps)
  if (needs > 0) return { state: "needs", needs }
  if (provisioning === "running" || live.some((a) => a.status === "RUNNING")) return { state: "working", needs: 0 }
  return { state: "idle", needs: 0 }
}

/** Lead first, then the permanent agents A–Z, then the temporary hires. */
export function orderAgents<A extends PanelAgent>(agents: A[]): A[] {
  const rank = (a: A) => (a.agent_role === "LEAD" ? 0 : a.ephemeral ? 2 : 1)
  return [...agents].sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name))
}

/** The short label beside a temporary hire: "hire" while it waits for
 *  approval, "expired" once it has gone, otherwise the time left. */
export function tempHireLabel(a: PanelAgent, now: number = Date.now()): { label: string; tone: "warn" | "muted" } | null {
  if (!a.ephemeral) return null
  if (a.expired_at) return { label: "expired", tone: "muted" }
  if (a.status === "PENDING_REVIEW") return { label: "hire", tone: "warn" }
  if (!a.expires_at) return { label: "temporary", tone: "warn" }
  const ms = new Date(a.expires_at).getTime() - now
  if (Number.isNaN(ms)) return { label: "temporary", tone: "warn" }
  if (ms <= 0) return { label: "expiring", tone: "warn" }
  const min = Math.max(1, Math.floor(ms / 60_000))
  if (min < 60) return { label: `${min} min`, tone: "warn" }
  const h = Math.floor(min / 60)
  const m = min % 60
  return { label: m ? `${h} h ${m} min` : `${h} h`, tone: "warn" }
}

export interface PanelCrewRow<C extends PanelCrew = PanelCrew, A extends PanelAgent = PanelAgent> {
  crew: C
  status: CrewStatus
  agents: A[]
}

export interface PanelSection<C extends PanelCrew = PanelCrew, A extends PanelAgent = PanelAgent> {
  key: CrewState | "all"
  /** Empty for the A–Z list: it has no sub-heading. */
  label: string
  rows: PanelCrewRow<C, A>[]
}

export interface PanelModel<C extends PanelCrew = PanelCrew, A extends PanelAgent = PanelAgent> {
  counts: Record<CrewView, number>
  sections: PanelSection<C, A>[]
  /** Crews shown after the view and the search. */
  shown: number
  unassigned: A[]
}

function hit(q: string, ...fields: (string | null | undefined)[]): boolean {
  return fields.some((f) => f != null && f.toLowerCase().includes(q))
}

const VIEW_TEST: Record<CrewView, (row: PanelCrewRow) => boolean> = {
  all: () => true,
  needs: (r) => r.status.state === "needs",
  working: (r) => r.status.state === "working",
  idle: (r) => r.status.state === "idle",
  temp: (r) => r.agents.some((a) => a.ephemeral),
}

const SECTION_LABEL: Record<CrewState, string> = { needs: "Needs you", working: "Working", idle: "Idle" }

export function buildPanel<C extends PanelCrew, A extends PanelAgent>({
  crews,
  agents,
  provisioningByCrew,
  gapsByCrew,
  state,
  search = "",
}: {
  crews: C[]
  agents: A[]
  provisioningByCrew?: ReadonlyMap<string, ProvisioningState>
  gapsByCrew?: ReadonlyMap<string, number>
  state: PanelState
  search?: string
}): PanelModel<C, A> {
  const byCrew = new Map<string, A[]>()
  const unassignedAll: A[] = []
  for (const a of agents) {
    if (!a.crew_id) { unassignedAll.push(a); continue }
    const list = byCrew.get(a.crew_id) ?? []
    list.push(a)
    byCrew.set(a.crew_id, list)
  }
  const rows: PanelCrewRow<C, A>[] = crews.map((crew) => {
    const list = byCrew.get(crew.id) ?? []
    return {
      crew,
      status: crewStatus(list, provisioningByCrew?.get(crew.id), gapsByCrew?.get(crew.id) ?? 0),
      agents: orderAgents(list),
    }
  })
  const counts = Object.fromEntries(
    CREW_VIEWS.map((v) => [v.id, rows.filter((r) => VIEW_TEST[v.id](r as PanelCrewRow)).length]),
  ) as Record<CrewView, number>

  const q = search.trim().toLowerCase()
  const visible = rows.filter(
    (r) =>
      VIEW_TEST[state.view](r as PanelCrewRow) &&
      (!q || hit(q, r.crew.name, r.crew.slug) || r.agents.some((a) => hit(q, a.name, a.slug, a.role_title))),
  )
  const byName = (a: PanelCrewRow<C, A>, b: PanelCrewRow<C, A>) => a.crew.name.localeCompare(b.crew.name)
  const sections: PanelSection<C, A>[] =
    state.group === "name"
      ? visible.length ? [{ key: "all", label: "", rows: [...visible].sort(byName) }] : []
      : (["needs", "working", "idle"] as const)
          .map((key) => ({ key, label: SECTION_LABEL[key], rows: visible.filter((r) => r.status.state === key).sort(byName) }))
          .filter((s) => s.rows.length > 0)

  const unassigned = orderAgents(
    q ? unassignedAll.filter((a) => hit(q, a.name, a.slug, a.role_title)) : unassignedAll,
  )
  return { counts, sections, shown: visible.length, unassigned: state.view === "all" ? unassigned : [] }
}

/** The crew /crews opens without a selection: the first one in the panel. */
export function firstCrewSlug(model: PanelModel): string | null {
  return model.sections[0]?.rows[0]?.crew.slug ?? null
}
