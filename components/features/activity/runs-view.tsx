"use client"

import { useEffect, useMemo, useState } from "react"
import { useSearchParams } from "next/navigation"
import {
  AlertCircle,
  Calendar,
  Check,
  ChevronDown,
  ChevronRight,
  CircleDot,
  Globe,
  Loader2,
  PauseCircle,
  ScrollText,
  Sparkles,
  Webhook,
} from "lucide-react"
import { motion, AnimatePresence } from "motion/react"
import Link from "next/link"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { usePipelineRuns, type PipelineRun } from "@/hooks/use-pipeline-runs"
import { statusIcon, statusTint } from "@/lib/activity/run-status"
import { relTime, formatDuration } from "@/lib/activity/format-time"

// RunsView — the /activity "what's happening right now" surface.
// Each row is one pipeline_run. Collapsed shows source pill + routine
// name + status; expanded shows the step tree with agent attribution
// and per-step output.
//
// Why this is the default sub-tab on /activity: Graph + Timeline +
// Feed answer "where do agents live" / "when did things happen" /
// "what events fired", but none of them answer the user's actual
// question — "what is happening right now and what did it produce."
// RunsView IS that answer.

type StatusFilter = "all" | "active" | "completed" | "failed"

interface RunsViewProps {
  workspaceId: string
}

// useEffect imported here so RunStepTree's lazy DSL fetch compiles
// without dragging the import into every helper. (Already imported
// above for focusRunId scroll-into-view, just confirming the symbol
// stays resolvable for the tree.)

export function RunsView({ workspaceId }: RunsViewProps) {
  const searchParams = useSearchParams()
  const focusRunId = searchParams.get("run")
  const [filter, setFilter] = useState<StatusFilter>("active")
  // Force "all" on mount when ?run= is present so the deep-link works
  // even if the focused run is already completed (and would otherwise
  // be filtered out of the active view).
  useEffect(() => {
    if (focusRunId) setFilter("all")
  }, [focusRunId])

  const { runs, loading, error } = usePipelineRuns(workspaceId, filter)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  // Auto-expand the focused run on mount + scroll it into view.
  useEffect(() => {
    if (!focusRunId) return
    setExpanded((prev) => new Set([...prev, focusRunId]))
    const el = document.getElementById(`run-card-${focusRunId}`)
    if (el) {
      setTimeout(() => el.scrollIntoView({ behavior: "smooth", block: "center" }), 100)
    }
  }, [focusRunId, runs.length])

  const counts = useMemo(() => {
    const active = runs.filter((r) =>
      r.status === "running" || r.status === "queued" || r.status === "paused"
    ).length
    const completed = runs.filter((r) => r.status === "completed").length
    const failed = runs.filter((r) => r.status === "failed" || r.status === "cancelled").length
    return { active, completed, failed, total: runs.length }
  }, [runs])

  const toggleExpand = (id: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  return (
    <div className="flex h-full flex-col bg-background">
      {/* Filter strip */}
      <div className="flex shrink-0 items-center gap-1 border-b border-white/[0.06] px-3 py-2">
        <FilterBtn label="Active" count={counts.active} active={filter === "active"} onClick={() => setFilter("active")} />
        <FilterBtn label="All" count={runs.length} active={filter === "all"} onClick={() => setFilter("all")} />
        <FilterBtn label="Completed" count={counts.completed} active={filter === "completed"} onClick={() => setFilter("completed")} />
        <FilterBtn label="Failed" count={counts.failed} active={filter === "failed"} onClick={() => setFilter("failed")} />
        <div className="flex-1" />
        {loading && <Loader2 className="h-3 w-3 animate-spin text-muted-foreground/50" />}
      </div>

      {/* Run list */}
      <div className="flex-1 overflow-y-auto">
        {loading && runs.length === 0 ? (
          <div className="space-y-2 p-4">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-16 w-full rounded-md" />
            ))}
          </div>
        ) : error ? (
          <div className="p-6 text-center text-xs text-rose-300">Runs unavailable: {error}</div>
        ) : runs.length === 0 ? (
          <EmptyState filter={filter} />
        ) : (
          <ul className="divide-y divide-white/[0.04]">
            {runs.map((run) => (
              <RunCard
                key={run.id}
                run={run}
                workspaceId={workspaceId}
                expanded={expanded.has(run.id)}
                focused={focusRunId === run.id}
                onToggle={() => toggleExpand(run.id)}
              />
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}

function FilterBtn({
  label,
  count,
  active,
  onClick,
}: {
  label: string
  count: number
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      onClick={onClick}
      className={cn(
        "flex items-center gap-1.5 rounded px-2 py-1 text-xs transition-colors",
        active ? "bg-blue-500/15 text-blue-300" : "text-muted-foreground hover:text-foreground/80",
      )}
    >
      <span>{label}</span>
      <span className={cn(
        "rounded px-1 py-0.5 text-[10px] tabular-nums",
        active ? "bg-blue-500/20 text-blue-200" : "bg-white/[0.06] text-foreground/40",
      )}>
        {count}
      </span>
    </button>
  )
}

function RunCard({
  run,
  workspaceId,
  expanded,
  focused,
  onToggle,
}: {
  run: PipelineRun
  workspaceId: string
  expanded: boolean
  focused: boolean
  onToggle: () => void
}) {
  const StatusIcon = statusIcon(run.status)
  const statusColor = statusTint(run.status)

  return (
    <li
      id={`run-card-${run.id}`}
      className={cn(
        "transition-colors",
        focused && "ring-1 ring-blue-400/40",
        expanded && "bg-card/40",
      )}
    >
      {/* Header row — always visible, click toggles expansion */}
      <button
        onClick={onToggle}
        aria-expanded={expanded}
        aria-controls={`run-card-content-${run.id}`}
        className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-white/[0.02]"
      >
        <span
          className={cn(
            "flex h-6 w-6 shrink-0 items-center justify-center rounded-full",
            statusColor.bg,
          )}
        >
          <StatusIcon className={cn("h-3 w-3", statusColor.icon)} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-sm font-medium">{run.pipeline_name || run.pipeline_slug}</span>
            <SourcePill run={run} />
            <StatusPill status={run.status} />
          </div>
          <div className="mt-0.5 flex items-center gap-2 text-[10px] text-muted-foreground/70">
            <span className="font-mono">{run.id}</span>
            <span>·</span>
            <span>{relTime(run.started_at)}</span>
            {run.duration_ms > 0 && (
              <>
                <span>·</span>
                <span>{formatDuration(run.duration_ms)}</span>
              </>
            )}
            {run.cost_usd > 0 && (
              <>
                <span>·</span>
                <span>${run.cost_usd.toFixed(4)}</span>
              </>
            )}
          </div>
        </div>
        <span className="text-muted-foreground/40">
          {expanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
        </span>
      </button>

      {/* Expanded body — step tree + outputs */}
      <AnimatePresence initial={false}>
        {expanded && (
          <motion.div
            id={`run-card-content-${run.id}`}
            initial={{ height: 0, opacity: 0 }}
            animate={{ height: "auto", opacity: 1 }}
            exit={{ height: 0, opacity: 0 }}
            transition={{ duration: 0.15 }}
            className="overflow-hidden"
          >
            <RunStepTree workspaceId={workspaceId} run={run} />
          </motion.div>
        )}
      </AnimatePresence>
    </li>
  )
}

// SourcePill renders a chip linking this run back to its trigger:
// an issue identifier (clickable to /issues), a schedule, a webhook,
// a parent pipeline, or a manual run. The user's mental model is
// "this happened because X" — the pill is the X.
function SourcePill({ run }: { run: PipelineRun }) {
  if (run.triggered_via === "issue" && run.issue_identifier) {
    return (
      <Link
        href={`/issues/${encodeURIComponent(run.issue_identifier)}`}
        onClick={(e) => e.stopPropagation()}
        className="rounded bg-blue-500/15 px-1.5 py-0.5 text-[10px] font-medium text-blue-300 hover:bg-blue-500/25"
      >
        <CircleDot className="mr-1 inline h-2.5 w-2.5" />
        {run.issue_identifier}
      </Link>
    )
  }
  if (run.triggered_via === "schedule") {
    return (
      <span className="rounded bg-violet-500/15 px-1.5 py-0.5 text-[10px] font-medium text-violet-300">
        <Calendar className="mr-1 inline h-2.5 w-2.5" />
        schedule
      </span>
    )
  }
  if (run.triggered_via === "webhook") {
    return (
      <span className="rounded bg-amber-500/15 px-1.5 py-0.5 text-[10px] font-medium text-amber-300">
        <Webhook className="mr-1 inline h-2.5 w-2.5" />
        webhook
      </span>
    )
  }
  if (run.triggered_via === "call_pipeline") {
    return (
      <span className="rounded bg-white/[0.08] px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">
        <ScrollText className="mr-1 inline h-2.5 w-2.5" />
        sub-run
      </span>
    )
  }
  return (
    <span className="rounded bg-white/[0.06] px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">
      manual
    </span>
  )
}

function StatusPill({ status }: { status: string }) {
  const tint = statusTint(status)
  return (
    <span className={cn("rounded px-1.5 py-0.5 text-[10px] font-medium capitalize", tint.bg, tint.text)}>
      {status}
    </span>
  )
}

// PipelineDSL is the trimmed shape we pull from the pipeline detail
// endpoint. Only `steps` is needed for the tree — id + type drive the
// rendering, agent_slug + wait copy are surfaced when present.
interface PipelineDSLStep {
  id: string
  type: string
  agent_slug?: string
  wait?: { kind?: string; approval_prompt?: string }
}

interface PipelineDSL {
  steps?: PipelineDSLStep[]
}

interface PipelineDetail {
  id: string
  slug: string
  name: string
  definition?: PipelineDSL
}

// RunStepTree fetches the pipeline DEFINITION lazily so we can render
// every step (done / current / future) instead of just the ones with
// outputs. The previous version iterated over step_outputs only and
// the user couldn't see step 4 of 4 ("publish") sitting there waiting.
// Cache via component state — one fetch per expansion of this run.
function RunStepTree({ workspaceId, run }: { workspaceId: string; run: PipelineRun }) {
  const [definition, setDefinition] = useState<PipelineDSL | null>(null)
  useEffect(() => {
    if (!run.pipeline_slug) return
    let cancelled = false
    fetch(
      `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(run.pipeline_slug)}`,
    )
      .then(async (res) => (res.ok ? ((await res.json()) as PipelineDetail) : null))
      .then((data) => {
        if (cancelled || !data?.definition) return
        setDefinition(data.definition)
      })
      .catch(() => { /* swallow — fall back to outputs-only view */ })
    return () => { cancelled = true }
  }, [workspaceId, run.pipeline_slug])

  const stepOutputs = run.step_outputs ?? {}
  const completedSet = new Set(Object.keys(stepOutputs))

  // Step status:
  //   - in step_outputs            → done
  //   - id === current_step_id     → paused/running (the wait or in-flight one)
  //   - else                       → pending (only when DSL is loaded)
  function statusOf(stepID: string): "done" | "current" | "pending" {
    if (completedSet.has(stepID)) return "done"
    if (run.current_step_id && stepID === run.current_step_id) return "current"
    return "pending"
  }

  // Two render paths:
  //   1) DSL loaded → full step list (done + current + pending) gives
  //      the user the whole "here's where we are out of N total" view.
  //   2) DSL not loaded yet (or pipeline gone) → outputs-only fallback
  //      so the user still sees what the run produced.
  const dslSteps = definition?.steps ?? []
  const hasDSL = dslSteps.length > 0

  return (
    <div className="border-t border-white/[0.06] bg-card/20 px-4 py-3 text-xs">
      {/* Triggered-by row */}
      <div className="mb-2 flex items-center gap-2 text-[10px] text-muted-foreground/60">
        <Globe className="h-3 w-3" />
        <span>triggered_by:</span>
        <span className="font-mono">{run.triggered_via}</span>
        {run.triggered_by_id && (
          <>
            <span>·</span>
            <span className="font-mono">{run.triggered_by_id}</span>
          </>
        )}
      </div>

      {hasDSL ? (
        <ol className="space-y-1">
          {dslSteps.map((step, idx) => {
            const s = statusOf(step.id)
            return (
              <StepRow
                key={step.id}
                index={idx + 1}
                stepID={step.id}
                output={s === "done" ? stepOutputs[step.id] : undefined}
                stepStatus={s}
                stepType={step.type}
                agentSlug={step.agent_slug}
                waitPrompt={step.wait?.approval_prompt}
              />
            )
          })}
        </ol>
      ) : completedSet.size > 0 ? (
        <ol className="space-y-1">
          {Object.keys(stepOutputs).map((stepID, idx) => (
            <StepRow
              key={stepID}
              index={idx + 1}
              stepID={stepID}
              output={stepOutputs[stepID]}
              stepStatus="done"
            />
          ))}
          {run.current_step_id && !completedSet.has(run.current_step_id) && (
            <li className="flex items-center gap-2 px-2 py-1 text-amber-300">
              <PauseCircle className="h-3 w-3 animate-pulse" />
              <span className="font-mono text-[10px]">{run.current_step_id}</span>
              <span className="text-[10px] text-amber-200/70">— in flight</span>
            </li>
          )}
        </ol>
      ) : run.current_step_id ? (
        <div className="flex items-center gap-2 px-2 py-1 text-amber-300">
          <PauseCircle className="h-3 w-3 animate-pulse" />
          <span className="font-mono text-[10px]">{run.current_step_id}</span>
          <span className="text-[10px] text-amber-200/70">— in flight, no outputs yet</span>
        </div>
      ) : (
        <div className="px-2 py-1 text-[10px] text-muted-foreground/60">
          No step outputs recorded.
        </div>
      )}

      {/* Error trailer */}
      {run.error_message && (
        <div className="mt-2 rounded border border-rose-500/30 bg-rose-500/10 px-2 py-1.5 text-[11px] text-rose-300">
          <div className="flex items-center gap-1.5">
            <AlertCircle className="h-3 w-3 shrink-0" />
            <span className="font-medium">
              {run.failed_at_step ? `Failed at ${run.failed_at_step}` : "Failed"}
            </span>
          </div>
          <div className="mt-0.5 font-mono text-[10px] text-rose-200/70">{run.error_message}</div>
        </div>
      )}

      {/* Footer actions */}
      <div className="mt-3 flex items-center justify-between border-t border-white/[0.04] pt-2">
        <span className="font-mono text-[10px] text-muted-foreground/40">{run.pipeline_slug}</span>
        <div className="flex items-center gap-2">
          {/* Show "Resolve in Inbox" whenever the current step is a
            * wait — this catches both the official paused status AND
            * the more common case where the run sits at status=running
            * with current_step_id pointing at a wait step. The original
            * status==="paused" gate never matched in practice. */}
          {(() => {
            const currentStep = dslSteps.find((s) => s.id === run.current_step_id)
            const isWaitingForApproval =
              run.status === "paused" ||
              (run.current_step_id !== "" && currentStep?.type === "wait")
            if (!isWaitingForApproval) return null
            return (
              <Link href="/inbox">
                <Button size="sm" variant="ghost" className="h-6 gap-1.5 text-[10px]">
                  <Sparkles className="h-3 w-3" />
                  Resolve in Inbox
                </Button>
              </Link>
            )
          })()}
        </div>
      </div>
    </div>
  )
}

function StepRow({
  index,
  stepID,
  output,
  stepStatus = "done",
  stepType,
  agentSlug,
  waitPrompt,
}: {
  index: number
  stepID: string
  output?: unknown
  stepStatus?: "done" | "current" | "pending"
  stepType?: string
  agentSlug?: string
  waitPrompt?: string
}) {
  const [open, setOpen] = useState(false)
  const hasOutput = stepStatus === "done" && output != null && output !== ""
  const isCurrent = stepStatus === "current"
  const isPending = stepStatus === "pending"

  return (
    <li>
      <button
        type="button"
        onClick={() => {
          // aria-disabled vs disabled — same reason as the inbox
          // step row: keyboard tab order should still include
          // pending / no-output steps so a screen-reader user can
          // discover them. The early-return guards the click
          // semantically without removing focusability.
          if (!hasOutput) return
          setOpen((v) => !v)
        }}
        aria-disabled={!hasOutput}
        aria-expanded={hasOutput ? open : undefined}
        className={cn(
          "flex w-full items-center gap-2 rounded px-2 py-1 text-left transition-colors",
          hasOutput ? "hover:bg-white/[0.04]" : "cursor-default",
          isCurrent && "bg-amber-500/5",
          isPending && "opacity-50",
        )}
      >
        {stepStatus === "done" && (
          <span className="flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-emerald-500/20 text-emerald-400">
            <Check className="h-2.5 w-2.5" />
          </span>
        )}
        {isCurrent && (
          <span className="flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-amber-500/30 text-amber-400">
            <PauseCircle className="h-2.5 w-2.5 animate-pulse" />
          </span>
        )}
        {isPending && (
          <span className="h-4 w-4 shrink-0 rounded-full border border-muted-foreground/30" />
        )}
        <span className="font-mono text-[10px] text-muted-foreground/60">{index}.</span>
        <span className="font-mono text-xs">{stepID}</span>
        {stepType && (
          <span className="rounded bg-white/[0.06] px-1 py-0 font-mono text-[9px] text-muted-foreground">
            {stepType}
          </span>
        )}
        {agentSlug && (
          <span className="text-[10px] text-muted-foreground">— {agentSlug}</span>
        )}
        {waitPrompt && (
          <span className="truncate text-[10px] text-amber-200/80">— {waitPrompt}</span>
        )}
        {hasOutput && (
          <span className="ml-auto text-muted-foreground/40">
            {open ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
          </span>
        )}
      </button>
      {open && hasOutput && (
        <pre className="ml-6 mt-1 overflow-auto rounded bg-card/40 p-2 font-mono text-[10px] text-foreground/80">
          {typeof output === "string" ? output : JSON.stringify(output, null, 2)}
        </pre>
      )}
    </li>
  )
}

function EmptyState({ filter }: { filter: StatusFilter }) {
  const messages: Record<StatusFilter, string> = {
    active: "No routines running. Trigger one from /routines or run an issue with a bound routine.",
    all: "No runs in the workspace yet.",
    completed: "No completed runs yet.",
    failed: "No failed runs — workspace is clean.",
  }
  return (
    <div className="flex flex-col items-center justify-center gap-3 p-12 text-center">
      <ScrollText className="h-8 w-8 text-muted-foreground/30" />
      <div className="text-sm">{filter === "failed" ? "All green" : "Nothing here"}</div>
      <p className="max-w-md text-xs text-muted-foreground">{messages[filter]}</p>
    </div>
  )
}

