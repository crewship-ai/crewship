"use client"

import { routinePresetSummary } from "@/lib/routine-preset-summary"

import { useEffect, useMemo, useRef, useState } from "react"
import { Plus, Trash2, Calendar, Power, PowerOff, Pencil } from "lucide-react"
import {
  usePipelineSchedules,
  type PipelineSchedule,
  type SchedulePatchBody,
} from "@/hooks/use-pipeline-schedules"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { RoutineListSkeleton } from "./routine-skeletons"
import { Card, Pill, FieldLabel } from "./_shared"
import { WakeGateChip } from "./routine-wake-gate-chip"
import { describeCron } from "@/lib/cron-describe"
import { scheduleHealth } from "@/lib/schedule-health"
import { RoutineOnceSchedule } from "./routine-once-schedule"
import { RoutineRecurrenceFields } from "./routine-recurrence-fields"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog"
import { RoutinePresetForm } from "./routine-preset-form"
import { RoutineScheduleEditorDialog } from "./routine-schedule-editor-dialog"

// RoutineSchedulesTab — cron-trigger CRUD restyled for the dashboard.
// Card-wrapped list + inline form, Pill states, readable typography,
// describeCron hint stays as a human sanity-check.

interface Props {
  workspaceId: string
  pipelineId: string
  slug: string
  /**
   * §13.2 "If it overlaps" — concurrency_key/max_concurrent (F18). These are
   * DSL fields on the ROUTINE, shared by every schedule/webhook/manual run
   * of it, not a per-schedule setting — so this tab shows them read-only
   * rather than offering a control that would silently change every
   * trigger's behaviour from one schedule's dialog. The routine's own
   * Editor tab (POST .../pipelines/save) is the existing, correct door.
   */
  concurrencyKey?: string
  maxConcurrent?: number
}

export function RoutineSchedulesTab({
  workspaceId,
  pipelineId,
  slug,
  concurrencyKey,
  maxConcurrent,
}: Props) {
  const { schedules, loading, error, create, update, remove, preview } =
    usePipelineSchedules(workspaceId)
  const ours = useMemo(
    () =>
      schedules.filter(
        (s) => s.target_pipeline_id === pipelineId || s.target_pipeline_slug === slug,
      ),
    [schedules, pipelineId, slug],
  )
  const focusedSchedule = useRef<string | null>(null)
  useEffect(() => {
    let target: string
    try {
      target = decodeURIComponent(window.location.hash.slice(1))
    } catch {
      return
    }
    if (
      !target ||
      focusedSchedule.current === target ||
      !ours.some((s) => `schedule-${s.id}` === target)
    )
      return
    const row = document.getElementById(target)
    if (!row) return
    row.scrollIntoView?.({ block: "center" })
    row.focus({ preventScroll: true })
    focusedSchedule.current = target
  }, [ours])

  // Reliability editor (B9, #2362) — every §13.2 row, opened per schedule.
  const [editing, setEditing] = useState<PipelineSchedule | null>(null)
  const [editSaving, setEditSaving] = useState(false)
  const saveEdit = async (body: SchedulePatchBody) => {
    if (!editing) return
    setEditSaving(true)
    try {
      await update(editing.id, body)
      toast.success("Schedule updated")
      setEditing(null)
    } catch (e) {
      toast.error("Update failed", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setEditSaving(false)
    }
  }

  const [formOpen, setFormOpen] = useState(false)
  const [name, setName] = useState("")
  const [cronExpr, setCronExpr] = useState("0 9 * * *")
  const [timezone, setTimezone] = useState(
    Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
  )
  const [editingInputs, setEditingInputs] = useState<PipelineSchedule | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (inputs: Record<string, unknown>) => {
    setBusy(true)
    try {
      await create({
        name: name || `${slug} schedule`,
        target_pipeline_slug: slug,
        cron_expr: cronExpr,
        timezone,
        inputs,
        enabled: true,
      })
      toast.success("Schedule created")
      setFormOpen(false)
      setName("")
      setCronExpr("0 9 * * *")
    } catch (e) {
      toast.error("Create failed", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }

  const toggle = async (s: PipelineSchedule) => {
    try {
      await update(s.id, { cron_expr: s.cron_expr, enabled: !s.enabled })
      toast.success(s.enabled ? "Disabled" : "Enabled")
    } catch (e) {
      toast.error("Toggle failed", { description: e instanceof Error ? e.message : String(e) })
    }
  }

  const del = async (s: PipelineSchedule) => {
    if (!confirm(`Delete schedule "${s.name}"?`)) return
    try {
      await remove(s.id)
      toast.success("Schedule deleted")
    } catch (e) {
      toast.error("Delete failed", { description: e instanceof Error ? e.message : String(e) })
    }
  }

  if (loading && ours.length === 0) {
    return (
      <Card title="Schedules" subtitle="loading…">
        <div className="p-4">
          <RoutineListSkeleton rows={2} />
        </div>
      </Card>
    )
  }

  return (
    <div className="space-y-4">
      {error && (
        <Card tone="warn">
          <div className="px-4 py-3 text-sm text-warn">{error}</div>
        </Card>
      )}

      <section aria-label="Schedules" className="space-y-4">
        {/* List card */}
        {ours.length === 0 && !formOpen ? (
          <Card title="Repeating schedules">
            <div className="flex flex-wrap items-center justify-between gap-3 p-4">
              <p className="text-sm text-muted-foreground">
                Choose the days and time this routine should repeat.
              </p>
              <Button size="sm" onClick={() => setFormOpen(true)}>
                <Plus className="mr-1 h-3.5 w-3.5" />
                Add schedule
              </Button>
            </div>
          </Card>
        ) : ours.length > 0 ? (
          <Card
            title="Repeating schedules"
            subtitle={`${ours.length} for this routine`}
            action={
              !formOpen && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setFormOpen(true)}
                  className="h-8 gap-1.5 text-xs"
                >
                  <Plus className="h-3 w-3" />
                  Add schedule
                </Button>
              )
            }
          >
            <ol className="divide-y divide-border/40">
              {ours.map((s) => {
                const health = scheduleHealth(s)
                return (
                  <li
                    key={s.id}
                    id={`schedule-${s.id}`}
                    tabIndex={-1}
                    className="grid scroll-mt-4 grid-cols-[auto_minmax(0,1fr)_auto] items-start gap-3 px-4 py-3 target:bg-muted target:ring-1 target:ring-inset target:ring-border"
                  >
                    <div
                      className={cn(
                        "flex h-9 w-9 shrink-0 items-center justify-center rounded-lg",
                        s.enabled
                          ? "bg-purple/20 text-purple"
                          : "bg-muted text-muted-foreground",
                      )}
                    >
                      <Calendar className="h-4 w-4" />
                    </div>
                    <div className="min-w-0 space-y-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="truncate text-sm font-semibold">{s.name}</span>
                        <Pill tone={s.enabled ? "purple" : "default"}>
                          {s.enabled ? "enabled" : "paused"}
                        </Pill>
                        {/* Health — read-only. F18/A6: this is the only place a
                        circuit-breaker-disabled schedule's reason is shown;
                        the editor to change breaker settings is Track B. */}
                        {(health.tone === "destructive" || health.tone === "warn") && (
                          <Pill tone={health.tone} data-testid={`schedule-health-${s.id}`}>
                            {health.label}
                          </Pill>
                        )}
                        <WakeGateChip wakePipelineSlug={s.wake_pipeline_slug} />
                      </div>
                      <div className="text-xs text-muted-foreground">
                        <span className="text-foreground/80">{describeCron(s.cron_expr)}</span>
                        <span> · {s.timezone}</span>
                      </div>
                      {health.reason && (
                        <p
                          className={cn(
                            "text-[11px] leading-relaxed",
                            health.tone === "destructive" ? "text-destructive" : "text-warn",
                          )}
                          data-testid={`schedule-health-reason-${s.id}`}
                        >
                          {health.reason}
                        </p>
                      )}
                      {(s.next_run_at || s.last_run_at) && (
                        <div className="flex flex-wrap items-center gap-x-3 text-[11px] text-muted-foreground">
                          {s.next_run_at && (
                            <span>
                              Next:{" "}
                              <span className="text-foreground/85">
                                {new Date(s.next_run_at).toLocaleString("en-GB")}
                              </span>
                            </span>
                          )}
                          {s.last_run_at && (
                            <span>
                              Last:{" "}
                              <span className="text-foreground/85">
                                {new Date(s.last_run_at).toLocaleString("en-GB")}
                              </span>
                              {s.last_status && (
                                <span className="ml-1 text-muted-foreground">
                                  ({s.last_status})
                                </span>
                              )}
                            </span>
                          )}
                        </div>
                      )}
                      <p className="truncate text-xs text-muted-foreground">{routinePresetSummary(s.inputs)}</p>
                      <button
                        type="button"
                        className="text-xs underline underline-offset-4"
                        onClick={() => setEditingInputs(s)}
                      >
                        Edit inputs for {s.name}
                      </button>
                      {/* Reliability telemetry — read-only (F18). Consecutive
                      failures always shown so a slide toward the breaker is
                      visible before it trips; catch-up and wake-gate stats
                      only when they have ever fired, to keep a clean
                      schedule's row from being cluttered with zeros. */}
                      <div className="flex flex-wrap items-center gap-x-3 text-[11px] text-muted-foreground">
                        <span>
                          Failures:{" "}
                          <span className="text-foreground/85">{s.consecutive_failures}</span>
                          <span className="opacity-60">
                            /{s.max_consecutive_failures || "—"}
                          </span>
                        </span>
                        {s.catchup_policy && (
                          <span>
                            Catch-up:{" "}
                            <span className="text-foreground/85">{s.catchup_policy}</span>
                          </span>
                        )}
                        {!!s.last_missed_count && (
                          <span className="text-warn">
                            Missed last tick: {s.last_missed_count}
                          </span>
                        )}
                        {s.wake_pipeline_slug && (
                          <span>
                            Wake gate:{" "}
                            <span className="text-foreground/85">{s.wake_fire_count ?? 0}</span>{" "}
                            fired /{" "}
                            <span className="text-foreground/85">
                              {s.wake_check_count ?? 0}
                            </span>{" "}
                            checked
                            {s.last_wake_status && (
                              <span className="ml-1 opacity-80">({s.last_wake_status})</span>
                            )}
                          </span>
                        )}
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-1">
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => setEditing(s)}
                        className="h-8 w-8 p-0"
                        title="Edit reliability settings"
                        aria-label={`Edit schedule ${s.name}`}
                      >
                        <Pencil className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => toggle(s)}
                        className="h-8 w-8 p-0"
                        title={s.enabled ? "Disable" : "Enable"}
                        aria-label={`${s.enabled ? "Disable" : "Enable"} schedule ${s.name}`}
                      >
                        {s.enabled ? (
                          <PowerOff className="h-3.5 w-3.5" />
                        ) : (
                          <Power className="h-3.5 w-3.5" />
                        )}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => del(s)}
                        className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
                        title="Delete"
                        aria-label={`Delete schedule ${s.name}`}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  </li>
                )
              })}
            </ol>
          </Card>
        ) : null}

        {/* Inline form */}
        {formOpen && (
          <Card title="New schedule">
            <div className="space-y-4 p-4">
              <div>
                <FieldLabel>Name</FieldLabel>
                <Input
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={`${slug} schedule`}
                  className="mt-1.5 h-9 text-sm"
                />
              </div>
              <RoutineRecurrenceFields
                cron={cronExpr}
                timezone={timezone}
                onCronChange={setCronExpr}
                onTimezoneChange={setTimezone}
              />
              <RoutinePresetForm
                workspaceId={workspaceId}
                slug={slug}
                submitting={busy}
                onCancel={() => setFormOpen(false)}
                onSave={submit}
                submitLabel="Create schedule"
              />
            </div>
          </Card>
        )}

        {editingInputs && (
          <Dialog
            open
            onOpenChange={(open) => {
              if (!open && !busy) setEditingInputs(null)
            }}
          >
            <DialogContent className="max-h-[90dvh] overflow-y-auto">
              <DialogHeader>
                <DialogTitle>Inputs for {editingInputs.name}</DialogTitle>
                <DialogDescription>
                  These values are supplied to every scheduled start.
                </DialogDescription>
              </DialogHeader>
              <RoutinePresetForm
                key={editingInputs.id}
                workspaceId={workspaceId}
                slug={slug}
                version={editingInputs.target_pipeline_version}
                initialInputs={editingInputs.inputs}
                submitting={busy}
                allowDraft
                onCancel={() => setEditingInputs(null)}
                onSave={async (inputs) => {
                  setBusy(true)
                  try {
                    await update(editingInputs.id, { inputs })
                    setEditingInputs(null)
                    toast.success("Schedule inputs updated")
                  } catch (e) {
                    toast.error("Could not update inputs", {
                      description: e instanceof Error ? e.message : String(e),
                    })
                  } finally {
                    setBusy(false)
                  }
                }}
              />
            </DialogContent>
          </Dialog>
        )}

        <RoutineOnceSchedule key={slug} workspaceId={workspaceId} slug={slug} />
      </section>
      <details className="rounded-xl border border-border/60 bg-card p-4">
        <summary className="cursor-pointer text-xs text-muted-foreground">
          Advanced execution settings
        </summary>
        {/* §13.2 "If it overlaps" — read-only: concurrency_key/max_concurrent
          are routine-wide DSL fields, not per-schedule, so the writable
          door is the Editor tab, not a control here. */}
        <Card title="If it overlaps" subtitle="applies to every trigger of this routine">
          <div
            className="px-4 py-3 text-[12px] text-muted-foreground"
            data-testid="schedule-concurrency-readonly"
          >
            {concurrencyKey ? (
              <>
                Serialized by{" "}
                <span className="font-mono text-foreground/85">{concurrencyKey}</span>, up to{" "}
                <span className="text-foreground/85">
                  {maxConcurrent && maxConcurrent > 0 ? maxConcurrent : 1}
                </span>{" "}
                at once — a new run beyond that limit is rejected (429), not queued.
              </>
            ) : (
              "No overlap limit is configured. Runs can start at the same time."
            )}{" "}
            Change it using Code in the recipe editor.
            <details className="mt-2">
              <summary className="cursor-pointer">Technical details</summary>
              <span className="font-mono">concurrency_key</span> /{" "}
              <span className="font-mono">max_concurrent</span>
            </details>
          </div>
        </Card>
      </details>

      <RoutineScheduleEditorDialog
        schedule={editing}
        submitting={editSaving}
        onCancel={() => setEditing(null)}
        onSave={saveEdit}
        onPreview={preview}
      />
    </div>
  )
}
