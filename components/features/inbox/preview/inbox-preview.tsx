"use client"

import { useMemo, useState } from "react"

import { SidebarCollapseButton } from "@/components/layout/sidebar-kit"
import { cn } from "@/lib/utils"

import { InboxExplorer } from "./inbox-explorer"
import { ArchiveTable, SplitLayout, StreamLayout, TableLayout } from "./layouts"
import { bucketOf, subjectOf } from "./logic"
import { OUTCOME_LABEL } from "./logic"
import {
  PREVIEW_ARCHIVE, PREVIEW_ITEMS, PREVIEW_USER_ID, isVisibleTo,
  type PreviewInboxItem, type WorkspaceRole,
} from "./mock-data"
import type { Bucket, InboxView, LayoutStyle, SubjectFacet } from "./types"

// =============================================================================
// /inbox/preview — the 1.0 inbox design rendered against the real kit.
//
// The page is the rail plus one content surface. There is no page sub-bar: it
// held the title, a count and the role switch, and all three belong in the rail
// — the title is its first section, the count is on every row of it, and the
// role switch is a control, not chrome.
//
// Three content arrangements are switchable at the bottom of the rail so the
// choice can be made by looking. Rows come from a fixture set copied out of the
// Go producers, so the page can be opened on any instance and still show the
// same screen, and the role switch applies the SAME two rules the server does:
// inboxVisibilityClause for what is listed, canRole for what is decidable.
// =============================================================================

export interface InboxPreviewProps {
  initialRole?: WorkspaceRole
  initialView?: InboxView
  initialLayout?: LayoutStyle
  initialSelectedId?: string
}

export function InboxPreview({
  initialRole = "OWNER",
  initialView = "inbox",
  initialLayout = "split",
  initialSelectedId,
}: InboxPreviewProps) {
  const [role, setRole] = useState<WorkspaceRole>(initialRole)
  const [view, setView] = useState<InboxView>(initialView)
  const [layout, setLayout] = useState<LayoutStyle>(initialLayout)
  const [bucket, setBucket] = useState<Bucket | null>(null)
  const [subject, setSubject] = useState<string | null>(null)
  const [search, setSearch] = useState("")
  const [outcome, setOutcome] = useState<string | null>(null)
  const [actor, setActor] = useState<string | null>(null)
  const [period, setPeriod] = useState("30")
  const [selectedId, setSelectedId] = useState<string | null>(initialSelectedId ?? null)
  const [railCollapsed, setRailCollapsed] = useState(false)

  const archive = view === "archived"
  const source = archive ? PREVIEW_ARCHIVE : PREVIEW_ITEMS

  const visible = useMemo(
    () => source.filter((it) => isVisibleTo(it, role, PREVIEW_USER_ID)),
    [source, role],
  )

  const viewCounts = useMemo<Record<InboxView, number>>(() => {
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
    return visible.filter((it: PreviewInboxItem) => {
      if (view === "unread" && it.state !== "unread") return false
      if (!archive && bucket && bucketOf(it) !== bucket) return false
      if (archive && outcome && it.resolved_action !== outcome) return false
      if (archive && actor && it.resolved_by_user_id !== actor) return false
      if (subject && subjectOf(it).id !== subject) return false
      if (q) {
        const hay = `${it.title} ${it.sender_name ?? ""} ${it.body_md ?? ""}`.toLowerCase()
        if (!hay.includes(q)) return false
      }
      return true
    })
  }, [visible, view, archive, bucket, outcome, actor, subject, search])

  const layoutProps = {
    rows,
    total: visible.length,
    role,
    selectedId,
    onSelect: setSelectedId,
  }

  return (
    <div className="flex h-[calc(100vh-3rem)] overflow-hidden">
      <aside aria-label="Inbox filters"
        className={cn(
          "shrink-0 overflow-hidden border-r border-white/[0.06] bg-card transition-all",
          railCollapsed ? "w-9" : "w-[280px]",
        )}
      >
        {railCollapsed ? (
          <div className="flex h-full flex-col items-center pt-1.5">
            <SidebarCollapseButton collapsed onToggle={() => setRailCollapsed(false)} />
          </div>
        ) : (
          <InboxExplorer
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
            role={role}
            onRoleChange={setRole}
            layout={layout}
            onLayoutChange={setLayout}
            search={search}
            onSearchChange={setSearch}
            onToggleCollapse={() => setRailCollapsed(true)}
          />
        )}
      </aside>

      {archive ? (
        <ArchiveTable rows={rows} total={visible.length} />
      ) : layout === "table" ? (
        <TableLayout {...layoutProps} />
      ) : layout === "stream" ? (
        <StreamLayout {...layoutProps} />
      ) : (
        <SplitLayout {...layoutProps} />
      )}
    </div>
  )
}
