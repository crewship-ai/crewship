"use client"

import { formatRoutineTime } from "@/lib/routine-time"

import { describeStep } from "@/lib/routine-step-describe"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useIsMobile } from "@/hooks/use-mobile"
import { RoutineRunSteps } from "./routine-run-steps"
import { Segmented } from "@/components/ui/segmented"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { toast } from "@/lib/toast"
import { RoutineRunInputsDialog } from "./routine-run-inputs-dialog"
import { routineInputSpecs, type RoutineInputSpec } from "@/lib/routine-inputs"
import { apiFetch } from "@/lib/api-fetch"
import { RoutineStartIntent } from "@/lib/routine-start-intent"
import { useAbilities } from "@/hooks/use-abilities"
import { useWorkspaceAgentDirectory } from "@/hooks/use-workspace-agent-directory"
import { canKillRoutine, roleAtLeast, routinePermissions } from "@/lib/routine-governance"
import { runTraceMarkdown, timelineBars } from "@/lib/routine-page"
import { usePipelineRunRecords } from "@/hooks/use-pipeline-run-records"
import { SubBarActions, SubBarIconButton, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { RoutinePreviousRuns, RoutineRunPeek } from "./routine-run-peek"
import { RoutineRunTimeline } from "./routine-run-timeline"
import { useTrace } from "@/hooks/use-trace"
import { usePendingApproval } from "@/hooks/use-pending-approval"
import { RunActivityTimeline } from "@/components/features/activity/run-activity-timeline"
import { RunEvidencePanel } from "@/components/features/activity/run-evidence-panel"
import { declaredCredentialTypes, readableOriginSource, routineRunOrigin } from "@/lib/routine-run-provenance"
import { RoutineRunArtifacts } from "./routine-run-artifacts"
import { RoutineExecutionHistory } from "./routine-execution-history"
import { RoutineExecutionInsights } from "./routine-execution-insights"
import { useRunExecutions } from "@/hooks/use-run-executions"
import { RoutineApprovalBanner } from "./routine-approval-banner"
import { Button } from "@/components/ui/button"
import { formatDurationMs } from "@/lib/activity-stream"

import { RoutineResultContent } from "./routine-result-content"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import {
  FileText,
  Activity,
  Square,
  Clock,
  CheckCircle2,
  AlertCircle,
  History,
  Lightbulb,
  XCircle,
  Copy,
  RotateCcw,
  X,
} from "lucide-react"
import { DetailCard } from "@/components/ui/detail"
import { StatusPill } from "@/components/ui/status-pill"
import { toStatusTone } from "@/lib/status-tone"
import { RoutineIdentityHeader } from "./routine-identity-header"
import { routineViewHref } from "./routine-navigation"
import {
  routineRunBanner,
  routineRunPresentation,
  routineResultLabel,
  routineStoppingPointLabel,
} from "@/lib/routine-run-presentation"
import type { RoutineDetail } from "./routines-detail-panel"
import type { RoutineDraftSummary } from "./routines-workspace"

// `draft` is the contract's additive detail field; WP-B types it on
// RoutineDetail. Read here through a local extension so the page behaves
// correctly whether or not the field has landed.
type RoutineDetailWithDraft = RoutineDetail & { draft?: RoutineDraftSummary | null }

import { RoutineSavedInputs, readableFieldName } from "./routine-saved-inputs"
import { REPORTED_USAGE_HINT, formatReportedUsage } from "./routine-cost-format"


/** Shared run surface: every entry point loads the run directly, including old runs. */
interface RoutineRunDetailProps {
  workspaceId: string
  runId: string
  /** The top bar's action slot (Routines); the actions sit in the header without one (Activity). */
  /** Put the actions in the page's SubBar (SubBarActions); in the header without one. */
  actionsInBar?: boolean
  /** Back to the routine. */
  onClose?: () => void
  /** Opens another run of the same routine (from the peek). */
  onOpenRun?: (runId: string) => void
}

export function RoutineRunDetail({ workspaceId, runId, actionsInBar, onClose, onOpenRun }: RoutineRunDetailProps) {
  const { run, dsl, loading, error, refresh } = useTrace(workspaceId, runId)
  const { records: siblings } = usePipelineRunRecords(run?.pipeline_slug ? workspaceId : null, run?.pipeline_slug ?? null)
  const [peek, setPeek] = useState<string | null>(null)
  const [stepView, setStepView] = useState<"list" | "time">("list")
  const [routine, setRoutine] = useState<RoutineDetailWithDraft | null>(null)
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
  const bars = useMemoBars(executions.rows)
  const { role, capabilities } = useAbilities()
  const canRun = routinePermissions(role, capabilities).run
  // Stopping a run is POST …/runs/{id}/cancel, Owner and Admin on the server.
  const canStop = canKillRoutine(role)
  // "Ask the lead to fix it" opens the crew lead's chat with the run pinned
  // in the prompt (the chat page reads ?prompt= and sends it once). The
  // directory rows carry crew_id and agent_role beyond the typed identity.
  // Only looked up once the run is here: a run that cannot be loaded must
  // not fan out to other endpoints (run-drill-down-lookup-error).
  const directory = useWorkspaceAgentDirectory(run ? workspaceId : undefined)
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
  const [selectedHash, setSelectedHash] = useState<string | null>(null)
  const [previewVersion, setPreviewVersion] = useState<number | null>(null)
  const [runDefinition, setRunDefinition] = useState<Record<string, unknown> | null>(null)
  const [inputSpecs, setInputSpecs] = useState<RoutineInputSpec[] | null>(null)
  const [stopping, setStopping] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const [confirmStop, setConfirmStop] = useState(false)
  // KPI "Produced": the files the run left, as the results list reports them.
  const [producedState, setProduced] = useState<{ runId: string; count: number; more: boolean } | null>(null)
  const produced = producedState?.runId === runId ? producedState : null
  const reportProduced = useCallback((count: number, more: boolean) => setProduced({ runId, count, more }), [runId])
  const phone = useIsMobile()
  // One disclosure holds every piece of evidence a finished run leaves behind;
  // both lazy lists inside it mount on the first open.
  const [showTechnical, setShowTechnical] = useState(false)
  useEffect(() => {
    setConfirmStop(false)
    setShowTechnical(false)
  }, [runId])
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
  /** Resolves true once the server accepted the cancel. */
  const stop = async (): Promise<boolean> => {
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
      return true
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
      return false
    } finally {
      setStopping(false)
    }
  }
  const startAgain = async (inputs: Record<string, unknown>) => {
    if (!selectedHash) {
      setActionError("The selected recipe could not be verified. Reload it before running.")
      return
    }
    const url = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(run.pipeline_slug)}/run`
    const body = {
      inputs,
      expected_definition_hash: selectedHash,
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
      if (res.status === 409) throw new Error("The recipe changed since you opened this form. Reload the recipe before running.")
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
      let routine = await res.json()
      if (request !== preparation.current) return
      if (typeof routine.definition_hash !== "string" || !/^[0-9a-fA-F]{64}$/.test(routine.definition_hash))
        throw new Error("The selected recipe has no verifiable definition hash.")
      if (version === "current") {
        if (!Number.isInteger(routine.head_version) || routine.head_version <= 0)
          throw new Error("The current recipe version is unavailable. Reload before running.")
        const versionRes = await apiFetch(
          `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(run.pipeline_slug)}/versions/${routine.head_version}`,
        )
        if (!versionRes.ok) throw new Error("The current recipe changed while loading. Reload before running.")
        const archived = await versionRes.json()
        if (request !== preparation.current) return
        if (archived.definition_hash !== routine.definition_hash)
          throw new Error("The current recipe changed while loading. Reload before running.")
        routine = { ...routine, definition: archived.definition }
      }
      const specs = routineInputSpecs(routine.definition)
      setRunDefinition(routine.definition ?? null)
      setSelectedVersion(version)
      setSelectedHash(routine.definition_hash)
      setPreviewVersion(version === "current" && Number.isInteger(routine.head_version) ? routine.head_version : null)
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
  const presentation = routineRunPresentation(run)
  const currentStep = dsl?.steps?.find((s) => s.id === run.current_step_id)
  const active = ["queued", "running", "waiting", "paused"].includes(run.status)
  // A run that ended badly on its own merits (failed, interrupted, result
  // failed); a stopped run is not offered a fix because nothing broke.
  const failed = !active && presentation.tone === "destructive"
  const declaredResult = run.output ? routineResultLabel(run.output, dsl) : null
  const waitKind = active ? (approval.waitpoint ? "approval" : currentStep?.wait?.kind) : undefined
  const waitStep = approval.waitpoint
    ? dsl?.steps?.find((s) => s.id === approval.waitpoint?.step_id)
    : currentStep
  const banner = routineRunBanner({
    run,
    steps: dsl?.steps,
    waitKind,
    waiting: approval.waitpoint
      ? {
          who:
            (waitStep?.wait as { approval_title?: string } | undefined)?.approval_title
              ?.split(/\r?\n/)[0]
              ?.trim() || undefined,
          why: approval.waitpoint.prompt?.split(/\r?\n/)[0]?.trim() || undefined,
          expiresAt: approval.waitpoint.timeout_at,
        }
      : undefined,
    resultLabel: declaredResult,
  })
  // Without the server's failure projection there is no plain-language
  // reason, so the raw message stays in the banner (today's text); with it,
  // the raw message lives only in Technical details.
  const rawErrorInBanner = !run.failure?.summary && !!run.error_message && !active
  const failedStepName =
    run.failed_at_step && dsl?.steps?.some((step) => step.id === run.failed_at_step)
      ? describeStep(
          dsl.steps.find((step) => step.id === run.failed_at_step),
          1,
        ).title
      : null
  const publishedNow =
    run.pipeline_version != null &&
    routine?.slug === run.pipeline_slug &&
    routine.head_version != null &&
    routine.head_version > run.pipeline_version
      ? routine.head_version
      : null
  const lead = (() => {
    const rows = (directory.agents ?? []) as unknown as Array<{
      id: string
      slug: string
      crew_id?: string
      agent_role?: string
    }>
    const crewId = routine?.author_crew_id || run.invoking_crew_id
    const inCrew = crewId ? rows.filter((a) => a.crew_id === crewId) : []
    return (
      inCrew.find((a) => a.agent_role === "LEAD") ??
      rows.find((a) => a.agent_role === "LEAD") ??
      inCrew[0] ??
      null
    )
  })()
  const fixPrompt = `Run ${run.id} could not finish. Retrieve the run diagnostics using your authorized tools, tell me what to change in the routine, and save the change as a draft with save_routine_draft — do not publish.`
  const askLeadHref = lead
    ? `/chat/${encodeURIComponent(lead.slug)}?prompt=${encodeURIComponent(fixPrompt)}`
    : "/chat"
  const askLead = () => {
    if (lead) return
    // No lead to hand the prompt to: the reader picks the agent in chat and
    // pastes what we copied.
    void navigator.clipboard?.writeText(fixPrompt).then(
      () => toast.success("Copied the run reference — paste it to the agent you choose."),
      () => toast.error("Could not copy. Open the chat and describe the run by its id."),
    )
  }
  const identity =
    routine?.slug === run.pipeline_slug
      ? routine
      : {
          slug: run.pipeline_slug,
          name: run.pipeline_name || run.pipeline_slug,
        }
  const activityHref = `/activity?${new URLSearchParams({ pipeline: run.pipeline_slug, run: runId })}`
  const origin = routineRunOrigin(run)
  const credentialTypes = declaredCredentialTypes(dsl)
  const StatusIcon =
    banner.tone === "destructive"
      ? XCircle
      : banner.tone === "warn"
        ? AlertCircle
        : active
          ? Clock
          : banner.tone === "success"
            ? CheckCircle2
            : Square
  const triggerLabel = origin.label === "automation" ? "Automation" :
    (
      {
        manual: "Manual start",
        schedule: "Scheduled start",
        webhook: "Webhook",
        event: "Event",
        issue: "Issue",
      } as Record<string, string>
    )[origin.label] || readableFieldName(origin.label || "Unknown trigger")
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
        <div className="max-h-[65dvh] overflow-auto break-words">
          <RoutineResultContent output={run.output} />
        </div>
      )}
    </DetailCard>
  )
  const copyForAI = () => {
    const md = runTraceMarkdown(
      { ...run, pipeline_version: run.pipeline_version ?? null, duration_ms: run.duration_ms ?? null, cost_usd: run.cost_usd ?? null },
      (dsl?.steps ?? []).map((s) => ({ id: s.id, name: describeStep(s, 1).title, type: s.type })),
      (executions.rows ?? []).map((r) => ({ step_id: r.step_id, status: r.status, started_at: r.started_at, ended_at: r.ended_at, attempt: r.attempt, error: r.error })),
    )
    void navigator.clipboard?.writeText(md).then(
      () => toast.success("Run trace copied as Markdown", { description: "Inputs that look like secrets are left out." }),
      () => toast.error("Could not copy the trace"),
    )
  }
  const runActions = (
    <>
      <SubBarSecondary icon={Copy} onClick={copyForAI} title="Copy this run as Markdown for an assistant">
        Copy for AI
      </SubBarSecondary>
      {canStop && active && (
        <SubBarSecondary icon={Square} disabled={stopping} onClick={() => setConfirmStop(true)}>
          {stopping ? "Stopping…" : "Stop"}
        </SubBarSecondary>
      )}
      {canRun && !active && (
        <SubBarPrimary icon={RotateCcw} disabled={starting} onClick={() => void prepareAgain()}>
          {starting ? "Preparing…" : "Run again"}
        </SubBarPrimary>
      )}
      {failed && roleAtLeast(role, "MEMBER") && (
        <SubBarSecondary asChild>
          <Link href={askLeadHref} onClick={askLead}>
            Ask the lead to fix it
          </Link>
        </SubBarSecondary>
      )}
      {onClose && <SubBarIconButton icon={X} onClick={onClose} aria-label="Back to routine" title="Back to routine" />}
    </>
  )
  const doneSteps = executions.rows ? new Set(executions.rows.filter((r) => r.status === "completed" || r.status === "succeeded").map((r) => r.step_id)).size : null
  const totalSteps = dsl?.steps?.length ?? null
  const cap = typeof (dsl as { max_cost_usd?: unknown } | null)?.max_cost_usd === "number" ? (dsl as { max_cost_usd: number }).max_cost_usd : null
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
      {actionsInBar && <SubBarActions>{runActions}</SubBarActions>}
      <RoutineIdentityHeader
        routine={identity}
        workspaceId={workspaceId}
        onChanged={() => setIdentityRevision((v) => v + 1)}
        actions={actionsInBar ? undefined : runActions}
      >
        <StatusPill size="md" tone={toStatusTone(presentation.tone)} label={presentation.label} />
        {/* The facts of this run as one sentence: what used to be a four-cell
          stat strip below a second row of tabs. */}
        <span className="text-xs text-muted-foreground" data-testid="run-facts">
          started <span className="font-mono">{formatRoutineTime(run.started_at)}</span>
          {" · "}
          {triggerLabel}
          {!active && run.duration_ms != null && (
            <>
              {" · "}took <span className="font-mono">{formatDurationMs(run.duration_ms)}</span>
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
          {publishedNow && (
            <span className="text-muted-foreground-soft"> (v{publishedNow} is published now)</span>
          )}
        </span>
      </RoutineIdentityHeader>

      {/* One banner per state, in the reader's words: who needs to decide,
        where the work is, what stopped and what is kept. The raw error stays
        in Technical details. */}
      <div
        data-testid="run-banner"
        data-tone={banner.tone}
        className={`flex flex-col gap-2 rounded-xl border px-4 py-3 ${
          banner.tone === "destructive"
            ? "border-destructive/30 bg-destructive/10"
            : banner.tone === "warn"
              ? "border-warn/30 bg-warn/10"
              : banner.tone === "success"
                ? "border-success/30 bg-success/10"
                : banner.tone === "blue"
                  ? "border-primary/30 bg-primary/10"
                  : "border-border/60 bg-card"
        }`}
      >
        <div className="flex flex-wrap items-start gap-3">
          <span className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-xl bg-card">
            <StatusIcon
              className={`h-4 w-4 ${banner.tone === "destructive" ? "text-destructive" : banner.tone === "warn" ? "text-warn" : banner.tone === "blue" ? "text-primary" : banner.tone === "success" ? "text-success" : "text-muted-foreground"}`}
            />
          </span>
          <div className="min-w-0 flex-1 basis-[calc(100%-44px)] md:basis-auto">
            <h2 className="text-sm font-medium">{banner.title}</h2>
            <p className="mt-1 max-w-[85ch] text-xs leading-relaxed text-muted-foreground">
              {banner.detail}
            </p>
            {banner.kept !== undefined && (
              <p className="mt-1.5 max-w-[85ch] text-xs leading-relaxed">
                <span className="font-medium">Kept:</span> {banner.kept}.{" "}
                <span className="font-medium">Not done:</span> {banner.notDone}.
              </p>
            )}
            {rawErrorInBanner && (
              <div className="mt-2 rounded-lg border border-destructive/15 bg-card/60 px-3 py-2 text-xs">
                {run.failed_at_step && (
                  <p className="font-medium text-destructive">
                    {routineStoppingPointLabel(run)}: {failedStepName ?? "Name unavailable"}
                    <span className="ml-2 font-mono text-[10px] font-normal text-muted-foreground">
                      {run.failed_at_step}
                    </span>
                  </p>
                )}
                <p className="mt-1 whitespace-pre-wrap break-words" role="alert">
                  {run.error_message}
                </p>
              </div>
            )}
          </div>
          <Link
            className="inline-flex shrink-0 items-center gap-1.5 text-xs text-primary hover:underline"
            href={activityHref}
          >
            <Activity className="h-3.5 w-3.5" />
            Open in Activity ↗
          </Link>
        </div>
        {run.issue_identifier && (
          <Link
            className="text-xs text-primary"
            href={`/issues?issue=${encodeURIComponent(run.issue_identifier)}`}
          >
            Issue · {run.issue_identifier} ↗
          </Link>
        )}
      </div>
      <dl className="grid grid-cols-2 gap-2 sm:grid-cols-4" data-testid="run-kpis">
        <Kpi label="Duration" value={!active && run.duration_ms != null ? formatDurationMs(run.duration_ms) : active ? "running" : "—"} />
        <Kpi label="Cost" value={formatReportedUsage(run.cost_usd)} note={cap != null ? `of $${cap.toFixed(2)} cap` : undefined} />
        <Kpi label="Steps" value={doneSteps != null && totalSteps ? `${doneSteps} / ${totalSteps}` : totalSteps ? `— / ${totalSteps}` : "—"} />
        <Kpi label="Produced" value={produced ? `${produced.count}${produced.more ? "+" : ""}` : "—"} note={produced && produced.count > 0 ? (produced.count === 1 && !produced.more ? "file" : "files") : undefined} />
      </dl>
      {siblings.length > 1 && (
        <RoutinePreviousRuns runs={siblings} current={runId} onPeek={setPeek} />
      )}
      {failed && (
        <DetailCard title="What to do next" icon={Lightbulb} data-testid="run-next-step">
          <ul className="space-y-1.5 text-xs leading-relaxed">
            <li>
              <span className="font-medium">If the input was wrong</span>: fix or replace it, then{" "}
              {canRun ? (
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
              )}{" "}
              — a new run, it does not resume this one.
            </li>
            <li>
              <span className="font-medium">If a rule is too strict</span>:{" "}
              {roleAtLeast(role, "MEMBER") ? (
                <Link className="text-primary hover:underline" href={askLeadHref} onClick={askLead}>
                  ask the lead to change it
                </Link>
              ) : (
                "ask the lead to change it"
              )}
              , or edit it with the CLI (
              <code className="font-mono text-[11px]">
                crewship routine draft get {run.pipeline_slug}
              </code>
              ). Either way you get a draft to publish.
            </li>
            <li>
              <span className="font-medium">Keeps failing?</span> Compare with the earlier runs in{" "}
              <Link className="text-primary hover:underline" href={routineViewHref(run.pipeline_slug, "history")}>
                History
              </Link>
              .
            </li>
          </ul>
        </DetailCard>
      )}
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

      <ConfirmDialog
        open={confirmStop}
        onOpenChange={setConfirmStop}
        title="Stop this run?"
        description="Pending work is asked to stop. What already happened — a ledger write, a message — is not undone. Actions that already happened are not rolled back; recorded results stay in History."
        cancelLabel="Keep running"
        confirmLabel="Stop this run"
        destructive
        onConfirm={async () => {
          // A refused cancel keeps the question open, as before: the error
          // is shown and the person can try again or keep the run going.
          if (!(await stop())) throw new Error("cancel refused")
        }}
      />
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
        initialInputs={run.inputs}
        routineName={run.pipeline_name || run.pipeline_slug}
        headVersion={previewVersion}
        draft={routine?.slug === run.pipeline_slug ? routine.draft : undefined}
        submitting={starting}
        error={actionError}
        onReload={() => void prepareAgain(selectedVersion)}
        onCancel={() => {
          preparation.current += 1
          setInputSpecs(null)
          setSelectedHash(null)
          setStarting(false)
        }}
        onRun={startAgain}
      />
      {actionError && !inputSpecs && (
        <p role="alert" className="text-sm text-destructive">{actionError}</p>
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
      <div id="routine-recorded-evidence" className="scroll-mt-4" tabIndex={-1} />
      {resultPanel}
      <RoutineRunArtifacts
        key={`artifacts-${runId}`}
        workspaceId={workspaceId}
        runId={runId}
        active={active}
        compact
        noFinalResult={!run.output && run.step_outputs_available !== false}
        onCount={reportProduced}
      />

      <RoutineExecutionInsights
        rows={executions.error ? null : executions.rows}
        partial={executions.truncated}
        definition={dsl}
      />
      {dsl && (executions.rows?.length ?? 0) > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          <Segmented<"list" | "time">
            ariaLabel="Show steps as"
            value={stepView}
            onChange={setStepView}
            options={[
              { value: "list", label: "Steps" },
              { value: "time", label: "Timeline" },
            ]}
          />
          {stepView === "time" && <span className="text-xs text-muted-foreground">Bars show when each step ran, on this run's clock.</span>}
        </div>
      )}
      {dsl && stepView === "time" && (executions.rows?.length ?? 0) > 0 ? (
        <RoutineRunTimeline bars={bars} nameOf={(id) => { const s = dsl.steps?.find((x) => x.id === id); return s ? describeStep(s, 1).title : id }} />
      ) : dsl?.steps?.length ? (
        // RT-RUN: the steps with an inspector (Output · Input · Log · Details).
        <RoutineRunSteps
          key={`steps-${runId}`}
          workspaceId={workspaceId}
          runId={runId}
          steps={dsl.steps}
          executions={executions.byStep}
          outputs={run.step_outputs_available === false ? null : run.step_outputs}
          runInputs={run.inputs}
          currentStepId={run.current_step_id}
          failedStepId={run.failed_at_step}
          limits={{ costCap: cap, concurrency: concurrencyLine(dsl) }}
          phone={phone}
        />
      ) : dsl ? (
        <p className="rounded-xl border border-border/60 bg-card p-4 text-sm text-muted-foreground">This recipe has no steps.</p>
      ) : (
        <p className="rounded-xl border border-border/60 bg-card p-4 text-sm text-muted-foreground">
          The historical recipe is unavailable. Recorded outputs and activity remain
          accessible.
        </p>
      )}

      {executions.error && (
        <p role="alert" className="text-xs text-destructive">
          The step records could not be loaded, so states and times are missing.{" "}
          <button type="button" className="underline" onClick={executions.refresh}>
            Retry
          </button>
        </p>
      )}
      {executions.truncated && (
        <p className="text-xs text-muted-foreground">
          Only the first recorded executions are shown.{" "}
          <button type="button" className="text-primary underline" onClick={executions.loadMore}>
            Load more
          </button>
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
      {/* Provenance and the shareable evidence answer "can I trust and pass
        on this run?" — audit questions that come after "what came out", so
        they follow the result instead of pushing it below the fold. */}
      <DetailCard title="Run provenance" icon={History}>
        <dl className="grid gap-2 text-xs sm:grid-cols-2">
          <div><dt className="text-muted-foreground">Started by</dt><dd title={origin.source}>{triggerLabel}{readableOriginSource(origin.source) ? ` · ${readableOriginSource(origin.source)}` : ""}</dd></div>
          <div><dt className="text-muted-foreground">Executed recipe</dt><dd>{run.pipeline_version != null ? `v${run.pipeline_version}` : "Version unavailable"}{run.definition_hash ? <span className="ml-1 font-mono" title={run.definition_hash}>· {run.definition_hash.slice(0, 12)}</span> : null}</dd></div>
          <div><dt className="text-muted-foreground">Human initiator</dt><dd>Not independently verified in this run record</dd></div>
          <div><dt className="text-muted-foreground">Credentials declared in executed recipe</dt><dd>{dsl ? credentialTypes.length ? credentialTypes.join(", ") : "None declared" : "Historical recipe unavailable"}</dd></div>
        </dl>
        <p className="mt-2 text-xs text-muted-foreground">Credential use in this run: not recorded. Declarations do not prove use.</p>
      </DetailCard>
      <RunEvidencePanel
        key={runId}
        workspaceId={workspaceId}
        run={{
          kind: "routine",
          runId: run.id,
          status: run.status,
          outcome: run.outcome,
          startedAt: run.started_at,
          endedAt: run.ended_at,
          stepId: run.failed_at_step || run.current_step_id,
          failureKind: run.failure?.kind,
          trigger: origin.label,
          version: run.pipeline_version,
          definitionHash: run.definition_hash,
        }}
      />
      {peek && (
        <RoutineRunPeek
          workspaceId={workspaceId}
          runs={siblings}
          runId={peek}
          onChange={setPeek}
          onClose={() => setPeek(null)}
          onOpen={(id) => {
            setPeek(null)
            if (onOpenRun) onOpenRun(id)
            else router.push(`/routines?${new URLSearchParams({ slug: run.pipeline_slug, run: id })}`)
          }}
        />
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
            <dt>Reported usage</dt>
            <dd className="font-mono" title={REPORTED_USAGE_HINT}>{formatReportedUsage(run.cost_usd)}</dd>
            <dt>Mode</dt>
            <dd>{run.mode}</dd>
            {run.failed_at_step && (
              <>
                <dt>{routineStoppingPointLabel(run)}</dt>
                <dd>
                  {failedStepName ?? "Name unavailable"}{" "}
                  <span className="font-mono">{run.failed_at_step}</span>
                </dd>
              </>
            )}
            {run.failure?.kind && (
              <>
                <dt>Failure kind</dt>
                <dd className="font-mono">{run.failure.kind}</dd>
              </>
            )}
            {run.error_message && (
              <>
                <dt>Raw error</dt>
                <dd
                  className="whitespace-pre-wrap break-words font-mono"
                  data-testid="run-raw-error"
                >
                  {run.error_message}
                </dd>
              </>
            )}
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

/** How many runs of this routine may go at once, from the executed recipe. */
function concurrencyLine(dsl: unknown): string | null {
  const d = (dsl ?? {}) as { max_concurrent?: unknown; concurrency_key?: unknown; concurrency_policy?: unknown }
  if (typeof d.max_concurrent === "number" && d.max_concurrent > 1) return `Up to ${d.max_concurrent} at a time`
  if (typeof d.concurrency_key === "string" && d.concurrency_key) return d.concurrency_policy === "skip" ? "1 at a time; a new start is skipped" : "1 at a time; a new start waits in line"
  return null
}

function useMemoBars(rows: { step_id: string; status: string; started_at: string; ended_at: string; attempt: number }[] | null) {
  return useMemo(() => timelineBars((rows ?? []).map((r) => ({ ...r, ended_at: r.ended_at || undefined }))), [rows])
}

function Kpi({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="rounded-lg border border-hairline bg-card px-3 py-2">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="font-mono text-sm tabular-nums">
        {value}
        {note && <span className="ml-1 text-xs text-muted-foreground">{note}</span>}
      </dd>
    </div>
  )
}
