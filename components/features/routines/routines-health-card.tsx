"use client"

import * as React from "react"
import { HeartPulse } from "lucide-react"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { cn } from "@/lib/utils"
import { routineHealthRows, type HealthRoutine, type HealthRun } from "@/lib/routine-health"
import type { RunBarTone } from "@/lib/routine-page"
import type { BudgetSummary } from "@/hooks/use-routine-budget"
import { RoutineGlyph } from "./routine-glyph"

// Health (Harbor 2 RT-07): one row per routine — its last 20 runs as bars,
// the success rate over the finished ones, and what it spent this calendar
// month (GET /pipelines/budget-summary). The runs are the newest ones the page
// already loaded, and the figures say so; a per-routine 30-day aggregate is
// backend work (#3468).

const BAR: Record<RunBarTone, string> = {
  ok: "bg-success",
  bad: "bg-destructive",
  warn: "bg-warn",
  mut: "bg-muted-foreground/40",
  info: "bg-primary",
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`

export function RoutinesHealthCard({
  routines,
  runs,
  spend,
  onSelect,
}: {
  routines: (HealthRoutine & { id?: string })[]
  runs: HealthRun[]
  spend: BudgetSummary | null
  onSelect: (slug: string) => void
}) {
  const rows = React.useMemo(() => routineHealthRows(routines, runs, spend), [routines, runs, spend])
  const toFix = rows.filter((r) => r.checkup.bad > 0).length
  const toCheck = rows.filter((r) => r.checkup.bad === 0 && r.checkup.warn > 0).length
  const byslug = new Map(routines.map((r) => [r.slug, r]))
  return (
    <DashboardCard
      title="Health"
      icon={HeartPulse}
      data-testid="routines-health"
      hint={<span data-testid="health-summary">{toFix ? plural(toFix, "routine to fix", "routines to fix") : toCheck ? plural(toCheck, "routine to check", "routines to check") : "All good"}</span>}
    >
      {rows.length === 0 ? (
        <InlineEmpty icon={HeartPulse} text="No routines yet." />
      ) : (
        <ul className="flex flex-col">
          {rows.map((row) => (
            <li
              key={row.slug}
              data-testid={`health-row-${row.slug}`}
              className="flex flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-border/50 px-2 py-2 last:border-0"
            >
              <button
                type="button"
                onClick={() => onSelect(row.slug)}
                className="flex min-w-0 flex-1 basis-40 items-center gap-2 text-left coarse:min-h-[44px] hover:underline"
              >
                <RoutineGlyph routine={byslug.get(row.slug) ?? { slug: row.slug }} />
                <span className="truncate text-body font-medium text-foreground">{row.name}</span>
              </button>
              <span className="flex h-4 w-28 items-end gap-px" role="img" aria-label={row.runCount ? `Last ${plural(row.runCount, "run", "runs")}, oldest first` : "No runs"}>
                {row.bars.map((b) => (
                  <i key={b.id} data-testid="health-bar" title={b.label} className={cn("h-full min-w-px flex-1 rounded-[1px]", BAR[b.tone], b.tone === "mut" && "h-1/3")} />
                ))}
              </span>
              <span className="font-mono text-label tabular-nums text-muted-foreground">
                {row.successPct === null ? "—" : `${row.successPct}%`}
                <span className="text-muted-foreground-soft">{row.finished ? ` · ${row.ok} of ${row.finished}` : " · no finished runs"}</span>
              </span>
              {row.spentMonth !== null && <span className="font-mono text-label tabular-nums text-muted-foreground">${row.spentMonth.toFixed(2)} this month</span>}
              <span className={cn("font-mono text-label", row.checkup.bad ? "text-destructive" : row.checkup.warn ? "text-warn" : "text-success")}>
                {row.checkup.bad ? `${row.checkup.bad} to fix` : row.checkup.warn ? `${row.checkup.warn} to check` : "All good"}
              </span>
            </li>
          ))}
        </ul>
      )}
      <p className="mt-2 text-label text-muted-foreground-soft">
        Success counts the newest runs loaded for each routine (up to 20); spend is this calendar month.
      </p>
    </DashboardCard>
  )
}
