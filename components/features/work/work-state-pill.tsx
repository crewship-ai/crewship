"use client"

/**
 * How a work item's state is allowed to look
 * (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §9).
 *
 * The rule this file exists to enforce is the one about
 * `needs_reconciliation`: it is neither a success nor a failure, and it must
 * not be rendered as either. It means a runtime MAY still be alive under a
 * locator nobody has verified, it is still holding capacity, and a person has
 * to look. So it gets its own tone, its own icon and its own word — not the
 * red that says "this is over and it went badly", and certainly not the green
 * that says "this is over".
 */

import * as React from "react"
import {
  AlertTriangle, Ban, CheckCircle2, Clock, Hourglass, Loader2, TimerReset, XCircle,
} from "lucide-react"

import { Pill, type DetailTone } from "@/components/ui/detail"
import { cn } from "@/lib/utils"
import {
  WORK_STATE_LABEL,
  WORK_STATE_MEANING,
  workOutcome,
  type WorkOutcome,
  type WorkState,
} from "@/hooks/use-work-items"

/** The tone per OUTCOME, not per state — so the six things a reader needs to
 *  tell apart are six, and adding a state cannot quietly invent a seventh. */
const OUTCOME_TONE: Record<WorkOutcome, DetailTone> = {
  waiting: "default",
  live: "blue",
  succeeded: "success",
  failed: "destructive",
  cancelled: "default",
  // Not destructive: an unresolved item has not failed. Warn is the product's
  // "somebody has to look at this" colour, and it is the only outcome here
  // that is asking for a person.
  unresolved: "warn",
}

const STATE_ICON: Record<WorkState, React.ComponentType<{ className?: string }>> = {
  queued: Clock,
  starting: Loader2,
  running: Loader2,
  waiting: Hourglass,
  retry_wait: TimerReset,
  succeeded: CheckCircle2,
  failed: XCircle,
  expired: TimerReset,
  cancelled: Ban,
  needs_reconciliation: AlertTriangle,
}

export interface WorkStatePillProps {
  state: WorkState
  className?: string
  "data-testid"?: string
}

export function WorkStatePill({ state, className, "data-testid": testId }: WorkStatePillProps) {
  const outcome = workOutcome(state)
  const Icon = STATE_ICON[state]
  const spins = state === "starting" || state === "running"
  return (
    <span
      // The outcome is on the DOM, not only in a class name, so "is this
      // rendered as a success" is a question a test can ask without reading
      // Tailwind.
      data-work-state={state}
      data-work-outcome={outcome}
      data-testid={testId}
      title={WORK_STATE_MEANING[state]}
      className="inline-flex"
    >
      <Pill tone={OUTCOME_TONE[outcome]} className={cn("gap-1.5", className)}>
        <Icon className={cn("h-3 w-3", spins && "animate-spin")} />
        {WORK_STATE_LABEL[state]}
      </Pill>
    </span>
  )
}

/**
 * The callout an unresolved item gets instead of a result.
 *
 * Renders nothing for every other state, so a caller can drop it into a detail
 * header unconditionally and the one state that needs a paragraph is the only
 * one that gets it.
 */
export function NeedsReconciliationNotice({
  state,
  runtimeLocator,
  className,
}: {
  state: WorkState
  runtimeLocator?: string | null
  className?: string
}) {
  if (state !== "needs_reconciliation") return null
  return (
    <div
      data-testid="needs-reconciliation-notice"
      role="status"
      className={cn(
        "flex gap-2.5 rounded-lg border border-warn/30 bg-warn/[0.06] px-3 py-2.5 text-warn",
        className,
      )}
    >
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
      <div className="min-w-0 space-y-1">
        <div className="text-[13px] font-medium">Needs reconciliation — this is not a result</div>
        <p className="text-[11px] leading-relaxed text-warn/85">
          {WORK_STATE_MEANING.needs_reconciliation} It leaves this state only when somebody
          resolves it, never when a timer fires.
        </p>
        {runtimeLocator ? (
          <p className="text-[11px] leading-relaxed text-warn/85">
            Last known runtime: <span className="font-mono">{runtimeLocator}</span>
          </p>
        ) : null}
      </div>
    </div>
  )
}
