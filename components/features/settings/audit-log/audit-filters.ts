/**
 * Audit log filters — pure, framework-free.
 *
 * One place decides what a filter means on the wire (auditQueryParams), how it
 * is written in the URL (filtersToSearch / filtersFromSearch) and how it reads
 * back to a person (activeFilterChips). The toolbar and the table only call
 * these; nothing else builds an /api/v1/audit query.
 *
 * Server facts this encodes (internal/api/audit.go):
 *   - the workspace trail filters on the server by search (action, entity type,
 *     person's name/email), user_id, entity_type, action (exact), date_from,
 *     date_to;
 *   - the crews / credentials / keeper trails filter by date only;
 *   - created_at is compared as text, so date_to is exclusive-by-day unless the
 *     end is sent as the start of the following day.
 */

export const AUDIT_SOURCES = [
  { value: "workspace", label: "Workspace", hint: "Settings, people, crews, credentials" },
  { value: "crews", label: "Crews", hint: "Cross-crew dispatch, messages, shared files" },
  { value: "credentials", label: "Credentials", hint: "Which secret was used, revealed or rotated" },
  { value: "keeper", label: "Keeper", hint: "Every gatekeeper decision, append-only" },
] as const
export type AuditSource = (typeof AUDIT_SOURCES)[number]["value"]

// The entity types the server writes for the workspace trail. Keep in step
// with the write sites, or a category starts returning an empty list.
export const AUDIT_CATEGORIES = [
  { label: "All", value: "all" },
  // Agent runs are most of a busy workspace's log; the writer spells the type
  // in lower case.
  { label: "Runs", value: "agent_run" },
  { label: "Agents", value: "AGENT" },
  { label: "Crews", value: "CREW" },
  { label: "Crew links", value: "CREW_LINK" },
  { label: "Credentials", value: "CREDENTIAL" },
  { label: "People", value: "WorkspaceMember" },
  { label: "Workspace", value: "WORKSPACE" },
] as const

/** How a run ended — sent as the exact action agent.run.<value>. */
export const AUDIT_RESULTS = [
  { value: "completed", label: "Completed", tone: "success" },
  { value: "failed", label: "Failed", tone: "danger" },
  { value: "cancelled", label: "Cancelled", tone: "muted" },
] as const
export type AuditResult = (typeof AUDIT_RESULTS)[number]["value"]

export const AUDIT_RANGES = [
  { value: "1h", label: "Last hour", ms: 3_600_000 },
  { value: "24h", label: "Last 24 hours", ms: 86_400_000 },
  { value: "7d", label: "Last 7 days", ms: 7 * 86_400_000 },
  { value: "30d", label: "Last 30 days", ms: 30 * 86_400_000 },
  { value: "90d", label: "Last 90 days", ms: 90 * 86_400_000 },
  { value: "all", label: "All time", ms: null },
] as const
export type AuditRange = (typeof AUDIT_RANGES)[number]["value"] | "custom"

export interface AuditFilters {
  source: AuditSource
  /** entity_type, or "all". Workspace trail only. */
  category: string
  range: AuditRange
  /** Custom range, as YYYY-MM-DD (inclusive on both ends). */
  from: string
  to: string
  /** Server-side search. Workspace trail only. */
  q: string
  /** Person (user id). Workspace trail only. */
  userId: string
  /** How a run ended, or "". Workspace trail only. */
  result: AuditResult | ""
}

export const DEFAULT_AUDIT_FILTERS: AuditFilters = {
  source: "workspace",
  category: "all",
  range: "7d",
  from: "",
  to: "",
  q: "",
  userId: "",
  result: "",
}

/** Which filters a trail honours on the server. */
export function sourceSupports(source: AuditSource): { search: boolean; person: boolean; category: boolean; result: boolean } {
  const all = source === "workspace"
  return { search: all, person: all, category: all, result: all }
}

/** The action a result filter stands for. */
export const resultAction = (result: AuditResult) => `agent.run.${result}`

const DAY_MS = 86_400_000
const isDay = (v: string) => /^\d{4}-\d{2}-\d{2}$/.test(v) && !Number.isNaN(Date.parse(`${v}T00:00:00Z`))
const dayStart = (v: string) => new Date(`${v}T00:00:00Z`).toISOString()

/** The instants to send as date_from / date_to. */
export function rangeBounds(filters: AuditFilters, now: Date): { from?: string; to?: string } {
  if (filters.range === "custom") {
    return {
      from: isDay(filters.from) ? dayStart(filters.from) : undefined,
      to: isDay(filters.to) ? new Date(Date.parse(dayStart(filters.to)) + DAY_MS).toISOString() : undefined,
    }
  }
  const preset = AUDIT_RANGES.find((r) => r.value === filters.range)
  if (!preset || preset.ms == null) return { from: undefined, to: undefined }
  return { from: new Date(now.getTime() - preset.ms).toISOString(), to: undefined }
}

/** The /api/v1/audit query for one page. The only place it is built. */
export function auditQueryParams(workspaceId: string, filters: AuditFilters, page: number, limit: number, now: Date): URLSearchParams {
  const params = new URLSearchParams({ workspace_id: workspaceId, page: String(page), limit: String(limit), source: filters.source })
  const supports = sourceSupports(filters.source)
  const q = filters.q.trim()
  if (supports.search && q) params.set("search", q)
  if (supports.person && filters.userId) params.set("user_id", filters.userId)
  if (supports.category && filters.category !== "all") params.set("entity_type", filters.category)
  if (supports.result && filters.result) params.set("action", resultAction(filters.result))
  const { from, to } = rangeBounds(filters, now)
  if (from) params.set("date_from", from)
  if (to) params.set("date_to", to)
  return params
}

// ── URL ────────────────────────────────────────────────────────────────────

const URL_KEYS: Record<keyof AuditFilters, string> = {
  source: "audit_source",
  category: "audit_type",
  range: "audit_range",
  from: "audit_from",
  to: "audit_to",
  q: "audit_q",
  userId: "audit_user",
  result: "audit_result",
}

/** Merge the filters into an existing query string; defaults are omitted. */
export function filtersToSearch(filters: AuditFilters, current: string): string {
  const params = new URLSearchParams(current)
  for (const key of Object.keys(URL_KEYS) as (keyof AuditFilters)[]) {
    const value = filters[key]
    const custom = key === "from" || key === "to"
    if (value === DEFAULT_AUDIT_FILTERS[key] || (custom && filters.range !== "custom")) params.delete(URL_KEYS[key])
    else params.set(URL_KEYS[key], value)
  }
  const s = params.toString()
  return s ? `?${s}` : ""
}

/** Read filters back; anything unrecognised falls back to its default. */
export function filtersFromSearch(search: string): AuditFilters {
  const params = new URLSearchParams(search)
  const get = (k: keyof AuditFilters) => params.get(URL_KEYS[k]) ?? ""
  const source = AUDIT_SOURCES.some((s) => s.value === get("source")) ? (get("source") as AuditSource) : DEFAULT_AUDIT_FILTERS.source
  const range = get("range") === "custom" || AUDIT_RANGES.some((r) => r.value === get("range")) ? (get("range") as AuditRange) : DEFAULT_AUDIT_FILTERS.range
  const category = AUDIT_CATEGORIES.some((c) => c.value === get("category")) ? get("category") : DEFAULT_AUDIT_FILTERS.category
  return {
    source,
    category,
    range,
    from: range === "custom" && isDay(get("from")) ? get("from") : "",
    to: range === "custom" && isDay(get("to")) ? get("to") : "",
    q: get("q"),
    userId: get("userId"),
    result: AUDIT_RESULTS.some((x) => x.value === get("result")) ? (get("result") as AuditResult) : "",
  }
}

// ── Reading the filters back to a person ───────────────────────────────────

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]
/** "1 Sep 2026" — fixed format so the chip reads the same in every locale. */
export function formatDay(day: string): string {
  if (!isDay(day)) return ""
  const [y, m, d] = day.split("-").map(Number)
  return `${d} ${MONTHS[m - 1]} ${y}`
}

export function rangeLabel(filters: AuditFilters): string {
  if (filters.range !== "custom") return AUDIT_RANGES.find((r) => r.value === filters.range)?.label ?? ""
  if (filters.from && filters.to) return `${formatDay(filters.from)} – ${formatDay(filters.to)}`
  if (filters.from) return `Since ${formatDay(filters.from)}`
  if (filters.to) return `Until ${formatDay(filters.to)}`
  return "Custom range"
}

export interface FilterChip {
  key: "q" | "userId" | "category" | "result" | "range"
  label: string
}

/** The non-default filters, in words, each clearable on its own. */
export function activeFilterChips(filters: AuditFilters, people: Record<string, string>): FilterChip[] {
  const chips: FilterChip[] = []
  const supports = sourceSupports(filters.source)
  if (supports.search && filters.q.trim()) chips.push({ key: "q", label: `“${filters.q.trim()}”` })
  if (supports.person && filters.userId) chips.push({ key: "userId", label: `Person: ${people[filters.userId] ?? "Unknown"}` })
  if (supports.category && filters.category !== "all") {
    chips.push({ key: "category", label: AUDIT_CATEGORIES.find((c) => c.value === filters.category)?.label ?? filters.category })
  }
  if (supports.result && filters.result) {
    chips.push({ key: "result", label: `Runs: ${AUDIT_RESULTS.find((x) => x.value === filters.result)?.label.toLowerCase()}` })
  }
  if (filters.range !== DEFAULT_AUDIT_FILTERS.range) chips.push({ key: "range", label: rangeLabel(filters) })
  return chips
}
