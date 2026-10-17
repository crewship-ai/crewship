// /activity — one list of work and changes (Harbor 2, #3389).
//
// One row is one piece of work — a routine run and what it caused, or agent
// work on an issue — or one change somebody (or an agent) made: an issue that
// moved, a routine that was published, a credential that was revealed. Work
// comes from the chain index (GET /api/v1/chains), changes from the journal.
// Both are merged by time, grouped (Active now / Today / Yesterday / a day, or
// by what, object or who), and repeated completed runs of one routine on one
// day collapse into a single row so the list stays readable.
//
// Pure on purpose: the merge, the bundling and the range math are the parts
// worth testing, and they should not need a mounted page with a stream in it.

import type { ChainSummary } from "@/hooks/use-chains"
import { chainStatus, isChainActive, matchesQuery, startedByWord, workflowName } from "@/lib/activity-lenses"
import type { JournalEntry } from "@/lib/types/journal"

/* ------------------------------------------------------------------ *
 *  Vocabulary — the one Routines uses
 * ------------------------------------------------------------------ */

export type WorkStatus = "running" | "waiting" | "failed" | "completed" | "stopped"

export const WORK_STATUS_LABEL: Record<WorkStatus, string> = {
  running: "Running",
  waiting: "Waiting for a person",
  failed: "Could not finish",
  completed: "Completed",
  stopped: "Stopped",
}

/** The StatusPill tone for each state. */
export const WORK_STATUS_TONE: Record<WorkStatus, "blue" | "warn" | "danger" | "success" | "muted"> = {
  running: "blue",
  waiting: "warn",
  failed: "danger",
  completed: "success",
  stopped: "muted",
}

export function workStatusOf(c: ChainSummary): WorkStatus {
  const s = chainStatus(c)
  return s === "done" ? "completed" : s
}

/** The views in the panel. Each is a status, except Everything. */
export type ActivityView = "all" | "need" | "live" | "failed"

export const ACTIVITY_VIEWS: readonly { key: ActivityView; label: string; status: WorkStatus | null }[] = [
  { key: "all", label: "Everything", status: null },
  { key: "need", label: "Needs a person", status: "waiting" },
  { key: "live", label: "Running", status: "running" },
  { key: "failed", label: "Could not finish", status: "failed" },
]

export function viewOfStatus(status: WorkStatus | null): ActivityView | null {
  return ACTIVITY_VIEWS.find((v) => v.status === status)?.key ?? null
}

/* ------------------------------------------------------------------ *
 *  Where work came from
 * ------------------------------------------------------------------ */

export type WorkSource = "schedule" | "webhook" | "issue" | "chat" | "person" | "routine" | "rule" | "other"

export const WORK_SOURCE_LABEL: Record<WorkSource, string> = {
  schedule: "Schedule",
  webhook: "Webhook",
  issue: "Issue",
  chat: "Chat",
  person: "By hand",
  routine: "Another routine",
  rule: "Rule",
  other: "Other",
}

export function workSourceOf(c: ChainSummary): WorkSource {
  switch (c.started_by_kind) {
    case "schedule":
      return "schedule"
    case "webhook":
      return "webhook"
    case "issue":
    case "lead_planning":
      return "issue"
    case "agent":
      return "chat"
    case "user":
      return "person"
    case "routine":
    case "run":
      return "routine"
    case "automation":
      return "rule"
    default:
      return "other"
  }
}

/* ------------------------------------------------------------------ *
 *  Changes
 * ------------------------------------------------------------------ */

export type ChangeKind = "issue" | "routine" | "artifact" | "crew" | "access"

export const CHANGE_KIND_LABEL: Record<ChangeKind, string> = {
  issue: "Issues",
  routine: "Routines",
  artifact: "Pages",
  crew: "Crews, skills and integrations",
  access: "Access and credentials",
}

/**
 * The journal entry types that are a CHANGE somebody made, by kind. Run and
 * step events are not here: they are the work rows' own story, and listing
 * them beside the work would repeat every run several times.
 */
export const CHANGE_TYPES: Record<ChangeKind, readonly string[]> = {
  issue: ["mission.created", "mission.assigned", "mission.status_change", "mission.comment"],
  routine: ["pipeline.published", "pipeline.dry_run", "pipeline.schedule.circuit_breaker_tripped"],
  artifact: [
    "page.published",
    "page.link_revoked",
    "page.grant_added",
    "page.grant_removed",
    "page.folder_changed",
    "page.owner_transferred",
    "page.spec.changed",
  ],
  crew: [
    "skill.assigned",
    "skill.unassigned",
    "skill.imported",
    "skill.deleted",
    "memory.skill_approved",
    "audit.entity_created",
    "audit.entity_updated",
    "audit.entity_deleted",
    "audit.entity_restored",
  ],
  access: [
    "credential.revealed",
    "credential.lease_issued",
    "credential.reveal_policy_changed",
    "credential.sensitivity_lowered",
    "invitation.created",
    "invitation.revoked",
    "invitation.accepted",
    "member.access_changed",
    "approval.trust_granted",
    "approval.trust_revoked",
  ],
}

const KIND_BY_TYPE: ReadonlyMap<string, ChangeKind> = new Map(
  (Object.keys(CHANGE_TYPES) as ChangeKind[]).flatMap((k) => CHANGE_TYPES[k].map((t) => [t, k] as const)),
)

export function changeKindOf(entryType: string): ChangeKind | null {
  return KIND_BY_TYPE.get(entryType) ?? null
}

/** Every change type, or one kind's, for the journal's `entry_type=` filter. */
export function changeEntryTypes(kind?: ChangeKind | null): string[] {
  return kind ? [...CHANGE_TYPES[kind]] : (Object.values(CHANGE_TYPES) as string[][]).flat()
}

const ACTION_LABEL: Record<string, string> = {
  "mission.created": "Created",
  "mission.assigned": "Assigned",
  "mission.status_change": "Status changed",
  "mission.comment": "Comment added",
  "pipeline.published": "Published",
  "pipeline.dry_run": "Test run",
  "pipeline.schedule.circuit_breaker_tripped": "Schedule turned off",
  "page.published": "Published",
  "page.link_revoked": "Public link revoked",
  "page.grant_added": "Shared",
  "page.grant_removed": "Access removed",
  "page.folder_changed": "Moved to a folder",
  "page.owner_transferred": "Owner changed",
  "page.spec.changed": "Changed",
  "skill.assigned": "Skill added",
  "skill.unassigned": "Skill removed",
  "skill.imported": "Skill imported",
  "skill.deleted": "Skill deleted",
  "memory.skill_approved": "Skill approved",
  "audit.entity_created": "Created",
  "audit.entity_updated": "Updated",
  "audit.entity_deleted": "Deleted",
  "audit.entity_restored": "Restored",
  "credential.revealed": "Revealed",
  "credential.lease_issued": "Lease issued",
  "credential.reveal_policy_changed": "Reveal policy changed",
  "credential.sensitivity_lowered": "Sensitivity lowered",
  "invitation.created": "Invited",
  "invitation.revoked": "Invitation revoked",
  "invitation.accepted": "Joined",
  "member.access_changed": "Role changed",
  "approval.trust_granted": "Standing approval granted",
  "approval.trust_revoked": "Standing approval revoked",
}

export function changeAction(entryType: string): string {
  const known = ACTION_LABEL[entryType]
  if (known) return known
  const tail = entryType.split(".").slice(1).join(" ").replace(/_/g, " ").trim()
  return tail ? tail.charAt(0).toUpperCase() + tail.slice(1) : entryType
}

/* ------------------------------------------------------------------ *
 *  Rows
 * ------------------------------------------------------------------ */

export interface WorkItem {
  type: "work"
  id: string
  /** When it started (ms) — the time the row is sorted and grouped by. */
  ts: number
  /** Still going or still asking a person: belongs to "Active now". */
  active: boolean
  status: WorkStatus
  title: string
  source: WorkSource
  /** "schedule", "webhook · GitHub" — the row's second line. */
  origin: string
  routineSlug?: string
  agents: { id: string; name: string }[]
  issues: { id: string; label: string }[]
  durationMs: number | null
  chain: ChainSummary
  /** A bundle of repeated runs: the runs it holds, newest first. */
  runs?: WorkItem[]
}

export interface ChangeItem {
  type: "change"
  id: string
  ts: number
  kind: ChangeKind
  /** The object that changed — an issue, a routine, a page… */
  object: string
  action: string
  /** Who made it. */
  who: string
  /** "In progress → In review", when the entry says. */
  diff?: string
  missionId?: string
  routineSlug?: string
  agentId?: string
  entry: JournalEntry
}

export type FeedItem = WorkItem | ChangeItem

export interface WorkContext {
  routineName?: (slug: string) => string | undefined
}

export function workFromChain(c: ChainSummary, ctx: WorkContext = {}): WorkItem {
  const routineName = c.routine_slug ? ctx.routineName?.(c.routine_slug) : undefined
  const ts = Date.parse(c.first_activity)
  return {
    type: "work",
    id: c.origin,
    ts: Number.isNaN(ts) ? 0 : ts,
    active: isChainActive(c),
    status: workStatusOf(c),
    title: workflowName(c, routineName),
    source: workSourceOf(c),
    origin: startedByWord(c) || WORK_SOURCE_LABEL[workSourceOf(c)],
    routineSlug: c.routine_slug || undefined,
    agents: (c.agents ?? []).map((a) => ({ id: a.id, name: a.name || a.slug || a.id })),
    issues: (c.issues ?? []).map((i) => ({ id: i.id, label: i.identifier || i.title || i.id })),
    durationMs: c.duration_ms,
    chain: c,
  }
}

export interface ChangeContext {
  agentName?: (id: string) => string | undefined
  issueLabel?: (id: string) => string | undefined
  routineName?: (slug: string) => string | undefined
}

function str(v: unknown): string | undefined {
  return typeof v === "string" && v.trim() ? v.trim() : undefined
}

/** `agent_template` → "Agent template". */
function entityLabel(type: string): string {
  const words = type.replace(/[_.-]+/g, " ").trim()
  return words ? words.charAt(0).toUpperCase() + words.slice(1) : type
}

// The audit log's own summary, "hook.delete hook hk_4734b7ad8": action, type, id.
const RAW_AUDIT_SUMMARY = /^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\s+([a-z][a-z0-9_]*)\s+(\S+)$/

/**
 * What an audited create / update / delete / restore was about, in words:
 * "Hook hk_4734b7ad8", "Crew Platform". The row says the verb under it, so
 * the event name the audit log stores ("hook.delete …") never reaches the
 * title. Null when the entry names no object, so the caller keeps looking.
 */
export function auditObject(e: Pick<JournalEntry, "payload" | "summary">): string | null {
  const p = e.payload ?? {}
  const meta = p["metadata"] && typeof p["metadata"] === "object" ? (p["metadata"] as Record<string, unknown>) : {}
  let type = str(p["entity_type"])
  let id = str(p["entity_id"])
  if (!type) {
    const m = RAW_AUDIT_SUMMARY.exec(e.summary?.trim() ?? "")
    if (!m) return null
    type = m[1]
    id = m[2]
  }
  const name = str(p["name"]) ?? str(meta["name"]) ?? str(p["title"]) ?? str(meta["title"]) ?? str(meta["slug"])
  const label = entityLabel(type)
  if (name) return `${label} ${name}`
  return id ? `${label} ${id.length > 12 ? id.slice(0, 12) : id}` : label
}

/**
 * The change as one sentence for the Peek: the entry's own summary, unless
 * that is the audit log's event name — then "Deleted hook hk_4734b7ad8".
 */
export function changeSentence(c: Pick<ChangeItem, "action" | "object" | "entry">): string {
  const summary = c.entry.summary?.trim() ?? ""
  if (summary && !RAW_AUDIT_SUMMARY.test(summary)) return summary
  return `${c.action} ${c.object.charAt(0).toLowerCase()}${c.object.slice(1)}`
}

export function changeFromEntry(e: JournalEntry, ctx: ChangeContext = {}): ChangeItem | null {
  const kind = changeKindOf(e.entry_type)
  if (!kind) return null
  const p = e.payload ?? {}
  const routineSlug = str(p["pipeline_slug"]) ?? str(p["routine_slug"])
  const object =
    (e.entry_type.startsWith("audit.") && auditObject(e)) ||
    (e.mission_id && (ctx.issueLabel?.(e.mission_id) ?? str(p["identifier"]) ?? str(p["title"]))) ||
    (routineSlug && (ctx.routineName?.(routineSlug) ?? routineSlug)) ||
    str(p["page_title"]) ||
    str(p["credential_name"]) ||
    str(p["skill_name"]) ||
    str(p["name"]) ||
    str(p["title"]) ||
    e.summary
  const who =
    e.actor_type === "agent" && e.agent_id
      ? (ctx.agentName?.(e.agent_id) ?? "An agent")
      : e.actor_type === "user"
        ? "A person"
        : "System"
  const from = str(p["from"])
  const to = str(p["to"])
  const ts = Date.parse(e.ts)
  return {
    type: "change",
    id: e.id,
    ts: Number.isNaN(ts) ? 0 : ts,
    kind,
    object,
    action: changeAction(e.entry_type),
    who,
    diff: from && to ? `${from} → ${to}` : undefined,
    missionId: e.mission_id || undefined,
    routineSlug,
    agentId: e.actor_type === "agent" ? e.agent_id : undefined,
    entry: e,
  }
}

/** Work and changes, newest first. Ties keep work above the change it caused. */
export function mergeFeed(work: WorkItem[], changes: ChangeItem[]): FeedItem[] {
  return [...work, ...changes].sort((a, b) => b.ts - a.ts || (a.type === b.type ? 0 : a.type === "work" ? -1 : 1))
}

/* ------------------------------------------------------------------ *
 *  Time — days in the viewer's own zone
 * ------------------------------------------------------------------ */

function startOfDay(ms: number): number {
  const d = new Date(ms)
  d.setHours(0, 0, 0, 0)
  return d.getTime()
}

const DAY_MS = 86_400_000

/** Whole days between the item's day and today: 0 today, 1 yesterday. */
export function daysAgo(ts: number, now: number): number {
  return Math.round((startOfDay(now) - startOfDay(ts)) / DAY_MS)
}

export function dayLabel(ts: number, now: number): string {
  const n = daysAgo(ts, now)
  if (n <= 0) return "Today"
  if (n === 1) return "Yesterday"
  return new Date(ts).toLocaleDateString("en-GB", { weekday: "short", day: "numeric", month: "short" })
}

/** "08:14" today, "Wed 03:00" this week, "15 Sep 08:15" before. */
export function whenLabel(ts: number, now: number): string {
  const d = new Date(ts)
  const hm = d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit", hour12: false })
  const n = daysAgo(ts, now)
  if (n <= 0) return hm
  if (n < 7) return `${d.toLocaleDateString("en-GB", { weekday: "short" })} ${hm}`
  return `${d.toLocaleDateString("en-GB", { day: "numeric", month: "short" })} ${hm}`
}

/* ------------------------------------------------------------------ *
 *  Repeated runs
 * ------------------------------------------------------------------ */

/** Fewer runs than this stay separate rows. */
export const BUNDLE_MIN = 3

/**
 * Several completed runs of one routine on one day become one row ("×12
 * runs"). Nothing that needs the reader is ever hidden in a bundle: active,
 * failed, waiting and stopped runs stay rows of their own.
 */
export function bundleRepeats(items: FeedItem[], now: number): FeedItem[] {
  const keyOf = (x: FeedItem) =>
    x.type === "work" && !x.runs && !x.active && x.status === "completed" && x.routineSlug
      ? `${x.routineSlug}|${daysAgo(x.ts, now)}`
      : null
  const groups = new Map<string, WorkItem[]>()
  for (const x of items) {
    const k = keyOf(x)
    if (k) groups.set(k, [...(groups.get(k) ?? []), x as WorkItem])
  }
  const done = new Set<string>()
  const out: FeedItem[] = []
  for (const x of items) {
    const k = keyOf(x)
    const g = k ? groups.get(k)! : null
    if (!k || !g || g.length < BUNDLE_MIN) {
      out.push(x)
      continue
    }
    if (done.has(k)) continue
    done.add(k)
    out.push(bundleOf(g))
  }
  return out
}

function bundleOf(runs: WorkItem[]): WorkItem {
  const newest = runs[0]
  const total = runs.reduce((ms, r) => ms + (r.durationMs ?? 0), 0)
  return {
    ...newest,
    id: `bundle:${newest.routineSlug}:${newest.id}`,
    durationMs: Math.round(total / runs.length),
    runs,
  }
}

/* ------------------------------------------------------------------ *
 *  Groups
 * ------------------------------------------------------------------ */

export type GroupBy = "time" | "what" | "object" | "who"

export const GROUP_BY_LABEL: Record<GroupBy, string> = { time: "Time", what: "What", object: "Object", who: "Who" }

export interface FeedGroup {
  key: string
  label: string
  items: FeedItem[]
}

export interface GroupContext {
  routineName?: (slug: string) => string | undefined
}

export function groupFeed(items: FeedItem[], by: GroupBy, now: number, ctx: GroupContext = {}): FeedGroup[] {
  const order: string[] = []
  const map = new Map<string, FeedGroup>()
  const add = (key: string, label: string, x: FeedItem) => {
    let g = map.get(key)
    if (!g) {
      g = { key, label, items: [] }
      map.set(key, g)
      order.push(key)
    }
    g.items.push(x)
  }
  for (const x of items) {
    if (by === "time") {
      if (x.type === "work" && x.active) add("now", "Active now", x)
      else add(`d${daysAgo(x.ts, now)}`, dayLabel(x.ts, now), x)
    } else if (by === "what") {
      if (x.type === "work") add("work", "Runs and agent work", x)
      else add(x.kind, CHANGE_KIND_LABEL[x.kind], x)
    } else if (by === "who") {
      if (x.type === "work") {
        const a = x.agents[0]
        add(a ? `a:${a.id}` : "nobody", a ? a.name : "No agent", x)
      } else add(x.agentId ? `a:${x.agentId}` : `w:${x.who}`, x.who, x)
    } else {
      if (x.type === "work") {
        if (x.routineSlug) add(`r:${x.routineSlug}`, ctx.routineName?.(x.routineSlug) ?? x.routineSlug, x)
        else if (x.issues[0]) add(`i:${x.issues[0].id}`, x.issues[0].label, x)
        else add("other", "Other work", x)
      } else if (x.missionId) add(`i:${x.missionId}`, x.object, x)
      else if (x.routineSlug) add(`r:${x.routineSlug}`, x.object, x)
      else add(`o:${x.object}`, x.object, x)
    }
  }
  const groups = order.map((k) => map.get(k)!)
  // Active now first, whatever its first row's time; then newest day first.
  if (by === "time") groups.sort((a, b) => (a.key === "now" ? -1 : b.key === "now" ? 1 : 0))
  // "Other" groups go last.
  if (by !== "time") groups.sort((a, b) => Number(a.key === "other" || a.key === "nobody") - Number(b.key === "other" || b.key === "nobody"))
  return groups
}

/* ------------------------------------------------------------------ *
 *  Filters
 * ------------------------------------------------------------------ */

export type WhatFilter = "work" | "changes" | ChangeKind

export interface FeedFilters {
  q: string
  status: WorkStatus | null
  what: WhatFilter | null
  routine: string | null
  agent: string | null
  crew: string | null
  source: WorkSource | null
  /** One issue: its work and the changes to it. */
  issue: string | null
}

export const EMPTY_FILTERS: FeedFilters = {
  q: "",
  status: null,
  what: null,
  routine: null,
  agent: null,
  crew: null,
  source: null,
  issue: null,
}

/** How many facets narrow the list (search and the view are not counted). */
export function activeFacetCount(f: FeedFilters): number {
  return [f.what, f.routine, f.agent, f.crew, f.source, f.issue].filter(Boolean).length +
    (f.status && !viewOfStatus(f.status) ? 1 : 0)
}

export interface FilterContext {
  routineName?: (slug: string) => string | undefined
  crewOfAgent?: (id: string) => string | undefined
}

/** Does this work row pass every facet except the ones listed in `skip`? */
export function workPasses(w: WorkItem, f: FeedFilters, ctx: FilterContext = {}, skip: (keyof FeedFilters)[] = []): boolean {
  const on = (k: keyof FeedFilters) => !skip.includes(k)
  if (on("what") && f.what && f.what !== "work") return false
  if (on("status") && f.status && w.status !== f.status) return false
  if (on("routine") && f.routine && w.routineSlug !== f.routine) return false
  if (on("agent") && f.agent && !w.agents.some((a) => a.id === f.agent)) return false
  if (on("crew") && f.crew && !w.agents.some((a) => ctx.crewOfAgent?.(a.id) === f.crew)) return false
  if (on("source") && f.source && w.source !== f.source) return false
  if (on("issue") && f.issue && !w.issues.some((i) => i.id === f.issue)) return false
  if (on("q") && f.q && !matchesQuery(w.chain, f.q, w.routineSlug ? ctx.routineName?.(w.routineSlug) : undefined)) return false
  return true
}

/**
 * Changes show only in Everything, and only while no work-only facet is set:
 * "Running" or "started by a schedule" are questions about work.
 */
export function changePasses(c: ChangeItem, f: FeedFilters, ctx: FilterContext = {}): boolean {
  if (f.status || f.source) return false
  if (f.what === "work") return false
  if (f.what && f.what !== "changes" && f.what !== c.kind) return false
  if (f.routine && c.routineSlug !== f.routine) return false
  if (f.agent && c.agentId !== f.agent) return false
  if (f.crew && (!c.agentId || ctx.crewOfAgent?.(c.agentId) !== f.crew)) return false
  if (f.issue && c.missionId !== f.issue) return false
  if (f.q) {
    const q = f.q.trim().toLowerCase()
    if (q && ![c.object, c.action, c.who, c.entry.summary, c.diff ?? ""].some((s) => s.toLowerCase().includes(q))) return false
  }
  return true
}

/** Counts for the views, over the work that passes every OTHER facet. */
export function viewCounts(
  work: WorkItem[],
  changes: ChangeItem[],
  f: FeedFilters,
  ctx: FilterContext = {},
  /** How Everything folds its rows (repeated runs), so its count matches the list. */
  fold: (items: FeedItem[]) => FeedItem[] = (x) => x,
): Record<ActivityView, number> {
  const base = work.filter((w) => workPasses(w, f, ctx, ["status"]))
  const n = (s: WorkStatus) => base.filter((w) => w.status === s).length
  const everything = fold(mergeFeed(base, changes.filter((c) => changePasses(c, { ...f, status: null }, ctx)))).length
  return { all: everything, need: n("waiting"), live: n("running"), failed: n("failed") }
}

/* ------------------------------------------------------------------ *
 *  Time range
 * ------------------------------------------------------------------ */

export type RangeKey = "1h" | "24h" | "today" | "yday" | "7d" | "week" | "30d" | "custom"

export interface TimeRange {
  key: RangeKey
  /** Custom range only: start and end (ms). */
  from?: number
  to?: number
}

export const DEFAULT_RANGE: TimeRange = { key: "7d" }

export const RANGE_PRESETS: readonly { key: Exclude<RangeKey, "custom">; label: string; kbd: string }[] = [
  { key: "1h", label: "Last hour", kbd: "1h" },
  { key: "24h", label: "Last 24 hours", kbd: "24h" },
  { key: "today", label: "Today", kbd: "T" },
  { key: "yday", label: "Yesterday", kbd: "Y" },
  { key: "7d", label: "Last 7 days", kbd: "7d" },
  { key: "week", label: "This week", kbd: "W" },
  { key: "30d", label: "Last 30 days", kbd: "30d" },
]

/** What the ‹ › arrows step through, shortest first. */
export const RANGE_STEPS: readonly RangeKey[] = ["1h", "24h", "today", "7d", "30d"]

export function isRangeKey(v: string | null | undefined): v is RangeKey {
  return v === "custom" || RANGE_PRESETS.some((p) => p.key === v)
}

export function rangeBounds(r: TimeRange, now: number): { since: number; until: number | null } {
  const today = startOfDay(now)
  switch (r.key) {
    case "1h":
      return { since: now - 3_600_000, until: null }
    case "24h":
      return { since: now - DAY_MS, until: null }
    case "today":
      return { since: today, until: null }
    case "yday": {
      const y = new Date(today)
      y.setDate(y.getDate() - 1)
      return { since: y.getTime(), until: today }
    }
    case "week": {
      const d = new Date(today)
      d.setDate(d.getDate() - ((d.getDay() + 6) % 7))
      return { since: d.getTime(), until: null }
    }
    case "30d":
      return { since: now - 30 * DAY_MS, until: null }
    case "custom": {
      const from = r.from ?? now - 7 * DAY_MS
      const to = r.to ?? now
      return { since: Math.min(from, to), until: to >= now ? null : Math.max(from, to) }
    }
    case "7d":
    default:
      return { since: now - 7 * DAY_MS, until: null }
  }
}

export function inRange(ts: number, r: TimeRange, now: number): boolean {
  const b = rangeBounds(r, now)
  return ts >= b.since && (b.until == null || ts < b.until)
}

export function rangeLabel(r: TimeRange): string {
  return r.key === "custom" ? "Custom" : (RANGE_PRESETS.find((p) => p.key === r.key)?.label ?? "Last 7 days")
}

const dm = (ms: number) => new Date(ms).toLocaleDateString("en-GB", { day: "numeric", month: "short" })

/** "8 – 15 Oct", "15 Sep – 15 Oct", "15 Oct", "07:15 – now". */
export function rangeDates(r: TimeRange, now: number): string {
  const b = rangeBounds(r, now)
  if (r.key === "1h") {
    return `${new Date(b.since).toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit", hour12: false })} – now`
  }
  const end = b.until == null ? now : b.until - 1
  const a = new Date(b.since)
  const z = new Date(end)
  if (a.toDateString() === z.toDateString()) return dm(b.since)
  if (a.getMonth() === z.getMonth() && a.getFullYear() === z.getFullYear()) return `${a.getDate()} – ${dm(end)}`
  return `${dm(b.since)} – ${dm(end)}`
}

/** The next range one step shorter (-1) or longer (+1), or null at the end. */
export function stepRange(key: RangeKey, dir: -1 | 1): RangeKey | null {
  let i = RANGE_STEPS.indexOf(key)
  if (i < 0) i = RANGE_STEPS.indexOf("7d")
  const next = i + dir
  return next < 0 || next >= RANGE_STEPS.length ? null : RANGE_STEPS[next]
}

/* ------------------------------------------------------------------ *
 *  The URL
 * ------------------------------------------------------------------ */

export interface FeedUrlState {
  filters: FeedFilters
  group: GroupBy
  bundle: boolean
  range: TimeRange
  /** The work row open in the Peek. */
  peek?: string
}

const STATUS_FROM_LEGACY: Record<string, WorkStatus> = {
  active: "running",
  waiting: "waiting",
  failed: "failed",
  done: "completed",
}
const STATUS_FROM_VIEW: Record<string, WorkStatus> = { need: "waiting", live: "running", failed: "failed" }
const STATUSES = new Set<string>(["running", "waiting", "failed", "completed", "stopped"])
const GROUP_FROM_LENS: Record<string, GroupBy> = { issues: "object", agents: "who", routines: "object" }
const GROUPS = new Set<string>(["time", "what", "object", "who"])
const SOURCES = new Set<string>(Object.keys(WORK_SOURCE_LABEL))
const WHATS = new Set<string>(["work", "changes", ...Object.keys(CHANGE_TYPES)])

function param(p: URLSearchParams, k: string): string | null {
  const v = p.get(k)?.trim()
  return v ? v : null
}

/**
 * Read the list's state from the URL. New links spell `?view=`, `?group=`,
 * `?range=`; every older spelling still lands (`?status=active|waiting|failed|
 * done`, `?lens=issues|agents|routines`, `?mission=`, `?pipeline=`).
 */
export function parseFeedUrl(p: URLSearchParams): FeedUrlState {
  const view = param(p, "view")
  const statusRaw = param(p, "status")
  const status =
    (view && STATUS_FROM_VIEW[view]) ||
    (statusRaw && (STATUSES.has(statusRaw) ? (statusRaw as WorkStatus) : STATUS_FROM_LEGACY[statusRaw])) ||
    null
  const groupRaw = param(p, "group")
  const lens = param(p, "lens")
  const group: GroupBy = groupRaw && GROUPS.has(groupRaw) ? (groupRaw as GroupBy) : (lens && GROUP_FROM_LENS[lens]) || "time"
  const rangeRaw = param(p, "range")
  let range: TimeRange = isRangeKey(rangeRaw) ? { key: rangeRaw } : DEFAULT_RANGE
  if (range.key === "custom") {
    const from = Date.parse(param(p, "from") ?? "")
    const to = Date.parse(param(p, "to") ?? "")
    range = Number.isNaN(from) ? DEFAULT_RANGE : { key: "custom", from, to: Number.isNaN(to) ? undefined : to }
  }
  const source = param(p, "source")
  const what = param(p, "what")
  const routine = param(p, "pipeline") ?? param(p, "routine")
  // A routine's history is read over a month unless the link said otherwise.
  if (routine && !rangeRaw) range = { key: "30d" }
  return {
    filters: {
      q: param(p, "q") ?? "",
      status,
      what: what && WHATS.has(what) ? (what as WhatFilter) : null,
      routine,
      agent: param(p, "agent"),
      crew: param(p, "crew"),
      source: source && SOURCES.has(source) ? (source as WorkSource) : null,
      issue: param(p, "mission") ?? param(p, "issue"),
    },
    group,
    bundle: param(p, "bundle") !== "off",
    range,
    peek: param(p, "run") ?? undefined,
  }
}

/** The canonical query for a state; empty for the default list. */
export function feedUrlParams(s: FeedUrlState): URLSearchParams {
  const p = new URLSearchParams()
  const f = s.filters
  const view = viewOfStatus(f.status)
  if (view && view !== "all") p.set("view", view)
  else if (f.status) p.set("status", f.status)
  if (f.what) p.set("what", f.what)
  if (f.routine) p.set("pipeline", f.routine)
  if (f.issue) p.set("mission", f.issue)
  if (f.agent) p.set("agent", f.agent)
  if (f.crew) p.set("crew", f.crew)
  if (f.source) p.set("source", f.source)
  if (f.q) p.set("q", f.q)
  if (s.group !== "time") p.set("group", s.group)
  if (!s.bundle) p.set("bundle", "off")
  const defaultRange = f.routine ? "30d" : DEFAULT_RANGE.key
  if (s.range.key !== defaultRange) {
    p.set("range", s.range.key)
    if (s.range.key === "custom") {
      if (s.range.from != null) p.set("from", new Date(s.range.from).toISOString())
      if (s.range.to != null) p.set("to", new Date(s.range.to).toISOString())
    }
  }
  if (s.peek) p.set("run", s.peek)
  return p
}

/* ------------------------------------------------------------------ *
 *  Earlier runs of a routine
 * ------------------------------------------------------------------ */

export interface RunRecordLike {
  id: string
  status: string
  started_at: string
  duration_ms?: number
}

export interface RunHistory {
  bars: { id: string; status: string; durationMs?: number; current: boolean; startedAt: string }[]
  /** Share of finished runs that completed, 0–100; null with nothing finished. */
  successPct: number | null
  /** Median duration of the completed runs, ms. */
  medianMs: number | null
}

const FINISHED = new Set(["completed", "failed", "cancelled", "interrupted"])

/** The last `max` runs, oldest first, and how they went. */
export function runHistory(records: RunRecordLike[], currentId: string | null, max = 20): RunHistory {
  const last = [...records]
    .sort((a, b) => a.started_at.localeCompare(b.started_at) || a.id.localeCompare(b.id))
    .slice(-max)
  const finished = last.filter((r) => FINISHED.has(r.status))
  const ok = finished.filter((r) => r.status === "completed")
  const durations = ok
    .map((r) => r.duration_ms ?? 0)
    .filter((d) => d > 0)
    .sort((a, b) => a - b)
  const mid = Math.floor(durations.length / 2)
  return {
    bars: last.map((r) => ({ id: r.id, status: r.status, durationMs: r.duration_ms, current: r.id === currentId, startedAt: r.started_at })),
    successPct: finished.length ? Math.round((ok.length / finished.length) * 100) : null,
    medianMs: durations.length
      ? durations.length % 2
        ? durations[mid]
        : Math.round((durations[mid - 1] + durations[mid]) / 2)
      : null,
  }
}

/* ------------------------------------------------------------------ *
 *  Export
 * ------------------------------------------------------------------ */

function csvCell(v: string): string {
  return /[",\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v
}

/** The rows on screen as CSV — one line per run or change; bundles unfold. */
export function feedCsv(items: FeedItem[]): string {
  const lines = [["started", "type", "status", "title", "origin", "agents", "duration_ms", "id"].join(",")]
  const work = (w: WorkItem) =>
    lines.push(
      [new Date(w.ts).toISOString(), "work", w.status, w.title, w.origin, w.agents.map((a) => a.name).join(" "), w.durationMs == null ? "" : String(w.durationMs), w.id]
        .map(csvCell)
        .join(","),
    )
  for (const x of items) {
    if (x.type === "work") (x.runs ?? [x]).forEach(work)
    else
      lines.push(
        [new Date(x.ts).toISOString(), `change:${x.kind}`, x.action, x.object, x.who, "", "", x.id].map(csvCell).join(","),
      )
  }
  return lines.join("\n") + "\n"
}
