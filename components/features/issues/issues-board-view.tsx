"use client"

import { useCallback, useMemo, useState } from "react"
import { IssueCard } from "./issue-card"
import { StatusIcon, statusLabel } from "./status-icon"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { CirclePlus } from "lucide-react"
import type { Mission, MissionStatus } from "@/lib/types/mission"

interface IssuesBoardViewProps {
  issues: Mission[]
  onIssueClick: (issue: Mission) => void
  onCreateClick?: () => void
  selectedIssueId?: string | null
}

const MAIN_STATUSES: MissionStatus[] = ["BACKLOG", "TODO", "IN_PROGRESS", "REVIEW", "COMPLETED"]
const SECONDARY_STATUSES: MissionStatus[] = ["FAILED", "CANCELLED", "DUPLICATE"]

/**
 * How many cards a column shows before it folds (README §4: priority, cap,
 * fold). A backlog of 982 is not five screens of cards; it is six and a
 * count.
 */
export const BOARD_COLUMN_CAP = 6

/**
 * The columns, and what each shows: the first `cap` cards and how many are
 * behind the fold. Pure, so the fold is testable without a render.
 */
export function foldColumns(issues: Mission[], statuses: MissionStatus[], cap: number, open: ReadonlySet<string>) {
  const byStatus = new Map<string, Mission[]>()
  for (const s of statuses) byStatus.set(s, [])
  for (const i of issues) byStatus.get(i.status)?.push(i)
  return statuses.map((status) => {
    const all = byStatus.get(status) ?? []
    const shown = open.has(status) ? all : all.slice(0, cap)
    return { status, all, shown, hidden: all.length - shown.length }
  })
}

export function IssuesBoardView({ issues, onIssueClick, onCreateClick, selectedIssueId }: IssuesBoardViewProps) {
  const hasIssues = issues.length > 0
  const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set())
  const toggleOpen = useCallback((status: string) => {
    setOpen((prev) => {
      const next = new Set(prev)
      if (next.has(status)) next.delete(status)
      else next.add(status)
      return next
    })
  }, [])

  const main = useMemo(() => foldColumns(issues, MAIN_STATUSES, BOARD_COLUMN_CAP, open), [issues, open])
  const secondary = useMemo(
    () => foldColumns(issues, SECONDARY_STATUSES, BOARD_COLUMN_CAP, open).filter((c) => c.all.length > 0),
    [issues, open],
  )

  const handleIssueClick = useCallback((issue: Mission) => onIssueClick(issue), [onIssueClick])

  if (!hasIssues) {
    return (
      <div className="p-2">
        <InlineEmpty
          icon={CirclePlus}
          text="No issues yet. Create one here, or `crewship issue create --crew <slug> --title …`."
          action={
            onCreateClick ? (
              <Button type="button" variant="link" size="xs" onClick={onCreateClick} className="h-auto p-0 text-label text-primary-hover">
                New issue →
              </Button>
            ) : undefined
          }
        />
      </div>
    )
  }

  const renderCard = (issue: Mission, width?: string) => {
    const isDimmed = selectedIssueId != null && issue.id !== selectedIssueId
    const isHighlighted = selectedIssueId != null && issue.id === selectedIssueId
    return (
      <div
        key={issue.id}
        className={cn(
          "transition-all duration-200",
          width,
          isDimmed && "opacity-40",
          isHighlighted && "ring-1 ring-primary/50 rounded-lg",
        )}
      >
        <IssueCard issue={issue} onClick={() => handleIssueClick(issue)} />
      </div>
    )
  }

  const renderFold = (col: { status: MissionStatus; all: Mission[]; hidden: number }) =>
    col.all.length > BOARD_COLUMN_CAP ? (
      <button
        type="button"
        onClick={() => toggleOpen(col.status)}
        className="mt-1 w-full rounded-md py-2 text-center text-label text-primary-hover hover:underline"
        data-testid={`board-fold-${col.status}`}
      >
        {col.hidden > 0 ? `${col.hidden} more · Show all` : "Show fewer"}
      </button>
    ) : null

  return (
    <div className="flex flex-col gap-4">
      {/* Five columns that FIT: `minmax(0,1fr)` at 1440 so Done is on screen
          (five fixed 280px columns hid it behind a scroller), one column
          per status stacked below md (README §6: no horizontal overflow at
          390). Each column folds at six cards. */}
      <div className="grid grid-cols-1 gap-3 md:grid-cols-3 lg:grid-cols-5">
        {main.map((col) => (
          <section key={col.status} className="flex min-w-0 flex-col" aria-label={statusLabel[col.status]}>
            <div className="flex items-center gap-2 px-1 pb-2">
              <StatusIcon status={col.status} className="h-3.5 w-3.5" />
              <span className="text-sm font-medium text-foreground/80">{statusLabel[col.status]}</span>
              <span className="text-xs text-foreground/50 tabular-nums">{col.all.length}</span>
            </div>
            <div className="flex flex-col gap-2 px-0.5">
              {col.shown.length === 0 ? (
                <div className="flex h-14 items-center justify-center rounded-lg border border-dashed border-border/50">
                  <span className="text-xs text-foreground/40">No issues</span>
                </div>
              ) : (
                col.shown.map((issue) => renderCard(issue))
              )}
              {renderFold(col)}
            </div>
          </section>
        ))}
      </div>

      {/* Failed / Cancelled / Duplicate: a second row, only when non-empty. */}
      {secondary.length > 0 && (
        <div className="grid grid-cols-1 gap-3 border-t pt-3 md:grid-cols-3">
          {secondary.map((col) => (
            <section key={col.status} className="min-w-0" aria-label={statusLabel[col.status]}>
              <div className="mb-2 flex items-center gap-2">
                <StatusIcon status={col.status} className="h-3.5 w-3.5" />
                <span className="text-xs font-medium text-muted-foreground">{statusLabel[col.status]}</span>
                <span className="text-xs text-foreground/50 tabular-nums">{col.all.length}</span>
              </div>
              <div className="flex flex-col gap-2">
                {col.shown.map((issue) => renderCard(issue))}
                {renderFold(col)}
              </div>
            </section>
          ))}
        </div>
      )}
    </div>
  )
}
