"use client"

import { useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

interface ActionInput { name: string; label?: string; type: string; required: boolean; options?: string[] }
interface PageAction { intent_hash: string; panel_id: string; id: string; label: string; inputs: ActionInput[]; confirm?: { title: string; body: string } }
interface Page { slug: string; name: string; publication?: number; actions: PageAction[] }
interface Invocation extends PageAction { key: string; page: Page }
interface Result { run_id: string; status: string; step_outputs: Record<string, string> }

export function RestrictedPages({ workspaceId }: { workspaceId: string }) {
  const [catalog, setCatalog] = useState<Invocation[]>([])
  const [selected, setSelected] = useState("")
  const [values, setValues] = useState<Record<string, string>>({})
  const [run, setRun] = useState<Result | null>(null)
  const [error, setError] = useState("")
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const request = useRef<{ key: string; body: string; url: string } | null>(null)
  const owner = useRef(workspaceId)
  owner.current = workspaceId
  const runId = run?.run_id
  const runState = run?.status
  const action = catalog.find(item => item.key === selected)

  useEffect(() => {
    const controller = new AbortController()
    setCatalog([]); setSelected(""); setValues({}); setRun(null); setError(""); setLoading(true); request.current = null
    void apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/restricted-pages`, { signal: controller.signal }).then(async response => {
      if (!response.ok) throw new Error("Page actions unavailable.")
      const body = await response.json() as Page[]
      if (!controller.signal.aborted) setCatalog(body.flatMap(page => page.actions.map(action => ({ ...action, page, key: JSON.stringify([page.slug, action.panel_id, action.id]) }))))
    }).catch(cause => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : "Page actions unavailable.") }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [workspaceId])

  useEffect(() => {
    if (!runId || !runState || !["SCHEDULED", "DEDUPED", "pending", "running"].includes(runState)) return
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    const poll = async () => {
      try {
        const response = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/restricted-routine-runs/${encodeURIComponent(runId)}`, { signal: controller.signal })
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
        const path = `/api/v1/pages/${encodeURIComponent(action.page.slug)}`
        const target = `${encodeURIComponent(action.panel_id)}/${encodeURIComponent(action.id)}`
        const published = !!action.page.publication
        request.current = { key: crypto.randomUUID(), url: (published ? `${path}/application/actions/${target}` : `${path}/panels/${encodeURIComponent(action.panel_id)}/actions/${encodeURIComponent(action.id)}`) + `?workspace_id=${encodeURIComponent(workspace)}`, body: JSON.stringify(published ? { inputs, publication: action.page.publication, expected_intent_hash: action.intent_hash } : { inputs, expected_intent_hash: action.intent_hash }) }
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
  return <div className="mx-auto w-full max-w-3xl space-y-5 overflow-auto p-4 md:p-6">
    <h1 className="text-xl font-semibold">Page actions</h1>
    <p className="text-sm text-muted-foreground">Run a declared action. Its results are visible to you.</p>
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {loading ? <p>Loading Page actions…</p> : catalog.length === 0 ? <p>No Page actions are available with your current access.</p> : <>
      <label className="block space-y-2"><span>Action</span><select aria-label="Action" className="min-h-10 w-full rounded-md border bg-background p-2 coarse:min-h-12" value={selected} disabled={active || !!request.current} onChange={event => { setSelected(event.target.value); setValues({}); setRun(null); setError("") }}><option value="">Choose an action</option>{catalog.map(item => <option key={item.key} value={item.key}>{item.page.name} — {item.label}</option>)}</select></label>
      {action && <form className="space-y-4" onSubmit={event => { event.preventDefault(); void submit() }}>
        {action.inputs.map(field => <label key={field.name} className="block space-y-2"><span>{field.label ?? field.name}{field.required ? " (required)" : ""}</span>{field.type === "select" ? <select aria-label={field.name} disabled={active || !!request.current} value={values[field.name] ?? ""} className="min-h-10 w-full rounded-md border bg-background p-2 coarse:min-h-12" onChange={event => setValues(current => ({ ...current, [field.name]: event.target.value }))}><option value="">Choose a value</option>{field.options?.map(value => <option key={value} value={value}>{value}</option>)}</select> : field.type === "textarea" ? <textarea aria-label={field.name} disabled={active || !!request.current} value={values[field.name] ?? ""} className="min-h-24 w-full rounded-md border bg-background p-2" onChange={event => setValues(current => ({ ...current, [field.name]: event.target.value }))} /> : <Input aria-label={field.name} disabled={active || !!request.current} value={values[field.name] ?? ""} onChange={event => setValues(current => ({ ...current, [field.name]: event.target.value }))} />}</label>)}
        <Button type="submit" disabled={active}>{submitting ? "Submitting…" : request.current ? "Retry request" : "Run action"}</Button>
      </form>}
    </>}
    {run && <section className="space-y-3" aria-live="polite"><p>Status: {run.status}</p>{Object.entries(run.step_outputs).map(([step, output]) => <div key={step}><h2 className="text-sm font-medium">{step}</h2><pre className="max-h-96 overflow-auto whitespace-pre-wrap rounded bg-muted p-3 text-sm">{output}</pre></div>)}</section>}
  </div>
}
