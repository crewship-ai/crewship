"use client"

import { useEffect, useMemo, useState } from "react"
import { toast } from "sonner"

import { useInbox, type InboxItem } from "@/hooks/use-inbox"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAgentSummaries } from "@/hooks/use-dashboard-data"
import { usePipelines } from "@/hooks/use-pipelines"
import { inboxBulk } from "@/lib/api/inbox"

import { InboxDetail } from "./inbox-detail"
import { InboxListPanel } from "./inbox-list-panel"
import type { DirectoryEntry } from "./inbox-subject-picker"
import {
  OUTCOME_LABEL, bucketOf, expiresIn, subjectOf, type WorkspaceRole,
} from "./inbox-derive"
import type { Bucket, GroupBy, InboxView } from "./inbox-types"

export { KindActions } from "./kind-actions"

// =============================================================================
// InboxList — the /inbox surface.
//
// Two columns: the list carries its own chrome (search, Select, view tabs,
// Filter, Display) and the reading pane sits beside it. What changed from the
// version this replaces, and why:
//
//   · Seven kinds render. memory_consolidation, schedule_missed and
//     schedule_circuit_breaker_tripped had been written by the backend since
//     v90/v155/v168 and fell through to a generic "Notification" with a
//     Dismiss button — a routine that disabled itself after five failures said
//     so in prose and offered no way to act.
//   · timeout_at is rendered. Every waitpoint carries it and the old pane
//     showed the item's AGE instead, on the one row here with a deadline.
//   · Decisions are gated by the role the server enforces, so a MANAGER no
//     longer gets an Approve button on a skill proposal that answers 403.
//   · Selection works. Select turns on checkboxes and shift-click takes a
//     range; before this you could not tick a message at all.
//   · Search covers the body, and the subject facet searches the workspace
//     roster rather than the loaded rows — with LIMIT 100 a facet built from
//     the page offers an agent only while they are noisy.
//
// What did NOT change: KindActions, which owns every resolve endpoint and the
// contracts around them (which calls cascade the inbox row server-side, which
// 409 if the inbox patches itself). It moved to its own file untouched.
// =============================================================================

/** Kept for the grouping tests and any caller that still reasons in buckets. */
export const SMART_ORDER: Record<string, number> = {
  "sm:decisions": 0,
  "sm:replies": 1,
  "sm:review": 2,
  "sm:fyi": 3,
}

const BUCKET_TO_SMART: Record<Bucket, { key: string; label: string }> = {
  decisions: { key: "sm:decisions", label: "Decisions needed" },
  replies: { key: "sm:replies", label: "Agent replies" },
  review: { key: "sm:review", label: "Needs review" },
  routines: { key: "sm:fyi", label: "FYI / advisories" },
  other: { key: "sm:fyi", label: "FYI / advisories" },
}

/**
 * The smart-bucket key/label for an item.
 *
 * Kept as an exported function because it is the one piece of this file other
 * code (and its tests) reasoned about directly. It now delegates to bucketOf,
 * which is the same rule plus the routine-progress lane the old version
 * collapsed into FYI.
 */
export function groupOf(item: InboxItem): { key: string; label: string } {
  return BUCKET_TO_SMART[bucketOf(item)]
}

export function InboxList() {
  const { workspaceId, role } = useWorkspace()
  const [view, setView] = useState<InboxView>("inbox")
  const [bucket, setBucket] = useState<Bucket | null>(null)
  const [subject, setSubject] = useState<string | null>(null)
  const [search, setSearch] = useState("")
  const [outcome, setOutcome] = useState<string | null>(null)
  const [actor, setActor] = useState<string | null>(null)
  const [period, setPeriod] = useState("30")
  const [groupBy, setGroupBy] = useState<GroupBy>("smart")
  const [sort, setSort] = useState("newest")
  const [selectedId, setSelectedId] = useState<string | null>(null)

  const archive = view === "archived"
  // inbox → active (unread + read, resolved excluded SERVER-side so archived
  // rows don't eat the LIMIT window); the other two map straight through.
  const stateParam = archive ? "resolved" : view === "unread" ? "unread" : "active"
  const { items, unreadCount, loading, error, patch, refresh } = useInbox(workspaceId, stateParam)

  // The roster the subject picker searches. Agents come from the summaries
  // every other surface already loads (React Query dedupes it); routines from
  // the pipeline list. Both are workspace-scoped and neither depends on what
  // happens to be in the inbox window.
  const { data: agents } = useAgentSummaries(workspaceId)
  const { pipelines, refresh: refreshPipelines } = usePipelines(workspaceId)
  useEffect(() => { void refreshPipelines() }, [refreshPipelines])

  const directory = useMemo<DirectoryEntry[]>(() => {
    const out: DirectoryEntry[] = []
    for (const a of agents ?? []) out.push({ id: a.name, label: a.name, kind: "agent" })
    for (const p of pipelines) out.push({ id: p.name ?? p.slug, label: p.name ?? p.slug, kind: "routine" })
    // System senders have no roster of their own, so they come from the rows.
    const seen = new Set(out.map((d) => d.id))
    for (const it of items) {
      const s = subjectOf(it)
      if (s.kind === "system" && !seen.has(s.id)) {
        out.push({ id: s.id, label: s.label, kind: "system" })
        seen.add(s.id)
      }
    }
    return out
  }, [agents, pipelines, items])

  const viewCounts = useMemo<Record<InboxView, number | null>>(() => ({
    // Only ONE list is loaded at a time, so only the active tab has a number we
    // actually know. Unread is the exception: it comes from /inbox/count, the
    // same source the bell badge reads, so it is right from any tab.
    //
    // The others render without a count rather than borrowing the loaded list's
    // length — a tab that says "10" because that is how many ARCHIVED rows are
    // in memory is worse than a tab that says nothing. Per-state counts in the
    // list response would fix it for real.
    inbox: view === "inbox" ? items.length : null,
    unread: unreadCount || null,
    archived: view === "archived" ? items.length : null,
  }), [items.length, unreadCount, view])

  const bucketCounts = useMemo(() => {
    const counts: Record<Bucket, number> = { decisions: 0, replies: 0, review: 0, routines: 0, other: 0 }
    for (const it of items) counts[bucketOf(it)] += 1
    return counts
  }, [items])

  const subjects = useMemo(() => {
    const map = new Map<string, { id: string; label: string; kind: ReturnType<typeof subjectOf>["kind"]; seed?: string; count: number }>()
    for (const it of items) {
      const s = subjectOf(it)
      const found = map.get(s.id)
      if (found) found.count += 1
      else map.set(s.id, { ...s, count: 1 })
    }
    return [...map.values()].sort((a, b) => b.count - a.count || a.label.localeCompare(b.label))
  }, [items])

  const outcomeCounts = useMemo(() => {
    const map = new Map<string, number>()
    for (const it of items) {
      if (!it.resolved_action) continue
      map.set(it.resolved_action, (map.get(it.resolved_action) ?? 0) + 1)
    }
    return [...map.entries()].map(([id, count]) => ({ id, label: OUTCOME_LABEL[id] ?? id, count }))
  }, [items])

  const actorCounts = useMemo(() => {
    const map = new Map<string, number>()
    for (const it of items) {
      if (!it.resolved_by_user_id) continue
      map.set(it.resolved_by_user_id, (map.get(it.resolved_by_user_id) ?? 0) + 1)
    }
    return [...map.entries()].map(([id, count]) => ({ id, count }))
  }, [items])

  const rows = useMemo(() => {
    const q = search.trim().toLowerCase()
    const filtered = items.filter((it) => {
      if (!archive && bucket && bucketOf(it) !== bucket) return false
      if (archive && outcome && it.resolved_action !== outcome) return false
      if (archive && actor && it.resolved_by_user_id !== actor) return false
      if (subject && subjectOf(it).id !== subject) return false
      if (q) {
        // Body included: the sentence someone remembers is usually in the
        // message, and the search this replaces never looked there.
        const hay = `${it.title} ${it.sender_name ?? ""} ${it.body_md ?? ""}`.toLowerCase()
        if (!hay.includes(q)) return false
      }
      return true
    })

    return [...filtered].sort((a, b) => {
      if (sort === "expiring") {
        // A waitpoint is the only thing here with a deadline, so "expiring
        // first" means those, soonest first, and everything else after them.
        const ea = expiresIn(a)
        const eb = expiresIn(b)
        if (ea != null && eb != null) return ea - eb
        if (ea != null) return -1
        if (eb != null) return 1
      }
      const ta = Date.parse(a.created_at)
      const tb = Date.parse(b.created_at)
      return sort === "oldest" ? ta - tb : tb - ta
    })
  }, [items, archive, bucket, outcome, actor, subject, search, sort])

  // Keep the last opened item rendered even after it leaves the filtered list
  // — opening an unread row marks it read, which drops it from the Unread
  // view, and a pane derived purely from `rows` would snap shut underneath.
  const [snapshot, setSnapshot] = useState<InboxItem | null>(null)
  const live = rows.find((r) => r.id === selectedId) ?? null
  useEffect(() => { if (live) setSnapshot(live) }, [live])
  const selected =
    live ??
    (snapshot?.id === selectedId && snapshot.workspace_id === workspaceId ? snapshot : null) ??
    rows[0] ??
    null

  async function bulk(ids: string[], state: "read" | "resolved", action?: string) {
    if (!workspaceId || ids.length === 0) return
    // Chunked to the backend's 500-id cap so a large select-all cannot fail
    // the whole action; the server skips decision items it must not close.
    const CHUNK = 500
    let updated = 0
    let skipped = 0
    for (let i = 0; i < ids.length; i += CHUNK) {
      const res = await inboxBulk(workspaceId, ids.slice(i, i + CHUNK), state, action)
      if (!res.ok) {
        toast.error(res.error)
        return
      }
      updated += res.result.updated
      skipped += res.result.skipped
    }
    const verb = state === "resolved" ? "resolved" : "marked read"
    toast.success(skipped > 0 ? `${updated} ${verb} · ${skipped} left open (need a decision)` : `${updated} ${verb}`)
    await refresh()
  }

  return (
    <div className="flex h-[calc(100vh-3rem)] overflow-hidden">
      <InboxListPanel
        rows={rows}
        total={items.length}
        role={(role as WorkspaceRole | null) ?? null}
        view={view}
        onViewChange={(v) => {
          setView(v)
          setBucket(null)
          setOutcome(null)
          setActor(null)
          setSubject(null)
          setSelectedId(null)
        }}
        viewCounts={viewCounts}
        selectedId={selected?.id ?? null}
        onSelect={(id) => {
          setSelectedId(id)
          const item = rows.find((r) => r.id === id)
          if (item) setSnapshot(item)
          if (item?.state === "unread") {
            // Fire-and-forget; useInbox surfaces its own error, so swallow the
            // rejection rather than leaving it unhandled.
            void patch(id, "read").catch(() => {})
          }
        }}
        bucket={bucket}
        onBucketChange={(b) => { setBucket(b); setSelectedId(null) }}
        bucketCounts={bucketCounts}
        subjects={subjects}
        selectedSubject={subject}
        onSubjectChange={(s) => { setSubject(s); setSelectedId(null) }}
        outcome={outcome}
        onOutcomeChange={(o) => { setOutcome(o); setSelectedId(null) }}
        outcomeCounts={outcomeCounts}
        actor={actor}
        onActorChange={(a) => { setActor(a); setSelectedId(null) }}
        actorCounts={actorCounts}
        period={period}
        onPeriodChange={setPeriod}
        groupBy={groupBy}
        onGroupByChange={setGroupBy}
        sort={sort}
        onSortChange={setSort}
        search={search}
        onSearchChange={setSearch}
        directory={directory}
        onBulk={bulk}
        loading={loading}
        error={error}
      />

      <div className="min-w-0 flex-1 overflow-y-auto bg-background p-4">
        {selected ? (
          <InboxDetail
            key={selected.id}
            item={selected}
            role={(role as WorkspaceRole | null) ?? null}
            onResolve={async (action) => {
              await patch(selected.id, "resolved", action)
              toast.success(`Marked as ${action}`)
              await refresh()
            }}
            onArchive={async () => {
              const prev = selected.state
              await patch(selected.id, "resolved", "archived")
              toast.success("Archived", {
                action: {
                  label: "Undo",
                  onClick: () => {
                    void patch(selected.id, prev === "unread" ? "unread" : "read").then(refresh).catch(() => {})
                  },
                },
              })
            }}
            onMarkUnread={() => void patch(selected.id, "unread").then(refresh).catch(() => {})}
            onRefresh={refresh}
          />
        ) : (
          <p className="type-row px-4 py-10 text-center text-muted-foreground-soft">
            {loading ? "Loading…" : "Pick an item."}
          </p>
        )}
      </div>
    </div>
  )
}
