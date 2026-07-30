"use client"

import { useState } from "react"
import { Archive, Bell, CircleDot, Inbox as InboxIcon, ScrollText, Share2, Sparkles } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import {
  SidebarCollapseButton, SidebarFilterButton, SidebarRow, SidebarSearch, SidebarSection, SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { cn } from "@/lib/utils"

import type { Bucket, InboxView, SubjectFacet } from "./types"

// =============================================================================
// The inbox rail — the same explorer every faceted page in the product uses.
//
// Built out of sidebar-kit, so it is the /issues and /routines sidebar with a
// different set of sections: toolbar (search + filter + collapse), a section of
// views, a section of buckets, and a scrolling list of senders at the bottom
// where those two pages put Issues and Routines.
//
// Picking "Archiv" swaps the middle sections rather than adding to them. The
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

  /** Archive facets — only rendered in the archive view. */
  outcome: string | null
  onOutcomeChange: (o: string | null) => void
  outcomeCounts: { id: string; label: string; count: number }[]
  actor: string | null
  onActorChange: (a: string | null) => void
  actorCounts: { id: string; count: number }[]
  period: string
  onPeriodChange: (p: string) => void

  search: string
  onSearchChange: (v: string) => void
  onToggleCollapse: () => void
}

const VIEWS: { id: InboxView; label: string; icon: LucideIcon }[] = [
  { id: "inbox", label: "Inbox", icon: InboxIcon },
  { id: "unread", label: "Nepřečtené", icon: Bell },
  { id: "archived", label: "Archiv", icon: Archive },
]

const BUCKETS: { id: Bucket; label: string; icon: LucideIcon; tone: string; testId: string }[] = [
  { id: "decisions", label: "Rozhodnutí", icon: Sparkles, tone: "text-warn", testId: "facet-bucket-decisions" },
  { id: "replies", label: "Odpovědi", icon: Share2, tone: "text-primary", testId: "facet-bucket-replies" },
  { id: "review", label: "K revizi", icon: CircleDot, tone: "text-purple", testId: "facet-bucket-review" },
  { id: "routines", label: "Průběh rutin", icon: ScrollText, tone: "text-notice", testId: "facet-bucket-routines" },
  { id: "other", label: "Ostatní", icon: InboxIcon, tone: "text-muted-foreground", testId: "facet-bucket-other" },
]

const PERIODS = [
  { id: "7", label: "7 dní" },
  { id: "30", label: "30 dní" },
  { id: "all", label: "Vše" },
]

export function InboxExplorer(props: InboxExplorerProps) {
  const {
    view, onViewChange, viewCounts,
    bucket, onBucketChange, bucketCounts,
    subjects, selectedSubject, onSubjectChange,
    outcome, onOutcomeChange, outcomeCounts,
    actor, onActorChange, actorCounts,
    period, onPeriodChange,
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
          placeholder={archive ? "Hledat v archivu…" : "Hledat ve schránce…"}
        />
        <SidebarFilterButton activeCount={activeFilters} />
        <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />
      </SidebarToolbar>

      <SidebarSection
        label="Zobrazení"
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
            label="Rozhodnutí"
            count={outcomeCounts.length}
            collapsible
            collapsed={!outcomeOpen}
            onToggle={() => setOutcomeOpen(!outcomeOpen)}
            className="border-b border-white/[0.06]"
          >
            <SidebarRow selected={outcome === null} onSelect={() => onOutcomeChange(null)}>
              <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-muted-foreground/40" />
              <span className="flex-1 truncate text-foreground/80">Vše</span>
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
            label="Kdo rozhodl"
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
                <span className="grid h-4 w-4 shrink-0 place-items-center rounded bg-surface-raised text-[9px] font-semibold uppercase text-muted-foreground">
                  {a.id.slice(0, 1)}
                </span>
                <span className="flex-1 truncate text-foreground/80">{a.id}</span>
                <span className="text-[10px] tabular-nums text-muted-foreground-soft">{a.count}</span>
              </SidebarRow>
            ))}
          </SidebarSection>

          <SidebarSection
            label="Období"
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
          label="Košík"
          count={BUCKETS.length}
          collapsible
          collapsed={!bucketsOpen}
          onToggle={() => setBucketsOpen(!bucketsOpen)}
          className="border-b border-white/[0.06]"
        >
          <SidebarRow selected={bucket === null} onSelect={() => onBucketChange(null)}>
            <InboxIcon className="h-3.5 w-3.5 shrink-0 text-muted-foreground/70" />
            <span className="flex-1 truncate text-foreground/80">Vše</span>
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

      {/* The long list at the bottom, where /issues puts ISSUES and /routines
          puts ROUTINES. Here it is who the rows are about — the subject from
          the payload, not the sender, so a Keeper request files under casey. */}
      <div className="flex min-h-0 flex-1 flex-col">
        <SidebarSection label={archive ? "Odesílatelé v archivu" : "Subjekt"} count={subjects.length} />
        <div className="min-h-0 flex-1 overflow-y-auto pb-1">
          {subjects.map((s) => (
            <SidebarRow
              key={s.id}
              selected={selectedSubject === s.id}
              onSelect={() => onSubjectChange(selectedSubject === s.id ? null : s.id)}
            >
              <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", s.kind === "agent" ? "bg-success" : "bg-purple")} />
              <span className="flex-1 truncate text-foreground/80">{s.label}</span>
              <span className="text-[10px] tabular-nums text-muted-foreground-soft">{s.count}</span>
            </SidebarRow>
          ))}
        </div>
      </div>
    </div>
  )
}

const OUTCOME_DOT: Record<string, string> = {
  approved: "bg-success",
  rejected: "bg-destructive",
  archived: "bg-muted-foreground/40",
  retried: "bg-primary",
  expired: "bg-warn",
}
