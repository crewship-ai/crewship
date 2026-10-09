/**
 * What the /crews explorer filters on, and how it orders and words what is
 * left — the same skeleton Routines and Skills use (status buckets, a Filter
 * panel of facets, a View menu), applied to agents.
 *
 * Every facet reads a field the page already loads (#3043): role, model,
 * runtime, hire type, last_active_at, and the crew's provisioning and
 * credential gaps. Pure functions; the component only draws what they return.
 */
import { getModelLabel } from "@/lib/cli-adapters"
import { adapterLabel } from "@/lib/credentials/provider-logins"
import { WAITING_STATUSES, type ExplorerAgent, type ProvisioningState } from "./explorer-groups"

export interface ExplorerFilterAgent extends ExplorerAgent {
  llm_model?: string | null
  cli_adapter?: string | null
  last_active_at?: string | null
  ephemeral?: boolean
  expires_at?: string | null
}

export interface ExplorerFilterContext {
  now: number
  provisioningByCrew?: ReadonlyMap<string, ProvisioningState>
  gapsByCrew?: ReadonlyMap<string, number>
}

export type AgentBucket = "all" | "needs" | "working" | "idle" | "expired"
export type ActivityKey = "today" | "week" | "quiet" | "never"
export type HealthKey = "rebuild" | "gaps"
export type FacetKey = "role" | "model" | "runtime" | "hire" | "activity" | "health"

export interface ExplorerFilters {
  bucket: AgentBucket
  role: string[]
  model: string[]
  runtime: string[]
  hire: string[]
  activity: ActivityKey[]
  health: HealthKey[]
}

export type ExplorerGroupBy = "crew" | "status" | "none"
export type ExplorerSort = "size" | "name" | "active"

export interface ExplorerView {
  group: ExplorerGroupBy
  sort: ExplorerSort
  /** Role line under agents and the activity line under crews. */
  details: boolean
}

export const EMPTY_FILTERS: ExplorerFilters = {
  bucket: "all", role: [], model: [], runtime: [], hire: [], activity: [], health: [],
}

// Crew size first: a hundred empty "Crew 0xx" shells never push the three
// real crews out of view, which is what the explorer did before there was
// a choice.
export const DEFAULT_VIEW: ExplorerView = { group: "crew", sort: "size", details: true }

export const AGENT_BUCKETS: { id: AgentBucket; label: string }[] = [
  { id: "all", label: "All" },
  { id: "needs", label: "Needs you" },
  { id: "working", label: "Working" },
  { id: "idle", label: "Idle" },
  { id: "expired", label: "Expired hires" },
]

const ROLE_LABEL: Record<string, string> = { lead: "Lead", member: "Member" }
const HIRE_LABEL: Record<string, string> = { permanent: "Permanent", temporary: "Temporary hire" }
const ACTIVITY_LABEL: Record<ActivityKey, string> = {
  today: "Active in the last 24 h",
  week: "Active this week",
  quiet: "Quiet for 7+ days",
  never: "Never ran",
}
const HEALTH_LABEL: Record<HealthKey, string> = { rebuild: "Needs a rebuild", gaps: "Credential gaps" }
const FACET_LABEL: Record<FacetKey, string> = {
  role: "Role", model: "Model", runtime: "Runtime", hire: "Hire", activity: "Activity", health: "Crew health",
}

export function agentBucket(a: ExplorerAgent): Exclude<AgentBucket, "all"> {
  if (a.expired_at) return "expired"
  if (a.status === "ERROR" || WAITING_STATUSES.has(a.status)) return "needs"
  if (a.status === "RUNNING") return "working"
  return "idle"
}

const DAY = 86_400_000

function lastActive(a: ExplorerFilterAgent): number | null {
  if (!a.last_active_at) return null
  const t = new Date(a.last_active_at).getTime()
  return Number.isNaN(t) ? null : t
}

export function agentActivity(a: ExplorerFilterAgent, now: number): ActivityKey {
  const t = lastActive(a)
  if (t == null) return "never"
  if (now - t <= DAY) return "today"
  if (now - t < 7 * DAY) return "week"
  return "quiet"
}

export function crewHealth(crewId: string | null, ctx: ExplorerFilterContext): HealthKey[] {
  if (!crewId) return []
  const out: HealthKey[] = []
  const p = ctx.provisioningByCrew?.get(crewId)
  if (p === "needs_provision" || p === "failed") out.push("rebuild")
  if ((ctx.gapsByCrew?.get(crewId) ?? 0) > 0) out.push("gaps")
  return out
}

interface FacetDef {
  key: FacetKey
  /** The values an agent has for this facet (several only for crew health). */
  of: (a: ExplorerFilterAgent, ctx: ExplorerFilterContext) => string[]
  label: (value: string) => string
  /** Fixed order for closed vocabularies; open ones sort by label. */
  order?: string[]
  /** Below this many values the facet is no choice at all and stays hidden. */
  minOptions: number
}

const FACETS: FacetDef[] = [
  { key: "role", of: (a) => [a.agent_role === "LEAD" ? "lead" : "member"], label: (v) => ROLE_LABEL[v] ?? v, order: ["lead", "member"], minOptions: 2 },
  { key: "model", of: (a) => (a.llm_model ? [a.llm_model] : []), label: (v) => getModelLabel(v) || v, minOptions: 2 },
  { key: "runtime", of: (a) => (a.cli_adapter ? [a.cli_adapter] : []), label: (v) => adapterLabel(v), minOptions: 2 },
  { key: "hire", of: (a) => [a.ephemeral ? "temporary" : "permanent"], label: (v) => HIRE_LABEL[v] ?? v, order: ["permanent", "temporary"], minOptions: 2 },
  { key: "activity", of: (a, ctx) => [agentActivity(a, ctx.now)], label: (v) => ACTIVITY_LABEL[v as ActivityKey] ?? v, order: ["today", "week", "quiet", "never"], minOptions: 2 },
  // One value is still a choice here: "the crews that need a rebuild" against
  // the rest.
  { key: "health", of: (a, ctx) => crewHealth(a.crew_id, ctx), label: (v) => HEALTH_LABEL[v as HealthKey] ?? v, order: ["rebuild", "gaps"], minOptions: 1 },
]

export function agentMatches(
  a: ExplorerFilterAgent,
  filters: ExplorerFilters,
  ctx: ExplorerFilterContext,
  { ignoreBucket = false }: { ignoreBucket?: boolean } = {},
): boolean {
  if (!ignoreBucket && filters.bucket !== "all" && agentBucket(a) !== filters.bucket) return false
  for (const f of FACETS) {
    const picked = filters[f.key] as string[]
    if (picked.length === 0) continue
    if (!f.of(a, ctx).some((v) => picked.includes(v))) return false
  }
  return true
}

export function bucketCounts(
  agents: ExplorerFilterAgent[],
  filters: ExplorerFilters,
  ctx: ExplorerFilterContext,
): Record<AgentBucket, number> {
  const out: Record<AgentBucket, number> = { all: 0, needs: 0, working: 0, idle: 0, expired: 0 }
  for (const a of agents) {
    if (!agentMatches(a, filters, ctx, { ignoreBucket: true })) continue
    out.all++
    out[agentBucket(a)]++
  }
  return out
}

export interface FacetOption {
  value: string
  label: string
  count: number
}

export interface Facet {
  key: FacetKey
  label: string
  options: FacetOption[]
}

/** The panel's facets: each with the values present in the roster. */
export function explorerFacets(
  agents: ExplorerFilterAgent[],
  filters: ExplorerFilters,
  ctx: ExplorerFilterContext,
): Facet[] {
  const out: Facet[] = []
  for (const f of FACETS) {
    const counts = new Map<string, number>()
    for (const a of agents) for (const v of f.of(a, ctx)) counts.set(v, (counts.get(v) ?? 0) + 1)
    const picked = filters[f.key] as string[]
    // A value that is picked stays listed even when nothing has it any more,
    // or there would be no way to unpick it.
    for (const v of picked) if (!counts.has(v)) counts.set(v, 0)
    const values = [...counts.keys()]
    if (f.order) values.sort((a, b) => f.order!.indexOf(a) - f.order!.indexOf(b))
    else values.sort((a, b) => f.label(a).localeCompare(f.label(b)))
    if (values.length < f.minOptions && picked.length === 0) continue
    out.push({ key: f.key, label: FACET_LABEL[f.key], options: values.map((v) => ({ value: v, label: f.label(v), count: counts.get(v) ?? 0 })) })
  }
  return out
}

export function activeFilterCount(filters: ExplorerFilters): number {
  return FACETS.reduce((n, f) => n + (filters[f.key] as string[]).length, 0)
}

export function isNarrowing(filters: ExplorerFilters, search: string): boolean {
  return search.trim() !== "" || filters.bucket !== "all" || activeFilterCount(filters) > 0
}

export interface FilterChip {
  key: FacetKey | "bucket"
  value: string
  label: string
}

export function filterChips(filters: ExplorerFilters): FilterChip[] {
  const out: FilterChip[] = []
  if (filters.bucket !== "all") {
    out.push({ key: "bucket", value: filters.bucket, label: AGENT_BUCKETS.find((b) => b.id === filters.bucket)?.label ?? filters.bucket })
  }
  for (const f of FACETS) for (const v of filters[f.key] as string[]) out.push({ key: f.key, value: v, label: f.label(v) })
  return out
}

export function removeChip(filters: ExplorerFilters, chip: FilterChip): ExplorerFilters {
  if (chip.key === "bucket") return { ...filters, bucket: "all" }
  return { ...filters, [chip.key]: (filters[chip.key] as string[]).filter((v) => v !== chip.value) }
}

export function toggleFacet(filters: ExplorerFilters, key: FacetKey, value: string): ExplorerFilters {
  const list = filters[key] as string[]
  return { ...filters, [key]: list.includes(value) ? list.filter((v) => v !== value) : [...list, value] }
}

/** Lead first (unless the list is not one crew), then by name or by the latest work. */
export function sortAgents<T extends ExplorerFilterAgent>(
  agents: T[],
  sort: ExplorerSort,
  { leadFirst = true }: { leadFirst?: boolean } = {},
): T[] {
  return [...agents].sort((a, b) => {
    if (leadFirst) {
      const lead = Number(b.agent_role === "LEAD") - Number(a.agent_role === "LEAD")
      if (lead) return lead
    }
    if (sort === "active") {
      const d = (lastActive(b) ?? -Infinity) - (lastActive(a) ?? -Infinity)
      if (d && !Number.isNaN(d)) return d
    }
    return a.name.localeCompare(b.name)
  })
}

/** The latest activity across a crew's agents, for ordering crews. */
export function crewLastActive(agents: ExplorerFilterAgent[]): number | null {
  let best: number | null = null
  for (const a of agents) {
    const t = lastActive(a)
    if (t != null && (best == null || t > best)) best = t
  }
  return best
}

/** "now", "30m", "5h", "9d"; null when the agent never ran. */
export function shortAgo(iso: string | null | undefined, now: number): string | null {
  if (!iso) return null
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return null
  const min = Math.max(0, Math.floor((now - t) / 60_000))
  if (min < 1) return "now"
  if (min < 60) return `${min}m`
  if (min < 60 * 24) return `${Math.round(min / 60)}h`
  return `${Math.round(min / (60 * 24))}d`
}

/** What is going on in a crew, in one line under its name. */
export function crewSubline(agents: ExplorerFilterAgent[], runningMissions: number, now: number): string {
  if (runningMissions > 0) return `${runningMissions} ${runningMissions === 1 ? "mission" : "missions"} running`
  const last = crewLastActive(agents)
  if (last == null) return "No work yet"
  const ago = shortAgo(new Date(last).toISOString(), now)
  return ago === "now" ? "Active now" : `Active ${ago} ago`
}

/* ------------------------------------------------------------------ URL */

const BUCKET_IDS = new Set<string>(AGENT_BUCKETS.map((b) => b.id))
const CLOSED: Partial<Record<FacetKey, Set<string>>> = {
  role: new Set(Object.keys(ROLE_LABEL)),
  hire: new Set(Object.keys(HIRE_LABEL)),
  activity: new Set(Object.keys(ACTIVITY_LABEL)),
  health: new Set(Object.keys(HEALTH_LABEL)),
}
const GROUPS = new Set<string>(["crew", "status", "none"])
const SORTS = new Set<string>(["size", "name", "active"])

/** The URL keys this explorer owns, so a link opens the same list. */
export const EXPLORER_PARAM_KEYS = ["status", "role", "model", "runtime", "hire", "activity", "health", "group", "sort", "details"] as const

export function explorerStateToParams(filters: ExplorerFilters, view: ExplorerView): Record<(typeof EXPLORER_PARAM_KEYS)[number], string | null> {
  const list = (v: string[]) => (v.length ? v.join(",") : null)
  return {
    status: filters.bucket === "all" ? null : filters.bucket,
    role: list(filters.role),
    model: list(filters.model),
    runtime: list(filters.runtime),
    hire: list(filters.hire),
    activity: list(filters.activity),
    health: list(filters.health),
    group: view.group === DEFAULT_VIEW.group ? null : view.group,
    sort: view.sort === DEFAULT_VIEW.sort ? null : view.sort,
    details: view.details ? null : "off",
  }
}

export function explorerStateFromParams(params: URLSearchParams): { filters: ExplorerFilters; view: ExplorerView } {
  const list = (key: FacetKey) => {
    const raw = params.get(key)
    if (!raw) return []
    const allowed = CLOSED[key]
    return raw.split(",").filter((v) => v !== "" && (!allowed || allowed.has(v)))
  }
  const status = params.get("status") ?? "all"
  const group = params.get("group") ?? ""
  const sort = params.get("sort") ?? ""
  return {
    filters: {
      bucket: (BUCKET_IDS.has(status) ? status : "all") as AgentBucket,
      role: list("role"),
      model: list("model"),
      runtime: list("runtime"),
      hire: list("hire"),
      activity: list("activity") as ActivityKey[],
      health: list("health") as HealthKey[],
    },
    view: {
      group: (GROUPS.has(group) ? group : DEFAULT_VIEW.group) as ExplorerGroupBy,
      sort: (SORTS.has(sort) ? sort : DEFAULT_VIEW.sort) as ExplorerSort,
      details: params.get("details") !== "off",
    },
  }
}
