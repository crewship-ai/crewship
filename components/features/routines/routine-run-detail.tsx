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

import { RoutineResultContent } from "./routine-result-content"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog"
import { Network, FileText, Activity, Square, ArrowDownToLine, Clock, CalendarClock, Play, CheckCircle2, AlertCircle, History, Layers, ListOrdered, ChevronRight } from "lucide-react"
import { DetailCard, Pill } from "@/components/ui/detail"
import { RoutineIdentityHeader } from "./routine-identity-header"
import { RoutineNavigation, RoutineSubNavigation, routineViewHref } from "./routine-navigation"
import { routineRunPresentation, routineResultLabel, routineRunExplanation } from "@/lib/routine-run-presentation"
import type { RoutineDetail } from "./routines-detail-panel"

import { RoutineSavedInputs, RoutineRecordedValue, readableFieldName } from "./routine-saved-inputs"

const EMPTY = new Map()
const RUN_TAB_ICONS = { result: FileText, workflow: Network, activity: Activity, inputs: ArrowDownToLine }

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
  const [tab, setTab] = useState("result")
  const [confirmStop, setConfirmStop] = useState(false)
  const [showAttempts, setShowAttempts] = useState(false)
  useEffect(() => { setTab("result"); setSelectedStep(null); setConfirmStop(false); setShowAttempts(false) }, [runId])
  const tokens = useMemo(() => approval.waitpoint
    ? new Map([[approval.waitpoint.step_id, approval.waitpoint.token]]) : EMPTY, [approval.waitpoint])
  if (!run && error === "run: 404") return <div className="p-6"><p className="mb-4 text-sm text-muted-foreground">No routine execution record is available for this activity. Recorded events are shown below.</p><RunActivityTimeline workspaceId={workspaceId} params={{ run_id: runId }} title="Run activity" card hideWhenEmpty={false} /></div>
  if (!run) return <div className="p-6 text-sm" role="status">{error ? <>Could not load this run. <Button variant="outline" onClick={refresh}>Try again</Button></> : loading ? "Loading run…" : "Run unavailable."}</div>
  const stop = async () => {
    setStopping(true); setActionError(null)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/runs/${encodeURIComponent(runId)}/cancel`, { method: "POST" })
      if (!res.ok) throw new Error("Could not stop this run. It may already have finished.")
      setConfirmStop(false); await refresh(); await approval.refresh()
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
  const selectedStepId = selectedStep || run.current_step_id || dsl?.steps?.[0]?.id || null
  const step = dsl?.steps?.find(s => s.id === selectedStepId)
  const presentation = routineRunPresentation(run)
  const currentStep = dsl?.steps?.find(s => s.id === run.current_step_id)
  const active = ["queued", "running", "waiting", "paused"].includes(run.status)
  const explanation = routineRunExplanation(run, active ? approval.waitpoint ? "approval" : currentStep?.wait?.kind : undefined)
  const declaredResult = run.output ? routineResultLabel(run.output, dsl) : null
  const identity = routine?.slug === run.pipeline_slug ? routine : { slug: run.pipeline_slug, name: run.pipeline_name || run.pipeline_slug }
  const activityHref = `/activity?${new URLSearchParams({ pipeline: run.pipeline_slug, run: runId })}`
  const StatusIcon = presentation.tone === "destructive" || presentation.tone === "warn" ? AlertCircle : active ? Clock : presentation.tone === "success" ? CheckCircle2 : Square
  const triggerLabel = ({ manual: "Manual start", schedule: "Scheduled start", webhook: "Webhook", event: "Event", issue: "Issue" } as Record<string, string>)[run.triggered_via] || readableFieldName(run.triggered_via || "Unknown trigger")
  const resultPanel = run.output && <DetailCard title="Recorded result" icon={FileText}>
    {declaredResult && <p className="mb-3 text-sm font-medium">{declaredResult}</p>}
    {["true", "false"].includes(run.output.trim()) && !declaredResult && <p className="mb-3 text-xs text-muted-foreground">Boolean result from the recipe. A false value is not an execution failure.</p>}
    {resultSummary ? <><p className="whitespace-pre-wrap break-words text-sm">{resultSummary}</p><details className="mt-3 text-xs text-muted-foreground"><summary className="cursor-pointer">Full recorded response</summary><pre className="mt-2 max-h-96 overflow-auto whitespace-pre-wrap break-words">{run.output}</pre></details></> : <div className="max-h-[65vh] overflow-auto break-words"><RoutineResultContent output={run.output} /></div>}

  </DetailCard>
  return <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4">
    <RoutineIdentityHeader routine={identity} workspaceId={workspaceId} onChanged={() => setIdentityRevision(v => v + 1)} actions={<>
      {canApproveRoutine(role) && active && <Button variant="outline" size="sm" disabled={stopping} onClick={() => setConfirmStop(true)}><Square className="mr-1.5 h-3.5 w-3.5" />{stopping ? "Stopping…" : "Stop run"}</Button>}
      {roleAtLeast(role, "MEMBER") && !active && <Button variant="outline" size="sm" disabled={starting} onClick={() => void prepareAgain()}>{starting ? "Preparing…" : "Run again"}</Button>}
    </>}><Pill tone={presentation.tone}>{presentation.label}</Pill>{run.pipeline_version != null && <span className="text-xs text-muted-foreground">Viewing run of recipe v{run.pipeline_version}</span>}</RoutineIdentityHeader>
    <RoutineNavigation slug={run.pipeline_slug} view="run" runId={runId} />
    <DetailCard title="Run summary" icon={StatusIcon} action={<Link className="inline-flex items-center gap-1.5 text-xs text-primary hover:underline" href={activityHref}><Activity className="h-3.5 w-3.5" />Open in Activity ↗</Link>}>
      <div className="flex items-start gap-3"><span className="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-muted"><StatusIcon className={`h-5 w-5 ${presentation.tone === "destructive" ? "text-destructive" : presentation.tone === "warn" ? "text-warn" : active ? "text-primary" : presentation.tone === "success" ? "text-success" : "text-muted-foreground"}`} /></span><div className="min-w-0 flex-1"><h2 className="text-sm font-medium">{explanation.title}</h2><details className="mt-2 text-xs text-muted-foreground"><summary className="cursor-pointer hover:text-foreground">What this means</summary><p className="mt-2 max-w-[85ch] leading-relaxed">{explanation.detail}</p></details></div></div>
      <dl className="mt-4 grid grid-cols-2 gap-3 border-t border-border/60 pt-4 lg:grid-cols-4">
        <div><dt className="mb-1 flex items-center gap-1.5 text-[11px] text-muted-foreground"><CalendarClock className="h-3 w-3" />Started</dt><dd className="text-xs">{new Date(run.started_at).toLocaleString("en-GB", { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" })}</dd></div>
        <div><dt className="mb-1 flex items-center gap-1.5 text-[11px] text-muted-foreground"><Clock className="h-3 w-3" />Duration</dt><dd className="text-xs">{formatDurationMs(run.duration_ms)}</dd></div>
        <div><dt className="mb-1 flex items-center gap-1.5 text-[11px] text-muted-foreground"><Play className="h-3 w-3" />Started by</dt><dd className="text-xs">{triggerLabel}</dd></div>
        <div><dt className="mb-1 flex items-center gap-1.5 text-[11px] text-muted-foreground"><Layers className="h-3 w-3" />Recipe</dt><dd className="text-xs">{run.pipeline_version != null ? <Link className="text-primary" href={`${routineViewHref(run.pipeline_slug, "versions")}&version=${run.pipeline_version}`}>Executed recipe v{run.pipeline_version} ↗</Link> : "Version unavailable"}</dd></div>
      </dl>
      {run.issue_identifier && <Link className="mt-3 inline-block text-xs text-primary" href={`/issues?issue=${encodeURIComponent(run.issue_identifier)}`}>Issue · {run.issue_identifier} ↗</Link>}
      {run.error_message && <details className="mt-3 rounded-xl border border-destructive/15 bg-destructive/5 px-3 py-2 text-xs"><summary className="cursor-pointer text-destructive">Recorded error{run.failed_at_step ? ` · ${readableFieldName(run.failed_at_step)}` : ""}</summary><p className="mt-2 whitespace-pre-wrap break-words" role="alert">{run.error_message}</p></details>}
    </DetailCard>
    <Dialog open={confirmStop} onOpenChange={setConfirmStop}><DialogContent><DialogHeader><DialogTitle>Stop this run?</DialogTitle><DialogDescription>Pending and active work will be asked to stop. Recorded results remain available. Actions that already happened are not rolled back.</DialogDescription></DialogHeader><DialogFooter><Button variant="outline" onClick={() => setConfirmStop(false)} disabled={stopping}>Keep running</Button><Button variant="destructive" onClick={stop} disabled={stopping}>{stopping ? "Stopping…" : "Stop this run"}</Button></DialogFooter></DialogContent></Dialog>
    <RoutineRunInputsDialog versionChoices={[{ value: "current", label: "Current recipe" }, ...(run.pipeline_version != null ? [{ value: String(run.pipeline_version), label: `Executed recipe · v${run.pipeline_version}` }] : [])]} selectedVersion={selectedVersion} onVersionChange={v => void prepareAgain(v)} inputs={inputSpecs} routineName={run.pipeline_name || run.pipeline_slug} submitting={starting} onCancel={() => { preparation.current += 1; setInputSpecs(null); setStarting(false) }} onRun={startAgain} />
    {actionError && <p role="alert" className="text-sm text-destructive">{actionError}</p>}
    {error && <p role="alert" className="text-sm text-destructive">Updates unavailable. Showing the last loaded state. <button onClick={refresh}>Retry</button></p>}
    {approval.waitpoint && <div className="space-y-2"><RoutineApprovalBanner waitpoint={approval.waitpoint} deciding={approval.deciding} onDecide={approval.decide} />{approval.waitpoint.inbox_item_id && <Link className="text-xs text-primary" href={`/inbox?item=${encodeURIComponent(approval.waitpoint.inbox_item_id)}`}>Open the same decision in Inbox ↗</Link>}</div>}
    {approval.error && <p role="alert" className="text-sm text-destructive">Could not load or update the decision. <button onClick={approval.refresh}>Retry</button></p>}

    <RoutineSubNavigation label="Run detail" items={["result", "workflow", "activity", "inputs"]} value={tab} onChange={setTab} icons={RUN_TAB_ICONS} />
    {tab === "workflow" && <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_320px]">
      {dsl ? <DetailCard title="Workflow map" icon={Network} subtitle={run.pipeline_version != null ? `Executed version ${run.pipeline_version}` : "Historical recipe"} bare><div className="h-[56vh] min-h-[380px] overflow-hidden"><TraceCanvas run={run} dsl={dsl} workspaceId={workspaceId} selectedStepId={selectedStepId} onStepSelect={setSelectedStep} waitpointTokensByStepId={tokens} heatmapBuckets={EMPTY} stepMetrics={EMPTY} initialFocus="all" centerOnSelect recenterOnResize /></div></DetailCard> : <p className="rounded-xl border p-6 text-sm text-muted-foreground">The historical recipe is unavailable. Recorded outputs and activity remain accessible.</p>}
      <aside className="space-y-4"><DetailCard title="Step details" icon={ListOrdered}>
        <label htmlFor={`run-step-${runId}`} className="mb-2 block text-xs text-muted-foreground">Explore a step</label>
        <select id={`run-step-${runId}`} className="mb-4 w-full rounded-xl border border-border bg-muted/30 px-3 py-2 text-sm" value={selectedStepId || ""} onChange={e => setSelectedStep(e.target.value)}>{dsl?.steps?.map((item, index) => <option key={item.id} value={item.id}>{index + 1}. {readableFieldName(item.id)}</option>)}</select>
        {step ? <div className="space-y-4"><details className="text-xs"><summary className="cursor-pointer text-primary">Step instructions</summary><div className="mt-3"><RoutineStepDefinition step={step as unknown as Record<string, unknown>} /></div></details><div className="border-t border-border/60 pt-3"><h3 className="mb-3 flex items-center gap-2 text-xs font-medium"><FileText className="h-3.5 w-3.5 text-primary" />Recorded output</h3>{run.step_outputs_available === false ? <p className="text-xs text-destructive">Step outputs could not be loaded.</p> : Object.hasOwn(run.step_outputs ?? {}, step.id) ? <RoutineRecordedValue value={run.step_outputs![step.id]} /> : <p className="text-xs text-muted-foreground">No output recorded yet.</p>}</div><details className="border-t border-border/60 pt-3 text-xs"><summary className="cursor-pointer">Agent and tool activity · {mapSubSpans(run.sub_spans?.[step.id]).length}</summary><div className="mt-3 space-y-2">{mapSubSpans(run.sub_spans?.[step.id]).map((span, index) => <details key={`${span.startedAt}-${span.name}-${index}`} className="rounded-lg border border-border/60 p-2"><summary className="cursor-pointer">{span.name} · {span.status}</summary><p className="mt-2 whitespace-pre-wrap break-words">{span.detail}</p></details>)}</div></details></div> : <p className="text-xs text-muted-foreground">Select a step in the map to inspect its recorded work.</p>}
      </DetailCard></aside>
    </div>}
    {tab === "workflow" && <RoutineExecutionHistory key={runId} workspaceId={workspaceId} runId={runId} active={["running", "queued", "waiting", "paused"].includes(run.status)} />}
    {tab === "result" && <div className="space-y-4">
      {resultPanel}
      <RoutineRunArtifacts key={runId} workspaceId={workspaceId} runId={runId} active={active} compact />
      {!dsl && <p className="rounded-3xl border border-white/[0.06] bg-card p-4 text-sm text-muted-foreground">The historical recipe is unavailable. Recorded outputs and activity remain accessible.</p>}
      <DetailCard title="Recorded work" icon={ListOrdered} action={<Button size="sm" variant="ghost" onClick={() => setTab("workflow")}><Network className="mr-1.5 h-3.5 w-3.5" />Workflow map</Button>}>
        {!run.output && <p className="mb-3 text-xs text-muted-foreground">No final response has been recorded{active ? " yet" : ""}. Individual step responses and files may still be available.</p>}
        {run.step_outputs_available === false ? <p role="alert" className="text-sm text-destructive">Step outputs could not be loaded. <button onClick={refresh}>Retry</button></p> : outputs.length === 0 ? <p className="text-sm text-muted-foreground">No step outputs recorded yet.</p> : <details className="text-sm"><summary className="flex cursor-pointer items-center gap-2 text-primary"><ChevronRight className="h-4 w-4" />Step responses · {outputs.length}</summary><div className="mt-3 space-y-3">{outputs.map(([id, value]) => <details key={id} className="rounded-xl border border-border/60 bg-muted/15 p-3"><summary className="cursor-pointer text-sm font-medium">{readableFieldName(id)} <span className="font-normal text-muted-foreground">· recorded response</span></summary><div className="mt-3"><RoutineRecordedValue value={value} /></div><button className="mt-3 text-xs text-primary" onClick={() => { setSelectedStep(id); setTab("workflow") }}>Open this step in Workflow →</button></details>)}</div></details>}
        <p className="mt-3 text-xs text-muted-foreground">Individual step responses and attempts are available in Workflow.</p>
      </DetailCard>
    </div>}
    {tab === "activity" && <div className="space-y-4"><div className="flex flex-wrap items-center justify-between gap-3 rounded-2xl border border-border/60 bg-card px-4 py-3"><div className="flex items-center gap-2"><Activity className="h-4 w-4 text-primary" /><span className="text-sm font-medium">Events from this run</span><Pill tone={active ? "blue" : "default"}>{active ? "Live updates" : "Recorded history"}</Pill></div><Link className="text-xs text-primary" href={activityHref}>Open in Activity ↗</Link></div><RunActivityTimeline workspaceId={workspaceId} params={{ run_id: runId }} title="Run activity" card hideWhenEmpty={false} forceRunning={run.status === "running" && !approval.waitpoint} showControls /><details onToggle={e => setShowAttempts(e.currentTarget.open)} className="rounded-2xl border border-border/60 bg-card p-4 text-xs"><summary className="flex cursor-pointer items-center gap-2"><History className="h-4 w-4 text-muted-foreground" />Execution attempts</summary><div className="mt-4">{showAttempts && <RoutineExecutionHistory key={runId} workspaceId={workspaceId} runId={runId} active={active} />}</div></details></div>}
    {tab === "inputs" && <RoutineSavedInputs key={runId} values={run.inputs} definition={dsl} />}
    <details className="text-xs text-muted-foreground"><summary className="cursor-pointer">Technical details</summary><dl className="mt-2 space-y-1"><div>Run: {run.id}</div><div>Definition: {run.definition_hash || "unavailable"}</div><div>Cost: ${run.cost_usd.toFixed(4)}</div><div>Mode: {run.mode}</div></dl></details>
  </div>
}
