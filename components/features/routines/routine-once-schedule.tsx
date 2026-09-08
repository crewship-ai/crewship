"use client"

import { useCallback, useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { RoutineRunInputsDialog } from "./routine-run-inputs-dialog"
import { routineInputSpecs, type RoutineInputSpec } from "@/lib/routine-inputs"

interface Pending { id: string; pipeline_slug: string; fire_at: string }
export function RoutineOnceSchedule({ workspaceId, slug }: { workspaceId: string; slug: string }) {
  const [at, setAt] = useState("")
  const [pending, setPending] = useState<Pending[]>([])
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [specs, setSpecs] = useState<RoutineInputSpec[] | null>(null)
  const base = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}`
  const refresh = useCallback(async () => {
    const res = await apiFetch(`${base}/pipelines/pending`)
    if (!res.ok) throw new Error("Could not load scheduled starts")
    setPending((await res.json()).filter((p: Pending) => p.pipeline_slug === slug))
  }, [base, slug])
  useEffect(() => { void refresh().catch(e => setError(e.message)) }, [refresh])
  const schedule = async (inputs: Record<string, unknown>) => {
    setBusy(true); setError(null)
    try {
      const res = await apiFetch(`${base}/pipelines/${encodeURIComponent(slug)}/run`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fire_at: new Date(at).toISOString(), inputs }) })
      if (!res.ok) { const data = await res.json(); throw new Error(data.error || "Could not schedule run") }
      setSpecs(null); setAt(""); await refresh()
    } catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setBusy(false) }
  }
  const prepare = async () => {
    setBusy(true); setError(null)
    try {
      const res = await apiFetch(`${base}/pipelines/${encodeURIComponent(slug)}`)
      if (!res.ok) throw new Error("Could not load routine inputs")
      const routine = await res.json()
      const inputs = routineInputSpecs(routine.definition)
      if (inputs.length) setSpecs(inputs); else await schedule({})
    } catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setBusy(false) }
  }
  const cancel = async (id: string) => {
    setBusy(true); setError(null)
    try {
      const res = await apiFetch(`${base}/pipelines/pending/${encodeURIComponent(id)}/cancel`, { method: "POST" })
      if (!res.ok) throw new Error("Could not cancel; the run may already have started. Refresh its history.")
      await refresh()
    } catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setBusy(false) }
  }
  return <section className="space-y-3 rounded-xl border p-4"><h2 className="text-sm font-medium">Run once on a date</h2><p className="text-xs text-muted-foreground">{Intl.DateTimeFormat().resolvedOptions().timeZone} · This date does not repeat.</p><div className="flex flex-wrap gap-2"><Input aria-label="One-time date and time" className="max-w-xs" type="datetime-local" value={at} onChange={e => setAt(e.target.value)} /><Button disabled={busy || !at || !(Date.parse(at) > Date.now())} onClick={prepare}>Schedule once</Button></div>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}{pending.map(p => <div key={p.id} className="flex flex-wrap items-center justify-between gap-2 rounded-lg bg-muted/40 p-3 text-sm"><span>{new Date(p.fire_at).toLocaleString()}</span><Button size="sm" variant="ghost" disabled={busy} onClick={() => cancel(p.id)}>Cancel scheduled start</Button></div>)}<RoutineRunInputsDialog inputs={specs} routineName={slug} submitting={busy} onCancel={() => setSpecs(null)} onRun={schedule} /></section>
}
