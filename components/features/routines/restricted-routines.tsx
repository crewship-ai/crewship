"use client"

import { useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Workflow, RotateCw } from "lucide-react"
import { AccessNotice, type AccessContext } from "@/components/features/restricted/access-notice"
import { SubBar, SubBarSecondary } from "@/components/layout/sub-bar"
import { RESTRICTED_RUNTIME_MISSING_MESSAGE, restrictedCatalogFailure, type RestrictedCatalogState } from "@/lib/restricted-catalog"

import { RestrictedRoutineDefinitions } from "./restricted-routine-definitions"

interface RoutineInput { name: string; type: string; required: boolean }
interface Routine { slug: string; name: string; definition_hash: string; execution_hash: string; inputs: RoutineInput[] }
interface Result { run_id: string; status: string; step_outputs: Record<string, string> }

export function RestrictedRoutines({ workspaceId, access }: { workspaceId: string; access?: AccessContext }) {
  const [catalog, setCatalog] = useState<Routine[]>([])
  const [selected, setSelected] = useState("")
  const [values, setValues] = useState<Record<string, string>>({})
  const [run, setRun] = useState<Result | null>(null)
  const [error, setError] = useState("")
  const [catalogState, setCatalogState] = useState<RestrictedCatalogState>("loading")
  const [submitting, setSubmitting] = useState(false)
  // Bumped by "Check again" / "Try again" to re-read the catalog.
  const [reload, setReload] = useState(0)
  const request = useRef<{ key: string; body: string; slug: string } | null>(null)
  const owner = useRef(workspaceId)
  owner.current = workspaceId
  const runId = run?.run_id
  const runState = run?.status
  const routine = catalog.find(item => item.slug === selected)

  useEffect(() => {
    const controller = new AbortController()
    setCatalog([]); setSelected(""); setValues({}); setRun(null); setError(""); setCatalogState("loading"); request.current = null
    void Promise.resolve().then(() => apiFetch(`/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/restricted-routines`, { signal: controller.signal })).then(async response => {
      if (!response.ok) { const failure = await restrictedCatalogFailure(response); if (!controller.signal.aborted) setCatalogState(failure); return }
      const body = await response.json() as Routine[]
      if (!controller.signal.aborted) { setCatalog(body); setCatalogState("ready") }
    }).catch(() => { if (!controller.signal.aborted) setCatalogState("unavailable") })
    return () => controller.abort()
  }, [workspaceId, reload])

  useEffect(() => {
    if (!runId || !runState || !["SCHEDULED", "DEDUPED", "pending", "running"].includes(runState)) return
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    const poll = async () => {
      try {
        const response = await apiFetch(`/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/restricted-routine-runs/${requireApiPathSegment(runId)}`, { signal: controller.signal })
        if (!response.ok) throw new Error("This run is no longer available.")
        const body = await response.json() as Result
        if (controller.signal.aborted) return
        setRun(body)
        if (["SCHEDULED", "DEDUPED", "pending", "running"].includes(body.status)) timer = setTimeout(poll, 1000)
      } catch (cause) {
        if (controller.signal.aborted) return
        setRun(null)
        setError(cause instanceof Error ? cause.message : "Run unavailable.")
      }
    }
    timer = setTimeout(poll, 300)
    return () => { controller.abort(); clearTimeout(timer) }
  }, [workspaceId, runId, runState])

  const submit = async () => {
    if (!routine || submitting) return
    const workspace = workspaceId
    setSubmitting(true); setError(""); setRun(null)
    try {
      if (!request.current) {
        const inputs: Record<string, string | number | boolean> = {}
        for (const field of routine.inputs) {
          const value = values[field.name] ?? ""
          if (!value && !field.required) continue
          if (field.type === "number") { const n = Number(value); if (!value || !Number.isFinite(n)) throw new Error(`${field.name} requires a number.`); inputs[field.name] = n }
          else if (field.type === "boolean") { if (!["true", "false"].includes(value)) throw new Error(`${field.name} requires true or false.`); inputs[field.name] = value === "true" }
          else { if (!value && field.required) throw new Error(`${field.name} is required.`); inputs[field.name] = value }
        }
        request.current = { key: crypto.randomUUID(), slug: routine.slug, body: JSON.stringify({ inputs, expected_definition_hash: routine.definition_hash, expected_execution_hash: routine.execution_hash }) }
      }
      const attempt = request.current
      const response = await apiFetch(`/api/v1/workspaces/${requireApiPathSegment(workspace)}/pipelines/${requireApiPathSegment(attempt.slug)}/run`, { method: "POST", headers: { "Content-Type": "application/json", Prefer: "respond-async", "Idempotency-Key": attempt.key }, body: attempt.body })
      const body = await response.json()
      if (owner.current !== workspace) return
      if (!response.ok) { request.current = null; throw new Error(body.error ?? "Routine unavailable.") }
      setRun({ run_id: body.run_id, status: body.status, step_outputs: {} })
      request.current = null
    } catch (cause) {
      if (owner.current === workspace) setError(cause instanceof TypeError ? "Could not confirm the outcome. Retry sends the same request ID." : cause instanceof Error ? cause.message : "Routine unavailable.")
    } finally { if (owner.current === workspace) setSubmitting(false) }
  }
  const active = submitting || !!run && ["SCHEDULED", "DEDUPED", "pending", "running"].includes(run.status)
  const notice = catalogState === "runtime_missing" || catalogState === "unavailable" || (catalogState === "ready" && catalog.length === 0)
  return <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col bg-background">
    <SubBar icon={Workflow} title="Private routines" ariaLabel="Private routines" description={catalogState === "ready" ? `${catalog.length} available to you` : "Restricted access"} actions={<SubBarSecondary icon={RotateCw} disabled={catalogState === "loading" || active || !!request.current} onClick={() => setReload(n => n + 1)}>Check again</SubBarSecondary>} />
    <div className="min-h-0 flex-1 overflow-auto p-4">
    <div className={notice ? "space-y-5" : "mx-auto w-full max-w-3xl space-y-5"}>
    <RestrictedRoutineDefinitions key={workspaceId} workspaceId={workspaceId} />
    {!notice && <p className="text-sm text-muted-foreground">Run an allowed routine. Its results are visible to you.</p>}
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {catalogState === "loading" ? <p>Loading routines…</p> : catalogState === "runtime_missing" ? <AccessNotice kind="not-installed" title="Private routines aren't set up here" description={RESTRICTED_RUNTIME_MISSING_MESSAGE} context={access} current="routines" /> : catalogState === "unavailable" ? <AccessNotice kind="unavailable" title="Routine catalog unavailable." description="The list of what you can run could not be loaded." context={access} current="routines" onRetry={() => setReload(n => n + 1)} /> : catalog.length === 0 ? <AccessNotice kind="no-access" title="No routines shared with you yet" description="No routines are available with your current access." context={access} current="routines" /> : <>
      <label className="block space-y-2"><span>Routine</span><select aria-label="Routine" className="min-h-10 w-full rounded-md border bg-background p-2 coarse:min-h-12" value={selected} disabled={active || !!request.current} onChange={event => { setSelected(event.target.value); setValues({}); setRun(null); setError("") }}><option value="">Choose a routine</option>{catalog.map(item => <option key={item.slug} value={item.slug}>{item.name}</option>)}</select></label>
      {routine && <form className="space-y-4" onSubmit={event => { event.preventDefault(); void submit() }}>
        {routine.inputs.map(field => <label key={field.name} className="block space-y-2"><span>{field.name}{field.required ? " (required)" : ""}</span><Input aria-label={field.name} disabled={active || !!request.current} value={values[field.name] ?? ""} onChange={event => setValues(current => ({ ...current, [field.name]: event.target.value }))} /></label>)}
        <Button type="submit" disabled={active}>{submitting ? "Submitting…" : request.current ? "Retry request" : "Run routine"}</Button>
      </form>}
    </>}
    {run && <section className="space-y-3" aria-live="polite"><p>Status: {run.status === "needs_reconciliation" ? "Needs review" : run.status}</p>{run.status === "needs_reconciliation" && <p className="text-sm text-muted-foreground">An administrator must review this run’s outcome. It will not run again automatically.</p>}{Object.entries(run.step_outputs).map(([step, output]) => <div key={step}><h2 className="text-sm font-medium">{step}</h2><pre className="max-h-96 overflow-auto whitespace-pre-wrap rounded bg-muted p-3 text-sm">{output}</pre></div>)}</section>}
    </div>
    </div>
  </div>
}
