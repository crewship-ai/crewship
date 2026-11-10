"use client"

import { useEffect, useRef, useState } from "react"
import Link from "next/link"
import { apiFetch } from "@/lib/api-fetch"
import { askConfirm } from "@/components/ui/confirm-host"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ArrowLeft, ChevronRight, LayoutGrid, Library, Lock, RotateCw } from "lucide-react"
import { AccessNotice, type AccessContext } from "@/components/features/restricted/access-notice"
import { PageNoAccess } from "@/components/layout/page-state"
import { SubBar, SubBarSecondary } from "@/components/layout/sub-bar"
import { RESTRICTED_RUNTIME_MISSING_MESSAGE, restrictedCatalogFailure, type RestrictedCatalogState } from "@/lib/restricted-catalog"

interface ActionInput { name: string; label?: string; type: string; required: boolean; options?: string[] }
interface ArtifactAction { intent_hash: string; panel_id: string; id: string; label: string; inputs: ActionInput[]; confirm?: { title: string; body: string } }
/** One entry of `GET /workspaces/{id}/restricted-pages`: the server's only answer about what this member may run. */
interface GrantedArtifact { id: string; slug: string; name: string; publication?: number; actions: ArtifactAction[] }
interface Invocation extends ArtifactAction { key: string; artifact: GrantedArtifact }
interface Result { run_id: string; status: string; step_outputs: Record<string, string> }

const ACTIVE = ["SCHEDULED", "DEDUPED", "pending", "running"]

/**
 * A restricted member's Artifacts (#5132): the artifacts whose declared
 * actions were shared with them, and those actions. It reads only the
 * restricted catalog (`GET /api/v1/workspaces/{id}/restricted-pages`), runs an
 * action with the same publication-bound POST and polls only its own run
 * (`/restricted-routine-runs/{runId}`): the calls the Page actions catalog
 * made at /pages before, and nothing from the library. `focus` is the slug
 * of an `/artifacts?item=<slug>` address or, with `byId`, the ID of an
 * `/artifacts/<id>` address (#5135). Either is matched only against the
 * catalog already read, so the ID never leaves the browser; an artifact the
 * catalog does not list (not granted, nonexistent or deleted alike) shows the
 * same access gate and never its name.
 */
export function RestrictedArtifacts({ workspaceId, access, focus, byId = false, hrefFor, homeHref }: {
  workspaceId: string
  access?: AccessContext
  focus: string | null
  byId?: boolean
  hrefFor: (slug: string) => string
  homeHref: string
}) {
  const [catalog, setCatalog] = useState<GrantedArtifact[]>([])
  const [selected, setSelected] = useState("")
  const [values, setValues] = useState<Record<string, string>>({})
  const [run, setRun] = useState<Result | null>(null)
  const [error, setError] = useState("")
  const [catalogState, setCatalogState] = useState<RestrictedCatalogState>("loading")
  const [submitting, setSubmitting] = useState(false)
  // Bumped by "Check again" / "Try again" to re-read the catalog.
  const [reload, setReload] = useState(0)
  // The frozen request a retry resends, bound to the one action it was built for.
  const request = useRef<{ action: string; key: string; body: string; url: string } | null>(null)
  const scope = JSON.stringify([workspaceId, focus, byId])
  // Bumped whenever the scope changes, the catalog is re-read or the view
  // unmounts: a confirmation or response that resumes later finds a different
  // generation and touches nothing (#5132).
  const generation = useRef(0)
  useEffect(() => () => { generation.current++ }, [])
  const runId = run?.run_id
  const runState = run?.status
  const artifact = focus === null ? null : catalog.find(entry => byId ? entry.id === focus : entry.slug === focus) ?? null
  const actions: Invocation[] = artifact ? artifact.actions.map(action => ({ ...action, artifact, key: JSON.stringify([artifact.slug, action.panel_id, action.id]) })) : []
  const action = actions.find(item => item.key === selected)

  useEffect(() => {
    const controller = new AbortController()
    setCatalog([]); setCatalogState("loading")
    void Promise.resolve().then(() => apiFetch(`/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/restricted-pages`, { signal: controller.signal })).then(async response => {
      if (!response.ok) { const failure = await restrictedCatalogFailure(response); if (!controller.signal.aborted) setCatalogState(failure); return }
      const body = await response.json() as GrantedArtifact[]
      if (!controller.signal.aborted) { setCatalog(body.filter(entry => entry.actions.length > 0)); setCatalogState("ready") }
    }).catch(() => { if (!controller.signal.aborted) setCatalogState("unavailable") })
    return () => controller.abort()
  }, [workspaceId, reload])

  // Another artifact, another workspace or a re-read starts from a clean form.
  useEffect(() => { generation.current++; setSelected(""); setValues({}); setRun(null); setError(""); setSubmitting(false); request.current = null }, [scope, reload])

  useEffect(() => {
    if (!runId || !runState || !ACTIVE.includes(runState)) return
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    const poll = async () => {
      try {
        const response = await apiFetch(`/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/restricted-routine-runs/${requireApiPathSegment(runId)}`, { signal: controller.signal })
        if (!response.ok) throw new Error("This run is no longer available.")
        const body = await response.json() as Result
        if (controller.signal.aborted) return
        setRun(body)
        if (ACTIVE.includes(body.status)) timer = setTimeout(poll, 1000)
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
    const started = generation.current
    const current = () => generation.current === started
    setSubmitting(true); setError(""); setRun(null)
    try {
      if (request.current && request.current.action !== action.key) request.current = null
      if (!request.current) {
        if (action.confirm && !await askConfirm({ title: action.confirm.title, description: action.confirm.body, confirmLabel: "Run action", cancelLabel: "Cancel" })) return
        if (!current()) return
        const inputs: Record<string, string | number | boolean> = {}
        for (const field of action.inputs) {
          const value = values[field.name] ?? ""
          if (!value && !field.required) continue
          if (field.type === "number") { const n = Number(value); if (!value || !Number.isFinite(n)) throw new Error(`${field.name} requires a number.`); inputs[field.name] = n }
          else if (field.type === "boolean") { if (!["true", "false"].includes(value)) throw new Error(`${field.name} requires true or false.`); inputs[field.name] = value === "true" }
          else { if (!value && field.required) throw new Error(`${field.name} is required.`); inputs[field.name] = value }
        }
        const path = `/api/v1/pages/${requireApiPathSegment(action.artifact.slug)}`
        const target = `${requireApiPathSegment(action.panel_id)}/${requireApiPathSegment(action.id)}`
        const published = !!action.artifact.publication
        request.current = { action: action.key, key: crypto.randomUUID(), url: (published ? `${path}/application/actions/${target}` : `${path}/panels/${requireApiPathSegment(action.panel_id)}/actions/${requireApiPathSegment(action.id)}`) + `?workspace_id=${encodeURIComponent(workspace)}`, body: JSON.stringify(published ? { inputs, publication: action.artifact.publication, expected_intent_hash: action.intent_hash } : { inputs, expected_intent_hash: action.intent_hash }) }
      }
      const attempt = request.current
      const response = await apiFetch(attempt.url, { method: "POST", headers: { "Content-Type": "application/json", "Idempotency-Key": attempt.key }, body: attempt.body })
      const body = await response.json()
      if (!current()) return
      if (!response.ok) { request.current = null; throw new Error(body.error ?? "Action unavailable.") }
      setRun({ run_id: body.run_id, status: body.status, step_outputs: {} })
      request.current = null
    } catch (cause) {
      if (current()) setError(cause instanceof TypeError ? "Could not confirm the outcome. Retry sends the same request ID." : cause instanceof Error ? cause.message : "Action unavailable.")
    } finally { if (current()) setSubmitting(false) }
  }

  const active = submitting || !!run && ACTIVE.includes(run.status)
  const locked = active || !!request.current
  const actionCount = catalog.reduce((sum, entry) => sum + entry.actions.length, 0)
  // Unknown to the catalog: the same gate as any address outside the access, never the slug or a name.
  if (focus !== null && catalogState === "ready" && !artifact) return <RestrictedArtifactGate homeHref={homeHref} />
  const notice = catalogState === "runtime_missing" || catalogState === "unavailable" || (catalogState === "ready" && catalog.length === 0)
  return <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col bg-background">
    <SubBar icon={Library} title="Artifacts" ariaLabel="Artifacts" description={catalogState === "ready" ? `${actionCount} ${actionCount === 1 ? "action" : "actions"} available to you` : "Restricted access"} actions={<SubBarSecondary icon={RotateCw} disabled={catalogState === "loading" || locked} onClick={() => setReload(n => n + 1)}>Check again</SubBarSecondary>} />
    <div className="min-h-0 flex-1 overflow-auto p-4">
    <div className={notice ? "space-y-5" : "mx-auto grid w-full max-w-5xl content-start gap-6"}>
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {catalogState === "loading" ? <p>Loading artifacts…</p>
      : catalogState === "runtime_missing" ? <AccessNotice kind="not-installed" title="Private artifact actions aren’t set up here" description={RESTRICTED_RUNTIME_MISSING_MESSAGE} context={access} current="pages" />
      : catalogState === "unavailable" ? <AccessNotice kind="unavailable" title="Artifacts unavailable." description="The list of what you can run could not be loaded." context={access} current="pages" onRetry={() => setReload(n => n + 1)} />
      : catalog.length === 0 ? <AccessNotice kind="no-access" title="No artifacts shared with you yet" description="No artifact actions are available with your current access." context={access} current="pages" />
      : artifact ? <section aria-labelledby="restricted-artifact-heading" className="mx-auto grid w-full max-w-3xl gap-4">
        <div>
          {locked
            ? <span aria-disabled="true" className="inline-flex items-center gap-1 text-sm text-muted-foreground"><ArrowLeft aria-hidden className="size-3.5" />All artifacts</span>
            : <Link href={homeHref} className="inline-flex items-center gap-1 text-sm text-primary-hover hover:underline coarse:min-h-[44px]"><ArrowLeft aria-hidden className="size-3.5" />All artifacts</Link>}
        </div>
        <h2 id="restricted-artifact-heading" className="text-base font-semibold">{artifact.name}</h2>
        <p className="text-sm text-muted-foreground">Run a declared action. Its results are visible to you.</p>
        <label className="block space-y-2"><span>Action</span><select aria-label="Action" className="min-h-10 w-full rounded-md border bg-background p-2 coarse:min-h-[44px]" value={selected} disabled={locked} onChange={event => { setSelected(event.target.value); setValues({}); setRun(null); setError("") }}><option value="">Choose an action</option>{actions.map(item => <option key={item.key} value={item.key}>{item.label}</option>)}</select></label>
        {action && <form className="space-y-4" onSubmit={event => { event.preventDefault(); void submit() }}>
          {action.inputs.map(field => <label key={field.name} className="block space-y-2"><span>{field.label ?? field.name}{field.required ? " (required)" : ""}</span>{field.type === "select" ? <select aria-label={field.name} disabled={locked} value={values[field.name] ?? ""} className="min-h-10 w-full rounded-md border bg-background p-2 coarse:min-h-[44px]" onChange={event => setValues(current => ({ ...current, [field.name]: event.target.value }))}><option value="">Choose a value</option>{field.options?.map(value => <option key={value} value={value}>{value}</option>)}</select> : field.type === "textarea" ? <textarea aria-label={field.name} disabled={locked} value={values[field.name] ?? ""} className="min-h-24 w-full rounded-md border bg-background p-2" onChange={event => setValues(current => ({ ...current, [field.name]: event.target.value }))} /> : <Input aria-label={field.name} disabled={locked} value={values[field.name] ?? ""} onChange={event => setValues(current => ({ ...current, [field.name]: event.target.value }))} />}</label>)}
          <Button type="submit" disabled={active}>{submitting ? "Submitting…" : request.current ? "Retry request" : "Run action"}</Button>
        </form>}
        {run && <section className="space-y-3" aria-live="polite"><p>Status: {run.status === "needs_reconciliation" ? "Needs review" : run.status}</p>{run.status === "needs_reconciliation" && <p className="text-sm text-muted-foreground">An administrator must review this run’s outcome. It will not run again automatically.</p>}{Object.entries(run.step_outputs).map(([step, output]) => <div key={step}><h3 className="text-sm font-medium">{step}</h3><pre className="max-h-96 overflow-auto whitespace-pre-wrap rounded bg-muted p-3 text-sm">{output}</pre></div>)}</section>}
      </section>
      : <section aria-labelledby="restricted-home-heading" className="grid gap-3">
        <h1 className="sr-only">Artifacts</h1>
        <div className="grid gap-1">
          <h2 id="restricted-home-heading" className="flex items-baseline gap-2 text-base font-semibold">Shared with you <span className="font-mono text-sm font-normal text-muted-foreground">{catalog.length}</span></h2>
          <p className="text-xs text-muted-foreground">Open an artifact to run one of its declared actions. Its results are visible to you.</p>
        </div>
        <ul className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,250px),1fr))] gap-2.5">
          {catalog.map(entry => <li key={entry.slug} className="min-w-0">
            <Link href={hrefFor(entry.slug)} className="flex w-full min-w-0 items-center gap-3 rounded-card border bg-card px-3 py-2.5 text-left transition-colors hover:border-foreground/20 focus-visible:outline-2 focus-visible:outline-primary coarse:min-h-[44px]">
              <span aria-hidden className="icon-tile grid size-8 shrink-0 place-items-center rounded-md"><LayoutGrid className="size-4" /></span>
              <span className="grid min-w-0 flex-1">
                <b className="truncate text-sm font-semibold">{entry.name}</b>
                <small className="truncate text-xs text-muted-foreground">{entry.actions.length} {entry.actions.length === 1 ? "action" : "actions"}</small>
              </span>
              <ChevronRight aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
            </Link>
          </li>)}
        </ul>
      </section>}
    </div>
    </div>
  </div>
}

/** An artifact address outside a restricted member's access: the screen's name, one sentence and one way on, never the artifact's name. */
export function RestrictedArtifactGate({ homeHref }: { homeHref: string }) {
  return <div role="status" className="flex min-h-full flex-col bg-background">
    <SubBar icon={Lock} title="Artifacts" description="Outside your access" ariaLabel="Artifacts" />
    <PageNoAccess
      area="Artifacts"
      title="This artifact isn’t part of your access"
      description="You can open what was shared with you in this workspace."
      primary={{ label: "Go to Artifacts", href: homeHref }}
    />
  </div>
}
