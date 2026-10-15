"use client"

import { useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { LayoutGrid, RotateCw } from "lucide-react"
import { AccessNotice, type AccessContext } from "@/components/features/restricted/access-notice"
import { SubBar, SubBarSecondary } from "@/components/layout/sub-bar"
import { RESTRICTED_RUNTIME_MISSING_MESSAGE, restrictedCatalogFailure, type RestrictedCatalogState } from "@/lib/restricted-catalog"

interface ActionInput { name: string; label?: string; type: string; required: boolean; options?: string[] }
interface PageAction { intent_hash: string; panel_id: string; id: string; label: string; inputs: ActionInput[]; confirm?: { title: string; body: string } }
interface Page { slug: string; name: string; publication?: number; actions: PageAction[] }
interface Invocation extends PageAction { key: string; page: Page }
interface Result { run_id: string; status: string; step_outputs: Record<string, string> }

export function RestrictedPages({ workspaceId, access }: { workspaceId: string; access?: AccessContext }) {
  const [catalog, setCatalog] = useState<Invocation[]>([])
  const [selected, setSelected] = useState("")
  const [values, setValues] = useState<Record<string, string>>({})
  const [run, setRun] = useState<Result | null>(null)
  const [error, setError] = useState("")
  const [catalogState, setCatalogState] = useState<RestrictedCatalogState>("loading")
  const [submitting, setSubmitting] = useState(false)
  // Bumped by "Check again" / "Try again" to re-read the catalog.
  const [reload, setReload] = useState(0)
  const request = useRef<{ key: string; body: string; url: string } | null>(null)
  const owner = useRef(workspaceId)
  owner.current = workspaceId
  const runId = run?.run_id
  const runState = run?.status
  const action = catalog.find(item => item.key === selected)

  useEffect(() => {
    const controller = new AbortController()
    setCatalog([]); setSelected(""); setValues({}); setRun(null); setError(""); setCatalogState("loading"); request.current = null
    void Promise.resolve().then(() => apiFetch(`/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/restricted-pages`, { signal: controller.signal })).then(async response => {
      if (!response.ok) { const failure = await restrictedCatalogFailure(response); if (!controller.signal.aborted) setCatalogState(failure); return }
      const body = await response.json() as Page[]
      if (!controller.signal.aborted) { setCatalog(body.flatMap(page => page.actions.map(action => ({ ...action, page, key: JSON.stringify([page.slug, action.panel_id, action.id]) })))); setCatalogState("ready") }
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
    if (!action || submitting) return
    const workspace = workspaceId
    setSubmitting(true); setError(""); setRun(null)
    try {
      if (!request.current) {
        if (action.confirm && !window.confirm(`${action.confirm.title}\n\n${action.confirm.body}`)) return
        const inputs: Record<string, string | number | boolean> = {}
        for (const field of action.inputs) {
          const value = values[field.name] ?? ""
          if (!value && !field.required) continue
          if (field.type === "number") { const n = Number(value); if (!value || !Number.isFinite(n)) throw new Error(`${field.name} requires a number.`); inputs[field.name] = n }
          else if (field.type === "boolean") { if (!["true", "false"].includes(value)) throw new Error(`${field.name} requires true or false.`); inputs[field.name] = value === "true" }
          else { if (!value && field.required) throw new Error(`${field.name} is required.`); inputs[field.name] = value }
        }
        const path = `/api/v1/pages/${requireApiPathSegment(action.page.slug)}`
        const target = `${requireApiPathSegment(action.panel_id)}/${requireApiPathSegment(action.id)}`
        const published = !!action.page.publication
        request.current = { key: crypto.randomUUID(), url: (published ? `${path}/application/actions/${target}` : `${path}/panels/${requireApiPathSegment(action.panel_id)}/actions/${requireApiPathSegment(action.id)}`) + `?workspace_id=${encodeURIComponent(workspace)}`, body: JSON.stringify(published ? { inputs, publication: action.page.publication, expected_intent_hash: action.intent_hash } : { inputs, expected_intent_hash: action.intent_hash }) }
      }
      const attempt = request.current
      const response = await apiFetch(attempt.url, { method: "POST", headers: { "Content-Type": "application/json", "Idempotency-Key": attempt.key }, body: attempt.body })
      const body = await response.json()
      if (owner.current !== workspace) return
      if (!response.ok) { request.current = null; throw new Error(body.error ?? "Page action unavailable.") }
      setRun({ run_id: body.run_id, status: body.status, step_outputs: {} })
      request.current = null
    } catch (cause) {
      if (owner.current === workspace) setError(cause instanceof TypeError ? "Could not confirm the outcome. Retry sends the same request ID." : cause instanceof Error ? cause.message : "Page action unavailable.")
    } finally { if (owner.current === workspace) setSubmitting(false) }
  }
  const active = submitting || !!run && ["SCHEDULED", "DEDUPED", "pending", "running"].includes(run.status)
  const notice = catalogState === "runtime_missing" || catalogState === "unavailable" || (catalogState === "ready" && catalog.length === 0)
  return <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col bg-background">
    <SubBar icon={LayoutGrid} title="Page actions" ariaLabel="Page actions" description={catalogState === "ready" ? `${catalog.length} available to you` : "Restricted access"} actions={<SubBarSecondary icon={RotateCw} disabled={catalogState === "loading" || active || !!request.current} onClick={() => setReload(n => n + 1)}>Check again</SubBarSecondary>} />
    <div className="min-h-0 flex-1 overflow-auto p-4">
    <div className={notice ? "space-y-5" : "mx-auto w-full max-w-3xl space-y-5"}>
    {!notice && <p className="text-sm text-muted-foreground">Run a declared action. Its results are visible to you.</p>}
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {catalogState === "loading" ? <p>Loading Page actions…</p> : catalogState === "runtime_missing" ? <AccessNotice kind="not-installed" title="Private Page actions aren't set up here" description={RESTRICTED_RUNTIME_MISSING_MESSAGE} context={access} current="pages" /> : catalogState === "unavailable" ? <AccessNotice kind="unavailable" title="Page actions unavailable." description="The list of what you can run could not be loaded." context={access} current="pages" onRetry={() => setReload(n => n + 1)} /> : catalog.length === 0 ? <AccessNotice kind="no-access" title="No Page actions shared with you yet" description="No Page actions are available with your current access." context={access} current="pages" /> : <>
      <label className="block space-y-2"><span>Action</span><select aria-label="Action" className="min-h-10 w-full rounded-md border bg-background p-2 coarse:min-h-12" value={selected} disabled={active || !!request.current} onChange={event => { setSelected(event.target.value); setValues({}); setRun(null); setError("") }}><option value="">Choose an action</option>{catalog.map(item => <option key={item.key} value={item.key}>{item.page.name} — {item.label}</option>)}</select></label>
      {action && <form className="space-y-4" onSubmit={event => { event.preventDefault(); void submit() }}>
        {action.inputs.map(field => <label key={field.name} className="block space-y-2"><span>{field.label ?? field.name}{field.required ? " (required)" : ""}</span>{field.type === "select" ? <select aria-label={field.name} disabled={active || !!request.current} value={values[field.name] ?? ""} className="min-h-10 w-full rounded-md border bg-background p-2 coarse:min-h-12" onChange={event => setValues(current => ({ ...current, [field.name]: event.target.value }))}><option value="">Choose a value</option>{field.options?.map(value => <option key={value} value={value}>{value}</option>)}</select> : field.type === "textarea" ? <textarea aria-label={field.name} disabled={active || !!request.current} value={values[field.name] ?? ""} className="min-h-24 w-full rounded-md border bg-background p-2" onChange={event => setValues(current => ({ ...current, [field.name]: event.target.value }))} /> : <Input aria-label={field.name} disabled={active || !!request.current} value={values[field.name] ?? ""} onChange={event => setValues(current => ({ ...current, [field.name]: event.target.value }))} />}</label>)}
        <Button type="submit" disabled={active}>{submitting ? "Submitting…" : request.current ? "Retry request" : "Run action"}</Button>
      </form>}
    </>}
    {run && <section className="space-y-3" aria-live="polite"><p>Status: {run.status === "needs_reconciliation" ? "Needs review" : run.status}</p>{run.status === "needs_reconciliation" && <p className="text-sm text-muted-foreground">An administrator must review this run’s outcome. It will not run again automatically.</p>}{Object.entries(run.step_outputs).map(([step, output]) => <div key={step}><h2 className="text-sm font-medium">{step}</h2><pre className="max-h-96 overflow-auto whitespace-pre-wrap rounded bg-muted p-3 text-sm">{output}</pre></div>)}</section>}
    </div>
    </div>
  </div>
}
