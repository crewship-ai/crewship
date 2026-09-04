"use client"

import { useMemo } from "react"
import { motion } from "motion/react"
import { X } from "lucide-react"
import { StatusIcon, statusLabel } from "./status-icon"
import { cn } from "@/lib/utils"
import type { Mission, MissionStatus } from "@/lib/types/mission"

// Status chip order matches Linear's status column convention:
// backlog → planning → todo → in-progress → review → done → cancelled.
// FAILED slots between REVIEW and DONE because failed runs are still
// "after-review" decisions. DUPLICATE is rare; tucked at the end with
// CANCELLED so it doesn't take real estate up top.
// Exported so the explorer's filter dropdown offers the same statuses in the
// same order. Two surfaces filtering one state must at least agree on what
// the options are.
export const STATUS_CHIPS: MissionStatus[] = [
  "BACKLOG",
  "TODO",
  "PLANNING",
  "IN_PROGRESS",
  "REVIEW",
  "FAILED",
  "COMPLETED",
  "CANCELLED",
]

interface IssuesStatusChipsProps {
  issues: Mission[]
  selected: MissionStatus[]
  onToggle: (status: MissionStatus) => void
  onClear: () => void
  /** The server's total for the whole workspace (X-Total-Count); the All chip shows it when the page is shorter. */
  total?: number | null
}

// Horizontal row of status chips with live counts. Multi-select: clicking
// a chip toggles it in/out of the filter set. Empty set = show all.
// "All" pill at the left clears the selection in one click.
export function IssuesStatusChips({
  issues,
  selected,
  onToggle,
  onClear,
  total = null,
}: IssuesStatusChipsProps) {
  const allCount = total != null && total > issues.length ? total : issues.length
  const perStatusIsPage = total != null && total > issues.length
  const counts = useMemo(() => {
    const m = new Map<MissionStatus, number>()
    for (const i of issues) {
      m.set(i.status, (m.get(i.status) ?? 0) + 1)
    }
    return m
  }, [issues])

  const allActive = selected.length === 0

  return (
    <div
      role="group"
      aria-label="Filter issues by status"
      className="flex items-center gap-1.5 overflow-x-auto px-3 py-2 [&::-webkit-scrollbar]:hidden"
    >
      <button
        type="button"
        onClick={onClear}
        aria-pressed={allActive}
        className={cn(
          "shrink-0 rounded-full border px-2.5 py-0.5 text-[11px] transition-colors",
          allActive
            ? "border-primary/40 bg-primary/[0.12] text-primary"
            : "border-white/[0.08] bg-white/[0.02] text-muted-foreground hover:text-foreground/80",
        )}
      >
        All
        <span className="ml-1 text-[10px] tabular-nums opacity-60">{allCount.toLocaleString()}</span>
      </button>

      <div className="h-4 w-px bg-white/[0.08] shrink-0" />

      {STATUS_CHIPS.map((status) => {
        const count = counts.get(status) ?? 0
        const active = selected.includes(status)
        if (count === 0 && !active) return null
        return (
          <button
            key={status}
            type="button"
            onClick={() => onToggle(status)}
            aria-pressed={active}
            title={perStatusIsPage ? `${count} of the ${issues.length} loaded issues` : undefined}
            className={cn(
              "shrink-0 inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] transition-colors",
              active
                ? "border-primary/40 bg-primary/[0.12] text-primary"
                : "border-white/[0.08] bg-white/[0.02] text-muted-foreground hover:text-foreground/80",
            )}
          >
            <StatusIcon status={status} className="h-3 w-3" />
            <span>{statusLabel[status]}</span>
            <span className="text-[10px] tabular-nums opacity-60">{count}</span>
            {active && (
              <motion.span
                initial={{ opacity: 0, scale: 0 }}
                animate={{ opacity: 1, scale: 1 }}
                exit={{ opacity: 0, scale: 0 }}
                onClick={(e) => {
                  e.stopPropagation()
                  onToggle(status)
                }}
                className="hover:text-white"
              >
                <X className="h-2.5 w-2.5" />
              </motion.span>
            )}
          </button>
        )
      })}
    </div>
  )
}
