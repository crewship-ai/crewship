"use client"

// Previous runs as a row of dots, and the run peek (Harbor 2 RT-PEEK): a look
// at one run without leaving the page — how it ended, when, which version,
// how long and what it cost. J / K move to the next and previous run, Enter
// (or Open run) opens the run page.

import * as React from "react"
import { Peek } from "@/components/ui/peek"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { DetailCard } from "@/components/ui/detail"
import { BarChart3 } from "lucide-react"
import { cn } from "@/lib/utils"
import { formatRoutineTime } from "@/lib/routine-time"
import { formatDurationMs } from "@/lib/activity-stream"
import { runBarTone, type RunBarTone } from "@/lib/routine-page"
import { runProvenance } from "@/lib/run-provenance"
import type { PipelineRunRecord } from "@/hooks/use-pipeline-run-records"
import { formatReportedUsage } from "./routine-cost-format"

const DOT: Record<RunBarTone, string> = {
  ok: "bg-success",
  bad: "bg-destructive",
  warn: "bg-warn",
  mut: "bg-muted-foreground/40",
  info: "bg-primary",
}

const TONE: Record<RunBarTone, "success" | "danger" | "warn" | "muted" | "blue"> = {
  ok: "success",
  bad: "danger",
  warn: "warn",
  mut: "muted",
  info: "blue",
}

/** What a run did, in one sentence. */
export function runSentence(r: Pick<PipelineRunRecord, "status" | "outcome" | "mode">): string {
  if (r.mode === "dry_run" || r.mode === "test_run") return "A test run: messages and writes were simulated."
  const { tone } = runBarTone(r)
  if (r.status === "cancelled") return "Someone stopped it. What finished before stays."
  if (tone === "bad") return "Could not finish. Nothing after the failed step ran."
  if (tone === "warn") return "Waits for a person to decide."
  if (tone === "info") return "Running now."
  if (r.outcome === "NO_CHANGE") return "Ran and found nothing new, so nothing was sent."
  return "Ran and finished."
}

export function RoutinePreviousRuns({ runs, current, onPeek }: { runs: PipelineRunRecord[]; current?: string; onPeek: (runId: string) => void }) {
  const list = runs.slice(0, 20).slice().reverse()
  const failed = list.filter((r) => runBarTone(r).tone === "bad").length
  return (
    <DetailCard title="Previous runs" icon={BarChart3} subtitle={`${list.length}`}>
      <div className="flex flex-wrap items-center gap-1.5" role="list" aria-label="Previous runs, oldest first" data-testid="run-previous">
        {list.map((r) => {
          const t = runBarTone(r)
          return (
            <button
              key={r.id}
              type="button"
              role="listitem"
              onClick={() => onPeek(r.id)}
              title={`${r.started_at ? formatRoutineTime(r.started_at) : ""} · ${t.label}`}
              aria-label={`Peek at run ${r.id.slice(0, 8)} · ${t.label}`}
              aria-current={r.id === current ? "true" : undefined}
              className={cn("h-3 w-3 rounded-full transition-transform hover:scale-125 coarse:h-6 coarse:w-6", DOT[t.tone], r.id === current && "ring-2 ring-foreground/60 ring-offset-2 ring-offset-background")}
            />
          )
        })}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        {failed} of {list.length} could not finish · oldest left · click one to peek
      </p>
    </DetailCard>
  )
}

export function RoutineRunPeek({
  runs,
  runId,
  onChange,
  onClose,
  onOpen,
}: {
  workspaceId: string
  runs: PipelineRunRecord[]
  runId: string
  onChange: (runId: string) => void
  onClose: () => void
  onOpen: (runId: string) => void
}) {
  const i = runs.findIndex((r) => r.id === runId)
  const run = i >= 0 ? runs[i] : null
  if (!run) return null
  const t = runBarTone(run)
  const prov = runProvenance(run)
  // Newer is up the list (K), older down (J): the list is newest first.
  const newer = i > 0 ? runs[i - 1] : null
  const older = i < runs.length - 1 ? runs[i + 1] : null
  return (
    <Peek
      open
      onOpenChange={(open) => !open && onClose()}
      title={`Run ${run.id.slice(0, 8)}`}
      subtitle={run.started_at ? formatRoutineTime(run.started_at) : undefined}
      onPrev={newer ? () => onChange(newer.id) : undefined}
      onNext={older ? () => onChange(older.id) : undefined}
      onOpenFull={() => onOpen(run.id)}
      footer={
        <Button type="button" size="sm" onClick={() => onOpen(run.id)}>
          Open run
        </Button>
      }
    >
      <div className="flex flex-col gap-4 p-4" data-testid="run-peek">
        <div className="flex flex-wrap items-center gap-2">
          <StatusPill size="md" tone={TONE[t.tone]} label={t.label} />
          {run.pipeline_version != null && <span className="font-mono text-xs text-muted-foreground">v{run.pipeline_version}</span>}
        </div>
        <p className="text-sm">{runSentence(run)}</p>
        <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-2 text-xs">
          <dt className="text-muted-foreground">Started</dt>
          <dd>
            {run.started_at ? formatRoutineTime(run.started_at) : "—"} · {prov.label}
          </dd>
          <dt className="text-muted-foreground">Version</dt>
          <dd className="font-mono">{run.pipeline_version != null ? `v${run.pipeline_version}` : "—"}</dd>
          <dt className="text-muted-foreground">Took</dt>
          <dd className="font-mono">{run.duration_ms > 0 ? formatDurationMs(run.duration_ms) : "—"}</dd>
          <dt className="text-muted-foreground">Cost</dt>
          <dd className="font-mono">{formatReportedUsage(run.cost_usd)}</dd>
          {run.error_message && (
            <>
              <dt className="text-muted-foreground">Error</dt>
              <dd className="line-clamp-3 break-words">{run.error_message.split("\n")[0]}</dd>
            </>
          )}
        </dl>
        <p className="text-xs text-muted-foreground-soft">J and K move between runs; Enter opens the run.</p>
      </div>
    </Peek>
  )
}
