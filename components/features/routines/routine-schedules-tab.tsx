"use client"

import { formatRoutineTime } from "@/lib/routine-time"

import { routinePresetSummary } from "@/lib/routine-preset-summary"

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react"
import { Plus, Trash2, Pencil } from "lucide-react"
import {
  usePipelineSchedules,
  type PipelineSchedule,
  type SchedulePatchBody,
} from "@/hooks/use-pipeline-schedules"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
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

// RoutineSchedulesTab — the Plan: a Repeating card and a One-time starts card
// (operator console proposal, screen 2 · Plan). Each row says in one line
// when it starts, which version it uses (latest published vs pinned) and
// with what answers; the switch is the existing enable/disable door.

interface Props {
  workspaceId: string
  pipelineId: string
  slug: string
  /** The routine's published head — "Uses the latest published version (now vN)". */
  headVersion?: number | null
  /** The routine's unpublished draft, named on a one-time start so the reader
   * knows it is not what will run. */
  draft?: { revision: number } | null
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
  /** Webhooks and automations, rendered by the detail page, fold under the
   * "Other ways this routine starts" disclosure at the bottom. */
  otherWays?: ReactNode
}

/** "Weekdays at 08:00" — the words a person would use, so a new schedule is
 * named after what it does instead of the routine's slug. */
export function defaultScheduleName(cronExpr: string): string {
  const words = describeCron(cronExpr)
  if (words === "—" || words === cronExpr.trim()) return "Repeating schedule"
  return words.replace(/^Every weekday at/, "Weekdays at").replace(/^Every weekend day at/, "Weekends at")
}

/** Which version a schedule's starts use, from the contract fields with a
 * fallback to the pin alone on older servers. */
export function scheduleVersionLine(
  s: Pick<PipelineSchedule, "target_pipeline_version" | "effective_version" | "version_pinned">,
  headVersion?: number | null,
): { pinned: boolean; version: number | null } {
  const pinned = s.version_pinned ?? s.target_pipeline_version != null
  if (pinned) return { pinned: true, version: s.effective_version ?? s.target_pipeline_version ?? null }
  return { pinned: false, version: s.effective_version ?? headVersion ?? null }
}

export function RoutineSchedulesTab({
  workspaceId,
  pipelineId,
  slug,
  headVersion,
  draft,
  concurrencyKey,
  maxConcurrent,
  otherWays,
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
      toast.success("Schedule saved")
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
        name: name || defaultScheduleName(cronExpr),
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
      toast.success(
        s.enabled
          ? "Schedule off · nothing starts automatically"
          : `Schedule on${s.next_run_at ? ` · next ${formatRoutineTime(s.next_run_at, s.timezone)}` : ""}`,
      )
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
      <Card title="Repeating" subtitle="loading…">
        <div className="p-4">
          <RoutineListSkeleton rows={2} />
        </div>
      </Card>
    )
  }

  const addButton = (
    <Button
      size="sm"
      variant="outline"
      onClick={() => setFormOpen(true)}
      className="h-8 gap-1.5 text-xs"
    >
      <Plus className="h-3 w-3" />
      Add
    </Button>
  )

  return (
    <div className="space-y-4">
      {error && (
        <Card tone="warn">
          <div className="px-4 py-3 text-sm text-warn">{error}</div>
        </Card>
      )}

      <section aria-label="Schedules" className="space-y-4">
        <Card
          title="Repeating"
          subtitle={ours.length ? `${ours.length} for this routine` : undefined}
          action={!formOpen && addButton}
        >
          {ours.length === 0 && !formOpen ? (
            <p className="px-4 py-3 text-sm text-muted-foreground">
              No repeating schedule. Add one to choose the days and time this routine repeats.
            </p>
          ) : (
            <ol className="divide-y divide-border/40">
              {ours.map((s) => {
                const health = scheduleHealth(s)
                const uses = scheduleVersionLine(s, headVersion)
                const isDraft = s.activation === "draft"
                return (
                  <li
                    key={s.id}
                    id={`schedule-${s.id}`}
                    tabIndex={-1}
                    className="grid scroll-mt-4 grid-cols-[auto_minmax(0,1fr)] items-start gap-3 px-4 py-3 target:bg-muted target:ring-1 target:ring-inset target:ring-border md:grid-cols-[auto_minmax(0,1fr)_auto]"
                  >
                    <Switch
                      checked={s.enabled}
                      disabled={isDraft}
                      aria-label={`${s.enabled ? "Disable" : "Enable"} schedule ${s.name}`}
                      title={
                        isDraft
                          ? "Awaiting activation by a manager"
                          : s.enabled
                            ? "On — starts automatically"
                            : "Off — nothing starts automatically"
                      }
                      className="mt-1"
                      onCheckedChange={() => toggle(s)}
                    />
                    <div className="min-w-0 space-y-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-sm font-semibold">{describeCron(s.cron_expr)}</span>
                        <span className="text-xs text-muted-foreground">· {s.timezone}</span>
                        {s.name && s.name !== describeCron(s.cron_expr) && (
                          <span className="truncate text-xs text-muted-foreground">· {s.name}</span>
                        )}
                        {isDraft && <Pill tone="purple">awaiting activation</Pill>}
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
                      <p className="text-xs text-muted-foreground" data-testid={`schedule-uses-${s.id}`}>
                        {uses.pinned ? (
                          <>
                            Pinned to{" "}
                            <span className="font-medium text-foreground/85">
                              v{uses.version ?? "?"}
                            </span>{" "}
                            — publishing a newer version does not change this start
                          </>
                        ) : (
                          <>
                            Uses the{" "}
                            <span className="font-medium text-foreground/85">latest published</span>{" "}
                            version{uses.version ? ` (now v${uses.version})` : ""}
                          </>
                        )}
                        {s.enabled
                          ? s.next_run_at
                            ? ` · next ${formatRoutineTime(s.next_run_at, s.timezone)}`
                            : ""
                          : " · off"}
                        {" · with: "}
                        {routinePresetSummary(s.inputs).replace(/^Inputs: /, "")}
                      </p>
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
                      {/* Reliability telemetry — read-only (F18). Consecutive
                      failures always shown so a slide toward the breaker is
                      visible before it trips; catch-up and wake-gate stats
                      only when they have ever fired, to keep a clean
                      schedule's row from being cluttered with zeros. */}
                      <div className="flex flex-wrap items-center gap-x-3 text-[11px] text-muted-foreground">
                        {s.last_run_at && (
                          <span>
                            Last:{" "}
                            <span className="text-foreground/85">
                              {formatRoutineTime(s.last_run_at, s.timezone)}
                            </span>
                            {s.last_status && (
                              <span className="ml-1 text-muted-foreground">({s.last_status})</span>
                            )}
                          </span>
                        )}
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
                    <div className="col-start-2 flex shrink-0 items-center gap-1 md:col-start-3">
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => setEditing(s)}
                        className="h-8 text-xs"
                        aria-label={`Edit schedule ${s.name}`}
                      >
                        <Pencil className="mr-1 h-3 w-3" />
                        Edit
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => setEditingInputs(s)}
                        className="h-8 text-xs"
                        aria-label={`Edit inputs for ${s.name}`}
                      >
                        Answers
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
          )}
        </Card>

        {/* Inline form */}
        {formOpen && (
          <Card title="New repeating schedule">
            <div className="space-y-4 p-4">
              <RoutineRecurrenceFields
                cron={cronExpr}
                timezone={timezone}
                onCronChange={setCronExpr}
                onTimezoneChange={setTimezone}
              />
              <div>
                <FieldLabel>Name</FieldLabel>
                <Input
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={defaultScheduleName(cronExpr)}
                  className="mt-1.5 h-9 text-sm"
                />
                <p className="mt-1 text-[11px] text-muted-foreground">
                  Leave it empty to name it after the days and time. New schedules use the
                  latest published version{headVersion ? ` (now v${headVersion})` : ""}; pin one
                  in Edit.
                </p>
              </div>
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
                <DialogTitle>Answers for {editingInputs.name}</DialogTitle>
                <DialogDescription>
                  These values are supplied to every start of this schedule.
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
                    toast.success("Schedule answers updated")
                  } catch (e) {
                    toast.error("Could not update the answers", {
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

        <RoutineOnceSchedule
          key={slug}
          workspaceId={workspaceId}
          slug={slug}
          headVersion={headVersion}
          draft={draft}
        />
      </section>
      <details className="rounded-xl border border-dashed border-border/60 bg-card px-4 py-2">
        <summary className="cursor-pointer py-1 text-xs font-medium text-muted-foreground">
          Other ways this routine starts · webhooks, automations, overlap rules
        </summary>
        <div className="mt-3 space-y-4">
          {otherWays}
          {/* §13.2 "If it overlaps" — read-only: concurrency_key/max_concurrent
            are routine-wide DSL fields, not per-schedule, so the writable
            door is the recipe, not a control here. */}
          <Card title="If a run is already going" subtitle="applies to every start of this routine">
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
              Change it with the CLI (
              <span className="font-mono">crewship routine draft {slug}</span>).
              <details className="mt-2">
                <summary className="cursor-pointer">Technical details</summary>
                <span className="font-mono">concurrency_key</span> /{" "}
                <span className="font-mono">max_concurrent</span>
              </details>
            </div>
          </Card>
        </div>
      </details>

      <RoutineScheduleEditorDialog
        schedule={editing}
        headVersion={headVersion}
        submitting={editSaving}
        onCancel={() => setEditing(null)}
        onSave={saveEdit}
        onPreview={preview}
      />
    </div>
  )
}
