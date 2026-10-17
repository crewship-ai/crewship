"use client"

import { useEffect, useId, useState } from "react"
import { CalendarClock } from "lucide-react"
import { toast } from "@/lib/toast"
import { apiFetch } from "@/lib/api-fetch"
import type { Pipeline } from "@/hooks/use-pipelines"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { InputsForm, inputsSubmitContent, submitInputsForm } from "@/components/features/routines/routine-run-inputs-dialog"
import { RoutineDateTimePicker } from "@/components/features/routines/routine-date-time-picker"
import { RoutineRecurrenceFields } from "@/components/features/routines/routine-recurrence-fields"
import { Segmented } from "@/components/ui/segmented"
import { routineInputSpecs, type RoutineInputSpec } from "@/lib/routine-inputs"
import { dateKey, scheduledInstant } from "@/lib/routine-calendar"
import { formatRoutineTime, routineTimeZone } from "@/lib/routine-time"
import { cn } from "@/lib/utils"

type Mode = "once" | "repeat"

/** Why Repeating is off for someone who may not add schedules (v67 D-RTSCHED). */
export const REPEAT_BLOCKED_REASON = "Managers and above can add repeating schedules."

const pad = (n: number) => String(n).padStart(2, "0")
/** A slot on Fri 14:00 suggests "every Friday at 14:00". */
export const weeklyCronFor = (d: Date) => `${d.getMinutes()} ${d.getHours()} * * ${d.getDay()}`

/**
 * Schedule a start (Harbor 2, D-RTSCHED): a routine, One time or Repeating,
 * when, and the routine's inputs. A one-time start goes into the routine's
 * pending queue (`POST …/run` with `fire_at`); a repeating one is a schedule
 * (`POST …/pipeline-schedules`), which only people allowed to create
 * schedules may add — everyone else sees why instead of a button.
 */
export function ScheduleStartDialog({
  workspaceId,
  routines,
  date,
  canRepeat,
  onClose,
  onScheduled,
}: {
  workspaceId: string
  routines: Pipeline[]
  /** Prefilled from the clicked slot, or the next full hour. */
  date: Date
  canRepeat: boolean
  onClose: () => void
  onScheduled: () => void
}) {
  const base = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}`
  const eligible = routines.filter((r) => !r.status || r.status === "active")
  const [slug, setSlug] = useState(eligible.length === 1 ? eligible[0].slug : "")
  const [mode, setMode] = useState<Mode>("once")
  const [when, setWhen] = useState(`${dateKey(date)}T${pad(date.getHours())}:${pad(date.getMinutes())}`)
  const [cron, setCron] = useState(weeklyCronFor(date))
  const [timezone, setTimezone] = useState(routineTimeZone())
  const [next, setNext] = useState<{ state: "idle" | "loading" | "ok" | "error"; occurrences: string[]; error?: string }>({ state: "idle", occurrences: [] })
  const [loadedSlug, setLoadedSlug] = useState("")
  const [specs, setSpecs] = useState<RoutineInputSpec[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [retry, setRetry] = useState(0)
  const formId = useId()
  const routineId = useId()

  useEffect(() => {
    const controller = new AbortController()
    setSpecs(null)
    setError(null)
    if (!slug) return () => controller.abort()
    void (async () => {
      try {
        const res = await apiFetch(`${base}/pipelines/${encodeURIComponent(slug)}`, { signal: controller.signal })
        if (!res.ok) throw new Error("Could not load this routine's inputs.")
        const recipe = await res.json()
        if (!controller.signal.aborted) {
          setSpecs(routineInputSpecs(recipe.definition))
          setLoadedSlug(slug)
        }
      } catch (e) {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e))
      }
    })()
    return () => controller.abort()
  }, [base, slug, retry])

  // The next three starts come from the server's own cron parser, the same
  // preview the New routine wizard uses.
  useEffect(() => {
    if (mode !== "repeat" || !canRepeat) return
    if (!cron.trim() || !timezone.trim()) {
      setNext({ state: "error", occurrences: [], error: !cron.trim() ? "Enter a cron expression." : "Pick a time zone." })
      return
    }
    setNext((n) => ({ ...n, state: "loading" }))
    const controller = new AbortController()
    const timer = setTimeout(async () => {
      try {
        const params = new URLSearchParams({ cron_expr: cron, timezone, count: "3" })
        const res = await apiFetch(`${base}/pipeline-schedules/preview?${params}`, { signal: controller.signal })
        const body = (await res.json().catch(() => ({}))) as { occurrences?: string[]; error?: string }
        if (controller.signal.aborted) return
        if (!res.ok) setNext({ state: "error", occurrences: [], error: body.error || "This schedule does not parse." })
        else setNext({ state: "ok", occurrences: Array.isArray(body.occurrences) ? body.occurrences : [] })
      } catch {
        if (!controller.signal.aborted) setNext({ state: "error", occurrences: [], error: "Could not preview the schedule." })
      }
    }, 300)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [base, mode, canRepeat, cron, timezone])

  const scheduleOnce = async (inputs: Record<string, unknown>) => {
    const [day, time] = when.split("T")
    const at = scheduledInstant(day ?? "", time ?? "")
    if (!at) {
      setError("Choose a valid date and time. This local hour may not exist because of daylight saving time.")
      return
    }
    if (at.getTime() <= Date.now()) {
      setError("Choose a time in the future.")
      return
    }
    setSaving(true)
    setError(null)
    try {
      const res = await apiFetch(`${base}/pipelines/${encodeURIComponent(slug)}/run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ fire_at: at.toISOString(), inputs }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok || !data.pending_id) throw new Error(data.error || data.detail || "Could not schedule this routine.")
      toast.success(`Start planned · ${formatRoutineTime(at)}`)
      onScheduled()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  const addSchedule = async (inputs: Record<string, unknown>) => {
    setSaving(true)
    setError(null)
    try {
      const res = await apiFetch(`${base}/pipeline-schedules`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ target_pipeline_slug: slug, cron_expr: cron, timezone, inputs, enabled: true }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error(data.error || data.detail || "Could not add this schedule.")
      toast.success("Schedule added · it shows on the calendar")
      onScheduled()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  // The right can go while the dialog is open (a role change); fall back to
  // One time rather than leave a Repeating form without a button.
  useEffect(() => {
    if (!canRepeat && mode === "repeat") setMode("once")
  }, [canRepeat, mode])
  const blocked = mode === "repeat" && !canRepeat
  const formReady = !blocked && specs !== null && loadedSlug === slug
  const submitLabel = mode === "once" ? "Schedule" : "Add schedule"
  const dismiss = () => {
    if (!saving) onClose()
  }

  return (
    <CreateSurface open onOpenChange={(open) => !open && dismiss()} size="sm" onSubmit={() => formReady && submitInputsForm(formId)}>
      <CreateSurfaceHeader concept="routines" icon={CalendarClock} title="Schedule a start" description="Plan when a routine starts." onClose={dismiss} />
      <CreateSurfaceBody className="space-y-4">
        <CreateSurfaceField label="Routine" htmlFor={routineId} hint={!eligible.length ? "No active routine can be scheduled." : undefined}>
          <select
            id={routineId}
            value={slug}
            disabled={saving}
            onChange={(e) => setSlug(e.target.value)}
            className={cn(CREATE_SURFACE_INPUT, "w-full rounded-md border border-border bg-card px-2")}
          >
            <option value="">Choose a routine…</option>
            {eligible.map((r) => (
              <option key={r.slug} value={r.slug}>
                {r.name || r.slug}
              </option>
            ))}
          </select>
        </CreateSurfaceField>
        {/* Without the right to add schedules, Repeating is a disabled
            segment: the reason is its hover title and the note under it, so
            nobody picks a choice that ends in a form without a button. */}
        <div className="space-y-1.5">
          <Segmented<Mode>
            ariaLabel="How often"
            value={mode}
            onChange={setMode}
            options={[
              { value: "once", label: "One time" },
              { value: "repeat", label: "Repeating", disabled: !canRepeat, title: canRepeat ? undefined : REPEAT_BLOCKED_REASON },
            ]}
          />
          {!canRepeat && (
            <p role="note" data-testid="repeat-blocked" className="text-label text-muted-foreground">
              You can add one-time starts here. {REPEAT_BLOCKED_REASON} Members allowed to create schedules can too.
            </p>
          )}
        </div>
        {mode === "once" ? (
          <>
            <CreateSurfaceField label="When" hint={`${routineTimeZone()} · your browser's time zone`}>
              <RoutineDateTimePicker value={when} onChange={setWhen} label="Day" />
            </CreateSurfaceField>
            <p className="text-label text-muted-foreground">Shows on the calendar and in the routine&apos;s Plan. Uses the published version.</p>
          </>
        ) : (
          <>
            <RoutineRecurrenceFields cron={cron} timezone={timezone} onCronChange={setCron} onTimezoneChange={setTimezone} />
            <div className="space-y-1">
              <p className="text-label font-medium text-muted-foreground">Next 3 starts</p>
              {next.state === "error" ? (
                <p className="text-label text-destructive">{next.error}</p>
              ) : next.state === "ok" ? (
                <ul aria-label="Next 3 starts" className="flex flex-wrap gap-1.5">
                  {next.occurrences.map((o) => (
                    <li key={o} className="rounded-md border border-border px-1.5 py-0.5 font-mono text-label">
                      {formatRoutineTime(o)}
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-label text-muted-foreground" role="status">
                  Working out the next starts…
                </p>
              )}
            </div>
          </>
        )}
        {error && (
          <p role="alert" className="text-body text-destructive">
            {error}
            {slug && specs === null && (
              <button type="button" className="ml-2 underline" onClick={() => setRetry((v) => v + 1)}>
                Retry
              </button>
            )}
          </p>
        )}
        {slug && !error && specs === null && <p role="status">Loading inputs…</p>}
        {!blocked && specs !== null && loadedSlug === slug && (
          <InputsForm
            key={`${slug}:${mode}`}
            formId={formId}
            inputs={specs}
            submitting={saving}
            onRun={mode === "once" ? scheduleOnce : addSchedule}
            submitLabel={submitLabel}
          />
        )}
      </CreateSurfaceBody>
      <CreateSurfaceFooter
        hint={null}
        onCancel={dismiss}
        guardCancel={false}
        primaryLabel={formReady ? inputsSubmitContent(submitLabel, saving) : undefined}
        onPrimary={formReady ? () => submitInputsForm(formId) : undefined}
        busy={saving}
      />
    </CreateSurface>
  )
}
