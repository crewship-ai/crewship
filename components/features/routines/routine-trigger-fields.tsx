"use client"
import { useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { CalendarDays, Play, Repeat2, Zap } from "lucide-react"
import { cn } from "@/lib/utils"
import { RoutineDateTimePicker } from "./routine-date-time-picker"
import { RoutineRecurrenceFields } from "./routine-recurrence-fields"
export interface RoutineTriggerDraft {
  kind: "manual" | "schedule" | "once" | "event"
  cron: string
  timezone: string
  at: string
}
export function RoutineTriggerFields({
  workspaceId,
  value,
  onChange,
}: {
  workspaceId: string
  value: RoutineTriggerDraft
  onChange: (v: RoutineTriggerDraft) => void
}) {
  const [preview, setPreview] = useState<string[]>([])
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    const controller = new AbortController()
    setPreview([])
    setError(null)
    if (value.kind !== "schedule") return
    const timer = setTimeout(async () => {
      try {
        const qs = new URLSearchParams({
          cron_expr: value.cron,
          timezone: value.timezone,
          count: "3",
        })
        const res = await apiFetch(
          `/api/v1/workspaces/${workspaceId}/pipeline-schedules/preview?${qs}`,
          { signal: controller.signal },
        )
        if (!res.ok) throw new Error("Check the schedule and timezone.")
        const data = await res.json()
        if (!controller.signal.aborted) setPreview(data.occurrences)
      } catch (e) {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e))
      }
    }, 300)
    return () => {
      controller.abort()
      clearTimeout(timer)
    }
  }, [workspaceId, value.kind, value.cron, value.timezone])
  return (
    <section className="space-y-3">
      <div role="group" aria-label="Routine start" className="grid gap-2 sm:grid-cols-2">
        {[
          { id: "manual", label: "When I start it", icon: Play },
          { id: "schedule", label: "Repeat", icon: Repeat2 },
          { id: "once", label: "Once on a date", icon: CalendarDays },
          { id: "event", label: "On an event", icon: Zap },
        ].map((option) => (
          <button
            type="button"
            key={option.id}
            aria-pressed={value.kind === option.id}
            onClick={() =>
              onChange({ ...value, kind: option.id as RoutineTriggerDraft["kind"] })
            }
            className={cn(
              "flex items-center gap-3 rounded-xl border p-3 text-left text-sm transition-colors",
              value.kind === option.id
                ? "border-primary/40 bg-primary/10 text-primary"
                : "border-hairline bg-muted/20 text-muted-foreground hover:bg-muted/40",
            )}
          >
            <option.icon className="h-4 w-4" />
            {option.label}
          </button>
        ))}
      </div>
      {value.kind === "schedule" && (
        <>
          <RoutineRecurrenceFields
            cron={value.cron}
            timezone={value.timezone}
            onCronChange={(cron) => onChange({ ...value, cron })}
            onTimezoneChange={(timezone) => onChange({ ...value, timezone })}
          />
          {preview.length > 0 && (
            <div className="text-xs text-muted-foreground">
              Next starts ({value.timezone}):
              {preview.map((at) => (
                <p key={at}>
                  {new Date(at).toLocaleString("en-GB", { timeZone: value.timezone })}
                </p>
              ))}
            </div>
          )}
          {error && (
            <p role="alert" className="text-xs text-destructive">
              {error}
            </p>
          )}
        </>
      )}
      {value.kind === "once" && (
        <div className="space-y-3">
          <RoutineDateTimePicker
            label="One-time start"
            value={value.at}
            onChange={(at) => onChange({ ...value, at })}
          />
          <p className="text-xs text-muted-foreground">
            {Intl.DateTimeFormat().resolvedOptions().timeZone} · Runs once
          </p>
        </div>
      )}
      {value.kind === "event" && (
        <p className="text-xs text-muted-foreground">
          After publishing, connect a webhook or an automation in the recipe details. The
          routine will wait for that configuration.
        </p>
      )}
    </section>
  )
}
