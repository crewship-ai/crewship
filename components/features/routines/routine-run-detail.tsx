"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { RoutineRunInputsDialog } from "./routine-run-inputs-dialog"
import { routineInputSpecs, type RoutineInputSpec } from "@/lib/routine-inputs"
import { apiFetch } from "@/lib/api-fetch"
import { useAbilities } from "@/hooks/use-abilities"
import { canApproveRoutine, roleAtLeast } from "@/lib/routine-governance"
import { useTrace } from "@/hooks/use-trace"
import { usePendingApproval } from "@/hooks/use-pending-approval"
import { TraceCanvas } from "@/components/features/activity/trace-canvas"
import { RunActivityTimeline } from "@/components/features/activity/run-activity-timeline"
import { RoutineStepDefinition } from "./routine-step-definition"
import { mapSubSpans } from "@/lib/trace/sub-spans"
import { RoutineRunArtifacts } from "./routine-run-artifacts"
import { RoutineExecutionHistory } from "./routine-execution-history"
import { RoutineApprovalBanner } from "./routine-approval-banner"
import { Button } from "@/components/ui/button"
import { formatDurationMs } from "@/lib/activity-stream"

import { DetailCard, Pill } from "@/components/ui/detail"
import { RoutineIdentityHeader } from "./routine-identity-header"
import { RoutineNavigation, RoutineSubNavigation, routineViewHref } from "./routine-navigation"
import { routineRunPresentation, routineResultLabel } from "@/lib/routine-run-presentation"
import type { RoutineDetail } from "./routines-detail-panel"

const EMPTY = new Map()

/** Shared run surface: every entry point loads the run directly, including old runs. */
export function RoutineRunDetail({ workspaceId, runId }: { workspaceId: string; runId: string }) {
  const { run, dsl, loading, error, refresh } = useTrace(workspaceId, runId)
  const [routine, setRoutine] = useState<RoutineDetail | null>(null)
  const [identityRevision, setIdentityRevision] = useState(0)
  useEffect(() => {
    setRoutine(null)
    if (!run?.pipeline_slug) return
    const controller = new AbortController()
    void apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(run.pipeline_slug)}`, { signal: controller.signal })
      .then(async res => { if (res.ok) { const data = await res.json(); if (!controller.signal.aborted) setRoutine(data) } })
      .catch(() => { /* Deleted/unavailable current recipe does not hide historical work. */ })
    return () => controller.abort()
  }, [workspaceId, run?.pipeline_slug, identityRevision])
  const approval = usePendingApproval(workspaceId, runId)
  const [selectedStep, setSelectedStep] = useState<string | null>(null)
  const { role } = useAbilities()
  const router = useRouter()
  const preparation = useRef(0)
  useEffect(() => () => { preparation.current += 1 }, [workspaceId, runId])
  const [starting, setStarting] = useState(false)
  const [selectedVersion, setSelectedVersion] = useState("current")
  const [inputSpecs, setInputSpecs] = useState<RoutineInputSpec[] | null>(null)
  const [stopping, setStopping] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const [tab, setTab] = useState("progress")
  const tokens = useMemo(() => approval.waitpoint
    ? new Map([[approval.waitpoint.step_id, approval.waitpoint.token]]) : EMPTY, [approval.waitpoint])
  if (!run && error === "run: 404") return <div className="p-6"><p className="mb-4 text-sm text-muted-foreground">No routine execution record is available for this activity. Recorded events are shown below.</p><RunActivityTimeline workspaceId={workspaceId} params={{ run_id: runId }} title="Run activity" card hideWhenEmpty={false} /></div>
  if (!run) return <div className="p-6 text-sm" role="status">{error ? <>Could not load this run. <Button variant="outline" onClick={refresh}>Try again</Button></> : loading ? "Loading run…" : "Run unavailable."}</div>
  const stop = async () => {
    setStopping(true); setActionError(null)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/runs/${encodeURIComponent(runId)}/cancel`, { method: "POST" })
      if (!res.ok) throw new Error("Could not stop this run. It may already have finished.")
      await refresh(); await approval.refresh()
    } catch (e) { setActionError(e instanceof Error ? e.message : String(e)) }
    finally { setStopping(false) }
  }
  const startAgain = async (inputs: Record<string, unknown>) => {
    setStarting(true); setActionError(null)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(run.pipeline_slug)}/run`, { method: "POST", headers: { "Content-Type": "application/json", Prefer: "respond-async" }, body: JSON.stringify({ inputs, ...(selectedVersion !== "current" ? { pinned_version: Number(selectedVersion) } : {}) }) })
      const data = await res.json()
      if (!res.ok || !data.run_id) throw new Error(data.error || data.detail || "Could not start a new run.")
      setInputSpecs(null)
      router.push(`/routines?slug=${encodeURIComponent(run.pipeline_slug)}&run=${encodeURIComponent(data.run_id)}`)
    } catch (e) { setActionError(e instanceof Error ? e.message : String(e)) }
    finally { setStarting(false) }
  }
  const prepareAgain = async (version = "current") => {
    const request = ++preparation.current
    setStarting(true); setActionError(null)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(run.pipeline_slug)}${version === "current" ? "" : `/versions/${encodeURIComponent(version)}`}`)
      if (!res.ok) throw new Error("The selected recipe version is unavailable.")
      const routine = await res.json()
      if (request !== preparation.current) return
      const specs = routineInputSpecs(routine.definition).map(spec => Object.hasOwn(run.inputs ?? {}, spec.name) ? { ...spec, default: run.inputs![spec.name] } : spec)
      setSelectedVersion(version)
      setInputSpecs(specs)
    } catch (e) { if (request === preparation.current) setActionError(e instanceof Error ? e.message : String(e)) }
    finally { if (request === preparation.current) setStarting(false) }
  }
  const resultSummary = run.output?.includes("---HANDOFF---") ? /^summary:\s*(.+)$/m.exec(run.output.slice(run.output.lastIndexOf("---HANDOFF---")))?.[1]?.trim() : undefined
  const outputs = Object.entries(run.step_outputs ?? {})
  const step = dsl?.steps?.find(s => s.id === selectedStep)
  const presentation = routineRunPresentation(run)
  const declaredResult = run.output ? routineResultLabel(run.output, dsl) : null
  const identity = routine?.slug === run.pipeline_slug ? routine : { slug: run.pipeline_slug, name: run.pipeline_name || run.pipeline_slug }
  const activityHref = `/activity?${new URLSearchParams({ pipeline: run.pipeline_slug, run: runId })}`
  return <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4">
    <RoutineIdentityHeader routine={identity} workspaceId={workspaceId} onChanged={() => setIdentityRevision(v => v + 1)} />
    <RoutineNavigation slug={run.pipeline_slug} view="history" />
    <header className="space-y-2">
      <Link className="text-xs text-muted-foreground hover:text-foreground" href={routineViewHref(run.pipeline_slug, "history")}>← All runs</Link>
      <div className="flex flex-wrap items-center justify-between gap-3"><div className="flex flex-wrap items-center gap-3"><h2 className="text-lg font-semibold">Run · {new Date(run.started_at).toLocaleString()}</h2><Pill tone={presentation.tone}>{presentation.label}</Pill></div><Link className="text-xs text-primary hover:underline" href={activityHref}>Open in Activity ↗</Link></div>
      <p className="text-xs text-muted-foreground">{run.triggered_via} · {formatDurationMs(run.duration_ms)}{run.pipeline_version != null && <> · Executed recipe <Link className="text-primary" href={`${routineViewHref(run.pipeline_slug, "versions")}&version=${run.pipeline_version}`}>v{run.pipeline_version}</Link></>}</p>
      {run.issue_identifier && <Link className="text-xs text-primary" href={`/issues?issue=${encodeURIComponent(run.issue_identifier)}`}>Issue · {run.issue_identifier} ↗</Link>}
    </header>
    {canApproveRoutine(role) && ["queued", "running", "waiting", "paused"].includes(run.status) && <div><Button variant="outline" disabled={stopping} onClick={stop}>{stopping ? "Stopping…" : "Stop run"}</Button></div>}
    {roleAtLeast(role, "MEMBER") && !["queued", "running", "waiting", "paused"].includes(run.status) && <div className="flex items-center gap-3"><Button variant="outline" disabled={starting} onClick={() => void prepareAgain()}>{starting ? "Starting…" : "Run again"}</Button><span className="text-xs text-muted-foreground">Choose the recipe version and review inputs before a new run.</span></div>}
    <RoutineRunInputsDialog versionChoices={[{ value: "current", label: "Current recipe" }, ...(run.pipeline_version != null ? [{ value: String(run.pipeline_version), label: `Executed recipe · v${run.pipeline_version}` }] : [])]} selectedVersion={selectedVersion} onVersionChange={v => void prepareAgain(v)} inputs={inputSpecs} routineName={run.pipeline_name || run.pipeline_slug} submitting={starting} onCancel={() => { preparation.current += 1; setInputSpecs(null); setStarting(false) }} onRun={startAgain} />
    {actionError && <p role="alert" className="text-sm text-destructive">{actionError}</p>}
    {error && <p role="alert" className="text-sm text-destructive">Updates unavailable. Showing the last loaded state. <button onClick={refresh}>Retry</button></p>}
    {run.error_message && <p role="alert" className="rounded-lg border border-destructive/40 p-4 text-sm">{run.failed_at_step && `${run.failed_at_step}: `}{run.error_message}</p>}
    {approval.waitpoint && <div className="space-y-2"><RoutineApprovalBanner waitpoint={approval.waitpoint} deciding={approval.deciding} onDecide={approval.decide} />{approval.waitpoint.inbox_item_id && <Link className="text-xs text-primary" href={`/inbox?item=${encodeURIComponent(approval.waitpoint.inbox_item_id)}`}>Open the same decision in Inbox ↗</Link>}</div>}
    {approval.error && <p role="alert" className="text-sm text-destructive">Could not load or update the decision. <button onClick={approval.refresh}>Retry</button></p>}
    {run.output && <DetailCard title="Result">{declaredResult && <p className="mb-3 text-sm font-medium">{declaredResult}</p>}{["true", "false"].includes(run.output.trim()) && !declaredResult && <p className="mb-3 text-xs text-muted-foreground">Boolean result from the recipe. A false value is not an execution failure.</p>}{resultSummary ? <><p className="whitespace-pre-wrap break-words text-sm">{resultSummary}</p><details className="mt-3 text-xs text-muted-foreground"><summary className="cursor-pointer">Full recorded response</summary><pre className="mt-2 max-h-60 overflow-auto whitespace-pre-wrap break-words">{run.output}</pre></details></> : <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words text-sm">{run.output}</pre>}</DetailCard>}
    <RoutineSubNavigation label="Run detail" items={["progress", "outputs", "activity", "inputs"]} value={tab} onChange={setTab} />
    {tab === "progress" && <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_320px]">
      {dsl ? <DetailCard title="Progress" subtitle={run.pipeline_version != null ? `Executed version ${run.pipeline_version}` : "Historical recipe"} bare><div className="h-[56vh] min-h-[380px] overflow-hidden"><TraceCanvas run={run} dsl={dsl} workspaceId={workspaceId} selectedStepId={selectedStep} onStepSelect={setSelectedStep} waitpointTokensByStepId={tokens} heatmapBuckets={EMPTY} stepMetrics={EMPTY} initialFocus="all" centerOnSelect recenterOnResize /></div></DetailCard> : <p className="rounded-xl border p-6 text-sm text-muted-foreground">The historical recipe is unavailable. Recorded outputs and activity remain accessible.</p>}
      <aside className="max-h-[calc(56vh+48px)] space-y-4 overflow-auto rounded-3xl border border-white/[0.06] bg-card p-4"><RoutineStepDefinition step={step as unknown as Record<string, unknown> | undefined} />{step && <><h3 className="text-xs font-medium">Recorded output</h3><pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words text-xs">{Object.hasOwn(run.step_outputs ?? {}, step.id) ? renderOutput(run.step_outputs![step.id]) : "No output recorded yet."}</pre><h3 className="text-xs font-medium">Agent and tool activity</h3>{mapSubSpans(run.sub_spans?.[step.id]).map((span, index) => <details key={`${span.startedAt}-${span.name}-${index}`} className="rounded border p-2 text-xs"><summary className="cursor-pointer">{span.name} · {span.status}</summary><p className="mt-2 whitespace-pre-wrap break-words">{span.detail}</p></details>)}</>}</aside>
    </div>}
    {tab === "progress" && <RoutineExecutionHistory key={runId} workspaceId={workspaceId} runId={runId} active={["running", "queued", "waiting", "paused"].includes(run.status)} />}
    {tab === "outputs" && <section className="space-y-3 rounded-3xl border border-white/[0.06] bg-card p-4"><RoutineRunArtifacts key={runId} workspaceId={workspaceId} runId={runId} active={["running", "queued", "waiting", "paused"].includes(run.status)} /><h2 className="pt-3 text-sm font-medium">Step responses</h2>{run.step_outputs_available === false ? <p role="alert">Step outputs could not be loaded. <button onClick={refresh}>Retry</button></p> : outputs.length === 0 ? <p className="text-sm text-muted-foreground">No step outputs recorded yet.</p> : outputs.map(([id, value]) => <details key={id} className="rounded-3xl border border-white/[0.06] bg-card p-4"><summary className="cursor-pointer text-sm font-medium">{id}</summary><pre className="mt-3 max-h-96 overflow-auto whitespace-pre-wrap break-words text-xs">{renderOutput(value)}</pre></details>)}</section>}
    {tab === "activity" && <RunActivityTimeline workspaceId={workspaceId} params={{ run_id: runId }} title="Run activity" card hideWhenEmpty={false} />}
    {tab === "inputs" && <section className="rounded-3xl border border-white/[0.06] bg-card p-4"><h2 className="mb-3 text-sm font-medium">Inputs used for this run</h2><p className="mb-3 text-xs text-muted-foreground">Saved at execution time. Editing the recipe does not change these values.</p><div className="mb-4 flex flex-wrap gap-3 text-xs"><Link className="text-primary" href={activityHref}>Activity ↗</Link><Link className="text-primary" href={routineViewHref(run.pipeline_slug, "settings")}>Current access and credentials ↗</Link></div><pre className="overflow-auto whitespace-pre-wrap break-words text-xs">{JSON.stringify(run.inputs ?? {}, null, 2)}</pre></section>}
    <details className="text-xs text-muted-foreground"><summary className="cursor-pointer">Technical details</summary><dl className="mt-2 space-y-1"><div>Run: {run.id}</div><div>Definition: {run.definition_hash || "unavailable"}</div><div>Cost: ${run.cost_usd.toFixed(4)}</div><div>Mode: {run.mode}</div></dl></details>
  </div>
}
function renderOutput(value: unknown): string { return typeof value === "string" ? value : JSON.stringify(value, null, 2) }
