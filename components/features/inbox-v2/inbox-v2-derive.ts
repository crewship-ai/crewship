import type { InboxItem } from "@/hooks/use-inbox"
import type { ApprovalRow } from "@/lib/types/approvals"
import type { Mission } from "@/lib/types/mission"

import { categoryOf, payloadString, subjectOf } from "../inbox/inbox-derive"
import type { InboxV2Entry } from "./inbox-v2-types"

/**
 * A source-less keeper advisory is kind=escalation for storage compatibility,
 * but there is no decision endpoint and nobody is blocked. Inbox 1.0 inferred
 * urgency from the kind and put these above real approvals. V2 asks the only
 * question that matters: is there a real source action behind this row?
 */
export function isActionableInboxItem(item: InboxItem): boolean {
  if (item.state === "resolved") return false
  if (item.kind === "waitpoint") return true
  if (item.kind === "escalation") {
    const proposalKind = payloadString(item, "kind")
    if (proposalKind === "skill_proposal" || proposalKind === "routine_proposal") return true
    if (payloadString(item, "escalation_type")) return true
    if (payloadString(item, "request_id") && item.payload?.request_type === "access") return true
    return false
  }
  if (item.blocking) return true
  if (item.kind === "memory_consolidation") return Boolean(payloadString(item, "proposal_id"))
  if (item.kind === "schedule_missed") return Boolean(payloadString(item, "schedule_id"))
  if (item.kind === "schedule_circuit_breaker_tripped") return Boolean(payloadString(item, "schedule_id"))
  return false
}

/**
 * The kinds that are actionable but block nobody.
 *
 * A missed occurrence and a tripped breaker both have a real source action
 * behind them — re-enable the schedule — so `isActionableInboxItem` says yes,
 * and for the explorer's Actionable facet that is the right answer. It is the
 * wrong answer for anything ranking by urgency: no run is parked on either
 * one, nothing times out, and there is no counterpart waiting for a reply.
 */
const NOTHING_IS_PARKED_ON = new Set([
  "schedule_missed", "schedule_circuit_breaker_tripped",
])

/**
 * Is a person the only thing standing between an agent and its next step?
 *
 * The stricter half of `isActionableInboxItem`, for surfaces that promise
 * "waiting on you" rather than "you could act on this" — the bell's decisions
 * bucket above all, where the count drives a badge and a warn tone. The two
 * questions are genuinely different and both worth asking; conflating them put
 * schedule advisories at the top of the bell under a heading that said an
 * answer was owed.
 */
export function isBlockingInboxItem(item: InboxItem): boolean {
  if (NOTHING_IS_PARKED_ON.has(item.kind)) return false
  return isActionableInboxItem(item)
}

export function inboxEntry(item: InboxItem): InboxV2Entry {
  const subject = subjectOf(item)
  const deadline = payloadString(item, "timeout_at")
  return {
    key: `inbox:${item.id}`,
    source: "inbox",
    title: item.title,
    summary: item.body_md ?? "",
    subject: subject.label,
    category: categoryOf(item),
    priority: item.priority,
    createdAt: item.resolved_at || item.created_at,
    deadlineAt: deadline || null,
    unread: item.state === "unread",
    actionable: isActionableInboxItem(item),
    historical: item.state === "resolved",
    outcome: item.resolved_action,
    inboxItem: item,
  }
}

export function approvalEntry(row: ApprovalRow): InboxV2Entry {
  const pending = row.status === "pending"
  const tool = typeof row.payload?.tool === "string" ? row.payload.tool : ""
  return {
    key: `approval:${row.id}`,
    source: "approval",
    title: approvalTitle(row),
    summary: row.reason,
    subject: row.requested_by || row.agent_id || "Agent",
    category: tool ? `approval · ${tool}` : `approval · ${row.kind}`,
    priority: row.kind === "destructive_op" ? "urgent" : "high",
    createdAt: row.decided_at || row.created_at,
    deadlineAt: row.timeout_at,
    unread: pending,
    actionable: pending,
    historical: !pending,
    outcome: pending ? null : row.status,
    approval: row,
  }
}

export function approvalTitle(row: ApprovalRow): string {
  const tool = typeof row.payload?.tool === "string" ? row.payload.tool : ""
  if (tool) return `Approve ${tool}`
  switch (row.kind) {
    case "destructive_op": return "Approve a destructive operation"
    case "cost_threshold": return "Approve additional spend"
    case "target_environment": return "Approve target environment"
    case "ephemeral_hire": return "Approve agent hire"
    case "autonomy_gate": return "Approve agent-created resource"
    default: return "Approval requested"
  }
}

export function missionEntries(missions: Mission[]): InboxV2Entry[] {
  const out: InboxV2Entry[] = []
  for (const mission of missions) {
    for (const task of mission.tasks ?? []) {
      const review = task.needs_review && task.status !== "COMPLETED" && task.status !== "SKIPPED"
      const exceptional = task.status === "FAILED" || task.status === "BLOCKED"
      if (!review && !exceptional) continue
      out.push({
        key: `mission:${mission.id}:${task.id}`,
        source: "mission",
        title: review ? `Review: ${task.title}` : `${task.status === "FAILED" ? "Task failed" : "Task blocked"}: ${task.title}`,
        summary: task.error_message || task.result_summary || mission.title,
        subject: task.agent_name || task.agent_slug || mission.lead_agent_name || "Mission",
        category: review ? "mission.review" : `mission.${task.status.toLowerCase()}`,
        priority: task.status === "FAILED" ? "high" : "medium",
        createdAt: task.updated_at || mission.updated_at,
        unread: true,
        actionable: review,
        historical: false,
        mission,
        task,
      })
    }
  }
  return out
}

/**
 * Approval-queue rows and inbox rows are two projections of ONE decision, and
 * the link between them is written in opposite directions depending on which
 * producer created it:
 *
 *   internal_autonomy_gate.go  → inbox payload carries `approval_id`
 *   agents_hire.go             → approval payload carries `inbox_item_id`
 *
 * V2 read only the first direction, so every gated ephemeral hire arrived in
 * Needs action twice — once as "Hire ephemeral agent: …" (the waitpoint) and
 * once as "Approve agent.hire" (the queue row), which do not even look
 * related. Verified against a live hire on dev2 before this was written.
 *
 * Returns the approval ids the merged feed must not render, given the inbox
 * rows it already has.
 */
export function suppressedApprovalIDs(items: InboxItem[], rows: ApprovalRow[]): Set<string> {
  const out = new Set<string>()
  const inboxIDs = new Set(items.map((item) => item.id))
  for (const item of items) {
    const linked = item.payload?.approval_id
    if (typeof linked === "string") out.add(linked)
  }
  for (const row of rows) {
    const linked = row.payload?.inbox_item_id
    if (typeof linked === "string" && inboxIDs.has(linked)) out.add(row.id)
  }
  return out
}

/**
 * Resolve the reading pane's selection. A `request:<id>` key comes from a
 * deep link (?item=), where the caller knows an id but not which source it
 * belongs to, so it matches any entry whose key ends in that id.
 *
 * There is deliberately no "fall back to the first row". The previous
 * `?? visible[0]` meant a link to a missing, foreign or still-loading item
 * silently armed a DIFFERENT decision: /inbox-v2?item=nope rendered a live
 * Approve button for whatever happened to sort first.
 */
export function selectEntry(entries: InboxV2Entry[], key: string | null): InboxV2Entry | null {
  if (!key) return null
  const exact = entries.find((entry) => entry.key === key)
  if (exact) return exact
  if (key.startsWith("request:")) {
    const id = key.slice("request:".length)
    return entries.find((entry) => entry.key.endsWith(`:${id}`)) ?? null
  }
  return null
}

export function groupAdvisories(entries: InboxV2Entry[]): InboxV2Entry[] {
  const grouped = new Map<string, InboxV2Entry[]>()
  const rest: InboxV2Entry[] = []
  for (const entry of entries) {
    const item = entry.inboxItem
    if (
      item &&
      !entry.actionable &&
      item.kind === "escalation" &&
      !payloadString(item, "escalation_type") &&
      !payloadString(item, "kind")
    ) {
      const key = `${item.sender_name || "system"}:${item.title.split(":", 1)[0]}`
      const bucket = grouped.get(key) ?? []
      bucket.push(entry)
      grouped.set(key, bucket)
    } else {
      rest.push(entry)
    }
  }

  for (const [key, rows] of grouped) {
    if (rows.length === 1) {
      rest.push(rows[0])
      continue
    }
    const items = rows.flatMap((row) => row.inboxItem ? [row.inboxItem] : [])
    rest.push({
      ...rows[0],
      key: `group:${key}`,
      source: "group",
      title: rows[0].inboxItem?.sender_name === "Skill Curator"
        ? "Skill checks could not run"
        : rows[0].title.split(":", 1)[0],
      summary: `${rows.length} related updates grouped · no client action required`,
      category: "system.health",
      unread: rows.some((row) => row.unread),
      groupedItems: items,
    })
  }
  return rest
}

/**
 * The facet vocabulary.
 *
 * Every value here maps to a predicate over a REAL field. The three selects
 * this replaces did not: "type" filtered on which of the three fetches a row
 * arrived in (a client-only field, with an invented "grouped incidents"
 * member), "subject" was harvested from whatever rows happened to be loaded
 * and meant three different things at once (sender for inbox rows, a raw user
 * id for approvals, agent name for missions), and "priority" is only a real
 * column for inbox rows — for approvals and mission signals `approvalEntry`
 * and `missionEntries` synthesise it. A filter has to be answerable, so
 * neither subject nor priority is offered as one.
 */
export const INBOX_V2_TYPES = [
  // the seven values of internal/inbox AllKinds, which the inbox_items CHECK
  // constraint and TestInboxKindsMatchSchema keep honest…
  { key: "waitpoint", label: "Waitpoint" },
  { key: "escalation", label: "Escalation" },
  { key: "failed_run", label: "Failed run" },
  { key: "message", label: "Message" },
  { key: "memory_consolidation", label: "Memory proposal" },
  { key: "schedule_missed", label: "Missed schedule" },
  { key: "schedule_circuit_breaker_tripped", label: "Circuit breaker" },
  // …plus the two other systems this inbox merges, named for what they are
  // rather than for the endpoint they came from.
  { key: "approval", label: "Approval gate" },
  { key: "mission", label: "Mission signal" },
] as const

export type InboxV2TypeKey = (typeof INBOX_V2_TYPES)[number]["key"]
export type InboxV2DeadlineKey = "hour" | "today" | "none"

export interface InboxV2Filters {
  search: string
  type: InboxV2TypeKey | null
  deadline: InboxV2DeadlineKey | null
  unreadOnly: boolean
}

export const EMPTY_INBOX_V2_FILTERS: InboxV2Filters = {
  search: "",
  type: null,
  deadline: null,
  unreadOnly: false,
}

/** The type facet a row answers to. A grouped advisory answers as its members. */
export function entryType(entry: InboxV2Entry): InboxV2TypeKey | null {
  if (entry.source === "approval") return "approval"
  if (entry.source === "mission") return "mission"
  const kind = entry.inboxItem?.kind ?? entry.groupedItems?.[0]?.kind
  if (!kind) return null
  return INBOX_V2_TYPES.some((t) => t.key === kind) ? (kind as InboxV2TypeKey) : null
}

/**
 * Deadline bucket, from the row's real `timeout_at` — payload.timeout_at for a
 * waitpoint, the column for an approval. A row whose deadline is further out
 * than today answers to neither bucket, which is the truthful answer; it is
 * not silently folded into "today".
 */
export function deadlineBucket(entry: InboxV2Entry, now = Date.now()): InboxV2DeadlineKey | "later" {
  if (!entry.deadlineAt) return "none"
  const at = Date.parse(entry.deadlineAt)
  if (Number.isNaN(at)) return "none"
  if (at - now <= 3_600_000) return "hour"
  const endOfDay = new Date(now)
  endOfDay.setHours(23, 59, 59, 999)
  return at <= endOfDay.getTime() ? "today" : "later"
}

export function matchesSearch(entry: InboxV2Entry, search: string): boolean {
  const q = search.trim().toLowerCase()
  if (!q) return true
  return `${entry.title} ${entry.summary} ${entry.subject} ${entry.category}`.toLowerCase().includes(q)
}

/**
 * Facet counts.
 *
 * Counted over the whole feed, which is exact ONLY because inbox-v2 loads
 * every page before rendering (`useInbox(..., { loadAll: true })`). The day
 * the feed becomes lazily paginated these numbers stop being counts and
 * become "counts of what happens to be downloaded" — at which point they have
 * to come from the server as a GROUP BY, the way Routines gets its bucket
 * counts. Counting a page and presenting it as a total is the failure this
 * whole filter rework is fixing; do not reintroduce it here.
 */
export function facetCounts(entries: InboxV2Entry[], now = Date.now()) {
  const type = {} as Record<InboxV2TypeKey, number>
  for (const t of INBOX_V2_TYPES) type[t.key] = 0
  const deadline: Record<InboxV2DeadlineKey, number> = { hour: 0, today: 0, none: 0 }
  let unread = 0
  for (const entry of entries) {
    const t = entryType(entry)
    if (t) type[t] += 1
    const d = deadlineBucket(entry, now)
    if (d !== "later") deadline[d] += 1
    if (entry.unread) unread += 1
  }
  return { type, deadline, unread, total: entries.length }
}

/**
 * Archiving is not deciding.
 *
 * History holds both — the PRD says so — but they are not the same thing, and
 * listing six archived curator advisories under a heading that reads "Decision
 * records" tells the reader six decisions were made. They were one click on
 * "Archive 6 updates". Splitting them keeps the receipt list honest and stops
 * the noise burying the decisions it sits next to.
 */
const NOT_A_DECISION = new Set([
  // cleared by a person, but not a decision on the request
  "archived", "dismissed",
  // settled by the system with nobody looking: the escalation expiry sweep
  // (escalation_lifecycle.go), the waitpoint timeout sweeps and
  // CancelWaitpointsForRun (pipeline/waitpoints.go), and the credential
  // name-match auto-resolve — which writes "approve" with resolved_by=system
  // and whose own comment calls it "a spurious approval in the audit trail".
  "expired", "timed_out", "cancelled",
  // The approvals queue spells its own sweep differently — harbormaster's
  // vocabulary is pending/approved/denied/timeout/cancelled, so "timeout"
  // here is not a duplicate of "timed_out" above. Both entry sources feed
  // this one set, and neither spelling is a decision.
  "timeout",
])

export function isArchivedNotDecided(entry: InboxV2Entry): boolean {
  if (entry.outcome && NOT_A_DECISION.has(entry.outcome)) return true
  // A decision has a decider. The auto-resolve paths leave the decider empty,
  // which is the only thing that separates "the system gave up on it" from
  // "somebody approved it" when the action string is the same word.
  //
  // Both fields, because the two entry sources are disjoint: inboxEntry sets
  // `inboxItem` and approvalEntry sets `approval`, never both. Reading only
  // the inbox one meant every decided approval had no decider to find and the
  // explorer filed the whole approval history under Archived — the exact
  // mislabelling the comment above this set is about, just from the other
  // source.
  const decided = entry.inboxItem?.resolved_by_user_id ?? entry.approval?.decided_by
  return Boolean(entry.outcome) && !decided
}

export function filterEntries(
  entries: InboxV2Entry[],
  filters: InboxV2Filters,
  now = Date.now(),
): InboxV2Entry[] {
  return entries.filter((entry) => {
    if (filters.unreadOnly && !entry.unread) return false
    if (filters.type && entryType(entry) !== filters.type) return false
    if (filters.deadline && deadlineBucket(entry, now) !== filters.deadline) return false
    return matchesSearch(entry, filters.search)
  })
}

export function sortEntries(entries: InboxV2Entry[]): InboxV2Entry[] {
  return [...entries].sort((a, b) => {
    if (a.actionable && b.actionable) {
      const ad = a.deadlineAt ? Date.parse(a.deadlineAt) : Number.POSITIVE_INFINITY
      const bd = b.deadlineAt ? Date.parse(b.deadlineAt) : Number.POSITIVE_INFINITY
      if (ad !== bd) return ad - bd
      return Date.parse(a.createdAt) - Date.parse(b.createdAt)
    }
    return Date.parse(b.createdAt) - Date.parse(a.createdAt)
  })
}

export function filterAndSortEntries(
  entries: InboxV2Entry[],
  filters: InboxV2Filters,
  now = Date.now(),
): InboxV2Entry[] {
  return sortEntries(filterEntries(entries, filters, now))
}

/* ------------------------------------------------------------- row anatomy */

/**
 * What a row IS, as a word and a tone (README §2: a pill is a dot and a word,
 * never a colour alone). The server's kind vocabulary is engine-shaped —
 * "waitpoint", "escalation" — and an escalation is three different asks
 * depending on its type; this is the client-facing name for each.
 */
export interface EntryKindPill {
  label: string
  tone: "success" | "blue" | "warn" | "danger" | "muted" | "purple"
}

export function entryKindPill(entry: InboxV2Entry): EntryKindPill {
  if (entry.source === "approval") {
    return entry.approval?.kind === "ephemeral_hire"
      ? { label: "Hire", tone: "blue" }
      : { label: "Approval", tone: "blue" }
  }
  if (entry.source === "mission") {
    return entry.task?.needs_review ? { label: "Review", tone: "warn" } : { label: "Task", tone: "danger" }
  }
  if (entry.source === "group") return { label: "System", tone: "muted" }
  const item = entry.inboxItem
  if (!item) return { label: "Update", tone: "muted" }
  switch (item.kind) {
    case "waitpoint":
      return payloadString(item, "kind") === "hire" ? { label: "Hire", tone: "blue" } : { label: "Approval", tone: "blue" }
    case "escalation": {
      const sub = payloadString(item, "kind")
      if (sub === "skill_proposal") return { label: "Skill proposal", tone: "purple" }
      if (sub === "routine_proposal") return { label: "Routine proposal", tone: "purple" }
      if (item.payload?.request_type === "access") return { label: "Access request", tone: "warn" }
      switch (payloadString(item, "escalation_type")) {
        case "LINK": return { label: "Link", tone: "blue" }
        case "CREDENTIAL": return { label: "Credential", tone: "warn" }
        case "TEXT": return { label: "Question", tone: "warn" }
      }
      return { label: "Notice", tone: "muted" }
    }
    case "failed_run": return { label: "Failed run", tone: "danger" }
    case "schedule_missed": return { label: "Missed run", tone: "warn" }
    case "schedule_circuit_breaker_tripped": return { label: "Paused schedule", tone: "warn" }
    case "memory_consolidation": return { label: "Memory proposal", tone: "purple" }
    case "message":
      if (payloadString(item, "chat_url")) return { label: "Reply", tone: "blue" }
      if (payloadString(item, "issue_identifier")) return { label: "Issue", tone: "blue" }
      return { label: "Update", tone: "muted" }
  }
  return { label: "Update", tone: "muted" }
}

/**
 * The row's title, without the server's prefix.
 *
 * `escalation_handler.go` titles every escalation "Agent escalation: <reason>"
 * — a 130px prefix that says nothing the kind pill does not, and it is the
 * reason ("Need the Grafana …") that gets truncated off a 340px column. The
 * payload carries the reason whole; the prefix is stripped when it is there.
 */
const ESCALATION_TITLE_PREFIX = /^agent escalation:\s*/i

export function entryTitle(entry: InboxV2Entry): string {
  const item = entry.inboxItem
  if (item?.kind === "escalation") {
    const reason = payloadString(item, "reason")
    if (ESCALATION_TITLE_PREFIX.test(item.title)) return reason || item.title.replace(ESCALATION_TITLE_PREFIX, "")
  }
  return entry.title
}

/**
 * The verb on the row — what pressing it will let the person do (README §1:
 * every row that needs someone carries a verb, never a bare chevron).
 */
export function entryVerb(entry: InboxV2Entry): string {
  if (!entry.actionable) return entry.historical ? "Record" : "Open"
  if (entry.source === "approval") return "Approve"
  if (entry.source === "mission") return "Review"
  const item = entry.inboxItem
  if (!item) return "Open"
  switch (item.kind) {
    case "waitpoint": return "Approve"
    case "escalation": {
      const sub = payloadString(item, "kind")
      if (sub === "skill_proposal" || sub === "routine_proposal") return "Review"
      switch (payloadString(item, "escalation_type")) {
        case "LINK": return "Review"
        case "CREDENTIAL": return "Grant"
        default: return "Answer"
      }
    }
    case "failed_run": return "Retry"
    case "schedule_missed": return "Run now"
    case "schedule_circuit_breaker_tripped": return "Re-enable"
    case "memory_consolidation": return "Review"
  }
  return "Open"
}

/** The crew a row belongs to, by id, when any source names one. */
export function entryCrewId(entry: InboxV2Entry): string | null {
  if (entry.source === "approval") return entry.approval?.crew_id ?? null
  if (entry.source === "mission") return entry.mission?.crew_id ?? null
  const item = entry.inboxItem ?? entry.groupedItems?.[0]
  if (!item) return null
  return payloadString(item, "invoking_crew_id") || payloadString(item, "crew_id") || null
}

/**
 * The agent behind a row, as the slug the roster is keyed by. Escalations
 * put the agent's SLUG in sender_name; hire waitpoints put the hired agent's
 * display name in payload.agent_name (which is not on the roster yet), so
 * both are offered and the caller resolves what it can.
 */
export function entryAgentRef(entry: InboxV2Entry): { slug: string | null; label: string } {
  if (entry.source === "approval") {
    const slug = entry.approval?.agent_id ?? null
    return { slug, label: entry.subject }
  }
  const item = entry.inboxItem
  if (!item) return { slug: null, label: entry.subject }
  const slug = payloadString(item, "agent_slug") || (item.sender_type === "agent" ? item.sender_name ?? null : null)
  return { slug: slug || null, label: entry.subject }
}

/**
 * A row's outcome as a status the shared pill knows. The two sources spell
 * the same decision differently (inbox `approve` / `reject`, queue
 * `approved` / `denied`, sweeps `expired` / `timeout` / `cancelled`), and the
 * explorer used to print whichever word it got.
 */
export function outcomeStatus(outcome: string | null | undefined): string | null {
  if (!outcome) return null
  switch (outcome.toLowerCase()) {
    case "approve": case "approved": return "APPROVED"
    case "reject": case "rejected": return "REJECTED"
    case "deny": case "denied": return "DENIED"
    case "cancel": case "cancelled": return "CANCELLED"
    case "expired": case "timeout": case "timed_out": return "EXPIRED"
    case "archived": return "ARCHIVED"
    case "dismissed": return "DISMISSED"
    case "retried": return "RETRIED"
    case "ran": return "RAN"
    case "reenabled": return "RE_ENABLED"
    case "resolved": return "RESOLVED"
    default: return outcome
  }
}
