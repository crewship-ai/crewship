"use client"

// The routine page's Triggers (Harbor 2 RT-TRIG): one list of everything that
// starts this routine — schedules, webhooks, the chat command, workspace events
// and the Run button — then Add trigger, the webhook delivery log, and below
// them the cards that manage each kind (repeating schedules, one-time starts,
// webhooks, automations).
//
// Who may do what is the server's: a schedule is created with the routine
// create role or the routine.create capability, changed and deleted by Owner
// and Admin; a webhook is created by Manager and above and changed or deleted
// by Owner and Admin.

import * as React from "react"
import { CalendarClock, Clock, Globe, Hand, MessageSquare, Plus, Webhook, Zap } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { DetailCard } from "@/components/ui/detail"
import { StatusPill } from "@/components/ui/status-pill"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceChoice,
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
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import { usePipelineWebhooks } from "@/hooks/use-pipeline-webhooks"
import { useAutomations } from "@/hooks/use-automations"
import { automationsForRoutine } from "@/lib/automations"
import { isRecord } from "@/lib/routine-step-describe"
import { routineTriggerRows, type DetailDepth, type TriggerKind, type TriggerRow } from "@/lib/routine-page"
import { AutomationList } from "@/components/features/automations/automation-list"
import { RoutineRecurrenceFields } from "./routine-recurrence-fields"
import { RoutinePresetForm } from "./routine-preset-form"
import { inputsSubmitContent, submitInputsForm } from "./routine-run-inputs-dialog"
import { RoutineSchedulesTab, defaultScheduleName } from "./routine-schedules-tab"
import { RoutineWebhooksTab } from "./routine-webhooks-tab"
import type { RoutineDetail } from "./routines-detail-panel"

const KIND_ICON: Record<TriggerKind, React.ComponentType<{ className?: string }>> = {
  schedule: CalendarClock,
  webhook: Webhook,
  chat: MessageSquare,
  event: Zap,
  manual: Hand,
}

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
  lifecycle: _lifecycle,
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
  const { create, preview } = usePipelineSchedules(workspaceId)
  const { webhooks } = usePipelineWebhooks(workspaceId)
  const { automations } = useAutomations(workspaceId)
  const mine = React.useMemo(() => automationsForRoutine(automations, routine.slug), [automations, routine.slug])
  const ourWebhooks = React.useMemo(
    () => webhooks.filter((w) => w.target_pipeline_id === routine.id || w.target_pipeline_slug === routine.slug),
    [webhooks, routine.id, routine.slug],
  )
  const receipts = useWebhookReceipts(workspaceId, ourWebhooks.map((w) => w.id))
  const webhookName = (id: string) => ourWebhooks.find((w) => w.id === id)?.name ?? "Webhook"
  const [adding, setAdding] = React.useState<AddKind | null>(null)
  const [webhookRequest, setWebhookRequest] = React.useState(0)
  const manageRef = React.useRef<HTMLDivElement>(null)
  const published = (routine.head_version ?? 0) > 0 && !routine.draft_only
  const def = isRecord(routine.definition) ? routine.definition : {}
  const concurrency =
    typeof def.max_concurrent === "number" && def.max_concurrent > 1
      ? `Up to ${def.max_concurrent} runs at once.`
      : typeof def.concurrency_key === "string" && def.concurrency_key
        ? "One at a time; a new start waits in line."
        : "Runs can start at the same time."

  const goTo = (anchor?: string) => {
    if (!anchor) return
    const el = document.getElementById(anchor)
    el?.scrollIntoView?.({ block: "center" })
    el?.focus({ preventScroll: true })
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
      <ul className="flex flex-col gap-2" aria-label="What starts this routine" data-testid="routine-trigger-list">
        {rows.map((row) => {
          const Icon = KIND_ICON[row.kind]
          const editable = row.kind === "schedule" || row.kind === "webhook" || row.kind === "chat"
          return (
            <li key={row.key} className="flex items-start gap-3 rounded-lg border border-hairline bg-card px-4 py-3" data-kind={row.kind}>
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
              {editable && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="shrink-0 coarse:h-11"
                  aria-label={`Edit ${row.kindLabel.toLowerCase()} ${row.title}`}
                  onClick={() => (row.kind === "chat" ? onSettings() : goTo(row.anchor))}
                >
                  Edit
                </Button>
              )}
            </li>
          )
        })}
      </ul>
      {rows.length === 1 && <p className="text-xs text-muted-foreground">Runs only when someone presses Run.</p>}

      <div className="flex flex-wrap items-center gap-3">
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="gap-1.5 coarse:h-11"
          disabled={!permissions.createSchedule}
          title={permissions.createSchedule ? undefined : "Adding a trigger needs Manager or above, or permission to create routines"}
          onClick={() => setAdding("schedule")}
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

      <div ref={manageRef} className="flex flex-col gap-4" aria-label="Manage triggers">
        <RoutineSchedulesTab
          workspaceId={workspaceId}
          pipelineId={routine.id}
          slug={routine.slug}
          headVersion={routine.head_version}
          draft={routine.draft}
          onAdd={() => setAdding("schedule")}
        />
        <RoutineWebhooksTab workspaceId={workspaceId} pipelineId={routine.id} slug={routine.slug} addRequest={webhookRequest} />
        {mine.length > 0 && (
          <DetailCard title="Automations" icon={Zap}>
            <div data-testid="routine-automations" className="flex flex-col gap-2.5">
              <p className="text-xs text-muted-foreground">
                <span data-testid="routine-automations-count" className="text-foreground/85">{mine.length}</span>{" "}
                {mine.length === 1 ? "automation" : "automations"} can start this routine.
              </p>
              <AutomationList automations={mine} />
            </div>
          </DetailCard>
        )}
        <p className="text-xs text-muted-foreground" data-testid="routine-concurrency">
          If a run is already going: {concurrency} Change it in Settings › Limits.
        </p>
      </div>

      {adding && (
        <AddTrigger
          kind={adding}
          onKind={setAdding}
          workspaceId={workspaceId}
          routine={routine}
          canWebhook={permissions.author}
          preview={preview}
          create={create}
          onClose={() => setAdding(null)}
          onWebhook={() => {
            setAdding(null)
            setWebhookRequest((n) => n + 1)
          }}
          onOnce={() => {
            setAdding(null)
            goTo("routine-once-starts")
          }}
          onChat={() => {
            setAdding(null)
            onSettings()
          }}
        />
      )}
    </div>
  )
}

function AddTrigger({
  kind,
  onKind,
  workspaceId,
  routine,
  canWebhook,
  preview,
  create,
  onClose,
  onWebhook,
  onOnce,
  onChat,
}: {
  kind: AddKind
  onKind: (k: AddKind) => void
  workspaceId: string
  routine: RoutineDetail
  canWebhook: boolean
  preview: ReturnType<typeof usePipelineSchedules>["preview"]
  create: ReturnType<typeof usePipelineSchedules>["create"]
  onClose: () => void
  onWebhook: () => void
  onOnce: () => void
  onChat: () => void
}) {
  const [cron, setCron] = React.useState("0 9 * * *")
  const [timezone, setTimezone] = React.useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC")
  const [name, setName] = React.useState("")
  const [next, setNext] = React.useState<{ at: string[]; error: string | null }>({ at: [], error: null })
  const [busy, setBusy] = React.useState(false)
  const [ready, setReady] = React.useState(false)
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

  return (
    <CreateSurface open onOpenChange={(o) => !o && !busy && onClose()} size="md" onSubmit={() => kind === "schedule" && ready && submitInputsForm(formId)}>
      <CreateSurfaceHeader concept="routines" context={`Routines › ${routine.name || routine.slug}`} title="Add trigger" description="What starts this routine, besides the Run button." onClose={onClose} />
      <CreateSurfaceBody className="flex flex-col gap-4">
        <CreateSurfaceChoice<AddKind>
          ariaLabel="Kind of trigger"
          value={kind}
          onChange={onKind}
          options={[
            { value: "schedule", label: "Schedule" },
            { value: "once", label: "One-time" },
            { value: "webhook", label: "Webhook" },
            { value: "chat", label: "Chat command" },
          ]}
        />
        {kind === "schedule" && (
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
          <CreateSurfaceNotice>
            A one-time start runs the published version once at a time you pick. It is listed and created under One-time starts below.
          </CreateSurfaceNotice>
        )}
        {kind === "webhook" && (
          <CreateSurfaceNotice tone={canWebhook ? undefined : "warn"}>
            {canWebhook
              ? "A webhook gives an outside app a URL and a signing secret to start this routine. The secret is shown once, when it is created."
              : "Creating a webhook needs Manager or above."}
          </CreateSurfaceNotice>
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
            ? ready
              ? inputsSubmitContent("Add schedule", busy)
              : undefined
            : kind === "once"
              ? "Go to one-time starts"
              : kind === "webhook"
                ? canWebhook
                  ? "Create a webhook"
                  : undefined
                : "Open Settings"
        }
        onPrimary={kind === "schedule" ? () => submitInputsForm(formId) : kind === "once" ? onOnce : kind === "webhook" ? onWebhook : onChat}
        busy={busy}
      />
    </CreateSurface>
  )
}
