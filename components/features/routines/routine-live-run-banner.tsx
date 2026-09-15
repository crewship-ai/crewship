"use client"

import { useState } from "react"
import { Eye, Gavel, Square } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"
import { formatAgo } from "@/lib/routine-run-presentation"
import { nameLookup } from "@/lib/routine-steps-layout"
import type { PipelineRunRecord } from "@/hooks/use-pipeline-run-records"

/**
 * The one banner under the routine header while a run is live.
 *
 * A run waiting for a person is the reader's job, so it leads with Decide;
 * a run in progress offers Watch and Stop. Stop asks first, and the question
 * says what stopping does not do — undo what already happened.
 */
export function RoutineLiveRunBanner({
  runs,
  definition,
  canStop,
  stopping,
  onOpen,
  onStop,
}: {
  runs: PipelineRunRecord[]
  definition?: unknown
  canStop: boolean
  stopping?: boolean
  onOpen: (runId: string) => void
  onStop: (runId: string) => void | Promise<void>
}) {
  const [confirming, setConfirming] = useState(false)
  if (!runs.length) return null
  const run = runs[0]
  const waiting = run.status === "waiting"
  const nameOf = nameLookup(definition && typeof definition === "object" ? (definition as Record<string, unknown>) : {})
  const step = run.current_step_id ? nameOf(run.current_step_id) : ""
  const more = runs.length - 1
  return (
    <div
      role="status"
      data-testid="routine-live-run-banner"
      className={cn(
        "flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3",
        waiting ? "border-warn/30 bg-warn/[0.08]" : "border-primary/30 bg-primary/[0.08]",
      )}
    >
      <span
        className={cn(
          "flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-card text-sm font-semibold",
          waiting ? "text-warn" : "text-primary",
        )}
        aria-hidden="true"
      >
        {waiting ? "?" : "▶"}
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-medium">
          {waiting ? "A run is waiting for your decision" : "A run is in progress"}
          {run.started_at && <span className="font-normal text-muted-foreground"> · started {formatAgo(run.started_at)}</span>}
        </div>
        <div className="text-xs text-muted-foreground">
          {step ? `Step “${step}”` : waiting ? "A person has to answer before it continues." : "Steps are being carried out."}
          {more > 0 && ` · ${more} more active ${more === 1 ? "run" : "runs"} in History`}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-1.5">
        <Button size="sm" variant={waiting ? "default" : "outline"} onClick={() => onOpen(run.id)} className="h-8 gap-1.5">
          {waiting ? <Gavel className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
          {waiting ? "Decide" : "Watch"}
        </Button>
        {canStop && (
          <Button size="sm" variant="ghost" disabled={stopping} onClick={() => setConfirming(true)} className="h-8 gap-1.5 text-muted-foreground hover:text-destructive">
            {stopping ? <Spinner className="h-3.5 w-3.5" /> : <Square className="h-3.5 w-3.5" />}
            Stop
          </Button>
        )}
      </div>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title="Stop this run?"
        description="Pending work is asked to stop. What already happened — a ledger write, a message — is not undone. Recorded results stay in History."
        confirmLabel="Stop run"
        cancelLabel="Keep running"
        destructive
        onConfirm={() => onStop(run.id)}
      />
    </div>
  )
}
