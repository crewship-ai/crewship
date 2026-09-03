/**
 * The explorer's order: what needs a person first, then what is busy, then
 * the idle majority — folded (docs/ux/README.md §4, audit-fleet.md §5 item 2).
 *
 * The tree used to render crews in API order (newest first) with no
 * grouping, no result count and no fold, so on a hundred crews the one with
 * an agent in error sat wherever its creation date put it. Pure functions
 * here; the component only draws what they return.
 */
import type { StatusTone } from "@/lib/format-status"

export interface ExplorerCrew {
  id: string
  name: string
  slug: string
  color: string | null
  icon: string | null
  _count?: { agents: number }
}

export interface ExplorerAgent {
  id: string
  name: string
  slug: string
  status: string
  role_title: string | null
  agent_role: string
  crew_id: string | null
  expired_at?: string | null
}

export type ProvisioningState = "idle" | "needs_provision" | "running" | "failed" | "completed"

export interface CrewPill {
  tone: StatusTone
  label: string
}

export interface ExplorerCrewRow {
  crew: ExplorerCrew
  /** Agents shown under the crew (filtered by the search when there is one). */
  agents: ExplorerAgent[]
  /** The crew's real roster size, whatever the search hides. */
  agentCount: number
  pill: CrewPill | null
}

export type ExplorerGroupKey = "attention" | "running" | "idle"

export interface ExplorerGroup {
  key: ExplorerGroupKey
  label: string
  rows: ExplorerCrewRow[]
}

export interface ExplorerGroups {
  groups: ExplorerGroup[]
  /** Agents with no crew, after the search. */
  unassigned: ExplorerAgent[]
  /** Counts after the search — what "N crews · M agents match" says. */
  matchedCrews: number
  matchedAgents: number
}

/** Status words that mean "a person is needed", beyond ERROR. */
const WAITING = new Set(["PENDING_REVIEW", "WAITING", "PAUSED", "AWAITING_APPROVAL"])

function live(a: ExplorerAgent): boolean {
  return !a.expired_at
}

/** One pill per crew, or none: the reason to look at it, not a census. */
export function crewPill(
  agents: ExplorerAgent[],
  provisioning: ProvisioningState | undefined,
  gaps: number,
): CrewPill | null {
  const liveAgents = agents.filter(live)
  const errors = liveAgents.filter((a) => a.status === "ERROR").length
  if (errors > 0) return { tone: "danger", label: errors === 1 ? "1 error" : `${errors} errors` }
  if (provisioning === "failed") return { tone: "danger", label: "Build failed" }
  if (provisioning === "needs_provision") return { tone: "warn", label: "Rebuild" }
  if (gaps > 0) return { tone: "warn", label: gaps === 1 ? "1 gap" : `${gaps} gaps` }
  const waiting = liveAgents.filter((a) => WAITING.has(a.status)).length
  if (waiting > 0) return { tone: "warn", label: waiting === 1 ? "Waiting" : `${waiting} waiting` }
  if (provisioning === "running") return { tone: "blue", label: "Building" }
  const running = liveAgents.filter((a) => a.status === "RUNNING").length
  if (running > 0) return { tone: "blue", label: running === 1 ? "1 running" : `${running} running` }
  return null
}

function groupOf(pill: CrewPill | null): ExplorerGroupKey {
  if (!pill) return "idle"
  if (pill.tone === "danger" || pill.tone === "warn") return "attention"
  return "running"
}

const GROUP_LABEL: Record<ExplorerGroupKey, string> = {
  attention: "Needs attention",
  running: "Running",
  idle: "Idle",
}

function matches(q: string, ...fields: (string | null | undefined)[]): boolean {
  return fields.some((f) => f != null && f.toLowerCase().includes(q))
}

export function groupExplorerCrews({
  crews,
  agents,
  search = "",
  provisioningByCrew,
  gapsByCrew,
}: {
  crews: ExplorerCrew[]
  agents: ExplorerAgent[]
  search?: string
  provisioningByCrew?: ReadonlyMap<string, ProvisioningState>
  gapsByCrew?: ReadonlyMap<string, number>
}): ExplorerGroups {
  const q = search.trim().toLowerCase()
  const byCrew = new Map<string | null, ExplorerAgent[]>()
  for (const a of agents) {
    const list = byCrew.get(a.crew_id) ?? []
    list.push(a)
    byCrew.set(a.crew_id, list)
  }
  const agentMatches = (a: ExplorerAgent) => !q || matches(q, a.name, a.slug, a.role_title)

  const rows: ExplorerCrewRow[] = []
  let matchedAgents = 0
  for (const crew of crews) {
    const all = byCrew.get(crew.id) ?? []
    const shown = q ? all.filter(agentMatches) : all
    const crewHit = !q || matches(q, crew.name, crew.slug) || shown.length > 0
    if (!crewHit) continue
    matchedAgents += shown.length
    rows.push({
      crew,
      agents: shown,
      agentCount: crew._count?.agents ?? all.length,
      pill: crewPill(all, provisioningByCrew?.get(crew.id), gapsByCrew?.get(crew.id) ?? 0),
    })
  }
  const unassigned = (byCrew.get(null) ?? []).filter(agentMatches)
  matchedAgents += unassigned.length

  const groups: ExplorerGroup[] = (["attention", "running", "idle"] as const)
    .map((key) => ({ key, label: GROUP_LABEL[key], rows: rows.filter((r) => groupOf(r.pill) === key) }))
    .filter((g) => g.rows.length > 0)
  // Inside a group: crews with a roster before empty shells, then by name —
  // stable between refreshes, and a hundred empty "Crew 0xx" rows never push
  // the three real crews out of view.
  for (const g of groups) {
    g.rows.sort((a, b) => b.agentCount - a.agentCount || a.crew.name.localeCompare(b.crew.name))
  }
  return { groups, unassigned, matchedCrews: rows.length, matchedAgents }
}

/** How many idle crews stay open before the rest fold behind "N more". */
export const EXPLORER_FOLD = 6

export function foldRows<T>(rows: T[], showAll: boolean, fold = EXPLORER_FOLD): { visible: T[]; hidden: number } {
  if (showAll || rows.length <= fold) return { visible: rows, hidden: 0 }
  return { visible: rows.slice(0, fold), hidden: rows.length - fold }
}

/** "103 crews · 308 agents", or the matched counts with the search. */
export function explorerCountLine({
  search,
  crewsTotal,
  agentsTotal,
  matchedCrews,
  matchedAgents,
}: {
  search: string
  crewsTotal: number | null
  agentsTotal: number | null
  matchedCrews: number
  matchedAgents: number
}): string {
  const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? "" : "s"}`
  if (search.trim()) return `${plural(matchedCrews, "crew")} · ${plural(matchedAgents, "agent")} match`
  return `${plural(crewsTotal ?? matchedCrews, "crew")} · ${plural(agentsTotal ?? matchedAgents, "agent")}`
}
