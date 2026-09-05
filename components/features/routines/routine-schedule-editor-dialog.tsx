"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { describeCron } from "@/lib/cron-describe"
import type { PipelineSchedule, SchedulePatchBody, SchedulePreview } from "@/hooks/use-pipeline-schedules"

/**
 * RoutineScheduleEditorDialog — the reliability editor (B9, #2362, §13.2).
 *
 * Every row of the reliability table that has a PATCH .../pipeline-schedules
 * door gets a control here: When (cron/timezone + a live next-five-fires
 * preview), After downtime (catchup_policy), Only run if (the wake gate),
 * On repeated failure (max_consecutive_failures), and Version (the pin).
 * "If it overlaps" (concurrency_key/max_concurrent) is a per-ROUTINE DSL
 * field shared by every trigger of that routine, not a per-schedule one —
 * it is surfaced read-only here with a link to the routine's own editor,
 * which is its real (and only) writable door. Health fields are read-only
 * telemetry and stay on the list row (a6); this dialog is for editing only.
 *
 * A draft trigger (activation === "draft") disables the enabled toggle —
 * only `.../activate` may turn one on (server enforces this too).
 */
export interface RoutineScheduleEditorDialogProps {
  schedule: PipelineSchedule | null
  submitting?: boolean
  onCancel: () => void
  onSave: (body: SchedulePatchBody) => void
  onPreview: (cronExpr: string, timezone: string, count?: number) => Promise<SchedulePreview>
}

const CATCHUP_OPTIONS: Array<{ value: "skip" | "once" | "all"; label: string; hint: string }> = [
  { value: "skip", label: "Skip", hint: "Fire nothing for the missed backlog." },
  { value: "once", label: "Once (default)", hint: "Fire once for the whole backlog." },
  { value: "all", label: "All (capped)", hint: "Fire once per missed occurrence, oldest first." },
]

export function RoutineScheduleEditorDialog({
  schedule,
  submitting,
  onCancel,
  onSave,
  onPreview,
}: RoutineScheduleEditorDialogProps) {
  const [name, setName] = useState("")
  const [cronExpr, setCronExpr] = useState("")
  const [timezone, setTimezone] = useState("UTC")
  const [enabled, setEnabled] = useState(true)
  const [catchup, setCatchup] = useState<"skip" | "once" | "all">("once")
  const [maxFailures, setMaxFailures] = useState<string>("5")
  const [wakeSlug, setWakeSlug] = useState("")
  const [wakeFailClosed, setWakeFailClosed] = useState(false)
  const [pinVersion, setPinVersion] = useState<string>("")
  const [preview, setPreview] = useState<SchedulePreview | null>(null)
  const [previewError, setPreviewError] = useState<string | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)
  // Guards against out-of-order responses: onPreview takes no AbortSignal
  // (it's a plain GET the hook fires-and-returns), so a request for an
  // EARLIER cron/timezone pair that happens to resolve AFTER a later one
  // must not overwrite the later result. Bumped on every request; a
  // response is applied only if it's still the most recent one in flight.
  const previewSeqRef = useRef(0)

  useEffect(() => {
    if (!schedule) return
    setName(schedule.name)
    setCronExpr(schedule.cron_expr)
    setTimezone(schedule.timezone || "UTC")
    setEnabled(schedule.enabled)
    setCatchup((schedule.catchup_policy as "skip" | "once" | "all") || "once")
    setMaxFailures(String(schedule.max_consecutive_failures || 5))
    setWakeSlug(schedule.wake_pipeline_slug || "")
    setWakeFailClosed(!!schedule.wake_fail_closed)
    setPinVersion(schedule.target_pipeline_version ? String(schedule.target_pipeline_version) : "")
    setPreview(null)
    setPreviewError(null)
  }, [schedule])

  const runPreview = useCallback(async () => {
    if (!cronExpr.trim()) return
    const seq = ++previewSeqRef.current
    setPreviewLoading(true)
    setPreviewError(null)
    try {
      const out = await onPreview(cronExpr, timezone || "UTC", 5)
      if (seq !== previewSeqRef.current) return // a newer request already landed
      setPreview(out)
    } catch (e) {
      if (seq !== previewSeqRef.current) return
      setPreview(null)
      setPreviewError(e instanceof Error ? e.message : String(e))
    } finally {
      if (seq === previewSeqRef.current) setPreviewLoading(false)
    }
  }, [cronExpr, timezone, onPreview])

  // Live preview: debounce on every keystroke of cron/timezone so the
  // editor answers "what will this fire" as it's typed, not just on demand.
  useEffect(() => {
    if (!schedule || !cronExpr.trim()) return
    const t = setTimeout(() => { void runPreview() }, 400)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cronExpr, timezone, schedule])

  if (!schedule) return null

  const isDraft = schedule.activation === "draft"

  const submit = () => {
    const body: SchedulePatchBody = {
      name,
      cron_expr: cronExpr,
      timezone,
      enabled: isDraft ? schedule.enabled : enabled,
      catchup_policy: catchup,
      max_consecutive_failures: Math.max(1, parseInt(maxFailures, 10) || 5),
      wake_pipeline_slug: wakeSlug.trim(),
      wake_fail_closed: wakeSlug.trim() ? wakeFailClosed : undefined,
      target_pipeline_version: pinVersion.trim() ? parseInt(pinVersion, 10) : null,
    }
    onSave(body)
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onCancel()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Edit schedule</DialogTitle>
          <DialogDescription>
            Every reliability setting for this trigger — the same fields the
            backend already accepts on save.
          </DialogDescription>
        </DialogHeader>

        <div className="max-h-[70vh] space-y-5 overflow-y-auto pr-1">
          <div>
            <Label htmlFor="sched-name">Name</Label>
            <Input id="sched-name" value={name} onChange={(e) => setName(e.target.value)} className="mt-1.5" />
          </div>

          <div className="space-y-2">
            <Label>When</Label>
            <div className="grid grid-cols-2 gap-2">
              <Input
                aria-label="Cron expression"
                value={cronExpr}
                onChange={(e) => setCronExpr(e.target.value)}
                className="font-mono text-sm"
                placeholder="0 9 * * *"
              />
              <Input
                aria-label="Timezone"
                value={timezone}
                onChange={(e) => setTimezone(e.target.value)}
                placeholder="Europe/Prague"
              />
            </div>
            <p className="text-[11px] text-muted-foreground">{describeCron(cronExpr)}</p>
            <div className="rounded-md border border-white/[0.08] bg-background/50 p-2.5 text-[12px]" data-testid="schedule-preview">
              {previewLoading && <span className="text-muted-foreground">Computing next fire times…</span>}
              {previewError && <span className="text-destructive">{previewError}</span>}
              {!previewLoading && !previewError && preview && preview.occurrences.length > 0 && (
                <div>
                  <p className="mb-1 text-muted-foreground">Next {preview.occurrences.length} fire times ({preview.timezone}):</p>
                  <ul className="space-y-0.5 font-mono text-[11px]">
                    {preview.occurrences.map((o) => (
                      <li key={o}>{new Date(o).toLocaleString()}</li>
                    ))}
                  </ul>
                </div>
              )}
            </div>
          </div>

          <div>
            <Label>After downtime</Label>
            <Select value={catchup} onValueChange={(v) => setCatchup(v as "skip" | "once" | "all")}>
              <SelectTrigger className="mt-1.5 w-full"><SelectValue /></SelectTrigger>
              <SelectContent>
                {CATCHUP_OPTIONS.map((o) => (
                  <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="mt-1 text-[11px] text-muted-foreground">
              {CATCHUP_OPTIONS.find((o) => o.value === catchup)?.hint} Ignored by wake-gated schedules.
            </p>
          </div>

          <div className="space-y-2">
            <Label>Only run if (wake gate)</Label>
            <Input
              value={wakeSlug}
              onChange={(e) => setWakeSlug(e.target.value)}
              placeholder="agentless probe routine slug — blank clears the gate"
            />
            <div className="flex items-center gap-2">
              <Switch
                id="sched-fail-closed"
                checked={wakeFailClosed}
                disabled={!wakeSlug.trim()}
                onCheckedChange={setWakeFailClosed}
              />
              <Label htmlFor="sched-fail-closed" className="text-xs font-normal text-muted-foreground">
                Fail closed (hold the run if the probe errors/times out, instead of firing anyway)
              </Label>
            </div>
          </div>

          <div>
            <Label htmlFor="sched-max-failures">On repeated failure</Label>
            <div className="mt-1.5 flex items-center gap-2">
              <Input
                id="sched-max-failures"
                type="number"
                min={1}
                value={maxFailures}
                onChange={(e) => setMaxFailures(e.target.value)}
                className="w-24"
              />
              <span className="text-[11px] text-muted-foreground">
                consecutive failures before auto-disabling (currently {schedule.consecutive_failures})
              </span>
            </div>
          </div>

          <div>
            <Label htmlFor="sched-pin">Version</Label>
            <Input
              id="sched-pin"
              type="number"
              min={1}
              value={pinVersion}
              onChange={(e) => setPinVersion(e.target.value)}
              placeholder="blank = track head"
              className="mt-1.5 w-32"
            />
          </div>

          <div className="flex items-center gap-2">
            <Switch id="sched-enabled" checked={enabled} disabled={isDraft} onCheckedChange={setEnabled} />
            <Label htmlFor="sched-enabled" className="text-xs font-normal text-muted-foreground">
              {isDraft
                ? "Enabled — awaiting MANAGER activation (use Activate, not this toggle)"
                : "Enabled"}
            </Label>
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={onCancel} disabled={submitting}>Cancel</Button>
          <Button onClick={submit} disabled={submitting || !cronExpr.trim()}>
            {submitting ? "Saving…" : "Save"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
