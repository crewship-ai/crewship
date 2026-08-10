// The decisions behind the Activity overview.
//
// The overview answers two questions and nothing else: "is anything waiting
// on me" and "what is broken". Both turn out to be judgements rather than
// counts, which is why they live here as pure functions instead of inline
// `filter` calls in the JSX:
//
//   - "waiting on me" is not "rows of type approval.requested". The journal
//     is an event log, so the ask stays in it forever — including after it
//     was answered. Counting asks reports a queue that has already been
//     cleared.
//   - "which zero is this" decides whether a 0 may be phrased as
//     reassurance. Usually it may not.
//
// Nothing here fetches, and nothing here names a colour: tokens stay in
// globals.css and the component resolves them.

import { entryCostUSD, entryDurationMs, scopeOf } from "@/lib/activity-stream"
import type { JournalEntry } from "@/lib/types/journal"

/* ------------------------------------------------------------------ *
 *  Which zero is this?
 * ------------------------------------------------------------------ */

/**
 * A zero is not a fact about the world, it is a fact about a question.
 *
 *   nothing-recorded    — the window holds no events at all. Nothing is
 *                         known; a reassuring word here is invented.
 *   nothing-of-this-kind— the window holds events and none is of this kind.
 *                         A real observation, but only over what was loaded
 *                         and filtered, never over everything.
 *
 * This is the distinction the old copy collapsed. A screen focused on one
 * routine read "0 — nothing broke" beside a rail reading "Failed 9": the
 * number answered "did THIS routine break", the words answered "did
 * anything break", and only one of them was true.
 */
export type ZeroKind = "nothing-recorded" | "nothing-of-this-kind"

/** Null when the bucket is non-empty — there is no zero to explain. */
export function zeroKind(windowTotal: number, bucketCount: number): ZeroKind | null {
  if (bucketCount > 0) return null
  return windowTotal === 0 ? "nothing-recorded" : "nothing-of-this-kind"
}

export interface ZeroCopy {
  /** Fits a KpiCard subtitle. */
  subtitle: string
  /** Fits an empty panel. `subject` names what was not found. */
  panel: string
}

/**
 * Words for a zero that say which zero it is.
 *
 * No branch may produce "nothing broke", "all clear", "all clean" or
 * "Nice." — every one of those asserts a scope the number does not have.
 * A test pins that across both kinds.
 */
export function zeroCopy(kind: ZeroKind, windowTotal: number, subject: string): ZeroCopy {
  if (kind === "nothing-recorded") {
    return {
      subtitle: "nothing recorded in this window",
      panel:
        "No events were recorded here at all, so this is not an all-clear — " +
        "there is simply nothing to read. Widen the range or clear a filter.",
    }
  }
  const n = windowTotal.toLocaleString()
  return {
    subtitle: `none of the ${n} events shown`,
    panel: `None of the ${n} events shown is ${subject}. Anything outside this window, or excluded by a filter, is not counted here.`,
  }
}

/* ------------------------------------------------------------------ *
 *  What counts as waiting on a person
 * ------------------------------------------------------------------ */

export type AskKind = "approval" | "escalation" | "keeper"

export interface AskRef {
  kind: AskKind
  id: string
}

function idFrom(entry: JournalEntry, ...keys: string[]): string | undefined {
  for (const k of keys) {
    const v = entry.refs?.[k] ?? entry.payload?.[k]
    if (typeof v === "string" && v !== "") return v
  }
  return undefined
}

/** True once an escalation row carries its terminal state. */
function escalationResolved(entry: JournalEntry): boolean {
  return entry.payload?.["state"] === "resolved"
}

/**
 * The ask an entry IS, or null.
 *
 * Three things block a person, and each names its id on both the ask and the
 * answer, so the two can be joined:
 *
 *   approval.requested   refs.approval_id        (harbormaster/store_mutate.go:83)
 *   keeper.request       refs.keeper_request_id  (api/keeper_request.go:235)
 *   peer.escalation      refs.escalation_id      (api/escalation_handler.go:255)
 *
 * `peer.escalation` is the awkward one: the SAME entry_type is emitted for
 * the ask and for its resolution, separated only by `payload.state`
 * ("pending" vs "resolved"). Reading the type alone therefore counts every
 * resolution as a fresh thing waiting on you — which is most of them, since
 * an instance that works resolves what it asks.
 */
export function askRef(entry: JournalEntry): AskRef | null {
  switch (entry.entry_type) {
    case "approval.requested":
      return { kind: "approval", id: idFrom(entry, "approval_id") ?? "" }
    case "keeper.request":
      return { kind: "keeper", id: idFrom(entry, "keeper_request_id", "request_id") ?? "" }
    case "peer.escalation":
      if (escalationResolved(entry)) return null
      return { kind: "escalation", id: idFrom(entry, "escalation_id") ?? "" }
    default:
      return null
  }
}

/**
 * The ask an entry CLOSES, or null.
 *
 * Approvals and keeper requests are closed by a different entry_type;
 * escalations are closed by their own type carrying state "resolved",
 * whether a person did it (escalation_handler.go:607) or the system did
 * (escalation_autoresolve.go:172).
 */
export function answerRef(entry: JournalEntry): AskRef | null {
  switch (entry.entry_type) {
    case "approval.granted":
    case "approval.denied":
    case "approval.cancelled":
    case "approval.timeout":
      return { kind: "approval", id: idFrom(entry, "approval_id") ?? "" }
    case "keeper.decision":
      return { kind: "keeper", id: idFrom(entry, "keeper_request_id", "request_id") ?? "" }
    case "peer.escalation":
      if (!escalationResolved(entry)) return null
      return { kind: "escalation", id: idFrom(entry, "escalation_id") ?? "" }
    default:
      return null
  }
}

/**
 * The asks in this window that nothing in it has answered — the real
 * "waiting on you" list, in feed order.
 *
 * An ask carrying no id is KEPT. It cannot be joined to an answer, so it
 * cannot be proven answered, and showing one row too many is a smaller
 * failure than hiding something a person is blocking.
 *
 * The join is on kind AND id, so an approval decision cannot close a keeper
 * request that happens to share an id string.
 */
export function openAsks(entries: JournalEntry[]): JournalEntry[] {
  const answered = new Set<string>()
  for (const e of entries) {
    const a = answerRef(e)
    if (a && a.id !== "") answered.add(`${a.kind}:${a.id}`)
  }
  return entries.filter((e) => {
    const ask = askRef(e)
    if (!ask) return false
    if (ask.id === "") return true
    return !answered.has(`${ask.kind}:${ask.id}`)
  })
}

/* ------------------------------------------------------------------ *
 *  What is broken
 * ------------------------------------------------------------------ */

export interface FailureCluster {
  /** Stable identity of the thing that is broken. */
  key: string
  count: number
  /** The newest row in the cluster — its face in the panel. */
  latest: JournalEntry
}

function clusterKey(entry: JournalEntry): string {
  const bag = { ...(entry.payload ?? {}), ...(entry.refs ?? {}) }
  const slug = bag["pipeline_slug"] ?? bag["routine_slug"]
  if (typeof slug === "string" && slug) return `routine:${slug}`
  const run = bag["run_id"] ?? entry.trace_id
  if (typeof run === "string" && run) return `run:${run}`
  if (entry.agent_id) return `agent:${entry.agent_id}`
  return `type:${entry.entry_type}`
}

/**
 * Failures grouped by the thing that is failing, biggest first.
 *
 * A broken routine writes one row per step per attempt, so a panel that
 * lists rows spends all of itself on the loudest failure and hides the rest.
 * Nine rows from one routine is one thing to fix; the count travels with the
 * cluster so nothing is silently dropped.
 *
 * Input is the feed, newest first — so the first row seen for a key is the
 * newest, and that is the one shown.
 */
export function failureClusters(entries: JournalEntry[]): FailureCluster[] {
  const byKey = new Map<string, FailureCluster>()
  for (const e of entries) {
    if (scopeOf(e) !== "failed") continue
    const key = clusterKey(e)
    const found = byKey.get(key)
    if (found) found.count += 1
    else byKey.set(key, { key, count: 1, latest: e })
  }
  // Map iteration is insertion-ordered, so equal counts stay newest-first.
  return [...byKey.values()].sort((a, b) => b.count - a.count)
}

/* ------------------------------------------------------------------ *
 *  The failure trend
 * ------------------------------------------------------------------ */

// Days since epoch in the VIEWER's timezone. Mirrors the private helper in
// activity-stream.ts; that module is owned by another workstream, so this is
// copied rather than exported across. Calendar arithmetic, not millisecond
// arithmetic — "yesterday" is a date boundary.
function localDayIndex(d: Date): number {
  return Math.floor((d.getTime() - d.getTimezoneOffset() * 60_000) / 86_400_000)
}

/**
 * How many days the loaded window ACTUALLY covers, capped at `max`.
 *
 * The trend card used to be a fixed "7 days" drawn from `entries` — but
 * `entries` is only ever the loaded window, and the range defaults to 24
 * hours. So six of its seven columns were empty every time, and they read as
 * six quiet days rather than six days nobody asked about. That is the same
 * lie as "nothing broke", told with an axis.
 *
 * Returns 0 for an empty window and 1 for a window inside one day — in both
 * cases there is no trend to draw, and the caller shows nothing instead of
 * an axis full of unasked questions.
 */
export function windowSpanDays(entries: JournalEntry[], max = 7): number {
  let oldest: number | null = null
  let newest: number | null = null
  for (const e of entries) {
    const t = Date.parse(e.ts ?? "")
    if (!Number.isFinite(t)) continue
    const day = localDayIndex(new Date(t))
    if (oldest == null || day < oldest) oldest = day
    if (newest == null || day > newest) newest = day
  }
  if (oldest == null || newest == null) return 0
  return Math.min(max, newest - oldest + 1)
}

/* ------------------------------------------------------------------ *
 *  The small "right now" signal
 * ------------------------------------------------------------------ */

export interface LiveSignal {
  /** Runs in flight — `scopeOf`, so the rail and this line agree. */
  running: number
  /** Distinct agents that wrote anything in the window. */
  agents: number
  spendUSD: number
  /** Null, not 0, when nothing reported a duration. */
  slowestMs: number | null
}

export function liveSignal(entries: JournalEntry[]): LiveSignal {
  const agents = new Set<string>()
  let running = 0
  let spendUSD = 0
  let slowestMs: number | null = null

  for (const e of entries) {
    if (scopeOf(e) === "active") running += 1
    if (e.agent_id) agents.add(e.agent_id)
    spendUSD += entryCostUSD(e) ?? 0
    const ms = entryDurationMs(e)
    if (ms != null && (slowestMs == null || ms > slowestMs)) slowestMs = ms
  }

  return { running, agents: agents.size, spendUSD, slowestMs }
}
