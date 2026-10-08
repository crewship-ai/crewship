// The work queue and webhook deliveries, said the way Activity says runs (#3012).
//
// The ledger's own vocabulary has ten states, two of which nothing ever
// writes (waiting, expired). A reader asks five questions — does something
// need me, what is in line, what runs, what finished, what went wrong — so the
// states fold into those tones here, where a test can hold them, and every
// card, lane and rail row reads the same fold.

import type { LedgerAgent, LedgerCrew, WorkItem, WorkSource, WorkState } from "@/hooks/use-work-items"
import type { WebhookDelivery } from "@/hooks/use-webhook-deliveries"
import { formatDurationMs } from "@/lib/activity-stream"

export type LedgerTone = "needs" | "line" | "running" | "done" | "failed" | "cancelled"

export const LEDGER_TONES: readonly LedgerTone[] = ["needs", "line", "running", "done", "failed", "cancelled"]

export const LEDGER_TONE_LABEL: Record<LedgerTone, string> = {
  needs: "Needs you",
  line: "In the queue",
  running: "Running",
  done: "Done",
  failed: "Failed",
  cancelled: "Cancelled",
}

/** Text colour per tone — the rail's status rows. */
export const LEDGER_TONE_TEXT: Record<LedgerTone, string> = {
  needs: "text-warn",
  line: "text-info",
  running: "text-primary",
  done: "text-success",
  failed: "text-destructive",
  cancelled: "text-muted-foreground",
}

export const LEDGER_TONE_DOT: Record<LedgerTone, string> = {
  needs: "bg-warn",
  line: "bg-info",
  running: "bg-primary",
  done: "bg-success",
  failed: "bg-destructive",
  cancelled: "bg-muted-foreground",
}

export function ledgerTone(state: WorkState): LedgerTone {
  switch (state) {
    case "needs_reconciliation":
      return "needs"
    case "queued":
    case "retry_wait":
    case "waiting":
      return "line"
    case "starting":
    case "running":
      return "running"
    case "succeeded":
      return "done"
    case "cancelled":
      return "cancelled"
    default:
      return "failed"
  }
}

const DELETED = "Deleted agent"

export function agentName(agent: LedgerAgent | null | undefined): string {
  return agent?.name || DELETED
}

/** No record left, or removed from the workspace with its record kept. */
export function isDeletedAgent(agent: LedgerAgent | null | undefined): boolean {
  return !agent || Boolean(agent.deleted)
}

const RAW_ID = /\bc[a-z0-9]{20,}\b/g

/**
 * A state reason as a reader should see it. The ledger writes reasons for
 * operators — "resolved by <user id>: runtime confirmed stopped by operator;
 * <what they wrote>", "agent <id> was deleted" — and the ids mean nothing on
 * a page that already names the agent.
 */
export function humanReason(reason: string | null | undefined): string {
  let r = (reason ?? "").trim()
  if (!r) return ""
  const settled = /^resolved by \S+:\s*runtime confirmed stopped by operator;\s*(.*)$/i.exec(r)
  if (settled) r = `${settled[1].trim() || "Outcome recorded"} — settled by hand`
  r = r.replace(/\bagent\s+c[a-z0-9]{20,}\b/gi, "the agent").replace(RAW_ID, "…")
  return r
}

/** First letter up, for a reason that stands on its own. */
export function sentence(s: string): string {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s
}

const SOURCE_WORD: Record<WorkSource, string> = {
  webhook: "Webhook",
  chat: "Chat turn",
  assignment: "Delegated task",
  schedule: "Scheduled",
  pipeline_step: "Routine step",
  manual: "Started by hand",
}

/** What came in: the event a delivery carried, else the producer in words. */
export function workSubject(item: WorkItem): string {
  return item.event_type || SOURCE_WORD[item.source] || item.source
}

/** "invoice.export" → "invoice.*"; a dotless event is its own family. */
export function eventFamily(event: string | null | undefined): string {
  if (!event) return ""
  const dot = event.indexOf(".")
  return dot > 0 ? `${event.slice(0, dot)}.*` : event
}

/** How it ended, as a sentence — never a raw state or id. */
export function workLine(item: WorkItem): string {
  const reason = humanReason(item.state_reason)
  switch (ledgerTone(item.state)) {
    case "needs":
      return reason ? `Outcome unclear — ${reason}` : "Outcome unclear"
    case "line":
      if (item.state === "retry_wait") return "Retry scheduled"
      return reason || "Waiting for capacity"
    case "running":
      return item.state === "starting" ? "Starting" : "Running"
    case "done":
      return item.duration_ms != null ? `Done · ${formatDurationMs(item.duration_ms)}` : "Done"
    case "cancelled":
      return (item.attempt_count ?? 0) === 0 ? "Cancelled before it started" : "Cancelled while running"
    case "failed":
      if (item.state === "expired") return "Expired before it started"
      return sentence(reason) || "Failed"
  }
}

export type LedgerCounts = Record<LedgerTone, number> & { total: number }

export function ledgerCounts(items: readonly WorkItem[]): LedgerCounts {
  const counts: LedgerCounts = { needs: 0, line: 0, running: 0, done: 0, failed: 0, cancelled: 0, total: 0 }
  for (const it of items) {
    counts[ledgerTone(it.state)]++
    counts.total++
  }
  return counts
}

export interface HeadPart {
  text: string
  tone: "default" | "primary" | "warn" | "destructive"
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`

export function ledgerHeadline(c: LedgerCounts): HeadPart[] {
  const parts: HeadPart[] = [{ text: plural(c.total, "piece of work", "pieces of work"), tone: "default" }]
  if (c.needs) parts.push({ text: `${c.needs} needs you`, tone: "warn" })
  if (c.running) parts.push({ text: `${c.running} running`, tone: "primary" })
  if (c.line) parts.push({ text: `${c.line} waiting in line`, tone: "default" })
  if (c.done) parts.push({ text: `${c.done} done`, tone: "default" })
  if (c.failed) parts.push({ text: `${c.failed} failed`, tone: "destructive" })
  if (c.cancelled) parts.push({ text: `${c.cancelled} cancelled`, tone: "default" })
  return parts
}

function median(values: number[]): number | null {
  if (values.length === 0) return null
  const s = [...values].sort((a, b) => a - b)
  const mid = Math.floor(s.length / 2)
  return s.length % 2 ? s[mid] : (s[mid - 1] + s[mid]) / 2
}

export interface LedgerFlow {
  arrived: number
  sources: number
  line: { count: number; blocked: boolean }
  running: number
  agents: number
  done: number
  medianDoneMs: number | null
  needs: number
  failed: number
  causes: number
  cancelled: number
}

/** Arrived → In line → Running → Done, with the three ways out. */
export function ledgerFlow(items: readonly WorkItem[]): LedgerFlow {
  const c = ledgerCounts(items)
  const blockedAgents = new Set(items.filter((i) => ledgerTone(i.state) === "needs").map((i) => i.agent_id))
  const inLine = items.filter((i) => ledgerTone(i.state) === "line")
  return {
    arrived: c.total,
    sources: new Set(items.map((i) => eventFamily(i.event_type) || i.source)).size,
    line: { count: c.line, blocked: inLine.some((i) => blockedAgents.has(i.agent_id)) },
    running: c.running,
    agents: new Set(items.map((i) => i.agent_id)).size,
    done: c.done,
    medianDoneMs: median(
      items.filter((i) => ledgerTone(i.state) === "done" && i.duration_ms != null).map((i) => i.duration_ms as number),
    ),
    needs: c.needs,
    failed: c.failed,
    causes: failureCauses(items).length,
    cancelled: c.cancelled,
  }
}

export interface NeedsYouEntry {
  item: WorkItem
  /** Items of the same agent held in line behind it — one live run per agent. */
  behind: number
}

export function needsYou(items: readonly WorkItem[]): NeedsYouEntry[] {
  return items
    .filter((i) => ledgerTone(i.state) === "needs")
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
    .map((item) => ({
      item,
      behind: items.filter((o) => o.agent_id === item.agent_id && ledgerTone(o.state) === "line").length,
    }))
}

export interface FailureCause {
  cause: string
  count: number
  agents: string[]
  events: string[]
  latestId: string
}

export function failureCauses(items: readonly WorkItem[]): FailureCause[] {
  const by = new Map<string, { items: WorkItem[] }>()
  for (const it of items) {
    if (ledgerTone(it.state) !== "failed") continue
    const cause = it.state === "expired" ? "Expired before it started" : humanReason(it.state_reason) || "No reason recorded"
    const g = by.get(cause) ?? { items: [] }
    g.items.push(it)
    by.set(cause, g)
  }
  return [...by.entries()]
    .map(([cause, g]) => {
      const newest = [...g.items].sort((a, b) => b.created_at.localeCompare(a.created_at))[0]
      return {
        cause,
        count: g.items.length,
        agents: [...new Set(g.items.map((i) => agentName(i.agent)))],
        events: [...new Set(g.items.map((i) => workSubject(i)))].sort(),
        latestId: newest.id,
      }
    })
    .sort((a, b) => b.count - a.count || a.cause.localeCompare(b.cause))
}

export interface LaneDot {
  id: string
  tone: LedgerTone
  left: number
  at: string
  subject: string
}

export interface LedgerLane {
  key: string
  name: string
  agent: LedgerAgent | null
  crew: LedgerCrew | null
  /** What this lane receives, for its subtitle. */
  feeds: string[]
  dots: LaneDot[]
  summary: { text: string; tone: LedgerTone | "default" }
}

/** One lane per agent across the window, busiest first; a deleted agent last. */
export function ledgerLanes(items: readonly WorkItem[], win: { from: number; to: number }): LedgerLane[] {
  const span = Math.max(1, win.to - win.from)
  const by = new Map<string, WorkItem[]>()
  for (const it of items) {
    const list = by.get(it.agent_id) ?? []
    list.push(it)
    by.set(it.agent_id, list)
  }
  return [...by.entries()]
    .map(([key, list]): LedgerLane & { size: number } => {
      const c = ledgerCounts(list)
      const summary: LedgerLane["summary"] = c.needs
        ? { text: `${c.needs} needs you`, tone: "needs" }
        : c.failed
          ? { text: `${c.failed} failed`, tone: "failed" }
          : c.running
            ? { text: `${c.running} running`, tone: "running" }
            : { text: `${c.done} done`, tone: "default" }
      const agent = list.find((i) => i.agent)?.agent ?? null
      return {
        key,
        name: agentName(agent),
        agent,
        crew: list.find((i) => i.crew)?.crew ?? null,
        feeds: [...new Set(list.map((i) => eventFamily(i.event_type) || SOURCE_WORD[i.source]))].slice(0, 3),
        dots: list.map((i) => ({
          id: i.id,
          tone: ledgerTone(i.state),
          left: Math.min(100, Math.max(0, ((Date.parse(i.created_at) - win.from) / span) * 100)),
          at: i.created_at,
          subject: workSubject(i),
        })),
        summary,
        size: list.length,
      }
    })
    .sort((a, b) => Number(isDeletedAgent(a.agent)) - Number(isDeletedAgent(b.agent)) || b.size - a.size || a.name.localeCompare(b.name))
    .map(({ size: _size, ...lane }) => lane)
}

export interface RailAgent {
  id: string
  name: string
  agent: LedgerAgent | null
  count: number
  state: "blocked" | "running" | "idle" | "deleted"
}

/** The rail's AGENTS section: who holds work, and whether their queue moves. */
export function ledgerAgents(items: readonly WorkItem[]): RailAgent[] {
  const by = new Map<string, WorkItem[]>()
  for (const it of items) by.set(it.agent_id, [...(by.get(it.agent_id) ?? []), it])
  return [...by.entries()]
    .map(([id, list]) => {
      const agent = list.find((i) => i.agent)?.agent ?? null
      const tones = new Set(list.map((i) => ledgerTone(i.state)))
      const state: RailAgent["state"] = isDeletedAgent(agent)
        ? "deleted"
        : tones.has("needs")
          ? "blocked"
          : tones.has("running")
            ? "running"
            : "idle"
      return { id, name: agentName(agent), agent, count: list.length, state }
    })
    .sort((a, b) => Number(isDeletedAgent(a.agent)) - Number(isDeletedAgent(b.agent)) || b.count - a.count || a.name.localeCompare(b.name))
}

/** The rail's EVENTS section: families with counts, largest first. */
export function familyCounts(events: readonly string[]): { family: string; count: number }[] {
  const by = new Map<string, number>()
  for (const e of events) {
    const f = eventFamily(e)
    if (f) by.set(f, (by.get(f) ?? 0) + 1)
  }
  return [...by.entries()]
    .map(([family, count]) => ({ family, count }))
    .sort((a, b) => b.count - a.count || a.family.localeCompare(b.family))
}

export interface WorkNarrowing {
  tone?: LedgerTone | "all"
  agentId?: string | null
  family?: string | null
}

export function narrowWork(items: readonly WorkItem[], n: WorkNarrowing): WorkItem[] {
  return items.filter(
    (i) =>
      (!n.tone || n.tone === "all" || ledgerTone(i.state) === n.tone) &&
      (!n.agentId || i.agent_id === n.agentId) &&
      (!n.family || eventFamily(i.event_type) === n.family),
  )
}

export function inWindow<T>(rows: readonly T[], at: (r: T) => string, from: number): T[] {
  return rows.filter((r) => {
    const t = Date.parse(at(r))
    return Number.isFinite(t) && t >= from
  })
}

// ── Deliveries ─────────────────────────────────────────────────────────────

export type DeliveryTone = "accepted" | "ignored"

export function deliveryTone(d: WebhookDelivery): DeliveryTone {
  return d.filter_decision === "ignored" ? "ignored" : "accepted"
}

/** What the delivery became: the filter's reason, or the work and its state. */
export function deliveryLine(d: WebhookDelivery): string {
  if (deliveryTone(d) === "ignored") {
    if (d.event_type === "ping") return "Connection test — nothing to do"
    const reason = humanReason(d.filter_reason)
    return reason && reason.toLowerCase() !== d.event_type.toLowerCase() ? sentence(reason) : "Ignored — nothing to do"
  }
  if (!d.work_id) return "Accepted"
  if (!d.work_state) return "→ work"
  return `→ work · ${LEDGER_TONE_LABEL[ledgerTone(d.work_state)].toLowerCase()}`
}

export interface EndpointHealth {
  endpointId: string
  name: string
  agent: LedgerAgent | null
  count: number
  families: string[]
  last: string
  profile: string
  verdict: "ok" | "blocked" | "gone"
}

/**
 * Is this webhook arriving at all, and does its work move? One entry per
 * endpoint, busiest first; a deleted endpoint last.
 */
export function endpointHealth(deliveries: readonly WebhookDelivery[], _now = Date.now()): EndpointHealth[] {
  const by = new Map<string, WebhookDelivery[]>()
  for (const d of deliveries) by.set(d.endpoint_id, [...(by.get(d.endpoint_id) ?? []), d])
  return [...by.entries()]
    .map(([endpointId, list]) => {
      const sorted = [...list].sort((a, b) => b.received_at.localeCompare(a.received_at))
      const agent = list.find((d) => d.agent)?.agent ?? null
      const gone = isDeletedAgent(agent) && list.every((d) => d.endpoint_kind === "agent")
      const blocked = list.some((d) => d.work_state === "needs_reconciliation")
      return {
        endpointId,
        name: agent?.name || (gone ? DELETED : endpointId),
        agent,
        count: list.length,
        families: familyCounts(list.map((d) => d.event_type)).map((f) => f.family),
        last: sorted[0].received_at,
        profile: sorted[0].profile,
        verdict: gone ? ("gone" as const) : blocked ? ("blocked" as const) : ("ok" as const),
      }
    })
    .sort((a, b) => Number(a.verdict === "gone") - Number(b.verdict === "gone") || b.count - a.count || a.name.localeCompare(b.name))
}

export interface DeliveryNarrowing {
  decision?: DeliveryTone | "all"
  endpointId?: string | null
  family?: string | null
}

export function narrowDeliveries(deliveries: readonly WebhookDelivery[], n: DeliveryNarrowing): WebhookDelivery[] {
  return deliveries.filter(
    (d) =>
      (!n.decision || n.decision === "all" || deliveryTone(d) === n.decision) &&
      (!n.endpointId || d.endpoint_id === n.endpointId) &&
      (!n.family || eventFamily(d.event_type) === n.family),
  )
}

export interface DeliveryLane {
  family: string
  dots: { id: string; tone: DeliveryTone; workTone: LedgerTone | null; left: number; at: string }[]
  accepted: number
  ignored: number
}

/** One lane per event family; every delivery a dot. */
export function deliveryLanes(deliveries: readonly WebhookDelivery[], win: { from: number; to: number }): DeliveryLane[] {
  const span = Math.max(1, win.to - win.from)
  const by = new Map<string, WebhookDelivery[]>()
  for (const d of deliveries) {
    const f = eventFamily(d.event_type) || "unknown"
    by.set(f, [...(by.get(f) ?? []), d])
  }
  return [...by.entries()]
    .map(([family, list]) => ({
      family,
      dots: list.map((d) => ({
        id: d.id,
        tone: deliveryTone(d),
        workTone: d.work_state ? ledgerTone(d.work_state) : null,
        left: Math.min(100, Math.max(0, ((Date.parse(d.received_at) - win.from) / span) * 100)),
        at: d.received_at,
      })),
      accepted: list.filter((d) => deliveryTone(d) === "accepted").length,
      ignored: list.filter((d) => deliveryTone(d) === "ignored").length,
    }))
    .sort((a, b) => b.dots.length - a.dots.length || a.family.localeCompare(b.family))
}
