"use client"

import * as React from "react"

import { cn } from "@/lib/utils"
import { SettingsCard, SettingsSaveBar, SettingsSegmented } from "@/components/features/settings/shared"
import { Chip, FieldRow, Gate, InlineInput, ItemRow, SmallButton, TD, TH } from "./backups-kit"
import {
  CADENCE_LABEL, CATEGORIES, PRESETS, computeNextRuns, contentsFromPreview, describeKeep, describePlanWhen, formatPlannedRun,
  instantToPlanned, requiredWhy, resolveContents, wallClock,
  type BackupPlan, type Cadence, type CalendarResponse, type CategoryKey, type ContentsRow, type EnvCadence, type Preset, type Schedule,
} from "./backups-model"
import { previewContents, savePlan, useBackupPlans, usePlanCalendar, usePlanNext } from "./use-backup-plans"
import { useBackupSettings, useRecipients } from "./use-backup-settings"
import { performSave } from "./use-backups-data"
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
 * Backups › Schedules: the plans, and one editor for what goes in (a preset,
 * or a custom choice that keeps its dependencies), when it runs, the calendar
 * of what ran and what will, and how long copies are kept where.
 */
export function BackupsSchedules({ ctx }: { ctx: SectionCtx }) {
  const plans = useBackupPlans()
  return (
    <Gate resource={plans} what="Backup plans" skeleton="h-[240px]">
      {(list) => <SchedulesBody plans={list} ctx={ctx} reload={plans.reload} />}
    </Gate>
  )
}

export function SchedulesBody({ plans, ctx, reload, now = new Date() }: { plans: BackupPlan[]; ctx: SectionCtx; reload?: () => void; now?: Date }) {
  const [draft, setDraft] = React.useState<PlanDraft>(() => (plans[0] ? draftOf(plans[0]) : newDraft(ctx.scope, ctx.selected)))
  // What the editor last loaded or saved: Discard goes back to it, and the
  // page's Save bar counts the fields that differ from it.
  const [base, setBase] = React.useState<PlanDraft>(draft)
  const [dirty, setDirty] = React.useState(false)
  const [dropped, setDropped] = React.useState<Set<CategoryKey>>(() => new Set())
  const [adv, setAdv] = React.useState(false)
  const [saving, setSaving] = React.useState(false)
  const settings = useBackupSettings()
  const recipients = useRecipients()
  const editorRef = React.useRef<HTMLDivElement>(null)

  const reset = (d: PlanDraft) => {
    setDraft(d)
    setBase(d)
    setDirty(false)
    setDropped(new Set(d.preset === "custom" ? CATEGORY_KEYS.filter((k) => k !== "env" && !d.contents.includes(k)) : []))
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
    // A new plan is unsaved until it is saved, edited or not.
    setDirty(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- a new signal is the only trigger
  }, [ctx.newPlanSignal])

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
      scope: draft.preset === "complete" ? "instance" : draft.preset === "workspace" ? "workspaces" : draft.scope,
      workspace_ids: draft.preset === "complete" ? [] : draft.workspace_ids.length ? draft.workspace_ids : [...ctx.selected],
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

  const offsiteReady = (settings.data?.destinations ?? []).some((d) => d.kind !== "local" && d.available)
  const recipientNames = (recipients.data ?? []).filter((r) => !draft.recipient_ids.length || draft.recipient_ids.includes(r.id)).map((r) => `“${r.name}”`)

  return (
    <>
      <SettingsCard title="Plans">
        {plans.length === 0 ? (
          <p className="px-4 py-3 text-[12.5px] text-muted-foreground">No plan yet. The editor below starts a Complete recovery plan.</p>
        ) : plans.map((p) => (
          <ItemRow key={p.id} lead={<Chip tone={p.enabled ? "ok" : "muted"}>{p.enabled ? "on" : "off"}</Chip>}
            title={<><b className="font-semibold">{p.name}</b> · {planScopeLabel(p, ctx)}</>}
            detail={planDetail(p)}
            action={<SmallButton onClick={() => load(draftOf(p))} aria-label={`Edit ${p.name}`}>Edit</SmallButton>} />
        ))}
      </SettingsCard>

      <div ref={editorRef} className="scroll-mt-4">
        <SettingsCard title={`${draft.id ? "Edit plan" : "New plan"} · What`} actions={dirty ? <span className="text-[11px] text-muted-foreground">unsaved</span> : undefined}>
          <div className="grid grid-cols-1 gap-2 px-3.5 py-3 sm:grid-cols-3" role="radiogroup" aria-label="What goes in">
            {(Object.keys(PRESETS) as Preset[]).map((k) => (
              <button key={k} type="button" role="radio" aria-checked={draft.preset === k}
                onClick={() => { set({ preset: k, name: draft.id ? draft.name : PRESETS[k].label }); if (k !== "custom") setDropped(new Set()) }}
                className={cn("flex flex-col gap-1 rounded-[10px] border px-3 py-2.5 text-left transition-colors",
                  draft.preset === k ? "border-primary bg-primary/[0.08]" : "border-border hover:border-line-strong")}>
                <b className="text-[13px] font-semibold">{PRESETS[k].label}</b>
                <small className="text-[12.5px] text-muted-foreground">{PRESETS[k].about}</small>
              </button>
            ))}
          </div>
          <details open={draft.preset === "custom" || undefined} className="border-t border-border">
            <summary className="cursor-pointer px-3.5 py-2.5 text-[13px] font-medium">Customize contents</summary>
            <ContentsTable rows={rows} editable={draft.preset === "custom"}
              onToggle={(k, on) => {
                if (k === "env") { set({ env_mode: on ? "complete" : "files" }); return }
                setDropped((d) => { const n = new Set(d); if (on) n.delete(k); else n.add(k); return n })
                setDirty(true)
              }} />
          </details>
          <FieldRow label="Container environments" className="border-t" detail={draft.env_mode === "complete"
            ? "The image with the container's local changes, every volume and bind mount, the settings needed to recreate it safely, and its architecture. Image layers are stored once and shared between backups."
            : "Chosen folders only (today's Quick / Standard / Full). A restore needs the original image to still exist."}>
            <SettingsSegmented<"files" | "complete"> label="Container environments" value={draft.env_mode} onChange={(v) => set({ env_mode: v })}
              options={[{ value: "files", label: "Files only" }, { value: "complete", label: "Complete environment" }]} />
          </FieldRow>
        </SettingsCard>
      </div>

      <WhenCard draft={draft} set={set} adv={adv} setAdv={setAdv} dirty={dirty} now={now} />

      <CalendarCard draft={draft} dirty={dirty} now={now} />

      <SettingsCard title="Keep, where, encryption">
        <FieldRow label="Keep">
          never fewer than <InlineInput aria-label="Checked backups to keep" type="number" min={1} value={draft.keep_min} onChange={(e) => set({ keep_min: Math.max(1, +e.target.value || 1) })} /> checked backups ·
          <InlineInput aria-label="Daily" type="number" min={0} value={draft.keep_daily} onChange={(e) => set({ keep_daily: Math.max(0, +e.target.value || 0) })} /> daily ·
          <InlineInput aria-label="Weekly" type="number" min={0} value={draft.keep_weekly} onChange={(e) => set({ keep_weekly: Math.max(0, +e.target.value || 0) })} /> weekly ·
          <InlineInput aria-label="Monthly" type="number" min={0} value={draft.keep_monthly} onChange={(e) => set({ keep_monthly: Math.max(0, +e.target.value || 0) })} /> monthly · pinned are never deleted
        </FieldRow>
        <FieldRow label="Where">
          <span className="flex flex-wrap gap-x-3.5 gap-y-1.5">
            <label className="flex items-center gap-1.5"><input type="checkbox" checked disabled /> This server</label>
            {(settings.data?.destinations ?? []).filter((d) => d.kind !== "local").map((d) => (
              <label key={d.id} className={cn("flex items-center gap-1.5", !d.available && "opacity-50")}>
                <input type="checkbox" disabled={!d.available} checked={draft.destinations.includes(d.id)}
                  onChange={(e) => set({ destinations: e.target.checked ? [...draft.destinations, d.id] : draft.destinations.filter((x) => x !== d.id) })} />
                {d.label}{!d.available && " (later)"}
              </label>
            ))}
            {!offsiteReady && (
              <span className="flex items-center gap-1.5 text-muted-foreground">
                off-site: <button type="button" className="underline" onClick={() => ctx.go("storage")}>add S3-compatible storage under Storage</button>
              </span>
            )}
          </span>
        </FieldRow>
        <FieldRow label="Encryption">always{recipientNames.length ? ` · to ${recipientNames.join(" and ")}` : " · add a backup key under Keys & alerts"}</FieldRow>
      </SettingsCard>
      <SettingsSaveBar label="Backup plan" count={changed} saving={saving} onSave={save} onDiscard={() => reset(base)} />
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
        <thead><tr><th className={TH}>Category</th><th className={TH}>In this backup</th><th className={TH}><span className="sr-only">Why</span></th></tr></thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.key} data-cat={r.key} data-state={r.state} className="[&:last-child>td]:border-b-0">
              <td className={TD}>
                <label className="flex items-center gap-2">
                  <input type="checkbox" checked={r.state !== "excluded"} disabled={(!editable && r.key !== "env") || r.state === "required"}
                    onChange={(e) => onToggle(r.key, e.target.checked)} />
                  {r.label}
                </label>
              </td>
              <td className={TD}>
                <span className={cn("rounded-full px-2 py-0.5 font-mono text-[11px] font-medium",
                  r.state === "included" ? "bg-success/15 text-success" : r.state === "required" ? "bg-primary/15 text-primary-hover" : "bg-muted text-muted-foreground-soft")}>
                  {r.state === "included" ? "Included" : r.state === "required" ? "Required dependency" : "Excluded"}
                </span>
              </td>
              <td className={cn(TD, "whitespace-normal text-[12.5px] text-muted-foreground")}>
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

const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]

function WhenCard({ draft, set, adv, setAdv, dirty, now }: {
  draft: PlanDraft; set: (p: Partial<PlanDraft>) => void; adv: boolean; setAdv: (v: boolean) => void; dirty: boolean; now: Date
}) {
  const zones = React.useMemo(timezones, [])
  const server = usePlanNext(draft.id && !dirty ? draft.id : null)
  const planned = server.status === "ready" && server.data?.runs.length
    ? server.data.runs.map((r) => instantToPlanned(r.at, draft.timezone, r.environments))
    : computeNextRuns(scheduleOf(draft), wallClock(now, draft.timezone), 5)
  return (
    <SettingsCard title="When">
      <FieldRow label="Repeat">
        <span className="flex flex-wrap items-center gap-2">
          <SettingsSegmented<Cadence> label="Repeat" value={draft.cadence}
            onChange={(v) => set({ cadence: v, cron_expr: v === "custom" ? draft.cron_expr ?? cronOf(draft) : draft.cron_expr })}
            options={(Object.keys(CADENCE_LABEL) as Cadence[]).map((k) => ({ value: k, label: CADENCE_LABEL[k] }))} />
          {draft.cadence !== "custom" && (
            <>
              at <InlineInput aria-label="Time" type="time" value={draft.time_of_day} onChange={(e) => e.target.value && set({ time_of_day: e.target.value })} className="w-[6.5rem]" />
            </>
          )}
          <select aria-label="Timezone" value={draft.timezone} onChange={(e) => set({ timezone: e.target.value })}
            className="h-7 max-w-[13rem] rounded-md border border-control-border bg-surface-subtle px-2 text-[12px] coarse:h-[2.75rem]">
            {(zones.includes(draft.timezone) ? zones : [draft.timezone, ...zones]).map((z) => <option key={z} value={z}>{z}</option>)}
          </select>
        </span>
      </FieldRow>
      {draft.cadence === "weekly" && (
        <FieldRow label="On">
          <SettingsSegmented<number> label="Weekday" value={draft.weekday ?? 0} onChange={(v) => set({ weekday: v })}
            options={[1, 2, 3, 4, 5, 6, 0].map((d) => ({ value: d, label: WEEKDAYS[d] }))} />
        </FieldRow>
      )}
      {draft.cadence === "monthly" && (
        <FieldRow label="On">
          <span className="flex flex-wrap items-center gap-2">
            <SettingsSegmented<"first" | "day"> label="Day of month" value={draft.monthday ? "day" : "first"} onChange={(v) => set({ monthday: v === "first" ? null : 1 })}
              options={[{ value: "first", label: "First Sunday" }, { value: "day", label: "A day of the month" }]} />
            {draft.monthday != null && <InlineInput aria-label="Day of month" type="number" min={1} max={28} value={draft.monthday} onChange={(e) => set({ monthday: Math.min(28, Math.max(1, +e.target.value || 1)) })} />}
          </span>
        </FieldRow>
      )}
      {draft.env_mode === "complete" && (
        <FieldRow label="Environments" hint="large; less often is fine">
          <SettingsSegmented<EnvCadence> label="Environments" value={draft.env_cadence} onChange={(v) => set({ env_cadence: v })}
            options={[{ value: "every", label: "With every run" }, { value: "weekly", label: `Weekly, Sunday ${envTime(draft.time_of_day)}` }, { value: "monthly", label: "Monthly" }]} />
        </FieldRow>
      )}
      <FieldRow label="Next runs">
        <span data-slot="next-runs" className="text-[13px]">
          {planned === null ? "shown once the server has read this cron expression" : planned.length ? planned.map((r) => formatPlannedRun(r)).join(" · ") : "none"}
        </span>
      </FieldRow>
      <FieldRow label="If busy">
        let running work finish, then hold every write to protected data for the copy; wait up to
        <InlineInput aria-label="Wait up to (hours)" type="number" min={0} step={0.5} value={draft.busy_wait_minutes / 60} onChange={(e) => set({ busy_wait_minutes: Math.round(Math.max(0, +e.target.value || 0) * 60) })} />h,
        then alert. The hold ends after
        <InlineInput aria-label="Hold cap (minutes)" type="number" min={1} value={draft.hold_cap_minutes} onChange={(e) => set({ hold_cap_minutes: Math.max(1, +e.target.value || 1) })} />min at most, and packing and uploading run after it.
      </FieldRow>
      <FieldRow label="If the server was off">one backup as soon as it is back, not one per missed run</FieldRow>
      <FieldRow label={<SmallButton onClick={() => setAdv(!adv)} aria-expanded={adv}>{adv ? "▾" : "▸"} Advanced</SmallButton>}>
        {adv ? (
          <span className="flex flex-wrap items-center gap-1">
            cron <InlineInput aria-label="Cron expression" className="w-[9rem] font-mono" value={draft.cadence === "custom" ? draft.cron_expr ?? "" : cronOf(draft)}
              onChange={(e) => set({ cadence: "custom", cron_expr: e.target.value })} />
            · runs on the server, never through an agent or a model
          </span>
        ) : <span className="text-muted-foreground">cron expression, run limits</span>}
      </FieldRow>
    </SettingsCard>
  )
}

function envTime(t: string): string {
  const [h, m] = t.split(":").map(Number)
  return `${String(((h || 0) + 1) % 24).padStart(2, "0")}:${String(m || 0).padStart(2, "0")}`
}

type CalPill = { text: string; kind: "ok" | "fail" | "skip" | "plan" | "env" | "envdone" }

const PILL: Record<CalPill["kind"], string> = {
  ok: "bg-success/20 text-foreground",
  fail: "bg-destructive/25 text-foreground",
  skip: "bg-warn/25 text-foreground",
  plan: "border border-dashed border-control-border text-muted-foreground",
  env: "border border-dashed border-purple text-purple",
  envdone: "bg-success/20 text-foreground",
}

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
    <SettingsCard title="Calendar" description={`${label} · done and planned runs`}>
      <div className="grid grid-cols-7 gap-1 px-3.5 py-3" data-slot="plan-calendar">
        {["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"].map((d) => (
          <div key={d} className="pb-1 text-center font-mono text-[11px] uppercase text-muted-foreground-soft">{d}</div>
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
              {pills.map((p, i) => <span key={i} className={cn("w-fit max-w-full truncate rounded-[5px] px-1 text-[11px]", PILL[p.kind])}>{p.text}</span>)}
            </div>
          )
        })}
      </div>
      <div className="flex flex-wrap items-center gap-2.5 px-3.5 pb-3 text-[13px]">
        <span className={cn("rounded-[5px] px-1 text-[11px]", PILL.ok)}>done</span>
        <span className={cn("rounded-[5px] px-1 text-[11px]", PILL.skip)}>skipped / catch-up</span>
        <span className={cn("rounded-[5px] px-1 text-[11px]", PILL.fail)}>failed</span>
        <span className={cn("rounded-[5px] px-1 text-[11px]", PILL.plan)}>planned</span>
        <span className={cn("rounded-[5px] px-1 text-[11px]", PILL.env)}>environments</span>
      </div>
    </SettingsCard>
  )
}
