"use client"

import * as React from "react"
import { AnimatePresence, motion, useReducedMotion } from "motion/react"

import { useJournalList } from "@/hooks/use-journal-list"
import type { JournalEntry } from "@/lib/types/journal"
import { cn } from "@/lib/utils"

const TICKER_LIMIT = 5

/** Pure: which dot an entry gets. Runs and errors are the only two things a
 *  glance needs to tell apart; everything else is neutral. */
export function tickerTone(entry: Pick<JournalEntry, "entry_type" | "severity">): "success" | "danger" | "warn" | "blue" | "muted" {
  if (entry.severity === "error" || entry.entry_type.endsWith(".failed") || entry.entry_type.endsWith(".timeout")) return "danger"
  if (entry.severity === "warn") return "warn"
  if (entry.entry_type === "run.completed") return "success"
  if (entry.entry_type.startsWith("run.") || entry.entry_type.startsWith("agent.")) return "blue"
  return "muted"
}

const TONE = {
  success: "bg-success",
  danger: "bg-destructive",
  warn: "bg-warn",
  blue: "bg-primary",
  muted: "bg-muted-foreground/60",
} as const

function clock(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return "—"
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false })
}

/**
 * The last few journal lines, newest first, under "Running now". Reads the
 * same list the Activity page reads; `reloadKey` is the page's debounced
 * realtime tick, so a run finishing shows up here within a second without
 * this component holding a socket of its own.
 */
export function ActivityTicker({ workspaceId, reloadKey }: { workspaceId: string | null; reloadKey: number }) {
  const reduce = useReducedMotion()
  const { entries, loading, refresh } = useJournalList({ workspaceId, limit: TICKER_LIMIT, maxEntries: TICKER_LIMIT })
  const first = React.useRef(true)
  React.useEffect(() => {
    if (first.current) {
      first.current = false
      return
    }
    void refresh()
  }, [reloadKey, refresh])

  const rows = entries.slice(0, TICKER_LIMIT)
  return (
    <div className="mt-3 border-t border-border/50 pt-2.5" data-testid="dashboard-activity-ticker">
      <div className="mb-1.5 flex items-center justify-between text-label text-muted-foreground">
        <span className="font-medium">Activity</span>
        <a href="/activity" className="font-mono text-[10px] text-primary-hover hover:underline">Journal →</a>
      </div>
      {rows.length === 0 ? (
        <div className="text-label text-muted-foreground-soft">{loading ? "Reading the journal…" : "Quiet so far — runs, hand-offs and errors will scroll here."}</div>
      ) : (
        <ul className="flex flex-col gap-1">
          <AnimatePresence initial={false}>
            {rows.map((entry) => (
              <motion.li
                key={entry.id}
                layout="position"
                initial={reduce ? false : { opacity: 0, y: -6 }}
                animate={{ opacity: 1, y: 0 }}
                exit={reduce ? undefined : { opacity: 0 }}
                transition={{ duration: 0.28, ease: [0.22, 1, 0.36, 1] }}
                className="flex items-center gap-2 text-label"
              >
                <span className="w-10 shrink-0 whitespace-nowrap font-mono text-[10px] tabular-nums text-muted-foreground">{clock(entry.ts)}</span>
                <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", TONE[tickerTone(entry)])} aria-hidden />
                <span className="min-w-0 flex-1 truncate text-foreground/85">{entry.summary}</span>
              </motion.li>
            ))}
          </AnimatePresence>
        </ul>
      )}
    </div>
  )
}
