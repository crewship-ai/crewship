"use client"

import { useMemo, useState } from "react"

import { useWorkspace } from "@/hooks/use-workspace"

import { InboxListPanel } from "./inbox-list-panel"
import { ItemDetail } from "./item-detail"
import { OUTCOME_LABEL, bucketOf, expiresIn, subjectOf } from "./logic"
import {
  PREVIEW_ARCHIVE, PREVIEW_ITEMS, PREVIEW_USER_ID, isVisibleTo,
  type PreviewInboxItem, type WorkspaceRole,
} from "./mock-data"
import type { Bucket, GroupBy, InboxView, SubjectFacet } from "./types"

// =============================================================================
// /inbox/preview — the 1.0 inbox design rendered against the real kit.
//
// Two columns, as the shipped page has: the list carries its own chrome and
// the reading pane sits beside it. An explorer rail was tried here and taken
// back out — see inbox-list-panel for why.
//
// The role is whoever is signed in. There is no picker: a person does not
// choose to be a VIEWER, and a control that pretends they might is a control
// that lies about the product. The two rules the server enforces still shape
// the page, they just take their input from the session —
// inboxVisibilityClause for what is listed, canRole for what is decidable.
//
// Rows come from a fixture set copied out of the Go producers, so the page can
// be opened on any instance and still show the same screen.
// =============================================================================

export interface InboxPreviewProps {
  /** Tests only — production takes the role from the signed-in session. */
  initialRole?: WorkspaceRole
  initialView?: InboxView
  initialSelectedId?: string
}

export function InboxPreview({ initialRole, initialView = "inbox", initialSelectedId }: InboxPreviewProps) {
  const { role: sessionRole } = useWorkspace()
  const role = (initialRole ?? sessionRole ?? null) as WorkspaceRole | null

  const [view, setView] = useState<InboxView>(initialView)
  const [bucket, setBucket] = useState<Bucket | null>(null)
  const [subject, setSubject] = useState<string | null>(null)
  const [search, setSearch] = useState("")
  const [outcome, setOutcome] = useState<string | null>(null)
  const [actor, setActor] = useState<string | null>(null)
  const [period, setPeriod] = useState("30")
  const [groupBy, setGroupBy] = useState<GroupBy>("smart")
  const [sort, setSort] = useState("newest")
  const [selectedId, setSelectedId] = useState<string | null>(initialSelectedId ?? null)

  const archive = view === "archived"
  const source = archive ? PREVIEW_ARCHIVE : PREVIEW_ITEMS

  // Until the workspace resolves the caller's role there is no honest answer to
  // "what may this person see", so the page waits rather than guessing high and
  // showing rows it might have to take back.
  const visible = useMemo(
    () => (role ? source.filter((it) => isVisibleTo(it, role, PREVIEW_USER_ID)) : []),
    [source, role],
  )

  const viewCounts = useMemo<Record<InboxView, number>>(() => {
    if (!role) return { inbox: 0, unread: 0, archived: 0 }
    const live = PREVIEW_ITEMS.filter((it) => isVisibleTo(it, role, PREVIEW_USER_ID))
    return {
      inbox: live.length,
      unread: live.filter((it) => it.state === "unread").length,
      archived: PREVIEW_ARCHIVE.filter((it) => isVisibleTo(it, role, PREVIEW_USER_ID)).length,
    }
  }, [role])

  const bucketCounts = useMemo(() => {
    const counts: Record<Bucket, number> = { decisions: 0, replies: 0, review: 0, routines: 0, other: 0 }
    for (const it of visible) counts[bucketOf(it)] += 1
    return counts
  }, [visible])

  const subjects = useMemo<SubjectFacet[]>(() => {
    const map = new Map<string, SubjectFacet>()
    for (const it of visible) {
      const s = subjectOf(it)
      const found = map.get(s.id)
      if (found) found.count += 1
      else map.set(s.id, { ...s, count: 1 })
    }
    return [...map.values()].sort((a, b) => b.count - a.count || a.label.localeCompare(b.label))
  }, [visible])

  const outcomeCounts = useMemo(() => {
    const map = new Map<string, number>()
    for (const it of visible) {
      const key = it.resolved_action ?? "—"
      map.set(key, (map.get(key) ?? 0) + 1)
    }
    return [...map.entries()].map(([id, count]) => ({ id, label: OUTCOME_LABEL[id] ?? id, count }))
  }, [visible])

  const actorCounts = useMemo(() => {
    const map = new Map<string, number>()
    for (const it of visible) {
      if (!it.resolved_by_user_id) continue
      map.set(it.resolved_by_user_id, (map.get(it.resolved_by_user_id) ?? 0) + 1)
    }
    return [...map.entries()].map(([id, count]) => ({ id, count }))
  }, [visible])

  const rows = useMemo(() => {
    const q = search.trim().toLowerCase()
    const filtered = visible.filter((it: PreviewInboxItem) => {
      if (view === "unread" && it.state !== "unread") return false
      if (!archive && bucket && bucketOf(it) !== bucket) return false
      if (archive && outcome && it.resolved_action !== outcome) return false
      if (archive && actor && it.resolved_by_user_id !== actor) return false
      if (subject && subjectOf(it).id !== subject) return false
      if (q) {
        // Body included: the sentence someone remembers is usually in the
        // message, and the shipped search never looks there.
        const hay = `${it.title} ${it.sender_name ?? ""} ${it.body_md ?? ""}`.toLowerCase()
        if (!hay.includes(q)) return false
      }
      return true
    })

    return [...filtered].sort((a, b) => {
      if (sort === "expiring") {
        // A waitpoint is the only thing here with a deadline, so "expiring
        // first" means those, soonest first, and everything else after them in
        // the default order rather than interleaved by age.
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
  }, [visible, view, archive, bucket, outcome, actor, subject, search, sort])

  const selected = rows.find((r) => r.id === selectedId) ?? rows[0] ?? null

  return (
    <div className="flex h-[calc(100vh-3rem)] overflow-hidden">
      {role && (
        <InboxListPanel
          rows={rows}
          total={visible.length}
          role={role}
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
          onSelect={setSelectedId}
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
        />
      )}

      <div className="min-w-0 flex-1 overflow-y-auto bg-background p-4">
        {!role ? (
          <p className="type-row px-4 py-10 text-center text-muted-foreground-soft">
            Resolving your workspace role…
          </p>
        ) : selected ? (
          <ItemDetail key={selected.id} item={selected} role={role} />
        ) : (
          <p className="type-row px-4 py-10 text-center text-muted-foreground-soft">Pick an item.</p>
        )}
      </div>
    </div>
  )
}
