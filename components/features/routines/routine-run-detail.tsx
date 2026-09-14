"use client"

import { formatRoutineTime } from "@/lib/routine-time"

import { describeStep } from "@/lib/routine-step-describe"
import { useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { RoutineRunInputsDialog } from "./routine-run-inputs-dialog"
import { routineInputSpecs, type RoutineInputSpec } from "@/lib/routine-inputs"
import { apiFetch } from "@/lib/api-fetch"
import { RoutineStartIntent } from "@/lib/routine-start-intent"
import { useAbilities } from "@/hooks/use-abilities"
import { canApproveRoutine, roleAtLeast } from "@/lib/routine-governance"
import { useTrace } from "@/hooks/use-trace"
import { usePendingApproval } from "@/hooks/use-pending-approval"
import { TraceCanvas } from "@/components/features/activity/trace-canvas"
import { RunActivityTimeline } from "@/components/features/activity/run-activity-timeline"
import { RoutineRunArtifacts } from "./routine-run-artifacts"
import { RoutineExecutionHistory } from "./routine-execution-history"
import { RoutineStepSpine } from "./routine-step-spine"
import { useRunExecutions } from "@/hooks/use-run-executions"
import { RoutineApprovalBanner } from "./routine-approval-banner"
import { Button } from "@/components/ui/button"
import { formatDurationMs } from "@/lib/activity-stream"

import { RoutineResultContent } from "./routine-result-content"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog"
import {
  FileText,
  Activity,
  Square,
  Clock,
  CheckCircle2,
  AlertCircle,
  History,
} from "lucide-react"
import { DetailCard, Pill } from "@/components/ui/detail"
import { RoutineIdentityHeader } from "./routine-identity-header"
import { routineViewHref } from "./routine-navigation"
import {
  routineRunPresentation,
  routineResultLabel,
  routineRunExplanation,
  routineStoppingPointLabel,
} from "@/lib/routine-run-presentation"
import type { RoutineDetail } from "./routines-detail-panel"

import { RoutineSavedInputs, readableFieldName } from "./routine-saved-inputs"

const EMPTY = new Map()

/** Shared run surface: every entry point loads the run directly, including old runs. */
interface RoutineRunDetailProps {
  workspaceId: string
  runId: string
}

export function RoutineRunDetail({ workspaceId, runId }: RoutineRunDetailProps) {
  const { run, dsl, loading, error, refresh } = useTrace(workspaceId, runId)
  const [routine, setRoutine] = useState<RoutineDetail | null>(null)
  const [identityRevision, setIdentityRevision] = useState(0)
  useEffect(() => {
    setRoutine(null)
    if (!run?.pipeline_slug) return
    const controller = new AbortController()
    void apiFetch(
      `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(run.pipeline_slug)}`,
      { signal: controller.signal },
    )
      .then(async (res) => {
        if (res.ok) {
          const data = await res.json()
          if (!controller.signal.aborted) setRoutine(data)
        }
      })
      .catch(() => {
        /* Deleted/unavailable current recipe does not hide historical work. */
      })
    return () => controller.abort()
  }, [workspaceId, run?.pipeline_slug, identityRevision])
  const approval = usePendingApproval(workspaceId, runId)
  // Fetched here, next to the run itself: a step's attempts are part of that
  // step, not a separate list at the bottom of another tab.
  const executions = useRunExecutions(
    workspaceId,
    runId,
    ["queued", "running", "waiting", "paused"].includes(run?.status ?? ""),
  )
  const [selectedStep, setSelectedStep] = useState<string | null>(null)
  const { role } = useAbilities()
  const router = useRouter()
  const preparation = useRef(0)
  const startIntent = useRef(new RoutineStartIntent())
  useEffect(
    () => () => {
      preparation.current += 1
    },
    [workspaceId, runId],
  )
  const [starting, setStarting] = useState(false)
  const [selectedVersion, setSelectedVersion] = useState("current")
  const [runDefinition, setRunDefinition] = useState<Record<string, unknown> | null>(null)
  const [inputSpecs, setInputSpecs] = useState<RoutineInputSpec[] | null>(null)
  const [stopping, setStopping] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const [confirmStop, setConfirmStop] = useState(false)
  // One disclosure holds every piece of evidence a finished run leaves behind;
  // both lazy lists inside it mount on the first open.
  const [showTechnical, setShowTechnical] = useState(false)
  useEffect(() => {
    setSelectedStep(null)
    setConfirmStop(false)
    setShowTechnical(false)
  }, [runId])
  const tokens = useMemo(
    () =>
      approval.waitpoint && !approval.waitpoint.decision_form
        ? new Map([[approval.waitpoint.step_id, approval.waitpoint.token]])
        : EMPTY,
    [approval.waitpoint],
  )
  if (!run && error === "run: 404")
    return (
      <div className="p-6">
        <p className="mb-4 text-sm text-muted-foreground">
          No routine execution record is available for this activity. Recorded events are
          shown below.
        </p>
        <RunActivityTimeline
          workspaceId={workspaceId}
          params={{ run_id: runId }}
          title="Run activity"
          card
          hideWhenEmpty={false}
        />
      </div>
    )
  if (!run)
    return (
      <div className="p-6 text-sm" role="status">
        {error ? (
          <>
            Could not load this run.{" "}
            <Button variant="outline" onClick={refresh}>
              Try again
            </Button>
          </>
        ) : loading ? (
          "Loading run…"
        ) : (
          "Run unavailable."
        )}
      </div>
    )
  const stop = async () => {
    setStopping(true)
    setActionError(null)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/runs/${encodeURIComponent(runId)}/cancel`,
        { method: "POST" },
      )
      if (!res.ok)
        throw new Error("Could not stop this run. It may already have finished.")
      setConfirmStop(false)
      await refresh()
      await approval.refresh()
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
    } finally {
      setStopping(false)
    }
  }
  const startAgain = async (inputs: Record<string, unknown>) => {
    const url = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(run.pipeline_slug)}/run`
    const body = {
      inputs,
      ...(selectedVersion !== "current"
        ? { pinned_version: Number(selectedVersion) }
        : {}),
    }
    const attempt = startIntent.current.begin(url, body)
    if (!attempt) return
    let accepted = false
    setStarting(true)
    setActionError(null)
    try {
      const res = await apiFetch(url, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Prefer: "respond-async",
          "Idempotency-Key": attempt.key,
        },
        body: JSON.stringify(body),
      })
      const data = await res.json()
      if (!res.ok || typeof data.run_id !== "string" || !data.run_id)
        throw new Error(data.error || data.detail || "Could not start a new run.")
      accepted = true
      setInputSpecs(null)
      router.push(
        `/routines?slug=${encodeURIComponent(run.pipeline_slug)}&run=${encodeURIComponent(data.run_id)}`,
      )
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
    } finally {
      attempt.finish(accepted)
      setStarting(false)
    }
  }
  const prepareAgain = async (version = "current") => {
    const request = ++preparation.current
    setStarting(true)
    setActionError(null)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(run.pipeline_slug)}${version === "current" ? "" : `/versions/${encodeURIComponent(version)}`}`,
      )
      if (!res.ok) throw new Error("The selected recipe version is unavailable.")
      const routine = await res.json()
      if (request !== preparation.current) return
      const specs = routineInputSpecs(routine.definition).map((spec) =>
        Object.hasOwn(run.inputs ?? {}, spec.name)
          ? { ...spec, default: run.inputs![spec.name] }
          : spec,
      )
      setRunDefinition(routine.definition ?? null)
      setSelectedVersion(version)
      setInputSpecs(specs)
    } catch (e) {
      if (request === preparation.current)
        setActionError(e instanceof Error ? e.message : String(e))
    } finally {
      if (request === preparation.current) setStarting(false)
    }
  }
  const resultSummary = run.output?.includes("---HANDOFF---")
    ? /^summary:\s*(.+)$/m
        .exec(run.output.slice(run.output.lastIndexOf("---HANDOFF---")))?.[1]
        ?.trim()
    : undefined
  const selectedStepId =
    dsl?.steps?.find((s) => s.id === (selectedStep || run.current_step_id))?.id ||
    dsl?.steps?.[0]?.id ||
    null
  const presentation = routineRunPresentation(run)
  const currentStep = dsl?.steps?.find((s) => s.id === run.current_step_id)
  const active = ["queued", "running", "waiting", "paused"].includes(run.status)
  // A run that ended badly on its own merits (failed, interrupted, result
  // failed); a stopped run is not offered a fix because nothing broke.
  const failed = !active && presentation.tone === "destructive"
  const explanation = routineRunExplanation(
    run,
    active ? (approval.waitpoint ? "approval" : currentStep?.wait?.kind) : undefined,
  )
  const declaredResult = run.output ? routineResultLabel(run.output, dsl) : null
  const identity =
    routine?.slug === run.pipeline_slug
      ? routine
      : {
          slug: run.pipeline_slug,
          name: run.pipeline_name || run.pipeline_slug,
        }
  const activityHref = `/activity?${new URLSearchParams({ pipeline: run.pipeline_slug, run: runId })}`
  const StatusIcon =
    presentation.tone === "destructive" || presentation.tone === "warn"
      ? AlertCircle
      : active
        ? Clock
        : presentation.tone === "success"
          ? CheckCircle2
          : Square
  const triggerLabel =
    (
      {
        manual: "Manual start",
        schedule: "Scheduled start",
        webhook: "Webhook",
        event: "Event",
        issue: "Issue",
      } as Record<string, string>
    )[run.triggered_via] || readableFieldName(run.triggered_via || "Unknown trigger")
  const resultPanel = run.output && (
    <DetailCard title="Results" icon={FileText}>
      {declaredResult && <p className="mb-3 text-sm font-medium">{declaredResult}</p>}
      {["true", "false"].includes(run.output.trim()) && !declaredResult && (
        <p className="mb-3 text-xs text-muted-foreground">
          Boolean result from the recipe. A false value is not an execution failure.
        </p>
      )}
      {resultSummary ? (
        <>
          <p className="whitespace-pre-wrap break-words text-sm">{resultSummary}</p>
          <details className="mt-3 text-xs text-muted-foreground">
            <summary className="cursor-pointer">Full recorded response</summary>
            <pre className="mt-2 max-h-96 overflow-auto whitespace-pre-wrap break-words">
              {run.output}
            </pre>
          </details>
        </>
      ) : (
        <div className="max-h-[65vh] overflow-auto break-words">
          <RoutineResultContent output={run.output} />
        </div>
      )}
    </DetailCard>
  )
  const spineRecord = {
    lookup: (stepId: string) => ({
      execution: executions.byStep.get(stepId),
      hasOutput: Object.hasOwn(run.step_outputs ?? {}, stepId),
      output: run.step_outputs?.[stepId],
    }),
    outputsAvailable: run.step_outputs_available !== false,
    subSpans: run.sub_spans as Record<string, unknown> | undefined,
    currentStepId: run.current_step_id,
    failedStepId: run.failed_at_step,
    active,
    executionsError: executions.error,
    executionsTruncated: executions.truncated,
    onRetryExecutions: executions.refresh,
    onLoadMoreExecutions: executions.loadMore,
  }
  // The graph is one view of the step list, not a place of its own, and it is
  // as tall as the recipe it draws.
  const mapHeight = Math.min(560, Math.max(300, (dsl?.steps?.length ?? 1) * 78))
  return (
    <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4">
      {/* A run reached by a deep link (Inbox banner, a shared URL) needs a way
        back to its routine and to the list; the layout's breadcrumb bar only
        renders around the routine detail. */}
      <nav
        aria-label="Run location"
        className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground"
      >
        <Link className="hover:text-foreground" href="/routines">
          Routines
        </Link>
        <span aria-hidden>›</span>
        <Link
          className="font-medium text-foreground/85 hover:text-foreground"
          href={`/routines?${new URLSearchParams({ slug: run.pipeline_slug })}`}
        >
          {run.pipeline_name || run.pipeline_slug}
        </Link>
        <span aria-hidden>›</span>
        <span>Run</span>
      </nav>
      <RoutineIdentityHeader
        routine={identity}
        workspaceId={workspaceId}
        onChanged={() => setIdentityRevision((v) => v + 1)}
        actions={
          <>
            {canApproveRoutine(role) && active && (
              <Button
                variant="outline"
                size="sm"
                disabled={stopping}
                onClick={() => setConfirmStop(true)}
              >
                <Square className="mr-1.5 h-3.5 w-3.5" />
                {stopping ? "Stopping…" : "Stop run"}
              </Button>
            )}
            {roleAtLeast(role, "MEMBER") && !active && (
              <Button
                variant="outline"
                size="sm"
                disabled={starting}
                onClick={() => void prepareAgain()}
              >
                {starting ? "Preparing…" : "Run again"}
              </Button>
            )}
          </>
        }
      >
        <Pill tone={presentation.tone}>{presentation.label}</Pill>
        {/* The facts of this run as one sentence: what used to be a four-cell
          stat strip below a second row of tabs. */}
        <span className="text-xs text-muted-foreground" data-testid="run-facts">
          started <span className="font-mono">{formatRoutineTime(run.started_at)}</span>
          {" · "}
          {triggerLabel}
          {!active && run.duration_ms != null && (
            <>
              {" · "}took{" "}
              <span className="font-mono">{formatDurationMs(run.duration_ms)}</span>
            </>
          )}
          {" · "}
          {run.pipeline_version != null ? (
            <Link
              className="text-primary hover:underline"
              href={`${routineViewHref(run.pipeline_slug, "versions")}&version=${run.pipeline_version}`}
            >
              recipe v{run.pipeline_version} ↗
            </Link>
          ) : (
            "recipe version unavailable"
          )}
        </span>
      </RoutineIdentityHeader>

      {/* The verdict, then the facts. Both used to sit inside a card called Run
        summary, above a second row of tabs — three levels of chrome before the
        answer the reader came for. */}
      <div className="flex flex-col gap-2 rounded-xl border border-border/60 bg-card px-4 py-3">
        <div className="flex flex-wrap items-start gap-3">
          <span className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-xl bg-muted">
            <StatusIcon
              className={`h-4 w-4 ${presentation.tone === "destructive" ? "text-destructive" : presentation.tone === "warn" ? "text-warn" : active ? "text-primary" : presentation.tone === "success" ? "text-success" : "text-muted-foreground"}`}
            />
          </span>
          <div className="min-w-0 flex-1">
            <h2 className="text-sm font-medium">{explanation.title}</h2>
            <p className="mt-1 max-w-[85ch] text-xs leading-relaxed text-muted-foreground">
              {explanation.detail}
            </p>
          </div>
          <Link
            className="inline-flex shrink-0 items-center gap-1.5 text-xs text-primary hover:underline"
            href={activityHref}
          >
            <Activity className="h-3.5 w-3.5" />
            Open in Activity ↗
          </Link>
        </div>
        {(run.error_message || run.failed_at_step) && (
          <div className="rounded-lg border border-destructive/15 bg-destructive/5 px-3 py-2 text-xs">
            {run.failed_at_step && (
              <p className="font-medium text-destructive">
                {routineStoppingPointLabel(run)}:{" "}
                {dsl?.steps?.some((step) => step.id === run.failed_at_step)
                  ? describeStep(
                      dsl.steps.find((step) => step.id === run.failed_at_step),
                      1,
                    ).title
                  : "Name unavailable"}
                <span className="ml-2 font-mono text-[10px] font-normal text-muted-foreground">
                  {run.failed_at_step}
                </span>
              </p>
            )}
            {run.error_message && (
              <p className="mt-1 whitespace-pre-wrap break-words" role="alert">
                {run.error_message}
              </p>
            )}
          </div>
        )}
        {failed && (
          <p className="text-xs text-muted-foreground" data-testid="run-next-step">
            What you can do: fix the step in{" "}
            {roleAtLeast(role, "MANAGER") ? (
              <Link
                className="text-primary hover:underline"
                href={`/routines?${new URLSearchParams({ slug: run.pipeline_slug, view: "edit" })}`}
              >
                Edit recipe
              </Link>
            ) : (
              "the recipe (a manager can edit it)"
            )}
            , then{" "}
            {roleAtLeast(role, "MEMBER") ? (
              <button
                type="button"
                className="text-primary hover:underline disabled:opacity-60"
                disabled={starting}
                onClick={() => void prepareAgain()}
              >
                run again
              </button>
            ) : (
              "run again"
            )}
            .
          </p>
        )}
        {run.issue_identifier && (
          <Link
            className="text-xs text-primary"
            href={`/issues?issue=${encodeURIComponent(run.issue_identifier)}`}
          >
            Issue · {run.issue_identifier} ↗
          </Link>
        )}
      </div>
      {approval.waitpoint && (
        <div className="space-y-2">
          <RoutineApprovalBanner
            waitpoint={approval.waitpoint}
            deciding={approval.deciding}
            onDecide={approval.decide}
          />
          {approval.waitpoint.inbox_item_id && (
            <Link
              className="text-xs text-primary"
              href={`/inbox?item=${encodeURIComponent(approval.waitpoint.inbox_item_id)}`}
            >
              Open the same decision in Inbox ↗
            </Link>
          )}
        </div>
      )}
      {approval.error && (
        <p role="alert" className="text-sm text-destructive">
          Could not load or update the decision.{" "}
          <button onClick={approval.refresh}>Retry</button>
        </p>
      )}

      <Dialog open={confirmStop} onOpenChange={setConfirmStop}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Stop this run?</DialogTitle>
            <DialogDescription>
              Pending and active work will be asked to stop. Recorded results remain
              available. Actions that already happened are not rolled back.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setConfirmStop(false)}
              disabled={stopping}
            >
              Keep running
            </Button>
            <Button variant="destructive" onClick={stop} disabled={stopping}>
              {stopping ? "Stopping…" : "Stop this run"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <RoutineRunInputsDialog
        definition={runDefinition}
        versionChoices={[
          { value: "current", label: "Current recipe" },
          ...(run.pipeline_version != null
            ? [
                {
                  value: String(run.pipeline_version),
                  label: `Executed recipe · v${run.pipeline_version}`,
                },
              ]
            : []),
        ]}
        selectedVersion={selectedVersion}
        onVersionChange={(v) => void prepareAgain(v)}
        inputs={inputSpecs}
        routineName={run.pipeline_name || run.pipeline_slug}
        submitting={starting}
        onCancel={() => {
          preparation.current += 1
          setInputSpecs(null)
          setStarting(false)
        }}
        onRun={startAgain}
      />
      {actionError && (
        <p role="alert" className="text-sm text-destructive">
          {actionError}
        </p>
      )}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          Updates unavailable. Showing the last loaded state.{" "}
          <button onClick={refresh}>Retry</button>
        </p>
      )}
      {run.step_outputs_available === false && (
        <p
          role="alert"
          className="rounded-xl border border-destructive/20 bg-card px-4 py-3 text-sm text-destructive"
        >
          Step outputs could not be loaded for this run, so the per-step responses below
          are missing.{" "}
          <button className="underline" onClick={refresh}>
            Retry
          </button>
        </p>
      )}
      {resultPanel}
      <RoutineRunArtifacts
        key={`artifacts-${runId}`}
        workspaceId={workspaceId}
        runId={runId}
        active={active}
        compact
        noFinalResult={!run.output && run.step_outputs_available !== false}
      />

      {dsl ? (
        <RoutineStepSpine
          workspaceId={workspaceId}
          definition={dsl}
          record={spineRecord}
          initialLimit={12}
          map={({ openStep }) => (
            <div className="overflow-hidden" style={{ height: mapHeight }}>
              <TraceCanvas
                run={run}
                dsl={dsl}
                workspaceId={workspaceId}
                selectedStepId={selectedStepId}
                onStepSelect={(id) => {
                  setSelectedStep(id)
                  if (id) openStep(id)
                }}
                waitpointTokensByStepId={tokens}
                heatmapBuckets={EMPTY}
                stepMetrics={EMPTY}
                initialFocus="all"
                centerOnSelect
                recenterOnResize
              />
            </div>
          )}
        />
      ) : (
        <p className="rounded-xl border border-border/60 bg-card p-4 text-sm text-muted-foreground">
          The historical recipe is unavailable. Recorded outputs and activity remain
          accessible.
        </p>
      )}

      <RoutineSavedInputs key={`inputs-${runId}`} values={run.inputs} definition={dsl} />

      {/* Live work is what the reader came for while a run is going, so it stays
        on the page; the event log, the attempt list and the identifiers of a
        finished run are evidence, and evidence lives behind ONE disclosure
        rather than three, under the readable result. */}
      {active && (
        <DetailCard title="Activity" icon={Activity} subtitle="live" bare>
          <RunActivityTimeline
            workspaceId={workspaceId}
            params={{ run_id: runId }}
            title="Run activity"
            card={false}
            hideWhenEmpty={false}
            forceRunning={run.status === "running" && !approval.waitpoint}
            showControls
          />
        </DetailCard>
      )}
      <details
        data-testid="run-technical-details"
        onToggle={(e) => setShowTechnical(e.currentTarget.open)}
        className="overflow-hidden rounded-xl border border-border/60 bg-card text-xs"
      >
        <summary className="flex cursor-pointer items-center gap-2 px-4 py-3 text-muted-foreground">
          <History className="h-4 w-4" />
          Technical details · activity, attempts, recorded executions
        </summary>
        <div className="space-y-4 border-t border-hairline px-4 py-3">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-muted-foreground">
            <dt>Run</dt>
            <dd className="break-all font-mono">{run.id}</dd>
            <dt>Recipe hash</dt>
            <dd className="break-all font-mono">{run.definition_hash || "unavailable"}</dd>
            <dt>Cost</dt>
            <dd className="font-mono">${run.cost_usd.toFixed(4)}</dd>
            <dt>Mode</dt>
            <dd>{run.mode}</dd>
          </dl>
          {!active && (
            <section data-testid="run-activity-section">
              <h3 className="mb-2 flex items-center gap-2 text-muted-foreground">
                <Activity className="h-4 w-4" />
                Activity · recorded events from this run
              </h3>
              {showTechnical && (
                <RunActivityTimeline
                  workspaceId={workspaceId}
                  params={{ run_id: runId }}
                  title="Run activity"
                  card={false}
                  hideWhenEmpty={false}
                  showControls
                />
              )}
            </section>
          )}
          <section data-testid="run-executions-section">
            <h3 className="mb-2 flex items-center gap-2 text-muted-foreground">
              <History className="h-4 w-4" />
              All recorded executions and attempts
            </h3>
            {showTechnical && (
              <RoutineExecutionHistory
                key={`executions-${runId}`}
                workspaceId={workspaceId}
                runId={runId}
                active={active}
              />
            )}
          </section>
        </div>
      </details>
    </div>
  )
}
