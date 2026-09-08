"use client"

import { Input } from "@/components/ui/input"

export function parseRecurrence(cron: string) {
  const [minute, hour, day, month, weekday] = cron.trim().split(/\s+/)
  const numeric = (v: string | undefined, max: number) => /^\d+$/.test(v ?? "") && Number(v) <= max
  if (!numeric(minute, 59) || !numeric(hour, 23) || month !== "*") return null
  const time = `${hour.padStart(2, "0")}:${minute.padStart(2, "0")}`
  if (day === "*" && weekday === "*") return { frequency: "daily", time, day: "1" }
  if (day === "*" && (numeric(weekday, 6) || weekday === "1-5")) return { frequency: weekday === "1-5" ? "weekdays" : "weekly", time, day: weekday }
  if (numeric(day, 31) && Number(day) > 0 && weekday === "*") return { frequency: "monthly", time, day }
  return null
}
export function RoutineRecurrenceFields({ cron, timezone, onCronChange, onTimezoneChange }: { cron: string; timezone: string; onCronChange: (cron: string) => void; onTimezoneChange: (timezone: string) => void }) {
  const recurrence = parseRecurrence(cron)
  const change = (frequency: string, time: string, day: string) => {
    if (frequency === "advanced") { onCronChange("@weekly"); return }
    const [hour, minute] = time.split(":").map(Number)
    if (!Number.isFinite(hour) || !Number.isFinite(minute)) return
    onCronChange(`${minute} ${hour} ${frequency === "monthly" ? day : "*"} * ${frequency === "weekly" ? day : frequency === "weekdays" ? "1-5" : "*"}`)
  }
  return <div className="space-y-3">
    <div className="grid gap-3 sm:grid-cols-2"><label className="space-y-1 text-xs">Repeat<select aria-label="Repeat" className="block h-9 w-full rounded-md border bg-background px-2" value={recurrence?.frequency ?? "advanced"} onChange={e => change(e.target.value, recurrence?.time ?? "09:00", "1")}><option value="daily">Every day</option><option value="weekdays">Weekdays</option><option value="weekly">Every week</option><option value="monthly">Every month</option><option value="advanced">Advanced schedule</option></select></label>
    <label className="space-y-1 text-xs">Timezone<Input aria-label="Timezone" value={timezone} onChange={e => onTimezoneChange(e.target.value)} placeholder="Europe/Prague" /></label></div>
    {recurrence && <div className="flex flex-wrap gap-3"><label className="space-y-1 text-xs">Time<Input aria-label="Time" type="time" value={recurrence.time} onChange={e => change(recurrence.frequency, e.target.value, recurrence.day)} /></label>{recurrence.frequency === "weekly" && <label className="space-y-1 text-xs">Day<select aria-label="Day of week" className="block h-9 rounded-md border bg-background px-2" value={recurrence.day} onChange={e => change("weekly", recurrence.time, e.target.value)}>{["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"].map((d, i) => <option key={d} value={i}>{d}</option>)}</select></label>}{recurrence.frequency === "monthly" && <label className="space-y-1 text-xs">Day of month<Input aria-label="Day of month" type="number" min={1} max={31} value={recurrence.day} onChange={e => { if (+e.target.value >= 1 && +e.target.value <= 31) change("monthly", recurrence.time, e.target.value) }} /><span className="text-muted-foreground">Months without this day are skipped.</span></label>}</div>}
    <details open={!recurrence}><summary className="cursor-pointer text-xs text-muted-foreground">Advanced · cron expression</summary><Input aria-label="Cron expression" className="mt-2 font-mono" value={cron} onChange={e => onCronChange(e.target.value)} /></details>
  </div>
}
