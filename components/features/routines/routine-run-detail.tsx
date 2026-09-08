"use client"

import { useMemo, useState } from "react"
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

const EMPTY = new Map()

/** Shared run surface: every entry point loads the run directly, including old runs. */
export function RoutineRunDetail({ workspaceId, runId }: { workspaceId: string; runId: string }) {
  const { run, dsl, loading, error, refresh } = useTrace(workspaceId, runId)
  const approval = usePendingApproval(workspaceId, runId)
  const [selectedStep, setSelectedStep] = useState<string | null>(null)
  const { role } = useAbilities()
  const router = useRouter()
  const [starting, setStarting] = useState(false)
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
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(run.pipeline_slug)}/run`, { method: "POST", headers: { "Content-Type": "application/json", Prefer: "respond-async" }, body: JSON.stringify({ inputs }) })
      const data = await res.json()
      if (!res.ok || !data.run_id) throw new Error(data.error || data.detail || "Could not start a new run.")
      setInputSpecs(null)
      router.push(`/routines?slug=${encodeURIComponent(run.pipeline_slug)}&run=${encodeURIComponent(data.run_id)}`)
    } catch (e) { setActionError(e instanceof Error ? e.message : String(e)) }
    finally { setStarting(false) }
  }
  const prepareAgain = async () => {
    setStarting(true); setActionError(null)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(run.pipeline_slug)}`)
      if (!res.ok) throw new Error("The current routine is unavailable.")
      const routine = await res.json()
      const specs = routineInputSpecs(routine.definition).map(spec => Object.hasOwn(run.inputs ?? {}, spec.name) ? { ...spec, default: run.inputs![spec.name] } : spec)
      if (specs.length) setInputSpecs(specs); else await startAgain({})
    } catch (e) { setActionError(e instanceof Error ? e.message : String(e)) }
    finally { setStarting(false) }
  }
  const outputs = Object.entries(run.step_outputs ?? {})
  const step = dsl?.steps?.find(s => s.id === selectedStep)
  const needsAttention = run.outcome === "FAILED" || run.outcome === "NEEDS_HUMAN" || ["failed", "interrupted"].includes(run.status)
  return <div className="mx-auto flex max-w-[1800px] flex-col gap-5 p-4 md:p-6">
    <header className="space-y-2">
      <Link className="text-xs text-primary" href={`/routines?slug=${encodeURIComponent(run.pipeline_slug)}`}>{run.pipeline_name || run.pipeline_slug}</Link>
      <div className="flex flex-wrap items-center gap-3"><h1 className="text-xl font-semibold">Run · {new Date(run.started_at).toLocaleString()}</h1><span className={needsAttention ? "text-sm text-destructive" : "text-sm text-muted-foreground"}>{run.status}{run.outcome && ` · ${run.outcome.toLowerCase().replaceAll("_", " ")}`}</span></div>
      <p className="text-xs text-muted-foreground">{run.triggered_via} · {formatDurationMs(run.duration_ms)}{run.pipeline_version != null && ` · Recipe version ${run.pipeline_version}`}</p>
      {run.issue_identifier && <Link className="text-xs text-primary" href={`/issues?issue=${encodeURIComponent(run.issue_identifier)}`}>{run.issue_identifier}</Link>}
    </header>
    {canApproveRoutine(role) && ["queued", "running", "waiting", "paused"].includes(run.status) && <div><Button variant="outline" disabled={stopping} onClick={stop}>{stopping ? "Stopping…" : "Stop run"}</Button></div>}
    {roleAtLeast(role, "MEMBER") && !["queued", "running", "waiting", "paused"].includes(run.status) && <div className="flex items-center gap-3"><Button variant="outline" disabled={starting} onClick={prepareAgain}>{starting ? "Starting…" : "Run again"}</Button><span className="text-xs text-muted-foreground">Creates a new run using the current recipe.</span></div>}
    <RoutineRunInputsDialog inputs={inputSpecs} routineName={run.pipeline_name || run.pipeline_slug} submitting={starting} onCancel={() => setInputSpecs(null)} onRun={startAgain} />
    {actionError && <p role="alert" className="text-sm text-destructive">{actionError}</p>}
    {error && <p role="alert" className="text-sm text-destructive">Updates unavailable. Showing the last loaded state. <button onClick={refresh}>Retry</button></p>}
    {run.error_message && <p role="alert" className="rounded-lg border border-destructive/40 p-4 text-sm">{run.failed_at_step && `${run.failed_at_step}: `}{run.error_message}</p>}
    {approval.waitpoint && <RoutineApprovalBanner waitpoint={approval.waitpoint} deciding={approval.deciding} onDecide={approval.decide} />}
    {approval.error && <p role="alert" className="text-sm text-destructive">Could not load or update the decision. <button onClick={approval.refresh}>Retry</button></p>}
    {run.output && <section className="rounded-xl border p-4"><h2 className="mb-2 text-sm font-medium">Result</h2><pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words text-sm">{run.output}</pre></section>}
    <nav aria-label="Run detail" className="flex gap-2 border-b pb-2">{["progress", "outputs", "activity", "inputs"].map(t => <Button key={t} size="sm" variant={tab === t ? "secondary" : "ghost"} onClick={() => setTab(t)} aria-pressed={tab === t}>{t[0].toUpperCase() + t.slice(1)}</Button>)}</nav>
    {tab === "progress" && <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
      {dsl ? <div className="h-[560px] min-h-[360px] overflow-hidden rounded-xl border"><TraceCanvas run={run} dsl={dsl} workspaceId={workspaceId} selectedStepId={selectedStep} onStepSelect={setSelectedStep} waitpointTokensByStepId={tokens} heatmapBuckets={EMPTY} stepMetrics={EMPTY} initialFocus="start" centerOnSelect /></div> : <p className="rounded-xl border p-6 text-sm text-muted-foreground">The historical recipe is unavailable. Recorded outputs and activity remain accessible.</p>}
      <aside className="max-h-[560px] space-y-4 overflow-auto rounded-xl border p-4"><RoutineStepDefinition step={step as unknown as Record<string, unknown> | undefined} />{step && <><h3 className="text-xs font-medium">Recorded output</h3><pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words text-xs">{Object.hasOwn(run.step_outputs ?? {}, step.id) ? renderOutput(run.step_outputs![step.id]) : "No output recorded yet."}</pre><h3 className="text-xs font-medium">Agent and tool activity</h3>{mapSubSpans(run.sub_spans?.[step.id]).map((span, index) => <details key={`${span.startedAt}-${span.name}-${index}`} className="rounded border p-2 text-xs"><summary className="cursor-pointer">{span.name} · {span.status}</summary><p className="mt-2 whitespace-pre-wrap break-words">{span.detail}</p></details>)}</>}</aside>
    </div>}
    {tab === "progress" && <RoutineExecutionHistory key={runId} workspaceId={workspaceId} runId={runId} active={["running", "queued", "waiting", "paused"].includes(run.status)} />}
    {tab === "outputs" && <section className="space-y-3"><RoutineRunArtifacts key={runId} workspaceId={workspaceId} runId={runId} active={["running", "queued", "waiting", "paused"].includes(run.status)} /><h2 className="pt-3 text-sm font-medium">Step responses</h2>{run.step_outputs_available === false ? <p role="alert">Step outputs could not be loaded. <button onClick={refresh}>Retry</button></p> : outputs.length === 0 ? <p className="text-sm text-muted-foreground">No step outputs recorded yet.</p> : outputs.map(([id, value]) => <details key={id} className="rounded-xl border p-4"><summary className="cursor-pointer text-sm font-medium">{id}</summary><pre className="mt-3 max-h-96 overflow-auto whitespace-pre-wrap break-words text-xs">{renderOutput(value)}</pre></details>)}</section>}
    {tab === "activity" && <RunActivityTimeline workspaceId={workspaceId} params={{ run_id: runId }} title="Run activity" card hideWhenEmpty={false} />}
    {tab === "inputs" && <section className="rounded-xl border p-4"><h2 className="mb-3 text-sm font-medium">Inputs used for this run</h2><pre className="overflow-auto whitespace-pre-wrap break-words text-xs">{JSON.stringify(run.inputs ?? {}, null, 2)}</pre></section>}
    <details className="text-xs text-muted-foreground"><summary className="cursor-pointer">Technical details</summary><dl className="mt-2 space-y-1"><div>Run: {run.id}</div><div>Definition: {run.definition_hash || "unavailable"}</div><div>Cost: ${run.cost_usd.toFixed(4)}</div><div>Mode: {run.mode}</div></dl></details>
  </div>
}
function renderOutput(value: unknown): string { return typeof value === "string" ? value : JSON.stringify(value, null, 2) }
