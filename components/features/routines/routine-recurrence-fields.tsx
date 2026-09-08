"use client"

import { Clock, Globe2, Repeat2 } from "lucide-react"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"

const days = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"]
const frequencies = [{ id: "daily", label: "Every day" }, { id: "weekdays", label: "Weekdays" }, { id: "weekends", label: "Weekends" }, { id: "weekly", label: "Every week" }, { id: "monthly", label: "Every month" }]
export function parseRecurrence(cron: string) {
  const fields = cron.trim().split(/\s+/)
  if (fields.length !== 5) return null
  const [minute, hour, day, month, weekday] = fields
  const numeric = (v: string, max: number) => /^\d+$/.test(v) && Number(v) <= max
  if (!numeric(minute, 59) || !numeric(hour, 23) || month !== "*") return null
  const time = `${hour.padStart(2, "0")}:${minute.padStart(2, "0")}`
  if (day === "*" && weekday === "*") return { frequency: "daily", time, day: "1" }
  if (day === "*" && weekday === "1-5") return { frequency: "weekdays", time, day: "1-5" }
  if (day === "*" && ["0,6", "6,0"].includes(weekday)) return { frequency: "weekends", time, day: "0,6" }
  if (day === "*" && weekday.split(",").every(d => numeric(d, 6))) return { frequency: "weekly", time, day: weekday }
  if (numeric(day, 31) && Number(day) > 0 && weekday === "*") return { frequency: "monthly", time, day }
  return null
}
export function RoutineRecurrenceFields({ cron, timezone, onCronChange, onTimezoneChange }: { cron: string; timezone: string; onCronChange: (cron: string) => void; onTimezoneChange: (timezone: string) => void }) {
  const recurrence = parseRecurrence(cron)
  const change = (frequency: string, time: string, day: string) => {
    if (!/^\d{2}:\d{2}$/.test(time)) return
    const [hour, minute] = time.split(":").map(Number)
    if (hour > 23 || minute > 59) return
    onCronChange(`${minute} ${hour} ${frequency === "monthly" ? day : "*"} * ${frequency === "weekly" ? day : frequency === "weekdays" ? "1-5" : frequency === "weekends" ? "0,6" : "*"}`)
  }
  const selectedDays = recurrence?.frequency === "weekly" ? recurrence.day.split(",").map(Number) : []
  return <div className="space-y-5">
    <div className="space-y-2"><p className="flex items-center gap-2 text-xs font-medium text-muted-foreground"><Repeat2 className="h-3.5 w-3.5" />Repeat</p><div role="group" aria-label="Repeat" className="flex flex-wrap gap-2">{frequencies.map(f => <button type="button" key={f.id} aria-pressed={recurrence?.frequency === f.id} onClick={() => change(f.id, recurrence?.time ?? "09:00", "1")} className={cn("rounded-full border px-3 py-2 text-xs transition-colors", recurrence?.frequency === f.id ? "border-primary/40 bg-primary/10 text-primary" : "border-hairline bg-muted/40 text-muted-foreground hover:text-foreground")}>{f.label}</button>)}</div></div>
    {recurrence?.frequency === "weekly" && <div className="space-y-2"><p className="text-xs text-muted-foreground">On these days</p><div role="group" aria-label="Days of week" className="flex flex-wrap gap-2">{[1,2,3,4,5,6,0].map(d => <button type="button" key={d} aria-label={days[d]} aria-pressed={selectedDays.includes(d)} onClick={() => { const next = selectedDays.includes(d) ? selectedDays.filter(v => v !== d) : [...selectedDays, d]; if (next.length) change("weekly", recurrence.time, next.sort().join(",")) }} className={cn("h-10 min-w-10 rounded-full border px-2 text-xs transition-colors", selectedDays.includes(d) ? "border-primary/40 bg-primary/10 text-primary" : "border-hairline text-muted-foreground hover:bg-muted")}>{days[d].slice(0,3)}</button>)}</div></div>}
    <div className="flex flex-wrap items-start gap-4">{recurrence && <label className="space-y-2 text-xs"><span className="flex items-center gap-1.5 text-muted-foreground"><Clock className="h-3.5 w-3.5" />At</span><Input aria-label="Time" type="time" value={recurrence.time} onChange={e => change(recurrence.frequency, e.target.value, recurrence.day)} className="w-36" /></label>}{recurrence?.frequency === "monthly" && <label className="space-y-2 text-xs"><span className="block text-muted-foreground">Day of month</span><Input aria-label="Day of month" type="number" min={1} max={31} value={recurrence.day} onChange={e => { if (/^\d+$/.test(e.target.value) && +e.target.value >= 1 && +e.target.value <= 31) change("monthly", recurrence.time, e.target.value) }} className="w-28" /></label>}<label className="space-y-2 text-xs"><span className="flex items-center gap-1.5 text-muted-foreground"><Globe2 className="h-3.5 w-3.5" />Timezone</span><Input aria-label="Timezone" value={timezone} onChange={e => onTimezoneChange(e.target.value)} placeholder="Europe/Prague" className="w-48" /></label></div>
    {recurrence?.frequency === "monthly" && Number(recurrence.day) > 28 && <p className="text-xs text-muted-foreground">Months without this day are skipped.</p>}
    <details open={!recurrence}><summary className="cursor-pointer text-xs text-muted-foreground">{recurrence ? "Advanced" : "Custom schedule"} · cron expression</summary><Input aria-label="Cron expression" className="mt-2 font-mono" value={cron} onChange={e => onCronChange(e.target.value)} /></details>
  </div>
}
