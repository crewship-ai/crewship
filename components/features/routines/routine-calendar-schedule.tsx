"use client"

import { useEffect, useState } from "react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import type { Pipeline } from "@/hooks/use-pipelines"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@/components/ui/dialog"
import { InputsForm } from "./routine-run-inputs-dialog"
import { routineInputSpecs, type RoutineInputSpec } from "@/lib/routine-inputs"
import { dateKey, scheduledInstant } from "@/lib/routine-calendar"

export function RoutineCalendarSchedule({ workspaceId, routines, date, onClose, onScheduled }: {
  workspaceId: string; routines: Pipeline[]; date: Date; onClose: () => void; onScheduled: () => void
}) {
  const [slug, setSlug] = useState("")
  const [day, setDay] = useState(dateKey(date))
  const [time, setTime] = useState(`${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`)
  const [loadedSlug, setLoadedSlug] = useState("")
  const [specs, setSpecs] = useState<RoutineInputSpec[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [retry, setRetry] = useState(0)
  const eligible = routines.filter(r => !r.status || r.status === "active")
  useEffect(() => {
    const controller = new AbortController()
    setSpecs(null); setError(null)
    if (!slug) return () => controller.abort()
    void (async () => {
      try {
        const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}`, { signal: controller.signal })
        if (!res.ok) throw new Error("Could not load this routine's inputs.")
        const recipe = await res.json()
        if (!controller.signal.aborted) { setSpecs(routineInputSpecs(recipe.definition)); setLoadedSlug(slug) }
      } catch (e) { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e)) }
    })()
    return () => controller.abort()
  }, [workspaceId, slug, retry])
  const schedule = async (inputs: Record<string, unknown>) => {
    if (saving) return
    const at = scheduledInstant(day, time)
    if (!at) { setError("Choose a valid date and time. This local hour may not exist because of daylight saving time."); return }
    if (at.getTime() <= Date.now()) { setError("Choose a time in the future."); return }
    setSaving(true); setError(null)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}/run`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fire_at: at.toISOString(), inputs }) })
      const data = await res.json()
      if (!res.ok || !data.pending_id) throw new Error(data.error || data.detail || "Could not schedule this routine.")
      toast.success(`Routine scheduled for ${at.toLocaleString()}`)
      onScheduled()
    } catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setSaving(false) }
  }
  return <Dialog open onOpenChange={open => { if (!open && !saving) onClose() }}><DialogContent className="max-h-[90vh] overflow-y-auto border-border bg-card sm:max-w-lg">
    <DialogHeader><DialogTitle>Schedule a routine</DialogTitle><DialogDescription>Choose an existing recipe and its inputs for one scheduled start. Repeating schedules can be managed in the routine’s Plan.</DialogDescription></DialogHeader>
    <div className="space-y-2"><label htmlFor="calendar-routine" className="text-sm font-medium">Routine</label><select id="calendar-routine" value={slug} disabled={saving} onChange={e => setSlug(e.target.value)} className="w-full rounded-md border bg-card p-2 text-sm"><option value="">Choose a routine…</option>{eligible.map(r => <option key={r.slug} value={r.slug}>{r.name || r.slug}</option>)}</select>{!eligible.length && <p className="text-xs text-muted-foreground">No active routines are available in this selection.</p>}</div>
    <div className="grid grid-cols-2 gap-3"><div><label htmlFor="calendar-date" className="text-sm">Date</label><input id="calendar-date" type="date" value={day} disabled={saving} onChange={e => setDay(e.target.value)} className="mt-1 w-full min-w-0 rounded-md border bg-card p-2 text-sm" /></div><div><label htmlFor="calendar-time" className="text-sm">Time</label><input id="calendar-time" type="time" value={time} disabled={saving} onChange={e => setTime(e.target.value)} className="mt-1 w-full rounded-md border bg-card p-2 text-sm" /></div></div>
    <p className="text-xs text-muted-foreground">Timezone: {Intl.DateTimeFormat().resolvedOptions().timeZone}. The current recipe will run at the selected time.</p>
    {error && <p role="alert" className="text-sm text-destructive">{error}{slug && specs === null && <button className="ml-2 underline" onClick={() => setRetry(v => v + 1)}>Retry</button>}</p>}
    {slug && !error && specs === null && <p role="status">Loading inputs…</p>}
    {specs !== null && loadedSlug === slug && <InputsForm key={slug} inputs={specs} submitting={saving} onCancel={onClose} onRun={schedule} submitLabel="Schedule routine" />}
  </DialogContent></Dialog>
}
