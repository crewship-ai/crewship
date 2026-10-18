"use client"

// The routine page's Triggers (Harbor 2 RT-TRIG): one list of everything that
// starts this routine — schedules, planned one-time starts, webhooks, the chat
// command, workspace events and the Run button — then Add trigger and the
// webhook delivery log. Each row is managed where it is listed (Edit and the
// ⋯ menu); nothing below the list repeats it (RT-08).
//
// Who may do what is the server's: a schedule is created with the routine
// create role or the routine.create capability, changed and deleted by Owner
// and Admin; a webhook is created by Manager and above and changed or deleted
// by Owner and Admin; a one-time start is a run (fire_at) and is cancelled by
// whoever the server allows (`can_cancel`).

import * as React from "react"
import { CalendarClock, Clock, Globe, Hand, MessageSquare, MoreHorizontal, Plus, Webhook, Zap } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Segmented } from "@/components/ui/segmented"
import { DetailCard } from "@/components/ui/detail"
import { StatusPill } from "@/components/ui/status-pill"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
} from "@/components/layout/create-surface"
import { toast } from "@/lib/toast"
import { apiFetch } from "@/lib/api-fetch"
import { useAbilities } from "@/hooks/use-abilities"
import { routinePermissions } from "@/lib/routine-governance"
import { formatRoutineTime } from "@/lib/routine-time"
import { routineInputSpecs, type RoutineInputSpec } from "@/lib/routine-inputs"
import { usePipelineSchedules, type PipelineSchedule, type SchedulePatchBody } from "@/hooks/use-pipeline-schedules"
import { usePipelineWebhooks, type PipelineWebhook, type WebhookUpdateBody } from "@/hooks/use-pipeline-webhooks"
import { usePendingStarts } from "@/hooks/use-pending-starts"
import { useAutomations } from "@/hooks/use-automations"
import { automationsForRoutine } from "@/lib/automations"
import { isRecord } from "@/lib/routine-step-describe"
import { pendingStartRows, routineTriggerRows, type DetailDepth, type TriggerKind, type TriggerRow } from "@/lib/routine-page"
import { RoutineRecurrenceFields } from "./routine-recurrence-fields"
import { RoutinePresetForm } from "./routine-preset-form"
import { RoutineRunInputsDialog, inputsSubmitContent, submitInputsForm } from "./routine-run-inputs-dialog"
import { defaultScheduleName } from "./routine-schedules-tab"
import { RoutineScheduleEditorDialog } from "./routine-schedule-editor-dialog"
import { RoutineWebhookEditorDialog } from "./routine-webhook-editor-dialog"
import { CreatedReveal } from "./routine-webhooks-tab"
import type { RoutineDetail } from "./routines-detail-panel"

const KIND_ICON: Record<TriggerKind, React.ComponentType<{ className?: string }>> = {
  schedule: CalendarClock,
  once: Clock,
  webhook: Webhook,
  chat: MessageSquare,
  event: Zap,
  manual: Hand,
}

const MANAGE_REASON = "Changing triggers needs Owner or Admin"

/** The rows the page counts in the tab label and shows on Overview. */
export function useRoutineTriggers(workspaceId: string, routine: RoutineDetail, lifecycle: "active" | "proposed" | "disabled"): TriggerRow[] {
  const { schedules } = usePipelineSchedules(workspaceId)
  const { webhooks } = usePipelineWebhooks(workspaceId)
  const { automations } = useAutomations(workspaceId)
  return React.useMemo(() => {
    const def = isRecord(routine.definition) ? routine.definition : {}
    const slash = isRecord(def.slash) ? (def.slash as { enabled?: boolean; label?: string }) : null
    return routineTriggerRows({
      slug: routine.slug,
      lifecycle,
      schedules: schedules.filter((s) => s.target_pipeline_id === routine.id || s.target_pipeline_slug === routine.slug),
      webhooks: webhooks.filter((w) => w.target_pipeline_id === routine.id || w.target_pipeline_slug === routine.slug),
      automations: automationsForRoutine(automations, routine.slug),
      slash,
      formatTime: (iso, tz) => formatRoutineTime(iso, tz),
    })
  }, [schedules, webhooks, automations, routine.id, routine.slug, routine.definition, lifecycle])
}

interface Receipt {
  id: string
  webhook_id: string
  run_id: string
  run_status: string | null
  received_at: string
}

/** The last deliveries of this routine's webhooks (GET /routine-webhook-receipts). */
function useWebhookReceipts(workspaceId: string, webhookIds: string[]) {
  const key = webhookIds.join(",")
  const [state, setState] = React.useState<{ rows: Receipt[]; error: boolean; loading: boolean }>({ rows: [], error: false, loading: false })
  React.useEffect(() => {
    if (!key) {
      setState({ rows: [], error: false, loading: false })
      return
    }
    const controller = new AbortController()
    setState((s) => ({ ...s, loading: true }))
    Promise.all(
      key.split(",").map(async (id) => {
        const res = await apiFetch(
          `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/routine-webhook-receipts?webhook_id=${encodeURIComponent(id)}`,
          { signal: controller.signal },
        )
        if (!res.ok) throw new Error("receipts")
        const body = (await res.json()) as { items?: Receipt[] }
        return body.items ?? []
      }),
    )
      .then((pages) => {
        if (controller.signal.aborted) return
        const rows = pages.flat().sort((a, b) => b.received_at.localeCompare(a.received_at)).slice(0, 10)
        setState({ rows, error: false, loading: false })
      })
      .catch(() => {
        if (!controller.signal.aborted) setState({ rows: [], error: true, loading: false })
      })
    return () => controller.abort()
  }, [workspaceId, key])
  return state
}

type AddKind = "schedule" | "once" | "webhook" | "chat"

export function RoutineTriggersTab({
  workspaceId,
  routine,
  lifecycle,
  depth,
  rows,
  onOpenRun,
  onSettings,
}: {
  workspaceId: string
  routine: RoutineDetail
  lifecycle: "active" | "proposed" | "disabled"
  depth: DetailDepth
  rows: TriggerRow[]
  onOpenRun: (runId: string) => void
  onSettings: () => void
}) {
  const { role, capabilities } = useAbilities()
  const permissions = routinePermissions(role, capabilities)
  const { schedules, create, update, remove, preview } = usePipelineSchedules(workspaceId)
  const { webhooks, create: createWebhook, update: updateWebhook, remove: removeWebhook } = usePipelineWebhooks(workspaceId)
  const { starts, refresh: refreshStarts } = usePendingStarts(workspaceId)
  const ourWebhooks = React.useMemo(
    () => webhooks.filter((w) => w.target_pipeline_id === routine.id || w.target_pipeline_slug === routine.slug),
    [webhooks, routine.id, routine.slug],
  )
  const receipts = useWebhookReceipts(workspaceId, ourWebhooks.map((w) => w.id))
  const webhookName = (id: string) => ourWebhooks.find((w) => w.id === id)?.name ?? "Webhook"
  const [adding, setAdding] = React.useState<AddKind | null>(null)
  const [planning, setPlanning] = React.useState<RoutineInputSpec[] | null>(null)
  const [planBusy, setPlanBusy] = React.useState(false)
  const [editingSchedule, setEditingSchedule] = React.useState<PipelineSchedule | null>(null)
  const [answersFor, setAnswersFor] = React.useState<PipelineSchedule | null>(null)
  const [deletingSchedule, setDeletingSchedule] = React.useState<PipelineSchedule | null>(null)
  const [editingWebhook, setEditingWebhook] = React.useState<PipelineWebhook | null>(null)
  const [deletingWebhook, setDeletingWebhook] = React.useState<PipelineWebhook | null>(null)
  const [reveal, setReveal] = React.useState<{ webhook: PipelineWebhook; mode: "created" | "rotated" } | null>(null)
  const [busy, setBusy] = React.useState(false)
  const published = (routine.head_version ?? 0) > 0 && !routine.draft_only
  const def = isRecord(routine.definition) ? routine.definition : {}
  const concurrency =
    typeof def.max_concurrent === "number" && def.max_concurrent > 1
      ? `Up to ${def.max_concurrent} runs at once.`
      : typeof def.concurrency_key === "string" && def.concurrency_key
        ? "One at a time; a new start waits in line."
        : "Runs can start at the same time."

  // The list: the page's rows with the planned one-time starts after the schedules.
  const onceRows = React.useMemo(
    () => pendingStartRows(starts, routine.slug, { canCancel: permissions.author, formatTime: (iso) => formatRoutineTime(iso) }),
    [starts, routine.slug, permissions.author],
  )
  // Schedules and webhooks come from this tab's own lists — the ones Add,
  // Edit, Delete and the On switch write through — so a save shows at once;
  // the page's rows were computed from another copy that does not refresh.
  const ownRows = React.useMemo(
    () =>
      routineTriggerRows({
        slug: routine.slug,
        lifecycle,
        schedules: schedules.filter((s) => s.target_pipeline_id === routine.id || s.target_pipeline_slug === routine.slug),
        webhooks: ourWebhooks,
        automations: [],
        formatTime: (iso, tz) => formatRoutineTime(iso, tz),
      }).filter((r) => r.kind === "schedule" || r.kind === "webhook"),
    [schedules, ourWebhooks, routine.id, routine.slug, lifecycle],
  )
  const listed = React.useMemo(() => {
    const base = [...ownRows, ...rows.filter((r) => r.kind !== "once" && r.kind !== "schedule" && r.kind !== "webhook")]
    const at = base.findIndex((r) => r.kind !== "schedule")
    return at < 0 ? [...base, ...onceRows] : [...base.slice(0, at), ...onceRows, ...base.slice(at)]
  }, [ownRows, rows, onceRows])

  // Deep links (⌘K schedule hits, the calendar) end in #schedule-<id>: bring
  // that row into view and focus it once it is listed, as the Plan tab did.
  const focusedAnchor = React.useRef<string | null>(null)
  React.useEffect(() => {
    let target: string
    try {
      target = decodeURIComponent(window.location.hash.slice(1))
    } catch {
      return
    }
    if (!target || focusedAnchor.current === target || !listed.some((r) => r.anchor === target)) return
    const el = document.getElementById(target)
    if (!el) return
    el.scrollIntoView?.({ block: "center" })
    el.focus({ preventScroll: true })
    focusedAnchor.current = target
  }, [listed])

  const scheduleOf = (row: TriggerRow) => schedules.find((s) => s.id === row.id) ?? null
  const webhookOf = (row: TriggerRow) => ourWebhooks.find((w) => w.id === row.id) ?? null

  const toggleSchedule = async (s: PipelineSchedule) => {
    try {
      await update(s.id, { cron_expr: s.cron_expr, enabled: !s.enabled })
      toast.success(s.enabled ? "Schedule off · nothing starts automatically" : `Schedule on${s.next_run_at ? ` · next ${formatRoutineTime(s.next_run_at, s.timezone)}` : ""}`)
    } catch (e) {
      toast.error("Could not change the schedule", { description: e instanceof Error ? e.message : String(e) })
    }
  }
  const saveSchedule = async (body: SchedulePatchBody) => {
    if (!editingSchedule) return
    setBusy(true)
    try {
      await update(editingSchedule.id, body)
      toast.success("Schedule saved")
      setEditingSchedule(null)
    } catch (e) {
      toast.error("Update failed", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }
  const saveWebhook = async (body: WebhookUpdateBody) => {
    if (!editingWebhook) return
    setBusy(true)
    try {
      const updated = await updateWebhook(editingWebhook.id, body)
      if (updated?.signing_secret) {
        setReveal({ webhook: updated, mode: "rotated" })
        toast.success("Secret rotated", { description: "Copy the new signing secret now — it won't be shown again" })
      } else toast.success("Webhook updated")
      setEditingWebhook(null)
    } catch (e) {
      toast.error("Update failed", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }
  const cancelStart = async (id: string) => {
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/pending/${encodeURIComponent(id)}/cancel`, { method: "POST" })
      if (!res.ok) throw new Error("The start may already have run. Refresh the runs.")
      toast.success("Start cancelled")
      await refreshStarts()
    } catch (e) {
      toast.error("Could not cancel the start", { description: e instanceof Error ? e.message : String(e) })
    }
  }
  const planStart = async (inputs: Record<string, unknown>, fireAt: string, pinnedVersion: number | null) => {
    setPlanBusy(true)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(routine.slug)}/run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ inputs, fire_at: fireAt, ...(pinnedVersion != null ? { pinned_version: pinnedVersion } : {}) }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => null)
        throw new Error((data && data.error) || `The start was refused (${res.status}).`)
      }
      toast.success("Start planned", { description: formatRoutineTime(fireAt) })
      setPlanning(null)
      await refreshStarts()
    } catch (e) {
      toast.error("Could not plan the start", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setPlanBusy(false)
    }
  }

  if (!published) {
    return (
      <DetailCard title="Triggers" icon={Clock}>
        <p className="text-sm text-muted-foreground">
          Not live yet. Schedules, webhooks and one-time starts are added once the routine is published; a draft cannot start.
        </p>
      </DetailCard>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      {reveal && <CreatedReveal webhook={reveal.webhook} mode={reveal.mode} onDismiss={() => setReveal(null)} />}
      <ul className="flex flex-col gap-2" aria-label="What starts this routine" data-testid="routine-trigger-list">
        {listed.map((row) => {
          const Icon = KIND_ICON[row.kind]
          const label = `${row.kindLabel} ${row.title}`
          const schedule = row.kind === "schedule" ? scheduleOf(row) : null
          const hook = row.kind === "webhook" ? webhookOf(row) : null
          return (
            <li key={row.key} id={row.anchor} tabIndex={row.anchor ? -1 : undefined} className="flex scroll-mt-4 flex-wrap items-start gap-3 rounded-lg border border-hairline bg-card px-4 py-3 target:ring-1 target:ring-inset target:ring-border sm:flex-nowrap" data-kind={row.kind}>
              <span className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-foreground/[0.05]">
                <Icon className="h-4 w-4 text-foreground/80" aria-hidden />
              </span>
              <div className="min-w-0 flex-1">
                <p className="text-micro uppercase tracking-wider text-muted-foreground">{row.kindLabel}</p>
                <p className="truncate text-sm font-medium">{row.title}</p>
                <p className="text-xs text-muted-foreground">{row.detail}</p>
                {depth === "full" && row.code && <p className="font-mono text-xs text-muted-foreground-soft">{row.code}</p>}
              </div>
              <StatusPill size="md" tone={row.status.tone} label={row.status.label} />
              {(schedule || hook) && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="shrink-0 coarse:h-[44px]"
                  disabled={!permissions.manage}
                  title={permissions.manage ? undefined : MANAGE_REASON}
                  aria-label={`Edit ${row.kindLabel.toLowerCase()} ${row.title}`}
                  onClick={() => (schedule ? setEditingSchedule(schedule) : hook && setEditingWebhook(hook))}
                >
                  Edit
                </Button>
              )}
              {row.kind === "chat" && (
                <Button type="button" variant="ghost" size="sm" className="shrink-0 coarse:h-[44px]" aria-label={`Edit chat command ${row.title}`} onClick={onSettings}>
                  Edit
                </Button>
              )}
              {row.kind === "once" && row.cancellable && row.id && (
                <Button type="button" variant="ghost" size="sm" className="shrink-0 coarse:h-[44px]" aria-label={`Cancel this start ${row.title}`} onClick={() => void cancelStart(row.id!)}>
                  Cancel
                </Button>
              )}
              {(schedule || hook) && permissions.manage && (
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button type="button" variant="ghost" size="icon-sm" className="shrink-0 coarse:h-[44px] coarse:w-[44px]" aria-label={`Options for ${label}`}>
                      <MoreHorizontal className="h-3.5 w-3.5" aria-hidden />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    {schedule && (
                      <>
                        <DropdownMenuItem disabled={schedule.activation === "draft"} onSelect={() => void toggleSchedule(schedule)}>
                          {schedule.enabled ? "Turn off" : "Turn on"}
                        </DropdownMenuItem>
                        <DropdownMenuItem onSelect={() => setAnswersFor(schedule)}>Answers…</DropdownMenuItem>
                        <DropdownMenuItem className="text-destructive" onSelect={() => setDeletingSchedule(schedule)}>
                          Delete…
                        </DropdownMenuItem>
                      </>
                    )}
                    {hook && (
                      <DropdownMenuItem className="text-destructive" onSelect={() => setDeletingWebhook(hook)}>
                        Delete…
                      </DropdownMenuItem>
                    )}
                  </DropdownMenuContent>
                </DropdownMenu>
              )}
            </li>
          )
        })}
      </ul>
      {listed.length === 1 && <p className="text-xs text-muted-foreground">Runs only when someone presses Run.</p>}

      <div className="flex flex-wrap items-center gap-3">
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="gap-1.5 coarse:h-[44px]"
          disabled={!permissions.createSchedule && !permissions.run}
          title={permissions.createSchedule || permissions.run ? undefined : "Adding a trigger needs Manager or above, or permission to create or run routines"}
          onClick={() => setAdding(permissions.createSchedule ? "schedule" : "once")}
        >
          <Plus className="h-3.5 w-3.5" aria-hidden />
          Add trigger
        </Button>
        <span className="text-xs text-muted-foreground">Schedule · One-time · Webhook · Chat command. Another routine and issues can always call it.</span>
      </div>

      {ourWebhooks.length > 0 && (
        <DetailCard title="Webhook deliveries" icon={Globe} subtitle={receipts.rows.length ? String(receipts.rows.length) : undefined} bare>
          {receipts.error ? (
            <p className="px-4 py-3 text-xs text-destructive" role="alert">The delivery log could not be loaded.</p>
          ) : receipts.rows.length === 0 ? (
            <p className="px-4 py-3 text-xs text-muted-foreground">{receipts.loading ? "Loading deliveries…" : "No delivery received yet."}</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-xs" data-testid="routine-webhook-deliveries">
                <thead className="text-left text-muted-foreground">
                  <tr className="border-b border-hairline">
                    <th className="px-4 py-2 font-medium">Received</th>
                    <th className="px-4 py-2 font-medium">Webhook</th>
                    <th className="px-4 py-2 font-medium">Run</th>
                  </tr>
                </thead>
                <tbody>
                  {receipts.rows.map((r) => (
                    <tr key={r.id} className="border-b border-hairline last:border-0">
                      <td className="px-4 py-2 font-mono">{formatRoutineTime(r.received_at)}</td>
                      <td className="px-4 py-2">{webhookName(r.webhook_id)}</td>
                      <td className="px-4 py-2">
                        <button type="button" onClick={() => onOpenRun(r.run_id)} className="inline-flex items-center gap-2 hover:underline">
                          {r.run_status ? <StatusPill status={r.run_status} /> : <span className="text-muted-foreground">run record unavailable</span>}
                          <span className="font-mono text-muted-foreground">{r.run_id.slice(0, 8)}</span>
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </DetailCard>
      )}

      <p className="text-xs text-muted-foreground" data-testid="routine-concurrency">
        If a run is already going: {concurrency} Change it in Settings › Limits.
      </p>

      {adding && (
        <AddTrigger
          kind={adding}
          onKind={setAdding}
          workspaceId={workspaceId}
          routine={routine}
          canSchedule={permissions.createSchedule}
          canWebhook={permissions.author}
          preview={preview}
          create={create}
          createWebhook={createWebhook}
          onClose={() => setAdding(null)}
          onWebhookCreated={(w) => {
            setAdding(null)
            setReveal({ webhook: w, mode: "created" })
          }}
          onOnce={() => {
            setAdding(null)
            setPlanning(routineInputSpecs(isRecord(routine.definition) ? routine.definition : null))
          }}
          onChat={() => {
            setAdding(null)
            onSettings()
          }}
        />
      )}

      {/* Add trigger › One-time is the run dialog on Later (R14). */}
      <RoutineRunInputsDialog
        definition={isRecord(routine.definition) ? routine.definition : null}
        headVersion={routine.head_version}
        inputs={planning}
        routineName={routine.name || routine.slug}
        modes={["later"]}
        submitting={planBusy}
        onCancel={() => setPlanning(null)}
        onRun={(inputs, options) => {
          if (options?.mode === "later" && options.fireAt) void planStart(inputs, options.fireAt, options.pinnedVersion ?? null)
        }}
      />

      <RoutineScheduleEditorDialog
        schedule={editingSchedule}
        headVersion={routine.head_version}
        submitting={busy}
        onCancel={() => setEditingSchedule(null)}
        onSave={(body) => void saveSchedule(body)}
        onPreview={preview}
      />
      <RoutineWebhookEditorDialog webhook={editingWebhook} submitting={busy} onCancel={() => setEditingWebhook(null)} onSave={(body) => void saveWebhook(body)} />
      {answersFor && <ScheduleAnswers workspaceId={workspaceId} slug={routine.slug} schedule={answersFor} update={update} onClose={() => setAnswersFor(null)} />}
      <ConfirmDialog
        open={deletingSchedule !== null}
        onOpenChange={(open) => !open && setDeletingSchedule(null)}
        title={`Delete schedule “${deletingSchedule?.name ?? ""}”?`}
        description="It stops starting this routine."
        consequences={[
          { tone: "lost", text: "Future starts of this schedule and its saved answers." },
          { tone: "kept", text: "Runs it already started stay in the history." },
        ]}
        confirmLabel="Delete schedule"
        destructive
        onConfirm={async () => {
          if (!deletingSchedule) return
          try {
            await remove(deletingSchedule.id)
            toast.success("Schedule deleted")
          } catch (e) {
            toast.error("Delete failed", { description: e instanceof Error ? e.message : String(e) })
            throw e
          }
        }}
      />
      <ConfirmDialog
        open={deletingWebhook !== null}
        onOpenChange={(open) => !open && setDeletingWebhook(null)}
        title={`Delete webhook “${deletingWebhook?.name ?? ""}”?`}
        description="Its URL stops accepting deliveries."
        consequences={[
          { tone: "lost", text: "Senders using this URL start failing; the URL cannot be reused." },
          { tone: "kept", text: "Runs it already started stay in the history." },
        ]}
        confirmLabel="Delete webhook"
        destructive
        onConfirm={async () => {
          if (!deletingWebhook) return
          try {
            await removeWebhook(deletingWebhook.id)
            toast.success("Webhook deleted")
          } catch (e) {
            toast.error("Delete failed", { description: e instanceof Error ? e.message : String(e) })
            throw e
          }
        }}
      />
    </div>
  )
}

/** The answers a schedule supplies to every start. */
function ScheduleAnswers({
  workspaceId,
  slug,
  schedule,
  update,
  onClose,
}: {
  workspaceId: string
  slug: string
  schedule: PipelineSchedule
  update: ReturnType<typeof usePipelineSchedules>["update"]
  onClose: () => void
}) {
  const formId = React.useId()
  const [ready, setReady] = React.useState(false)
  const [busy, setBusy] = React.useState(false)
  return (
    <CreateSurface open onOpenChange={(open) => !open && !busy && onClose()} size="sm" onSubmit={() => ready && submitInputsForm(formId)}>
      <CreateSurfaceHeader concept="routines" title={`Answers for ${schedule.name}`} description="These values are supplied to every start of this schedule." onClose={() => !busy && onClose()} />
      <CreateSurfaceBody>
        <RoutinePresetForm
          key={schedule.id}
          formId={formId}
          onReadyChange={setReady}
          workspaceId={workspaceId}
          slug={slug}
          version={schedule.target_pipeline_version}
          initialInputs={schedule.inputs}
          submitting={busy}
          allowDraft
          onCancel={onClose}
          onSave={async (inputs) => {
            setBusy(true)
            try {
              await update(schedule.id, { inputs })
              toast.success("Schedule answers updated")
              onClose()
            } catch (e) {
              toast.error("Could not update the answers", { description: e instanceof Error ? e.message : String(e) })
            } finally {
              setBusy(false)
            }
          }}
        />
      </CreateSurfaceBody>
      <CreateSurfaceFooter
        hint={null}
        onCancel={onClose}
        guardCancel={false}
        primaryLabel={ready ? inputsSubmitContent("Save inputs", busy) : undefined}
        onPrimary={ready ? () => submitInputsForm(formId) : undefined}
        busy={busy}
      />
    </CreateSurface>
  )
}

function AddTrigger({
  kind,
  onKind,
  workspaceId,
  routine,
  canSchedule,
  canWebhook,
  preview,
  create,
  createWebhook,
  onClose,
  onWebhookCreated,
  onOnce,
  onChat,
}: {
  kind: AddKind
  onKind: (k: AddKind) => void
  workspaceId: string
  routine: RoutineDetail
  canSchedule: boolean
  canWebhook: boolean
  preview: ReturnType<typeof usePipelineSchedules>["preview"]
  create: ReturnType<typeof usePipelineSchedules>["create"]
  createWebhook: ReturnType<typeof usePipelineWebhooks>["create"]
  onClose: () => void
  onWebhookCreated: (w: PipelineWebhook) => void
  onOnce: () => void
  onChat: () => void
}) {
  const [cron, setCron] = React.useState("0 9 * * *")
  const [timezone, setTimezone] = React.useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC")
  const [name, setName] = React.useState("")
  const [next, setNext] = React.useState<{ at: string[]; error: string | null }>({ at: [], error: null })
  const [busy, setBusy] = React.useState(false)
  const [ready, setReady] = React.useState(false)
  const [hookName, setHookName] = React.useState("")
  const [profile, setProfile] = React.useState<"crewship" | "github">("crewship")
  const [rateLimit, setRateLimit] = React.useState(60)
  const formId = React.useId()

  React.useEffect(() => {
    if (kind !== "schedule") return
    let live = true
    const t = setTimeout(() => {
      preview(cron, timezone, 5)
        .then((p) => live && setNext({ at: p.occurrences ?? [], error: null }))
        .catch((e) => live && setNext({ at: [], error: e instanceof Error ? e.message : "This schedule does not parse." }))
    }, 250)
    return () => {
      live = false
      clearTimeout(t)
    }
  }, [kind, cron, timezone, preview])

  const save = async (inputs: Record<string, unknown>) => {
    setBusy(true)
    try {
      await create({ name: name || defaultScheduleName(cron), target_pipeline_slug: routine.slug, cron_expr: cron, timezone, inputs, enabled: true })
      toast.success("Schedule added", { description: next.at[0] ? `First start ${formatRoutineTime(next.at[0], timezone)}` : undefined })
      onClose()
    } catch (e) {
      toast.error("Could not add the schedule", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }

  const saveWebhook = async () => {
    setBusy(true)
    try {
      const w = await createWebhook({
        ingress_profile: profile,
        name: hookName || `${routine.slug} webhook`,
        target_pipeline_slug: routine.slug,
        rate_limit_per_min: rateLimit,
        enabled: true,
        inputs_template: {},
      })
      toast.success("Webhook created", { description: "Copy the URL and signing secret now — they won't be shown again" })
      if (w) onWebhookCreated(w)
      else onClose()
    } catch (e) {
      toast.error("Could not create the webhook", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <CreateSurface
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      size="md"
      onSubmit={() => (kind === "schedule" ? ready && submitInputsForm(formId) : kind === "webhook" && canWebhook ? void saveWebhook() : undefined)}
    >
      <CreateSurfaceHeader concept="routines" context={`Routines › ${routine.name || routine.slug}`} title="Add trigger" description="What starts this routine, besides the Run button." onClose={onClose} />
      <CreateSurfaceBody className="flex flex-col gap-4">
        <Segmented<AddKind>
          ariaLabel="Kind of trigger"
          fill
          value={kind}
          onChange={onKind}
          options={[
            { value: "schedule", label: "Schedule" },
            { value: "once", label: "One-time" },
            { value: "webhook", label: "Webhook" },
            { value: "chat", label: "Chat command" },
          ]}
        />
        {kind === "schedule" && !canSchedule && (
          <CreateSurfaceNotice tone="warn">Adding a schedule needs Manager or above, or permission to create routines.</CreateSurfaceNotice>
        )}
        {kind === "schedule" && canSchedule && (
          <>
            <RoutineRecurrenceFields cron={cron} timezone={timezone} onCronChange={setCron} onTimezoneChange={setTimezone} />
            <CreateSurfaceField label="Name" htmlFor="add-trigger-name" hint="Leave it empty to name it after the days and time.">
              <Input id="add-trigger-name" value={name} placeholder={defaultScheduleName(cron)} onChange={(e) => setName(e.target.value)} className={CREATE_SURFACE_INPUT} />
            </CreateSurfaceField>
            <section aria-label="Next starts" className="flex flex-col gap-1">
              <p className="text-xs font-medium text-muted-foreground">Next 5 starts</p>
              {next.error ? (
                <p className="text-xs text-destructive" role="alert">{next.error}</p>
              ) : next.at.length === 0 ? (
                <p className="text-xs text-muted-foreground">Working out the next starts…</p>
              ) : (
                <ol className="flex flex-col gap-0.5 font-mono text-xs" data-testid="add-trigger-next">
                  {next.at.map((iso) => (
                    <li key={iso}>{formatRoutineTime(iso, timezone)}</li>
                  ))}
                </ol>
              )}
              <p className="text-xs text-muted-foreground-soft">Follows the published version; if a start is missed while the server is down it runs once.</p>
            </section>
            <RoutinePresetForm
              formId={formId}
              onReadyChange={setReady}
              workspaceId={workspaceId}
              slug={routine.slug}
              submitting={busy}
              onCancel={onClose}
              onSave={(inputs) => void save(inputs)}
              submitLabel="Add schedule"
            />
            <p className="text-xs text-muted-foreground-soft">Owner or Admin can change or delete schedules.</p>
          </>
        )}
        {kind === "once" && (
          <CreateSurfaceNotice>A one-time start runs the routine once at a time you pick. Next you choose the time, the version and the inputs; it is listed here until it starts, and you can cancel it.</CreateSurfaceNotice>
        )}
        {kind === "webhook" && !canWebhook && <CreateSurfaceNotice tone="warn">Creating a webhook needs Manager or above.</CreateSurfaceNotice>}
        {kind === "webhook" && canWebhook && (
          <>
            <CreateSurfaceField label="Webhook name" htmlFor="add-trigger-webhook-name">
              <Input id="add-trigger-webhook-name" value={hookName} placeholder={`${routine.slug} webhook`} onChange={(e) => setHookName(e.target.value)} className={CREATE_SURFACE_INPUT} />
            </CreateSurfaceField>
            <CreateSurfaceField label="Sender" hint={profile === "github" ? "GitHub signs with X-Hub-Signature-256." : "Senders sign with X-Crewship-Signature."}>
              <Select value={profile} onValueChange={(v) => setProfile(v as "crewship" | "github")}>
                <SelectTrigger aria-label="Webhook sender" size="sm" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="crewship">Crewship signature</SelectItem>
                  <SelectItem value="github">GitHub pull requests</SelectItem>
                </SelectContent>
              </Select>
            </CreateSurfaceField>
            <CreateSurfaceField label="Rate limit per minute" htmlFor="add-trigger-webhook-rate">
              <Input
                id="add-trigger-webhook-rate"
                type="number"
                min={1}
                max={600}
                value={rateLimit}
                onChange={(e) => setRateLimit(parseInt(e.target.value, 10) || 60)}
                className={CREATE_SURFACE_INPUT}
              />
            </CreateSurfaceField>
            <p className="text-xs text-muted-foreground-soft">You get the URL and a signing secret once, after it is created. Signing cannot be turned off.</p>
          </>
        )}
        {kind === "chat" && (
          <CreateSurfaceNotice>
            The chat command is part of the routine itself: turn it on in Settings › Chat command, then publish the change.
          </CreateSurfaceNotice>
        )}
      </CreateSurfaceBody>
      <CreateSurfaceFooter
        onCancel={onClose}
        guardCancel={false}
        primaryLabel={
          kind === "schedule"
            ? canSchedule && ready
              ? inputsSubmitContent("Add schedule", busy)
              : undefined
            : kind === "once"
              ? "Choose the time"
              : kind === "webhook"
                ? canWebhook
                  ? busy
                    ? "Creating…"
                    : "Create webhook"
                  : undefined
                : "Open Settings"
        }
        onPrimary={kind === "schedule" ? () => submitInputsForm(formId) : kind === "once" ? onOnce : kind === "webhook" ? () => void saveWebhook() : onChat}
        busy={busy}
      />
    </CreateSurface>
  )
}
