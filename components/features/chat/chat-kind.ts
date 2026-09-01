import type { LucideIcon } from "lucide-react"

import { CONCEPT_ICON } from "@/lib/concept-icons"

import { parseSessionTimestamp } from "./session-sort"
import type { ChatTreeThread } from "./chat-tree-data"

/**
 * What KIND of thing a chat row is — the axis this column was missing.
 *
 * Four writers put rows in the `chats` table and only one of them is a
 * conversation: a person opening a thread, a routine minting one chat PER
 * STEP, an issue starting work, and an agent delegating to another agent. The
 * column showed all four in one list ordered by activity, so a five-step
 * nightly routine did not merely clutter the list — it EVICTED it. The
 * per-agent page is 10 rows; four runs and the thread you wrote yesterday is
 * off the end of it.
 *
 * The mirror of internal/api/chat_kinds.go, deliberately: the server sends
 * `kind` on every row and this file is what the client falls back to. Both
 * are needed and neither is redundant —
 *
 *  · the server's copy is what makes `?kind=` filter BEFORE `LIMIT`, which is
 *    the only place the eviction can actually be fixed;
 *  · the client's copy classifies rows the server has not seen — a locally
 *    minted draft has no row yet — and rows from a server older than the
 *    field.
 *
 * They agree by construction: same three tests (mode wins, then origin, then
 * everything else is direct) in the same order.
 */
export type ChatKind = "direct" | "routine" | "issue" | "agent"

/**
 * Classify one thread.
 *
 * `kind` from the server is preferred and is not merely an optimisation: it
 * is the value the `?kind=` filter used to build the page, so trusting it
 * makes the list and the filter incapable of disagreeing. Re-deriving locally
 * from a row the server already judged would put a second opinion in the loop
 * for no gain.
 *
 * Falling back to mode+origin rather than to "direct" flat: an older server
 * still sends both columns, and guessing `direct` for every row would put
 * routine chats straight back in the list this exists to clear.
 *
 * Never reads the TITLE. A title is user-editable — `PATCH .../chats/{id}`
 * renames one — so a rule over it reclassifies a row the moment somebody
 * tidies its name, and misfiles a human conversation the moment somebody
 * calls one "Pipeline notes".
 */
export function classifyThread(t: Pick<ChatTreeThread, "mode" | "origin" | "kind">): ChatKind {
  if (t.kind && KIND_ORDER.includes(t.kind as ChatKind)) return t.kind as ChatKind
  if (t.mode === "MISSION") return "issue"
  switch (t.origin) {
    case "ROUTINE":
    case "CRON":
    case "WEBHOOK":
      return "routine"
    case "AGENT":
      return "agent"
  }
  return "direct"
}

const KIND_ORDER: ChatKind[] = ["direct", "routine", "issue", "agent"]

/**
 * The scopes the column offers, which are NOT one-to-one with the kinds.
 *
 * `agent` (one agent delegating to another) rides in the Routines scope
 * rather than getting a fourth tab. Three reasons, in the order they
 * mattered:
 *
 *  · From the reader's side the two are the same category — work that
 *    happened without them typing — and that is the distinction the scope
 *    strip exists to draw. The distinction BETWEEN them is real but smaller,
 *    so it is a badge on the row, not a tab.
 *  · A fourth equal-width tab in a 280px column leaves ~62px per label; the
 *    counts stop fitting and the labels start truncating.
 *  · Delegation already has a surface of its own (crew-peer-conversations),
 *    so this is its second home, not its only one.
 *
 * Nothing is disguised: every row carries `KIND_META[kind].label`.
 */
export type ChatScope = "direct" | "routine" | "issue"

export interface ScopeSpec {
  id: ChatScope
  label: string
  /** What `?kind=` is sent as. Comma-separated — the server ORs them. */
  kinds: ChatKind[]
  /**
   * From `CONCEPT_ICON`, never picked by hand.
   *
   * Each of these three scopes is a concept the nav rail already has an entry
   * for, and that map is the definition — app-sidebar.tsx reads it rather than
   * keeping its own copy. Picking again from memory is how the same concept
   * ends up wearing a different face on every screen, which is the exact
   * failure lib/concept-icons.ts was written to end. (It happened here: this
   * column shipped with Repeat2 for Routines and Target for Issues while the
   * rail two inches to the left showed ScrollText and CircleDot.)
   */
  icon: LucideIcon
  title: string
  /** Where this kind of work actually lives, offered when the scope is empty. */
  home?: { href: string; label: string }
  empty: string
}

export const CHAT_SCOPES: ScopeSpec[] = [
  {
    id: "direct",
    label: "Direct",
    kinds: ["direct"],
    // `sessions` is the rail's own Chat entry — "conversations with a person".
    icon: CONCEPT_ICON.sessions,
    title: "Conversations you opened with an agent",
    empty: "No conversations yet.",
  },
  {
    id: "routine",
    label: "Routines",
    kinds: ["routine", "agent"],
    icon: CONCEPT_ICON.routines,
    title: "Routine steps, scheduled and webhook runs, and agent-to-agent work",
    home: { href: "/routines", label: "Open Routines" },
    empty: "Nothing has run against these agents yet.",
  },
  {
    id: "issue",
    label: "Issues",
    kinds: ["issue"],
    icon: CONCEPT_ICON.issues,
    title: "The chat an issue runs its work in",
    home: { href: "/issues", label: "Open Issues" },
    empty: "No issue has started work here yet.",
  },
]

/**
 * A scope's total, from the server's per-kind counts.
 *
 * `null` in, `null` out, and the caller renders no number — the totals for a
 * scope you are not fetching are genuinely unknown, and /routines gets to show
 * a count on every bucket only because it holds the whole list client-side.
 */
export function scopeCount(
  scope: ChatScope,
  counts: Record<string, number> | null | undefined,
): number | null {
  if (!counts) return null
  const spec = CHAT_SCOPES.find((s) => s.id === scope)
  if (!spec) return null
  return spec.kinds.reduce((n, k) => n + (counts[k] ?? 0), 0)
}

/**
 * The badge a row wears when its scope holds more than one kind.
 *
 * Same map as the scopes, for the same reason — and one more concept:
 * `peers` is the rail's word for "messages from other agents", which is
 * exactly what a delegation is.
 *
 * No colour. A tone means something when it encodes STATE — /routines paints
 * its status buckets amber for awaiting and red for failed, and those are
 * claims about what happened. A colour invented per CONCEPT encodes nothing,
 * and it makes the same symbol wear a different face than the rail does two
 * inches to the left. The glyphs already differ; they can carry it.
 */
export const KIND_META: Record<ChatKind, { label: string; icon: LucideIcon }> = {
  direct: { label: "Direct", icon: CONCEPT_ICON.sessions },
  routine: { label: "Routine", icon: CONCEPT_ICON.routines },
  issue: { label: "Issue", icon: CONCEPT_ICON.issues },
  agent: { label: "Delegation", icon: CONCEPT_ICON.peers },
}

/**
 * Which scope shows a given kind.
 *
 * The inverse of `ScopeSpec.kinds`, and the reason the scope strip can be
 * brought to a conversation rather than only navigated by hand: a deep link
 * names a session, the session has a kind, and the column has to move to the
 * bucket that holds it. Total by construction — `CHAT_SCOPES` covers all four
 * kinds (pinned by a test) — but it answers `null` for a value it does not
 * recognise rather than guessing, because moving the column to the wrong
 * bucket is worse than leaving it where the reader put it.
 */
export function scopeForKind(kind: ChatKind | string): ChatScope | null {
  return CHAT_SCOPES.find((s) => (s.kinds as string[]).includes(kind))?.id ?? null
}

/** The `kind` query value for a scope, or "" for "do not narrow". */
export function scopeKindParam(scope: ChatScope): string {
  return CHAT_SCOPES.find((s) => s.id === scope)?.kinds.join(",") ?? ""
}

/* --------------------------------------------------------------- recency */

export interface RecencyBucket {
  label: string
  /** Inclusive lower bound in epoch ms; rows at or after it belong here. */
  from: number
}

/**
 * Recency headers, computed from a caller-supplied `now` so a test is not at
 * the mercy of the wall clock and so every row in one render is bucketed
 * against the SAME instant — bucketing each row against its own `Date.now()`
 * lets a list straddling midnight put two adjacent rows in non-adjacent
 * groups.
 *
 * Local midnight, not "24 hours ago": a person reading "Yesterday" means the
 * calendar day, and a thread from 23:50 last night is not "today" because it
 * is nine hours old.
 */
export function recencyBuckets(now: number): RecencyBucket[] {
  const midnight = new Date(now)
  midnight.setHours(0, 0, 0, 0)
  const day = 86_400_000
  return [
    { label: "Today", from: midnight.getTime() },
    { label: "Yesterday", from: midnight.getTime() - day },
    { label: "Earlier this week", from: midnight.getTime() - 7 * day },
    { label: "Earlier", from: Number.NEGATIVE_INFINITY },
  ]
}

/**
 * Split rows (already newest-first) into non-empty recency groups.
 *
 * Below `minRows` the grouping is skipped entirely and one unlabelled group
 * comes back. Three headers over four rows is not structure, it is three
 * lines of chrome explaining a list you can already see — and the common case
 * on this surface is a handful of conversations.
 */
export function groupByRecency<T>(
  rows: T[],
  at: (row: T) => number,
  now: number,
  minRows = 6,
): { label: string | null; rows: T[] }[] {
  if (rows.length < minRows) return rows.length ? [{ label: null, rows }] : []
  const buckets = recencyBuckets(now)
  const out: { label: string | null; rows: T[] }[] = []
  // One linear pass, not a filter per bucket: the input is already
  // newest-first and the bounds descend with it, so each bucket is a
  // contiguous slice. The last bound is -Infinity, which is what guarantees
  // the pass is total — a row whose stamp did not parse arrives as 0 and
  // still lands in "Earlier" rather than being silently dropped from a list
  // somebody is using to find it.
  let i = 0
  for (const b of buckets) {
    const start = i
    while (i < rows.length && at(rows[i]) >= b.from) i++
    if (i > start) out.push({ label: b.label, rows: rows.slice(start, i) })
  }
  return out
}

/* --------------------------------------------------------------- routines */

/**
 * The routine a step-chat belongs to, for grouping the Routines scope.
 *
 * The runner titles these `"<routine> · <step>"`, so the routine name is
 * everything before the LAST separator — last, not first, because a routine
 * may legitimately be called "Deploy · nightly" and splitting on the first
 * would file each of its steps under a different name.
 *
 * This IS a title heuristic, and it is allowed here for the reason the one in
 * `classifyThread` is not: it decides how rows are STACKED, never whether
 * they are shown. A title that does not match the shape groups under itself —
 * one group of one — which is exactly the flat list this replaced. Nothing
 * can go missing.
 */
export function routineGroupOf(title: string | null | undefined): { group: string; step: string | null } {
  const t = (title ?? "").trim()
  if (!t) return { group: "Untitled", step: null }
  const i = t.lastIndexOf(" · ")
  if (i <= 0) return { group: t, step: null }
  return { group: t.slice(0, i), step: t.slice(i + 3) || null }
}

/** Epoch ms of a thread's last movement — the one sort key this surface uses. */
export function threadActivityAt(t: Pick<ChatTreeThread, "last_activity_at" | "started_at">): number {
  return parseSessionTimestamp(t.last_activity_at ?? t.started_at)
}
