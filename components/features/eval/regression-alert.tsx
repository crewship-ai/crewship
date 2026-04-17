"use client"

import { AlertTriangle } from "lucide-react"
import { Button } from "@/components/ui/button"
import { formatRelativeTime } from "@/lib/time"
import type { JournalEntry } from "@/lib/types/journal"

interface RegressionAlertProps {
  regressions: JournalEntry[]
  onView?: (entry: JournalEntry) => void
}

/**
 * Banner that surfaces recent `eval.regression_detected` entries. Shown
 * above the Eval runs table so a bad metric doesn't hide three rows down.
 */
export function RegressionAlert({ regressions, onView }: RegressionAlertProps) {
  if (regressions.length === 0) return null
  const latest = regressions[0]
  const count = regressions.length

  return (
    <div className="rounded-lg border border-red-500/40 bg-red-500/5 px-3 py-2.5 flex items-start gap-2.5">
      <AlertTriangle className="h-4 w-4 text-red-400 shrink-0 mt-0.5" />
      <div className="flex-1 min-w-0">
        <div className="text-sm font-medium text-red-300">
          {count === 1 ? "Regression detected" : `${count} regressions detected`}
        </div>
        <div className="mt-0.5 text-[11px] text-muted-foreground line-clamp-2">
          {latest.summary || "Candidate metrics fell below baseline."}{" "}
          <span className="font-mono tabular-nums">{formatRelativeTime(latest.ts)}</span>
        </div>
      </div>
      {onView && (
        <Button variant="outline" size="sm" className="h-7 px-2 text-xs" onClick={() => onView(latest)}>
          View
        </Button>
      )}
    </div>
  )
}
