"use client"
import { useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { Input } from "@/components/ui/input"
import { RoutineRecurrenceFields } from "./routine-recurrence-fields"
export interface RoutineTriggerDraft { kind: "manual" | "schedule" | "once" | "event"; cron: string; timezone: string; at: string }
export function RoutineTriggerFields({ workspaceId, value, onChange }: { workspaceId: string; value: RoutineTriggerDraft; onChange: (v: RoutineTriggerDraft) => void }) {
  const [preview, setPreview] = useState<string[]>([])
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    const controller = new AbortController()
    setPreview([]); setError(null)
    if (value.kind !== "schedule") return
    const timer = setTimeout(async () => {
      try {
        const qs = new URLSearchParams({ cron_expr: value.cron, timezone: value.timezone, count: "3" })
        const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipeline-schedules/preview?${qs}`, { signal: controller.signal })
        if (!res.ok) throw new Error("Check the schedule and timezone.")
        const data = await res.json()
        if (!controller.signal.aborted) setPreview(data.occurrences)
      } catch (e) { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e)) }
    }, 300)
    return () => { controller.abort(); clearTimeout(timer) }
  }, [workspaceId, value.kind, value.cron, value.timezone])
  return <section className="space-y-3"><label className="block text-xs font-medium">When should it run?<select aria-label="Routine start" className="mt-1 block h-9 w-full rounded-md border bg-background px-2" value={value.kind} onChange={e => onChange({ ...value, kind: e.target.value as RoutineTriggerDraft["kind"] })}><option value="manual">When I start it</option><option value="once">Once on a date</option><option value="schedule">On a repeating schedule</option><option value="event">On an event · configure after creation</option></select></label>{value.kind === "schedule" && <><RoutineRecurrenceFields cron={value.cron} timezone={value.timezone} onCronChange={cron => onChange({ ...value, cron })} onTimezoneChange={timezone => onChange({ ...value, timezone })} />{preview.length > 0 && <div className="text-xs text-muted-foreground">Next starts ({value.timezone}):{preview.map(at => <p key={at}>{new Date(at).toLocaleString("en-GB", { timeZone: value.timezone })}</p>)}</div>}{error && <p role="alert" className="text-xs text-destructive">{error}</p>}</>}{value.kind === "once" && <label className="block text-xs">Date and time ({Intl.DateTimeFormat().resolvedOptions().timeZone})<Input aria-label="One-time start" type="datetime-local" value={value.at} onChange={e => onChange({ ...value, at: e.target.value })} /></label>}{value.kind === "event" && <p className="text-xs text-muted-foreground">After saving, connect a webhook or an automation under the routine’s Plan tab. The routine will wait for that configuration.</p>}</section>
}
