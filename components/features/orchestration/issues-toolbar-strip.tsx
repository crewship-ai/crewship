"use client"

import { Filter } from "lucide-react"

import { cn } from "@/lib/utils"
import { Spinner } from "@/components/ui/spinner"
import { StatusIcon } from "@/components/features/issues/status-icon"
import { GROUP_OPTIONS, type GroupBy } from "@/components/features/issues/issues-view-model"
import type { MissionStatus } from "@/lib/types/mission"

/** 1015 → "1 015": a count a person reads, not a locale-dependent one. */
export function formatCount(n: number): string {
  return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, " ")
}

/**
 * How much of the list is on screen, from the server's total — null when the
 * page IS the list, or the total is not known yet. The board used to print
 * the length of the page it received as if it were the workspace.
 */
export function issuesShowingLabel(loaded: number, total: number | null): string | null {
  if (total == null || total <= loaded) return null
  return `Showing newest ${formatCount(loaded)} of ${formatCount(total)}`
}

/** The status chips over the list, in this order. "All" clears them. */
export const TOOLBAR_STATUSES: { status: MissionStatus; label: string }[] = [
  { status: "IN_PROGRESS", label: "In progress" },
  { status: "REVIEW", label: "In review" },
  { status: "TODO", label: "Todo" },
  { status: "BACKLOG", label: "Backlog" },
]

export interface IssuesToolbarStripProps {
  statuses: MissionStatus[]
  onToggleStatus: (s: MissionStatus) => void
  onClearStatuses: () => void
  /** Issues on screen after every filter. */
  shown: number
  groupBy: GroupBy
  /** The board groups by status whatever the list does. */
  board?: boolean
  /** Phone: the Filters chip opens the panel sheet. */
  onOpenFilters?: () => void
  activeFilters?: number
  loaded?: number
  total?: number | null
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
}

const CHIP = "inline-flex h-7 shrink-0 items-center gap-1.5 rounded-full border px-2.5 text-label whitespace-nowrap transition-colors"
const IDLE = "border-border bg-card text-muted-foreground hover:border-line-strong hover:text-foreground"
const ON = "chip-active border-transparent"

/**
 * The strip over the list: status chips on the left, how many issues and how
 * they are grouped on the right — and, when the workspace holds more than one
 * page, how much of it is loaded with a way to load more.
 */
export function IssuesToolbarStrip({
  statuses,
  onToggleStatus,
  onClearStatuses,
  shown,
  groupBy,
  board = false,
  onOpenFilters,
  activeFilters = 0,
  loaded = 0,
  total = null,
  hasMore = false,
  loadingMore = false,
  onLoadMore,
}: IssuesToolbarStripProps) {
  const showing = issuesShowingLabel(loaded, total)
  const grouped = board ? "status" : (GROUP_OPTIONS.find((g) => g.id === groupBy)?.label.toLowerCase() ?? groupBy)
  return (
    <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border px-4 py-2.5" data-slot="issues-toolbar">
      {onOpenFilters && (
        <button type="button" onClick={onOpenFilters} className={cn(CHIP, activeFilters > 0 ? ON : IDLE)}>
          <Filter className="size-3.5" aria-hidden />
          Filters
          {activeFilters > 0 && <span className="font-mono tabular-nums">{activeFilters}</span>}
        </button>
      )}
      <div role="group" aria-label="Filter issues by status" className="flex min-w-0 items-center gap-1.5 overflow-x-auto [scrollbar-width:none]">
        <button type="button" aria-pressed={statuses.length === 0} onClick={onClearStatuses} className={cn(CHIP, statuses.length === 0 ? ON : IDLE)}>
          All
        </button>
        {TOOLBAR_STATUSES.map(({ status, label }) => {
          const on = statuses.includes(status)
          return (
            <button key={status} type="button" aria-pressed={on} onClick={() => onToggleStatus(status)} className={cn(CHIP, on ? ON : IDLE)}>
              <StatusIcon status={status} className="size-3" />
              {label}
            </button>
          )
        })}
      </div>
      <span className="flex-1" />
      <span className="text-label text-muted-foreground-soft max-sm:hidden" data-testid="issues-count">
        {formatCount(shown)} {shown === 1 ? "issue" : "issues"}
        {groupBy !== "none" || board ? ` · grouped by ${grouped}` : ""}
      </span>
      {showing && (
        <span className="flex items-center gap-2 font-mono text-label text-muted-foreground" data-testid="issues-showing">
          {showing}
          {hasMore && onLoadMore && (
            <button
              type="button"
              onClick={onLoadMore}
              disabled={loadingMore}
              className="inline-flex items-center gap-1 text-primary-hover hover:underline disabled:opacity-60"
            >
              {loadingMore && <Spinner className="size-3" />}
              Load 100 more
            </button>
          )}
        </span>
      )}
    </div>
  )
}
