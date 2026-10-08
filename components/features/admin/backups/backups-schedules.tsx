"use client"

import * as React from "react"

import { CalendarClock, CalendarDays, Clock, HardDrive, Layers, Package } from "lucide-react"
import { toast } from "sonner"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { StatusPill } from "@/components/ui/status-pill"
import { Switch } from "@/components/ui/switch"
import { toastSaveError } from "@/components/ui/page-save-bar"
import {
  SettingsCard, SettingsDangerCard, SettingsEmpty, SettingsRow, SettingsSaveBar, SettingsSegmented, SettingsSummary, SummaryItem, settingsControl,
} from "@/components/features/settings/shared"
import { Gate } from "./backups-kit"
import {
  CADENCE_LABEL, CATEGORIES, PRESETS, computeNextRuns, contentsFromPreview, describeKeep, describePlanWhen, formatPlannedRun, formatWhen,
  instantToPlanned, requiredWhy, resolveContents, wallClock,
  type BackupPlan, type Cadence, type CalendarResponse, type CategoryKey, type ContentsRow, type EnvCadence, type Schedule,
} from "./backups-model"
import { deletePlan, previewContents, savePlan, useBackupPlans, usePlanCalendar, usePlanNext } from "./use-backup-plans"
import { runNow } from "./use-backup-runs"
import { useBackupSettings, useRecipients } from "./use-backup-settings"
import { perform, performSave } from "./use-backups-data"
import type { SectionCtx } from "./backups-console"

export type PlanDraft = Omit<BackupPlan, "next_run_at" | "last_run_at" | "id"> & { id?: string }

const DEFAULT_TZ = typeof Intl !== "undefined" ? Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC" : "UTC"

export function newDraft(scope: SectionCtx["scope"], selected: Set<string>): PlanDraft {
  return {
    name: "Complete recovery", preset: "complete", scope, workspace_ids: scope === "workspaces" ? [...selected] : [], contents: [],
    env_mode: "complete", cadence: "daily", time_of_day: "03:00", weekday: 0, monthday: null, cron_expr: null, timezone: DEFAULT_TZ,
    env_cadence: "weekly", keep_min: 3, keep_daily: 7, keep_weekly: 4, keep_monthly: 12, destinations: ["local"], recipient_ids: [],
    busy_wait_minutes: 120, busy_retry_minutes: 15, hold_cap_minutes: 20, stale_alert_hours: 36, enabled: true,
  }
}

function draftOf(p: BackupPlan): PlanDraft {
  const { next_run_at: _n, last_run_at: _l, ...rest } = p
  void _n
  void _l
  return rest
}

/** The cron a Daily/Weekly/Monthly choice stands for (shown under Advanced). */
export function cronOf(d: Pick<PlanDraft, "cadence" | "time_of_day" | "weekday" | "monthday" | "cron_expr">): string {
  const [h, m] = d.time_of_day.split(":").map((x) => String(Number(x)))
  if (d.cadence === "daily") return `${m} ${h} * * *`
  if (d.cadence === "weekly") return `${m} ${h} * * ${d.weekday ?? 0}`
  if (d.cadence === "monthly") return d.monthday ? `${m} ${h} ${d.monthday} * *` : `${m} ${h} 1-7 * 0`
  return d.cron_expr ?? ""
}

function scheduleOf(d: PlanDraft): Schedule {
  return { cadence: d.cadence, time: d.time_of_day, weekday: d.weekday, monthday: d.monthday, cron: d.cron_expr, envMode: d.env_mode, envCadence: d.env_cadence }
}

/**
 * Backups › Schedules: one plan at a time — what goes in, when it runs, the
 * calendar of what ran and what will, and how long copies are kept where.
 *
 * On the nested page (ctx.inDrill) the side panel lists the plans, so this
 * opens the one it picked (ctx.focusPlan; "new" starts one) and carries the
 * plan's own actions: on/off and Run now in the summary line, Delete in the
 * danger zone. Edits save through the page's Save bar.
 *
 * A whole-instance plan is always a full backup. A workspace plan is a full
 * backup or "only some kinds" (custom), which does not count as protecting
 * the workspace: the work items live only in a full backup.
 */
export function BackupsSchedules({ ctx }: { ctx: SectionCtx }) {
  const plans = useBackupPlans()
  return (
    <Gate resource={plans} what="Backup plans" skeleton="h-[240px]">
      {(list) => <SchedulesBody plans={list} ctx={ctx} reload={plans.reload} />}
    </Gate>
  )
}

/** The plan the page opens: the panel's pick, a new one, or the first. */
function pickDraft(plans: BackupPlan[], ctx: SectionCtx): PlanDraft {
  if (ctx.focusPlan === "new") return newDraft(ctx.scope, ctx.selected)
  const hit = ctx.focusPlan ? plans.find((p) => p.id === ctx.focusPlan) : undefined
  const p = hit ?? plans[0]
  return p ? draftOf(p) : newDraft(ctx.scope, ctx.selected)
}

export function SchedulesBody({ plans, ctx, reload, now = new Date() }: { plans: BackupPlan[]; ctx: SectionCtx; reload?: () => void; now?: Date }) {
  const [draft, setDraft] = React.useState<PlanDraft>(() => (ctx.inDrill ? pickDraft(plans, ctx) : plans[0] ? draftOf(plans[0]) : newDraft(ctx.scope, ctx.selected)))
  // What the editor last loaded or saved: Discard goes back to it, and the
  // page's Save bar counts the fields that differ from it.
  const [base, setBase] = React.useState<PlanDraft>(draft)
  // A plan nobody has saved yet is unsaved until it is, edited or not.
  const [dirty, setDirty] = React.useState(() => !draft.id && !!ctx.inDrill && ctx.focusPlan === "new")
  const [dropped, setDropped] = React.useState<Set<CategoryKey>>(() => droppedOf(draft))
  const [adv, setAdv] = React.useState(false)
  const [saving, setSaving] = React.useState(false)
  const settings = useBackupSettings()
  const recipients = useRecipients()
  const editorRef = React.useRef<HTMLDivElement>(null)

  const reset = (d: PlanDraft) => {
    setDraft(d)
    setBase(d)
    setDirty(false)
    setDropped(droppedOf(d))
  }
  const load = (d: PlanDraft) => {
    reset(d)
    editorRef.current?.scrollIntoView?.({ behavior: "smooth", block: "start" })
  }
  const lastSignal = React.useRef(ctx.newPlanSignal)
  React.useEffect(() => {
    if (ctx.newPlanSignal === lastSignal.current) return
    lastSignal.current = ctx.newPlanSignal
    load(newDraft(ctx.scope, ctx.selected))
    setDirty(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- a new signal is the only trigger
  }, [ctx.newPlanSignal])
  // The side panel picked another plan (the page guard already asked about
  // unsaved edits), or the one on screen was deleted.
  const lastFocus = React.useRef(ctx.focusPlan)
  React.useEffect(() => {
    if (!ctx.inDrill || ctx.focusPlan === lastFocus.current) return
    lastFocus.current = ctx.focusPlan
    load(pickDraft(plans, ctx))
    if (ctx.focusPlan === "new") setDirty(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the pick is the only trigger
  }, [ctx.focusPlan])

  const set = (patch: Partial<PlanDraft>) => {
    setDraft((d) => ({ ...d, ...patch }))
    setDirty(true)
  }

  // Contents: the local dependency rule answers at once; the server's
  // preview replaces it when it answers (same rule, the server's word).
  const local = React.useMemo(() => resolveContents(draft.preset, draft.env_mode, dropped), [draft.preset, draft.env_mode, dropped])
  const [server, setServer] = React.useState<ContentsRow[] | null>(null)
  React.useEffect(() => {
    setServer(null)
    if (ctx.demo || draft.preset !== "custom") return
    let live = true
    const contents = local.filter((r) => r.state === "included").map((r) => r.key)
    const t = setTimeout(() => {
      void previewContents(draft.preset, contents, draft.env_mode).then((r) => { if (live && r.ok) setServer(contentsFromPreview(r.data)) })
    }, 300)
    return () => { live = false; clearTimeout(t) }
  }, [ctx.demo, draft.preset, draft.env_mode, local])
  const rows = server ?? local

  const save = async () => {
    setSaving(true)
    const body: PlanDraft = {
      ...draft,
      scope: draft.preset === "complete" ? "instance" : "workspaces",
      workspace_ids: draft.preset === "complete" ? [] : draft.workspace_ids,
      contents: draft.preset === "custom" ? rows.filter((r) => r.state !== "excluded" && r.key !== "env").map((r) => r.key) : [],
      cron_expr: draft.cadence === "custom" ? draft.cron_expr : null,
      recipient_ids: draft.recipient_ids.length ? draft.recipient_ids : (recipients.data ?? []).map((r) => r.id),
    }
    // The page's Save bar says "Saved"; a failure rejects into its toast and
    // keeps the draft.
    const out = await performSave(ctx.demo, () => savePlan(body)).finally(() => setSaving(false))
    if (out) {
      reset(draftOf(out))
      reload?.()
    }
  }
  const changed = dirty ? Math.max(1, (Object.keys(draft) as (keyof PlanDraft)[]).filter((k) => JSON.stringify(draft[k]) !== JSON.stringify(base[k])).length) : 0

  const saved = draft.id ? plans.find((p) => p.id === draft.id) ?? null : null

  return (
    <>
      {ctx.inDrill ? (
        saved && <PlanSummary plan={saved} ctx={ctx} reload={reload} now={now} />
      ) : (
        <SettingsCard title="Plans" icon={CalendarClock}>
          {plans.length === 0 ? (
            <SettingsEmpty>No plan yet. The editor below starts a Complete recovery plan.</SettingsEmpty>
          ) : plans.map((p) => (
            <SettingsRow key={p.id} label={<><span className="font-medium">{p.name}</span> · {planScopeLabel(p, ctx)}</>} description={planDetail(p)}>
              <StatusPill tone={p.enabled ? "success" : "muted"} label={p.enabled ? "on" : "off"} />
              <Button type="button" size="sm" variant="outline" className="h-7 text-xs" onClick={() => load(draftOf(p))} aria-label={`Edit ${p.name}`}>Edit</Button>
            </SettingsRow>
          ))}
        </SettingsCard>
      )}

      <div ref={editorRef} className="scroll-mt-4">
        <WhatCard draft={draft} set={set} rows={rows} ctx={ctx}
          onToggle={(k, on) => {
            if (k === "env") { set({ env_mode: on ? "complete" : "files" }); return }
            setDropped((d) => { const n = new Set(d); if (on) n.delete(k); else n.add(k); return n })
            setDirty(true)
          }}
          onKinds={(custom) => { if (!custom) setDropped(new Set()) }} />
      </div>

      <WhenCard draft={draft} set={set} adv={adv} setAdv={setAdv} dirty={dirty} now={now} />

      <CalendarCard draft={draft} dirty={dirty} now={now} />

      <KeepWhereCard draft={draft} set={set} ctx={ctx} destinations={settings.data?.destinations ?? []}
        recipientNames={(recipients.data ?? []).filter((r) => !draft.recipient_ids.length || draft.recipient_ids.includes(r.id)).map((r) => `“${r.name}”`)} />

      {ctx.inDrill && saved && <DeletePlan plan={saved} ctx={ctx} reload={reload} />}
      <SettingsSaveBar label="Backup plan" count={changed} saving={saving} onSave={save} onDiscard={() => reset(base)} />
    </>
  )
}

function droppedOf(d: PlanDraft): Set<CategoryKey> {
  return new Set(d.preset === "custom" ? CATEGORY_KEYS.filter((k) => k !== "env" && !d.contents.includes(k)) : [])
}

/** On/off, next run and scope in words; the plan's switch and Run now beside them. */
function PlanSummary({ plan, ctx, reload, now }: { plan: BackupPlan; ctx: SectionCtx; reload?: () => void; now: Date }) {
  // Optimistic: the switch moves at once and flips back if the server refuses.
  const [enabled, setEnabled] = React.useState(plan.enabled)
  React.useEffect(() => setEnabled(plan.enabled), [plan.enabled, plan.id])
  const toggle = async (on: boolean) => {
    setEnabled(on)
    if (ctx.demo) {
      toast.message("Demo data · nothing was sent")
      setEnabled(!on)
      return
    }
    const r = await savePlan({ ...draftOf(plan), enabled: on })
    if (r.ok) reload?.()
    else {
      setEnabled(!on)
      toastSaveError("Backup plan", r.error)
    }
  }
  const next = plan.next_run_at ? formatWhen(plan.next_run_at, now) : null
  return (
    <SettingsSummary>
      <SummaryItem tone={enabled ? "success" : undefined}>{enabled ? "On" : "Off"}</SummaryItem>
      {enabled && next && <SummaryItem n={next}>next run</SummaryItem>}
      <SummaryItem>{planScopeLabel(plan, ctx)}</SummaryItem>
      <span className="ml-auto flex items-center gap-2">
        <Button type="button" size="sm" variant="outline" className="h-7 text-xs"
          onClick={() => void perform(ctx.demo, () => runNow({ plan_id: plan.id, scope: plan.scope }), "Backup started · it appears in Backup history", "The backup could not start")}>
          Run now
        </Button>
        <label className="flex items-center gap-2 text-xs text-foreground">
          Enabled
          <Switch checked={enabled} onCheckedChange={(v) => void toggle(v)} aria-label="Enabled" />
        </label>
      </span>
    </SettingsSummary>
  )
}

function DeletePlan({ plan, ctx, reload }: { plan: BackupPlan; ctx: SectionCtx; reload?: () => void }) {
  const [open, setOpen] = React.useState(false)
  return (
    <SettingsDangerCard title="Danger zone">
      <SettingsRow label="Delete plan" description="It stops running; its backups stay">
        <Button type="button" size="sm" variant="destructive" className="h-7 text-xs" onClick={() => setOpen(true)}>Delete…</Button>
      </SettingsRow>
      <ConfirmDialog open={open} onOpenChange={setOpen} destructive title={`Delete “${plan.name}”?`}
        description="Backups it made stay until they age out."
        consequences={[{ tone: "lost", text: "No more runs on this schedule" }, { tone: "kept", text: "Every backup it made, pinned ones forever" }]}
        confirmLabel="Delete plan"
        onConfirm={async () => {
          const out = await perform(ctx.demo, () => deletePlan(plan.id), "Plan deleted", "The plan could not be deleted")
          if (out) {
            setOpen(false)
            reload?.()
          }
        }} />
    </SettingsDangerCard>
  )
}

function WhatCard({ draft, set, rows, ctx, onToggle, onKinds }: {
  draft: PlanDraft; set: (p: Partial<PlanDraft>) => void; rows: ContentsRow[]; ctx: SectionCtx
  onToggle: (k: CategoryKey, on: boolean) => void; onKinds: (custom: boolean) => void
}) {
  const instance = draft.preset === "complete"
  const custom = draft.preset === "custom"
  const pickScope = (v: "instance" | "workspaces") => {
    if (v === "instance") {
      onKinds(false)
      set({ preset: "complete", scope: "instance", workspace_ids: [], name: draft.id ? draft.name : PRESETS.complete.label })
    } else if (instance) {
      set({ preset: "workspace", scope: "workspaces", workspace_ids: draft.workspace_ids.length ? draft.workspace_ids : [...ctx.selected], name: draft.id ? draft.name : PRESETS.workspace.label })
    }
  }
  const toggleWs = (id: string, on: boolean) => {
    const cur = draft.workspace_ids.length ? draft.workspace_ids : ctx.workspaces.map((w) => w.id)
    const next = on ? [...new Set([...cur, id])] : cur.filter((x) => x !== id)
    // Every workspace ticked is "every workspace", including future ones.
    set({ workspace_ids: next.length === ctx.workspaces.length ? [] : next })
  }
  return (
    <SettingsCard title="What" description={instance ? "A whole-instance backup is always a full backup" : "A full backup protects the workspace; a partial one does not"} icon={Package}>
      <SettingsRow label="Name">
        <Input aria-label="Plan name" className={settingsControl} value={draft.name} onChange={(e) => set({ name: e.target.value })} />
      </SettingsRow>
      <SettingsRow label="Back up" description={instance ? "Every workspace, users, settings and the keys a restore needs" : undefined}>
        <SettingsSegmented<"instance" | "workspaces"> label="Back up" value={instance ? "instance" : "workspaces"} onChange={pickScope}
          options={[{ value: "instance", label: "Whole instance" }, { value: "workspaces", label: "Workspaces" }]} />
      </SettingsRow>
      {!instance && (
        <>
          <SettingsRow label="Workspaces" description={draft.workspace_ids.length ? undefined : "Every workspace, including ones created later"}>
            <span className="flex max-w-[22rem] flex-wrap justify-end gap-x-3 gap-y-1.5">
              {ctx.workspaces.map((w) => {
                const id = `plan-ws-${w.id}`
                const on = !draft.workspace_ids.length || draft.workspace_ids.includes(w.id)
                return (
                  <label key={w.id} htmlFor={id} className="flex items-center gap-1.5 text-[13px]">
                    <Checkbox id={id} checked={on} onCheckedChange={(v) => toggleWs(w.id, v === true)} />{w.name}
                  </label>
                )
              })}
            </span>
          </SettingsRow>
          <SettingsRow label="Contents">
            <SettingsSegmented<"full" | "some"> label="Contents" value={custom ? "some" : "full"}
              onChange={(v) => { onKinds(v === "some"); set({ preset: v === "some" ? "custom" : "workspace" }) }}
              options={[{ value: "full", label: "Full backup" }, { value: "some", label: "Only some kinds" }]} />
          </SettingsRow>
          {custom && (
            <>
              <div className="border-b border-border bg-warn/[0.06] px-4 py-2.5 text-xs text-warn">
                Not full protection: issues, inbox, projects and approvals are only in a full backup; this plan does not count as covering the workspace.
              </div>
              <ContentsTable rows={rows} editable onToggle={onToggle} />
            </>
          )}
        </>
      )}
      <SettingsRow label="Container environments" description={draft.env_mode === "complete" ? "Image, volumes and settings; larger and slower" : "Chosen folders only; a restore needs the original image"}>
        <SettingsSegmented<"files" | "complete"> label="Container environments" value={draft.env_mode} onChange={(v) => set({ env_mode: v })}
          options={[{ value: "files", label: "Files only" }, { value: "complete", label: "Complete" }]} />
      </SettingsRow>
    </SettingsCard>
  )
}

function KeepWhereCard({ draft, set, ctx, destinations, recipientNames }: {
  draft: PlanDraft; set: (p: Partial<PlanDraft>) => void; ctx: SectionCtx
  destinations: { id: string; kind: string; label: string; available: boolean }[]; recipientNames: string[]
}) {
  const offsite = destinations.filter((d) => d.kind !== "local")
  const offsiteReady = offsite.some((d) => d.available)
  const num = (label: string, value: number, min: number, onChange: (n: number) => void) => (
    <SettingsRow label={label}>
      <Input aria-label={label} type="number" min={min} className={cn(settingsControl, "sm:w-24")} value={value}
        onChange={(e) => onChange(Math.max(min, +e.target.value || min))} />
    </SettingsRow>
  )
  return (
    <>
      <SettingsCard title="Keep" description="Pinned backups are never deleted" icon={Layers} tint="var(--purple)">
        {num("Checked backups, at least", draft.keep_min, 1, (n) => set({ keep_min: n }))}
        {num("Daily", draft.keep_daily, 0, (n) => set({ keep_daily: n }))}
        {num("Weekly", draft.keep_weekly, 0, (n) => set({ keep_weekly: n }))}
        {num("Monthly", draft.keep_monthly, 0, (n) => set({ keep_monthly: n }))}
      </SettingsCard>
      <SettingsCard title="Where and encryption" icon={HardDrive}>
        <SettingsRow label="This server" description="Always; staging for every upload">
          <Checkbox checked disabled aria-label="This server" />
        </SettingsRow>
        {offsite.map((d) => {
          const id = `plan-dst-${d.id}`
          return (
            <SettingsRow key={d.id} label={<label htmlFor={id}>{d.label}{!d.available && " (later)"}</label>} className={cn(!d.available && "opacity-50")}>
              <Checkbox id={id} disabled={!d.available} checked={draft.destinations.includes(d.id)}
                onCheckedChange={(v) => set({ destinations: v === true ? [...draft.destinations, d.id] : draft.destinations.filter((x) => x !== d.id) })} />
            </SettingsRow>
          )
        })}
        {!offsiteReady && (
          <SettingsRow label="Off-site" description="Losing this server loses every local copy">
            <Button type="button" size="sm" variant="outline" className="h-7 text-xs" onClick={() => ctx.go("storage")}>add S3-compatible storage under Storage</Button>
          </SettingsRow>
        )}
        <SettingsRow label="Encryption">
          <span className="text-xs text-muted-foreground">{recipientNames.length ? `always · to ${recipientNames.join(" and ")}` : "always · add a backup key under Keys & alerts"}</span>
        </SettingsRow>
      </SettingsCard>
    </>
  )
}

const CATEGORY_KEYS: CategoryKey[] = ["agents", "memory", "chats", "att", "routines", "journal", "creds", "pages", "files", "env"]

function planScopeLabel(p: BackupPlan, ctx: SectionCtx): string {
  if (p.scope === "instance") return p.preset === "custom" ? "custom · whole instance" : "whole instance"
  const names = p.workspace_ids.map((id) => ctx.workspaces.find((w) => w.id === id)?.name).filter(Boolean)
  const who = names.length ? names.join(", ") : "every workspace"
  return p.preset === "custom" ? `custom · ${who}` : who
}

function planDetail(p: BackupPlan): string {
  const names = p.contents.map((k) => CATEGORIES.find((c) => c.key === k)?.label.toLowerCase()).filter(Boolean)
  const what = p.preset === "custom" ? `${names.join(", ") || "custom contents"} only` : describePlanWhen(p)
  const when = p.preset === "custom" && p.cadence !== "custom" ? describePlanWhen(p) : ""
  const tail = p.preset === "custom" ? "does not count as workspace protection" : p.destinations.length > 1 ? "this server and off-site" : "this server"
  return [what, when, describeKeep(p), tail].filter(Boolean).join(" · ")
}

export function ContentsTable({ rows, editable, onToggle }: { rows: ContentsRow[]; editable: boolean; onToggle: (k: CategoryKey, on: boolean) => void }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full" data-slot="contents-table">
        <thead><tr className="border-b border-border text-left text-[11px] font-medium text-muted-foreground">
          <th className="px-4 py-2 font-medium">Category</th><th className="px-4 py-2 font-medium">In this backup</th><th className="px-4 py-2"><span className="sr-only">Why</span></th>
        </tr></thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.key} data-cat={r.key} data-state={r.state} className="border-b border-border text-[13px] last:border-b-0">
              <td className="px-4 py-2">
                <label className="flex items-center gap-2">
                  <Checkbox checked={r.state !== "excluded"} disabled={(!editable && r.key !== "env") || r.state === "required"}
                    onCheckedChange={(v) => onToggle(r.key, v === true)} />
                  {r.label}
                </label>
              </td>
              <td className="px-4 py-2">
                <StatusPill tone={r.state === "included" ? "success" : r.state === "required" ? "blue" : "muted"}
                  label={r.state === "included" ? "Included" : r.state === "required" ? "Required dependency" : "Excluded"} />
              </td>
              <td className="px-4 py-2 text-xs text-muted-foreground">
                {r.state === "required" ? requiredWhy(r) : r.warning ? <span className="text-warn">{r.warning}</span> : ""}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function timezones(): string[] {
  try {
    const list = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.("timeZone")
    if (list?.length) return list.includes("UTC") ? list : ["UTC", ...list]
  } catch { /* older runtimes */ }
  return ["UTC", "Europe/Prague", "Europe/London", "America/New_York", "Asia/Tokyo"]
}

const WEEKDAY_NAMES = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"]

function WhenCard({ draft, set, adv, setAdv, dirty, now }: {
  draft: PlanDraft; set: (p: Partial<PlanDraft>) => void; adv: boolean; setAdv: (v: boolean) => void; dirty: boolean; now: Date
}) {
  const zones = React.useMemo(timezones, [])
  const server = usePlanNext(draft.id && !dirty ? draft.id : null)
  const planned = server.status === "ready" && server.data?.runs.length
    ? server.data.runs.map((r) => instantToPlanned(r.at, draft.timezone, r.environments))
    : computeNextRuns(scheduleOf(draft), wallClock(now, draft.timezone), 5)
  const next = planned === null ? "shown once the server has read this cron expression" : planned.length ? planned.slice(0, 3).map((r) => formatPlannedRun(r)).join(" · ") : "none"
  const pick = <T extends string>(label: string, value: T, options: { value: T; label: string }[], onChange: (v: T) => void) => (
    <Select value={value} onValueChange={(v) => onChange(v as T)}>
      <SelectTrigger aria-label={label} className={settingsControl}><SelectValue /></SelectTrigger>
      <SelectContent>{options.map((o) => <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>)}</SelectContent>
    </Select>
  )
  return (
    <SettingsCard title="When" icon={Clock} tint="var(--success)">
      <SettingsRow label="Repeat">
        {pick<Cadence>("Repeat", draft.cadence, (Object.keys(CADENCE_LABEL) as Cadence[]).map((k) => ({ value: k, label: CADENCE_LABEL[k] })),
          (v) => set({ cadence: v, cron_expr: v === "custom" ? draft.cron_expr ?? cronOf(draft) : draft.cron_expr }))}
      </SettingsRow>
      {draft.cadence === "weekly" && (
        <SettingsRow label="On">
          {pick("Weekday", String(draft.weekday ?? 0), [1, 2, 3, 4, 5, 6, 0].map((d) => ({ value: String(d), label: WEEKDAY_NAMES[d] })), (v) => set({ weekday: Number(v) }))}
        </SettingsRow>
      )}
      {draft.cadence === "monthly" && (
        <SettingsRow label="On" description={draft.monthday == null ? undefined : "Day 1–28, so every month has it"}>
          <span className="flex items-center gap-2">
            {draft.monthday != null && (
              <Input aria-label="Day of month" type="number" min={1} max={28} className={cn(settingsControl, "sm:w-20")} value={draft.monthday}
                onChange={(e) => set({ monthday: Math.min(28, Math.max(1, +e.target.value || 1)) })} />
            )}
            <SettingsSegmented<"first" | "day"> label="Day of month" value={draft.monthday ? "day" : "first"} onChange={(v) => set({ monthday: v === "first" ? null : 1 })}
              options={[{ value: "first", label: "First Sunday" }, { value: "day", label: "A day" }]} />
          </span>
        </SettingsRow>
      )}
      {draft.cadence !== "custom" && (
        <SettingsRow label="Time">
          <Input aria-label="Time" type="time" className={settingsControl} value={draft.time_of_day} onChange={(e) => e.target.value && set({ time_of_day: e.target.value })} />
        </SettingsRow>
      )}
      <SettingsRow label="Time zone">
        {pick("Timezone", draft.timezone, (zones.includes(draft.timezone) ? zones : [draft.timezone, ...zones]).map((z) => ({ value: z, label: z })), (v) => set({ timezone: v }))}
      </SettingsRow>
      {draft.env_mode === "complete" && (
        <SettingsRow label="Environments" description="Large; less often is fine">
          {pick<EnvCadence>("Environments", draft.env_cadence, [
            { value: "every", label: "With every run" }, { value: "weekly", label: `Weekly, Sunday ${envTime(draft.time_of_day)}` }, { value: "monthly", label: "Monthly" },
          ], (v) => set({ env_cadence: v }))}
        </SettingsRow>
      )}
      <SettingsRow label="Next runs">
        <span data-slot="next-runs" className="text-right text-xs text-muted-foreground">{next}</span>
      </SettingsRow>
      <SettingsRow label="If the server is busy" description="Waits for running work, then holds writes for the copy; alerts if it cannot start">
        <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
          wait
          <Input aria-label="Wait up to (hours)" type="number" min={0} step={0.5} className={cn(settingsControl, "sm:w-20")} value={draft.busy_wait_minutes / 60}
            onChange={(e) => set({ busy_wait_minutes: Math.round(Math.max(0, +e.target.value || 0) * 60) })} />h · hold
          <Input aria-label="Hold cap (minutes)" type="number" min={1} className={cn(settingsControl, "sm:w-20")} value={draft.hold_cap_minutes}
            onChange={(e) => set({ hold_cap_minutes: Math.max(1, +e.target.value || 1) })} />min
        </span>
      </SettingsRow>
      <SettingsRow label="If the server was off" description="One backup as soon as it is back, not one per missed run">
        <span className="text-xs text-muted-foreground">automatic</span>
      </SettingsRow>
      <SettingsRow label={<Button type="button" variant="ghost" size="sm" className="-ml-2 h-7 text-xs" onClick={() => setAdv(!adv)} aria-expanded={adv}>{adv ? "▾" : "▸"} Advanced</Button>}
        description={adv ? "Runs on the server, never through an agent or a model" : undefined}>
        {adv ? (
          <Input aria-label="Cron expression" className={cn(settingsControl, "font-mono")} value={draft.cadence === "custom" ? draft.cron_expr ?? "" : cronOf(draft)}
            onChange={(e) => set({ cadence: "custom", cron_expr: e.target.value })} />
        ) : <span className="text-xs text-muted-foreground">cron expression</span>}
      </SettingsRow>
    </SettingsCard>
  )
}

function envTime(t: string): string {
  const [h, m] = t.split(":").map(Number)
  return `${String(((h || 0) + 1) % 24).padStart(2, "0")}:${String(m || 0).padStart(2, "0")}`
}

type CalPill = { text: string; kind: CalKind }
type CalKind = "ok" | "fail" | "skip" | "plan" | "env" | "envdone"

/** Each entry says what it is in a mark and a word, never in colour alone. */
const PILL: Record<CalKind, { cls: string; mark: string }> = {
  ok: { cls: "bg-success/20 text-foreground", mark: "✓" },
  fail: { cls: "bg-destructive/25 text-foreground", mark: "✕" },
  skip: { cls: "bg-warn/25 text-foreground", mark: "↻" },
  plan: { cls: "border border-dashed border-control-border text-muted-foreground", mark: "○" },
  env: { cls: "border border-dashed border-purple text-purple", mark: "◇" },
  envdone: { cls: "bg-success/20 text-foreground", mark: "◇" },
}

const LEGEND: { kind: CalKind; text: string }[] = [
  { kind: "ok", text: "done" }, { kind: "fail", text: "failed" }, { kind: "skip", text: "catch-up / skipped" },
  { kind: "env", text: "environments" }, { kind: "plan", text: "planned" },
]

/** The weeks shown: Monday on or before the 1st through the Sunday after the month ends. */
export function calendarWeeks(now: Date): { from: Date; days: Date[] } {
  const first = new Date(Date.UTC(now.getFullYear(), now.getMonth(), 1))
  const start = new Date(first.getTime() - ((first.getUTCDay() + 6) % 7) * 86_400_000)
  const last = new Date(Date.UTC(now.getFullYear(), now.getMonth() + 1, 0))
  const end = new Date(last.getTime() + ((7 - last.getUTCDay()) % 7) * 86_400_000)
  const days: Date[] = []
  for (let t = start.getTime(); t <= end.getTime(); t += 86_400_000) days.push(new Date(t))
  return { from: start, days }
}

function pillsFrom(resp: CalendarResponse | null, key: string, draft: PlanDraft, planned: Set<string>): CalPill[] {
  const day = resp?.days.find((d) => d.date === key)
  if (day) {
    return day.entries.map((e) => {
      const t = e.at.slice(11, 16)
      if (e.kind === "environments") return { text: e.status === "planned" ? `env ${t}` : "env", kind: e.status === "planned" ? "env" : "envdone" }
      switch (e.status) {
        case "done": return { text: t, kind: "ok" }
        case "failed": return { text: `${t} failed`, kind: "fail" }
        case "skipped": return { text: "skipped", kind: "skip" }
        case "catchup": return { text: `${t} catch-up`, kind: "skip" }
        default: return { text: t, kind: "plan" }
      }
    })
  }
  if (!planned.has(key)) return []
  const out: CalPill[] = [{ text: draft.cadence === "custom" ? "runs" : draft.time_of_day, kind: "plan" }]
  if (planned.has(`${key}+env`)) out.push({ text: `env ${envTime(draft.time_of_day)}`, kind: "env" })
  return out
}

function CalendarCard({ draft, dirty, now }: { draft: PlanDraft; dirty: boolean; now: Date }) {
  const { from, days } = React.useMemo(() => calendarWeeks(now), [now])
  const to = days[days.length - 1]
  const cal = usePlanCalendar(draft.id && !dirty ? draft.id : null, from.toISOString().slice(0, 10), to.toISOString().slice(0, 10))
  const month = now.getMonth()
  const todayKey = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`
  // Unsaved or new: what the editor would plan, from today on.
  const planned = React.useMemo(() => {
    const s = new Set<string>()
    const runs = computeNextRuns(scheduleOf(draft), wallClock(now, draft.timezone), 200) ?? []
    for (const r of runs) {
      if (r.date > to.toISOString().slice(0, 10)) break
      s.add(r.date)
      if (r.environments) s.add(`${r.date}+env`)
    }
    return s
  }, [draft, now, to])
  const resp = cal.status === "ready" ? cal.data : null
  const next = new Date(now.getFullYear(), now.getMonth() + 1, 1)
  const label = `${now.toLocaleDateString("en-GB", { month: "long" })}–${next.toLocaleDateString("en-GB", { month: "long" })}`
  return (
    <SettingsCard title="Calendar" description={`${label} · done and planned runs`} icon={CalendarDays}>
      <div className="grid grid-cols-7 gap-1 px-4 py-3" data-slot="plan-calendar">
        {["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"].map((d) => (
          <div key={d} className="pb-1 text-center text-[11px] text-muted-foreground">{d}</div>
        ))}
        {days.map((d) => {
          const key = d.toISOString().slice(0, 10)
          const out = d.getUTCMonth() !== month && d < new Date(Date.UTC(now.getFullYear(), month, 1))
          const pills = out ? [] : pillsFrom(resp, key, draft, planned)
          return (
            <div key={key} data-date={key}
              className={cn("flex min-h-[46px] min-w-0 flex-col gap-[3px] overflow-hidden rounded-[7px] border border-border px-1.5 py-1 text-[12px] text-muted-foreground",
                key === todayKey && "border-primary", out && "opacity-35")}>
              <span>{d.getUTCDate()}</span>
              {pills.map((p, i) => <span key={i} className={cn("w-fit max-w-full truncate rounded-[5px] px-1 text-[11px]", PILL[p.kind].cls)}>{PILL[p.kind].mark} {p.text}</span>)}
            </div>
          )
        })}
      </div>
      <div className="flex flex-wrap items-center gap-2.5 px-4 pb-3" data-slot="calendar-legend">
        {LEGEND.map((l) => <span key={l.kind} className={cn("rounded-[5px] px-1 text-[11px]", PILL[l.kind].cls)}>{`${PILL[l.kind].mark} ${l.text}`}</span>)}
      </div>
    </SettingsCard>
  )
}
