"use client"

// The rail, focused on one routine (#2998).
//
// Picking a routine's row narrows the rail to it: everything else goes, and
// the routine's runs are listed by day with their time and outcome, under the
// same STATUS rows and the same time window the rail already has. A run opens
// in the column beside it; "‹ All activity" leaves the focus.

import * as React from "react"
import { ChevronLeft } from "lucide-react"

import { SidebarRow, SidebarSection } from "@/components/layout/sidebar-kit"
import { usePipelineRunRecords } from "@/hooks/use-pipeline-run-records"
import { focusRuns } from "@/lib/activity-rail-focus"
import { railStatusRows, TIME_RANGES, type RailStatusRow } from "@/lib/activity-rail"
import { RUN_TONE_DOT, RUN_TONE_LABEL, triggerPhrase } from "@/lib/activity-run"
import { formatDurationMs } from "@/lib/activity-stream"
import { cn } from "@/lib/utils"

import { StatusRows } from "./activity-status-rows"

export interface RailRoutineFocusProps {
  workspaceId: string
  slug: string
  name: string
  scope: string
  range: string
  openRunId: string | null
  onPickScope: (key: RailStatusRow["key"]) => void
  onOpenRun: (runId: string) => void
  onLeave: () => void
}

export function RailRoutineFocus({
  workspaceId,
  slug,
  name,
  scope,
  range,
  openRunId,
  onPickScope,
  onOpenRun,
  onLeave,
}: RailRoutineFocusProps) {
  const { records, loading } = usePipelineRunRecords(workspaceId, slug)
  const window = TIME_RANGES.find((r) => r.key === range) ?? TIME_RANGES[1]
  const { days, counts, total } = focusRuns(records, { scope, rangeMs: window.ms })
  const rows = railStatusRows(counts, total)

  return (
    <div className="flex flex-col">
      <div className="flex items-center gap-1 border-b border-foreground/[0.06] px-2 py-1.5">
        <button
          type="button"
          onClick={onLeave}
          className="inline-flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          <ChevronLeft className="h-3.5 w-3.5" />
          All activity
        </button>
      </div>
      <div className="px-3 pb-1 pt-2">
        <p className="eyebrow text-muted-foreground">Routine</p>
        <p className="truncate text-sm font-medium" title={name}>
          {name}
        </p>
        <p className="text-[10.5px] text-muted-foreground-soft">
          {total} {total === 1 ? "run" : "runs"} · {window.label.toLowerCase()}
        </p>
      </div>

      <SidebarSection label="Status" count={rows.length} className="border-b border-foreground/[0.06] pb-1">
        <StatusRows rows={rows} scope={scope} onPick={onPickScope} />
      </SidebarSection>

      {loading && records.length === 0 ? (
        <p className="px-3 py-2 text-[11px] text-muted-foreground-soft">Loading runs…</p>
      ) : days.length === 0 ? (
        <p className="px-3 py-2 text-[11px] leading-snug text-muted-foreground-soft">
          {total === 0
            ? `${name} did not run in ${window.label.toLowerCase()}. Widen the time range in Filter.`
            : "No run of this routine matches the status picked above."}
        </p>
      ) : (
        days.map((d) => (
          <SidebarSection key={d.key} label={d.label} count={d.runs.length}>
            <div aria-label={`Runs, ${d.label}`}>
              {d.runs.map((r) => (
                <SidebarRow
                  key={r.id}
                  selected={openRunId === r.id}
                  onSelect={() => onOpenRun(r.id)}
                  aria-label={`${r.time} · ${RUN_TONE_LABEL[r.tone]}`}
                >
                  <span aria-hidden className={cn("h-2 w-2 shrink-0 rounded-full", RUN_TONE_DOT[r.tone])} />
                  <span className="w-11 shrink-0 font-mono text-[11px] tabular-nums text-foreground/85">{r.time}</span>
                  <span className={cn("min-w-0 flex-1 truncate text-[11px]", r.tone === "failed" ? "text-destructive" : "text-muted-foreground")}>
                    {RUN_TONE_LABEL[r.tone]}
                    {r.triggeredVia ? ` · ${triggerPhrase({ triggered_via: r.triggeredVia }).toLowerCase()}` : ""}
                  </span>
                  {r.durationMs ? (
                    <span className="shrink-0 font-mono text-[10px] text-muted-foreground-soft">
                      {formatDurationMs(r.durationMs)}
                    </span>
                  ) : null}
                </SidebarRow>
              ))}
            </div>
          </SidebarSection>
        ))
      )}
    </div>
  )
}
