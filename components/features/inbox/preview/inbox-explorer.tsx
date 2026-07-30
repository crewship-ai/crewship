"use client"

import { useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import {
  Archive, Bell, Check, CircleDot, Inbox as InboxIcon, ScrollText, Share2, Sparkles,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

import {
  SidebarActiveChip, SidebarActiveChips, SidebarCollapseButton, SidebarFilterButton, SidebarRow,
  SidebarSearch, SidebarSection, SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { cn } from "@/lib/utils"

import { ActorAvatar } from "./actor"
import type { Bucket, InboxView, SubjectFacet } from "./types"

// =============================================================================
// The inbox rail — the same explorer every faceted page in the product uses.
//
// It carries exactly two things: the toolbar and Views. Everything else is a
// facet, and facets live behind the Filter button, which is where /issues and
// /routines put theirs. A rail that lists every bucket and every subject is a
// rail you scroll to find the one view you wanted.
//
// What is filtered stays visible as a removable chip under the toolbar, so the
// dropdown never becomes a place where state hides.
//
// Picking Archive swaps the filter's sections rather than adding to them. The
// archive is a different question — what was decided, by whom, when — and its
// facets have no meaning in the live inbox, exactly as the live buckets have
// none over resolved rows.
// =============================================================================

const dropdownAnim = {
  initial: { opacity: 0, scale: 0.95, y: -4 },
  animate: { opacity: 1, scale: 1, y: 0, transition: { duration: 0.12 } },
  exit: { opacity: 0, scale: 0.95, y: -4, transition: { duration: 0.1 } },
}

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

const OUTCOME_DOT: Record<string, string> = {
  approved: "bg-success",
  rejected: "bg-destructive",
  archived: "bg-muted-foreground/40",
  retried: "bg-primary",
  expired: "bg-warn",
}

/** One row of the filter dropdown. */
function FilterOption({
  active, onClick, children, count, testId,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
  count?: number
  testId?: string
}) {
  return (
    <button
      type="button"
      data-testid={testId}
      onClick={onClick}
      className={cn(
        "flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs transition-colors hover:bg-white/[0.06]",
        active ? "text-primary" : "text-muted-foreground/80",
      )}
    >
      {children}
      {count != null && <span className="ml-auto tabular-nums text-[10px] opacity-70">{count}</span>}
      {active && <Check className={cn("h-3 w-3 shrink-0", count != null && "ml-1")} />}
    </button>
  )
}

function FilterHeading({ children }: { children: React.ReactNode }) {
  return (
    <div className="px-3 py-1 text-[9px] font-semibold uppercase tracking-wider text-foreground/40">
      {children}
    </div>
  )
}

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
  const [filterOpen, setFilterOpen] = useState(false)

  const archive = view === "archived"
  const activeFilters = archive
    ? (outcome ? 1 : 0) + (actor ? 1 : 0) + (period !== "30" ? 1 : 0) + (selectedSubject ? 1 : 0)
    : (bucket ? 1 : 0) + (selectedSubject ? 1 : 0)

  const bucketLabel = BUCKETS.find((b) => b.id === bucket)?.label
  const subjectLabel = subjects.find((s) => s.id === selectedSubject)?.label
  const outcomeLabel = outcomeCounts.find((o) => o.id === outcome)?.label
  const periodLabel = PERIODS.find((p) => p.id === period)?.label

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        <SidebarSearch
          value={search}
          onValueChange={onSearchChange}
          placeholder={archive ? "Search the archive…" : "Search the inbox…"}
        />
        <div className="relative shrink-0">
          <SidebarFilterButton
            activeCount={activeFilters}
            aria-expanded={filterOpen}
            onClick={() => setFilterOpen(!filterOpen)}
          />
          <AnimatePresence>
            {filterOpen && (
              <>
                <div className="fixed inset-0 z-40" onClick={() => setFilterOpen(false)} />
                <motion.div
                  {...dropdownAnim}
                  className="absolute right-0 top-9 z-50 max-h-[380px] min-w-[220px] overflow-y-auto rounded-lg border border-white/[0.1] bg-card py-1 shadow-xl"
                >
                  {archive ? (
                    <>
                      <FilterHeading>Outcome</FilterHeading>
                      <FilterOption active={outcome === null} onClick={() => onOutcomeChange(null)}>
                        <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-muted-foreground/40" />
                        Any outcome
                      </FilterOption>
                      {outcomeCounts.map((o) => (
                        <FilterOption
                          key={o.id}
                          testId={`outcome-${o.id}`}
                          active={outcome === o.id}
                          count={o.count}
                          onClick={() => onOutcomeChange(outcome === o.id ? null : o.id)}
                        >
                          <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", OUTCOME_DOT[o.id] ?? "bg-muted-foreground/40")} />
                          {o.label}
                        </FilterOption>
                      ))}

                      <div className="mt-1 border-t border-white/[0.06]" />
                      <FilterHeading>Decided by</FilterHeading>
                      {actorCounts.map((a) => (
                        <FilterOption
                          key={a.id}
                          active={actor === a.id}
                          count={a.count}
                          onClick={() => onActorChange(actor === a.id ? null : a.id)}
                        >
                          <ActorAvatar actor={{ kind: "user", id: a.id, label: a.id }} size={20} />
                          {a.id}
                        </FilterOption>
                      ))}

                      <div className="mt-1 border-t border-white/[0.06]" />
                      <FilterHeading>Period</FilterHeading>
                      {PERIODS.map((p) => (
                        <FilterOption key={p.id} active={period === p.id} onClick={() => onPeriodChange(p.id)}>
                          <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-muted-foreground/40" />
                          {p.label}
                        </FilterOption>
                      ))}
                    </>
                  ) : (
                    <>
                      <FilterHeading>Buckets</FilterHeading>
                      <FilterOption active={bucket === null} onClick={() => onBucketChange(null)}>
                        <InboxIcon className="h-3.5 w-3.5 shrink-0 text-muted-foreground/70" />
                        All buckets
                      </FilterOption>
                      {BUCKETS.map((b) => (
                        <FilterOption
                          key={b.id}
                          testId={b.testId}
                          active={bucket === b.id}
                          count={bucketCounts[b.id]}
                          onClick={() => onBucketChange(bucket === b.id ? null : b.id)}
                        >
                          <b.icon className={cn("h-3.5 w-3.5 shrink-0", b.tone)} />
                          {b.label}
                        </FilterOption>
                      ))}
                    </>
                  )}

                  {subjects.length > 0 && (
                    <>
                      <div className="mt-1 border-t border-white/[0.06]" />
                      {/* Who the rows are ABOUT — the subject from the payload,
                          not the sender, so a Keeper request files under casey.
                          Agents draw their face, routines and the system a
                          glyph: square is a machine, circle is a person. */}
                      <FilterHeading>Subject</FilterHeading>
                      {subjects.map((s) => (
                        <FilterOption
                          key={s.id}
                          active={selectedSubject === s.id}
                          count={s.count}
                          onClick={() => onSubjectChange(selectedSubject === s.id ? null : s.id)}
                        >
                          <ActorAvatar actor={s} size={20} />
                          {s.label}
                        </FilterOption>
                      ))}
                    </>
                  )}
                </motion.div>
              </>
            )}
          </AnimatePresence>
        </div>
        <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />
      </SidebarToolbar>

      {/* Whatever the dropdown holds, the rail still says it out loud. */}
      <SidebarActiveChips>
        {bucketLabel && (
          <SidebarActiveChip onRemove={() => onBucketChange(null)}>{bucketLabel}</SidebarActiveChip>
        )}
        {subjectLabel && (
          <SidebarActiveChip onRemove={() => onSubjectChange(null)}>{subjectLabel}</SidebarActiveChip>
        )}
        {archive && outcomeLabel && (
          <SidebarActiveChip onRemove={() => onOutcomeChange(null)}>{outcomeLabel}</SidebarActiveChip>
        )}
        {archive && actor && (
          <SidebarActiveChip onRemove={() => onActorChange(null)}>{actor}</SidebarActiveChip>
        )}
        {archive && period !== "30" && (
          <SidebarActiveChip onRemove={() => onPeriodChange("30")}>{periodLabel}</SidebarActiveChip>
        )}
      </SidebarActiveChips>

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
    </div>
  )
}
