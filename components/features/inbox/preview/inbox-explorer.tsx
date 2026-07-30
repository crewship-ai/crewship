"use client"

import { useState } from "react"
import {
  Archive, Bell, CircleDot, Columns3, Inbox as InboxIcon, LayoutList, Rows3, ScrollText, Share2, Sparkles,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

import {
  SidebarCollapseButton, SidebarFilterButton, SidebarRow, SidebarSearch, SidebarSection, SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { cn } from "@/lib/utils"

import { ActorAvatar } from "./actor"
import type { Bucket, InboxView, LayoutStyle, SubjectFacet } from "./types"
import type { WorkspaceRole } from "./mock-data"

// =============================================================================
// The inbox rail — the same explorer every faceted page in the product uses.
//
// Built out of sidebar-kit, so it is the /issues and /routines sidebar with a
// different set of sections: toolbar (search + filter + collapse), views,
// facets, and a long list at the bottom where those two pages put Issues and
// Routines.
//
// Everything that steers the page lives here, including the role switch. It
// was in a second sub-bar and that bar existed only to hold it — page identity
// is already in the rail's first section, so the bar was a strip of chrome
// carrying one control.
//
// Picking Archive swaps the middle sections rather than adding to them. The
// archive is a different question — what was decided, by whom, when — and its
// facets have no meaning in the live inbox, exactly as the live buckets have
// none over resolved rows.
// =============================================================================

export interface InboxExplorerProps {
  view: InboxView
  onViewChange: (v: InboxView) => void
  viewCounts: Record<InboxView, number>

  bucket: Bucket | null
  onBucketChange: (b: Bucket | null) => void
  bucketCounts: Record<Bucket, number>

  subjects: SubjectFacet[]
  selectedSubject: string | null
  onSubjectChange: (id: string | null) => void

  outcome: string | null
  onOutcomeChange: (o: string | null) => void
  outcomeCounts: { id: string; label: string; count: number }[]
  actor: string | null
  onActorChange: (a: string | null) => void
  actorCounts: { id: string; count: number }[]
  period: string
  onPeriodChange: (p: string) => void

  role: WorkspaceRole
  onRoleChange: (r: WorkspaceRole) => void

  layout: LayoutStyle
  onLayoutChange: (l: LayoutStyle) => void

  search: string
  onSearchChange: (v: string) => void
  onToggleCollapse: () => void
}

const VIEWS: { id: InboxView; label: string; icon: LucideIcon }[] = [
  { id: "inbox", label: "Inbox", icon: InboxIcon },
  { id: "unread", label: "Unread", icon: Bell },
  { id: "archived", label: "Archive", icon: Archive },
]

const BUCKETS: { id: Bucket; label: string; icon: LucideIcon; tone: string; testId: string }[] = [
  { id: "decisions", label: "Needs a decision", icon: Sparkles, tone: "text-warn", testId: "facet-bucket-decisions" },
  { id: "replies", label: "Agent replies", icon: Share2, tone: "text-primary", testId: "facet-bucket-replies" },
  { id: "review", label: "Ready for review", icon: CircleDot, tone: "text-purple", testId: "facet-bucket-review" },
  { id: "routines", label: "Routine progress", icon: ScrollText, tone: "text-notice", testId: "facet-bucket-routines" },
  { id: "other", label: "Everything else", icon: InboxIcon, tone: "text-muted-foreground", testId: "facet-bucket-other" },
]

const PERIODS = [
  { id: "7", label: "Last 7 days" },
  { id: "30", label: "Last 30 days" },
  { id: "all", label: "All time" },
]

const ROLES: WorkspaceRole[] = ["OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"]

const LAYOUTS: { id: LayoutStyle; label: string; icon: LucideIcon }[] = [
  { id: "split", label: "Split", icon: Columns3 },
  { id: "table", label: "Table", icon: Rows3 },
  { id: "stream", label: "Stream", icon: LayoutList },
]

const OUTCOME_DOT: Record<string, string> = {
  approved: "bg-success",
  rejected: "bg-destructive",
  archived: "bg-muted-foreground/40",
  retried: "bg-primary",
  expired: "bg-warn",
}

export function InboxExplorer(props: InboxExplorerProps) {
  const {
    view, onViewChange, viewCounts,
    bucket, onBucketChange, bucketCounts,
    subjects, selectedSubject, onSubjectChange,
    outcome, onOutcomeChange, outcomeCounts,
    actor, onActorChange, actorCounts,
    period, onPeriodChange,
    role, onRoleChange,
    layout, onLayoutChange,
    search, onSearchChange, onToggleCollapse,
  } = props

  const [viewsOpen, setViewsOpen] = useState(true)
  const [bucketsOpen, setBucketsOpen] = useState(true)
  const [outcomeOpen, setOutcomeOpen] = useState(true)
  const [actorOpen, setActorOpen] = useState(true)
  const [periodOpen, setPeriodOpen] = useState(true)

  const archive = view === "archived"
  const activeFilters =
    (bucket ? 1 : 0) + (selectedSubject ? 1 : 0) + (outcome ? 1 : 0) + (actor ? 1 : 0) + (period !== "30" ? 1 : 0)

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        <SidebarSearch
          value={search}
          onValueChange={onSearchChange}
          placeholder={archive ? "Search the archive…" : "Search the inbox…"}
        />
        <SidebarFilterButton activeCount={activeFilters} />
        <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />
      </SidebarToolbar>

      <SidebarSection
        label="Views"
        count={VIEWS.length}
        collapsible
        collapsed={!viewsOpen}
        onToggle={() => setViewsOpen(!viewsOpen)}
        className="border-b border-white/[0.06]"
      >
        {VIEWS.map((v) => (
          <SidebarRow
            key={v.id}
            selected={view === v.id}
            onSelect={() => onViewChange(v.id)}
            data-testid={`view-${v.id}`}
          >
            <v.icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground/70" />
            <span className="flex-1 truncate text-foreground/80">{v.label}</span>
            <span className="text-[10px] tabular-nums text-muted-foreground-soft">{viewCounts[v.id]}</span>
          </SidebarRow>
        ))}
      </SidebarSection>

      {archive ? (
        <>
          <SidebarSection
            label="Outcome"
            count={outcomeCounts.length}
            collapsible
            collapsed={!outcomeOpen}
            onToggle={() => setOutcomeOpen(!outcomeOpen)}
            className="border-b border-white/[0.06]"
          >
            <SidebarRow selected={outcome === null} onSelect={() => onOutcomeChange(null)}>
              <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-muted-foreground/40" />
              <span className="flex-1 truncate text-foreground/80">All</span>
              <span className="text-[10px] tabular-nums text-muted-foreground-soft">
                {outcomeCounts.reduce((a, b) => a + b.count, 0)}
              </span>
            </SidebarRow>
            {outcomeCounts.map((o) => (
              <SidebarRow
                key={o.id}
                selected={outcome === o.id}
                onSelect={() => onOutcomeChange(outcome === o.id ? null : o.id)}
                data-testid={`outcome-${o.id}`}
              >
                <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", OUTCOME_DOT[o.id] ?? "bg-muted-foreground/40")} />
                <span className="flex-1 truncate text-foreground/80">{o.label}</span>
                <span className="text-[10px] tabular-nums text-muted-foreground-soft">{o.count}</span>
              </SidebarRow>
            ))}
          </SidebarSection>

          <SidebarSection
            label="Decided by"
            count={actorCounts.length}
            collapsible
            collapsed={!actorOpen}
            onToggle={() => setActorOpen(!actorOpen)}
            className="border-b border-white/[0.06]"
          >
            {actorCounts.map((a) => (
              <SidebarRow
                key={a.id}
                selected={actor === a.id}
                onSelect={() => onActorChange(actor === a.id ? null : a.id)}
              >
                <ActorAvatar actor={{ kind: "user", id: a.id, label: a.id }} size={20} />
                <span className="flex-1 truncate text-foreground/80">{a.id}</span>
                <span className="text-[10px] tabular-nums text-muted-foreground-soft">{a.count}</span>
              </SidebarRow>
            ))}
          </SidebarSection>

          <SidebarSection
            label="Period"
            count={PERIODS.length}
            collapsible
            collapsed={!periodOpen}
            onToggle={() => setPeriodOpen(!periodOpen)}
            className="border-b border-white/[0.06]"
          >
            {PERIODS.map((p) => (
              <SidebarRow key={p.id} selected={period === p.id} onSelect={() => onPeriodChange(p.id)}>
                <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-muted-foreground/40" />
                <span className="flex-1 truncate text-foreground/80">{p.label}</span>
              </SidebarRow>
            ))}
          </SidebarSection>
        </>
      ) : (
        <SidebarSection
          label="Buckets"
          count={BUCKETS.length}
          collapsible
          collapsed={!bucketsOpen}
          onToggle={() => setBucketsOpen(!bucketsOpen)}
          className="border-b border-white/[0.06]"
        >
          <SidebarRow selected={bucket === null} onSelect={() => onBucketChange(null)}>
            <InboxIcon className="h-3.5 w-3.5 shrink-0 text-muted-foreground/70" />
            <span className="flex-1 truncate text-foreground/80">All</span>
            <span className="text-[10px] tabular-nums text-muted-foreground-soft">
              {Object.values(bucketCounts).reduce((a, b) => a + b, 0)}
            </span>
          </SidebarRow>
          {BUCKETS.map((b) => (
            <SidebarRow
              key={b.id}
              selected={bucket === b.id}
              onSelect={() => onBucketChange(bucket === b.id ? null : b.id)}
              data-testid={b.testId}
            >
              <b.icon className={cn("h-3.5 w-3.5 shrink-0", b.tone)} />
              <span className="flex-1 truncate text-foreground/80">{b.label}</span>
              <span className="text-[10px] tabular-nums text-muted-foreground-soft">{bucketCounts[b.id]}</span>
            </SidebarRow>
          ))}
        </SidebarSection>
      )}

      {/* Who the rows are about — the subject from the payload, not the sender,
          so a Keeper request files under casey. Agents draw their face, routines
          and the system draw a glyph: square is a machine, circle is a person. */}
      <div className="flex min-h-0 flex-1 flex-col">
        <SidebarSection label={archive ? "Subjects in the archive" : "Subject"} count={subjects.length} />
        <div className="min-h-0 flex-1 overflow-y-auto pb-1">
          {subjects.map((s) => (
            <SidebarRow
              key={s.id}
              selected={selectedSubject === s.id}
              onSelect={() => onSubjectChange(selectedSubject === s.id ? null : s.id)}
            >
              <ActorAvatar actor={s} size={20} />
              <span className="flex-1 truncate text-foreground/80">{s.label}</span>
              <span className="text-[10px] tabular-nums text-muted-foreground-soft">{s.count}</span>
            </SidebarRow>
          ))}
        </div>
      </div>

      {/* Viewing-as + layout: preview controls, pinned to the bottom and
          visibly separated from the facets above so they never read as one. */}
      <div className="shrink-0 border-t border-white/[0.06] bg-surface-subtle/40 px-3 py-2">
        <div className="type-meta mb-1.5 uppercase tracking-wider text-foreground/50">Viewing as</div>
        <div className="mb-2.5 flex flex-wrap gap-1">
          {ROLES.map((r) => (
            <button
              key={r}
              type="button"
              onClick={() => onRoleChange(r)}
              aria-pressed={role === r}
              className={cn(
                "rounded px-1.5 py-0.5 text-[10px] font-medium transition-colors",
                role === r ? "bg-primary/20 text-primary" : "text-muted-foreground hover:text-foreground",
              )}
            >
              {r}
            </button>
          ))}
        </div>
        <div className="type-meta mb-1.5 uppercase tracking-wider text-foreground/50">Layout</div>
        <div className="flex gap-1">
          {LAYOUTS.map((l) => (
            <button
              key={l.id}
              type="button"
              onClick={() => onLayoutChange(l.id)}
              aria-pressed={layout === l.id}
              data-testid={`layout-${l.id}`}
              className={cn(
                "inline-flex flex-1 items-center justify-center gap-1 rounded-md border px-1.5 py-1 text-[10px] font-medium transition-colors",
                layout === l.id
                  ? "border-primary/30 bg-primary/15 text-primary"
                  : "border-white/[0.08] text-muted-foreground hover:text-foreground",
              )}
            >
              <l.icon className="h-3 w-3" />
              {l.label}
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
