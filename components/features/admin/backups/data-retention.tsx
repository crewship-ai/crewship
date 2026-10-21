"use client"

import * as React from "react"
import {
  Bell, Brain, Building, CheckSquare, Clock, FileText, KeyRound, MessageSquare, RefreshCw, ScrollText, Shield, Workflow,
  type LucideIcon,
} from "lucide-react"
import { toast } from "@/lib/toast"

import { cn } from "@/lib/utils"
import { SettingsCard, SettingsCardActions, SettingsRow, SettingsSaveBar, SettingsSegmented } from "@/components/features/settings/shared"
import { ConfirmDialog, type Consequence } from "@/components/ui/confirm-dialog"
import { Gate, WarnBar, WsAvatar } from "./backups-kit"
import { RETENTION_OPTIONS, retentionDiff, retentionLabel, type RetentionChange, type RetentionRow } from "./backups-model"
import { putRetention, putRetentionDefaults, useDataRetention, useRetentionDefaults, type RetentionDefaultsPut } from "./use-data-retention"
import { useBackupsScope, type SectionCtx } from "./backups-scope"

/**
 * Admin › Data retention: how long the server keeps each kind of live data,
 * per workspace. A change is never written blind — a dry run first lists
 * every value it overwrites and how many rows the next sweep deletes, and
 * older backups still hold those rows until they age out. Housekeeping the
 * server runs on its own is shown read-only.
 *
 * Harbor 2 layout: the "Applies to" strip (all workspaces or chosen ones,
 * kept in ?scope=&ws=), then the cards Keep for, Defaults for new workspaces
 * and Housekeeping.
 */
export function RetentionPage() {
  const { workspaceId, demo, workspaces, urlState, selected, scope, commit, backUpNow } = useBackupsScope(true)
  const ctx: SectionCtx = {
    scope, selected, workspaces, currentWorkspaceId: workspaceId, demo,
    focusRun: null, focusPath: null, backUpNow, newPlanSignal: 0, go: () => {},
  }
  return (
    <div data-slot="retention-page" className="space-y-4 text-control">
      <RetentionScope workspaces={workspaces} selected={selected} picking={urlState.ws !== null}
        onChange={(next) => commit(urlState.scope, next)} />
      {demo && <p className=" text-micro text-muted-foreground-soft">Demo data · ?demo=1 · nothing is sent</p>}
      <DataRetention ctx={ctx} />
    </div>
  )
}

/**
 * "Applies to  [All workspaces · 3 | Chosen workspaces]". Chosen shows the
 * workspaces as toggles; the selection is the URL's ?ws=, so a reload or a
 * shared link opens the same workspaces.
 */
export function RetentionScope({ workspaces, selected, picking: urlPicking, onChange }: {
  workspaces: { id: string; slug: string; name: string }[]
  selected: Set<string>
  /** True when the URL names workspaces (?ws=). */
  picking: boolean
  onChange: (next: Set<string>) => void
}) {
  const [picking, setPicking] = React.useState(urlPicking)
  React.useEffect(() => { if (urlPicking) setPicking(true) }, [urlPicking])
  const n = workspaces.length
  const mode = picking ? "pick" : "all"
  const names = workspaces.filter((w) => selected.has(w.id)).map((w) => w.name)
  return (
    <div data-slot="retention-scope" className="flex flex-wrap items-center gap-2.5">
      <span className="text-label text-muted-foreground">Applies to</span>
      <SettingsSegmented<"all" | "pick"> label="Applies to" value={mode}
        onChange={(v) => {
          setPicking(v === "pick")
          if (v === "all") onChange(new Set(workspaces.map((w) => w.id)))
        }}
        options={[{ value: "all", label: `All workspaces · ${n}` }, { value: "pick", label: "Chosen workspaces" }]} />
      <span className="flex-1" />
      <span className="text-label text-muted-foreground" data-slot="scope-count">
        {mode === "all" ? "One policy for the whole Crewship" : names.length ? names.join(", ") : "No workspace chosen"}
      </span>
      {mode === "pick" && (
        <div className="flex w-full flex-wrap items-center gap-1.5" role="group" aria-label="Chosen workspaces">
          {workspaces.map((w) => {
            const on = selected.has(w.id)
            return (
              <button key={w.id} type="button" aria-pressed={on} data-ws={w.slug}
                onClick={() => {
                  const next = new Set(selected)
                  if (on) next.delete(w.id)
                  else next.add(w.id)
                  onChange(next)
                }}
                className={cn(
                  "inline-flex h-7 items-center gap-1.5 rounded-full border border-control-border py-0.5 pl-1 pr-2.5 text-control transition-opacity coarse:h-[44px]",
                  !on && "border-dashed opacity-50",
                )}>
                <WsAvatar name={w.name} size={18} />
                {w.name}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}

export function DataRetention({ ctx }: { ctx: SectionCtx }) {
  const res = useDataRetention(ctx.selected)
  if (ctx.selected.size === 0) return <p className="text-control text-muted-foreground">Choose a workspace above.</p>
  return (
    <Gate resource={res} what="Data retention" skeleton="h-[360px]">
      {(data) => <RetentionBody rows={data.rows} ctx={ctx} reload={res.reload} />}
    </Gate>
  )
}

/** The row's grey tile, by retention key; an unknown key gets the clock. */
const KEY_ICON: Record<string, LucideIcon> = {
  routine_runs_days: Workflow,
  approvals_days: CheckSquare,
  audit_days: ScrollText,
  credential_audit_days: KeyRound,
  memory_versions_days: Brain,
  page_panel_data_days: FileText,
  inbox_days: Bell,
  chats_days: MessageSquare,
  keeper_decisions_days: Shield,
}
const keyIcon = (key: string) => KEY_ICON[key] ?? Clock

function fmtChange(c: RetentionChange, label: string): string {
  const rows = c.rows_affected ? ` · ${c.rows_affected.toLocaleString("en-GB")} row${c.rows_affected === 1 ? "" : "s"} go at the next sweep` : ""
  return `${c.workspace_name} · ${label}: ${retentionLabel(c.from)} → ${retentionLabel(c.to)}${rows}`
}

/** What the confirmation says when the server refuses a stale preview (409). */
export const STALE_PREVIEW = "Something changed since the preview — review again"
/** The line the confirmation adds once it has fetched a new preview after a 409. */
export const NEW_PREVIEW = "Someone changed retention while you were reviewing. This is the new preview; nothing was written."
/** Said instead of a dialog when the dry run finds nothing to write. */
export const NOTHING_TO_CHANGE = "Nothing to change — the chosen workspaces already keep these"

/**
 * A preview and the exact request it previewed. Confirming sends this
 * snapshot — targets, changes and the server's preview_id — never whatever
 * the form or the selection holds by then (review B3).
 */
interface RetentionPreview {
  plan: RetentionChange[]
  /** null is "every existing workspace". */
  targets: string[] | null
  changes: { key: string; days: number | null }[]
  previewId?: string
  /** For the dialog title: how many limits, on how many workspaces. */
  count: number
  all: boolean
  size: number
  /** Set after a 409: this preview replaced the one that went stale. */
  refreshed?: boolean
}

/** A segmented value of RETENTION_OPTIONS, plus the server's own value when it is not one of them. */
function windowOptions(r: RetentionRow, value: number | null) {
  const options = RETENTION_OPTIONS.filter((o) => o.days !== null || r.foreverAllowed !== false)
    .map((o) => ({ value: o.days === null ? "forever" : String(o.days), label: o.label }))
  if (value !== null && !options.some((o) => o.value === String(value))) options.push({ value: String(value), label: `${value} d` })
  return options
}

export function RetentionBody({ rows, ctx, reload }: { rows: RetentionRow[]; ctx: SectionCtx; reload?: () => void }) {
  const [draft, setDraft] = React.useState<Record<string, number | null>>({})
  const [preview, setPreview] = React.useState<RetentionPreview | null>(null)
  const [busy, setBusy] = React.useState(false)
  const [nothing, setNothing] = React.useState(false)
  const [stale, setStale] = React.useState(false)
  const diff = retentionDiff(rows, draft)
  const editable = rows.filter((r) => !r.housekeeping)
  const housekeeping = rows.filter((r) => r.housekeeping)
  const ids = [...ctx.selected].sort()
  // Every workspace ticked is sent as null: "every existing workspace", so one
  // created while the dialog is open is not left out (the server's preview_id
  // covers the list, so that is a 409, not a silent extra write). It is a
  // selection only; the defaults for new workspaces are a separate card.
  const all = ctx.workspaces.length > 0 && ids.length === ctx.workspaces.length
  const label = (key: string) => rows.find((r) => r.key === key)?.label ?? key
  const changes = diff.map((d) => ({ key: d.key, days: d.to }))

  // A draft and a preview belong to the workspaces they were made for. Any
  // change of selection drops both; any change of the draft drops the
  // preview. The epoch is bumped each time, so an answer asked for before
  // (A→B→A included) is recognised as stale and dropped.
  const scopeKey = all ? "all" : ids.join(",")
  const draftKey = JSON.stringify(changes)
  const epoch = React.useRef(0)
  const scopeRef = React.useRef(scopeKey)
  const draftKeyRef = React.useRef(draftKey)
  const draftRef = React.useRef(draft)
  draftRef.current = draft
  React.useEffect(() => {
    if (scopeRef.current === scopeKey) return
    scopeRef.current = scopeKey
    epoch.current += 1
    if (Object.keys(draftRef.current).length > 0) toast.info("The selection changed, so the unsaved retention changes were dropped.")
    setDraft({}); setPreview(null); setBusy(false); setStale(false)
  }, [scopeKey])
  React.useEffect(() => {
    if (draftKeyRef.current === draftKey) return
    draftKeyRef.current = draftKey
    epoch.current += 1
    setPreview(null); setBusy(false)
  }, [draftKey])

  const review = async () => {
    const asked = ++epoch.current
    const snap = { targets: all ? null : ids, changes, count: diff.length, all, size: ids.length }
    setBusy(true); setStale(false)
    if (ctx.demo) {
      setPreview({ ...snap, plan: ids.flatMap((id) => diff.map((d) => ({
        workspace_id: id, workspace_name: ctx.workspaces.find((w) => w.id === id)?.name ?? id, key: d.key, from: d.from, to: d.to,
        rows_affected: d.to !== null && (d.from === null || d.to < d.from) ? 120 : 0,
      }))) })
      setBusy(false)
      return
    }
    const r = await putRetention(snap.targets, snap.changes, true)
    // An answer for a selection or a draft that is no longer on screen is dropped.
    if (epoch.current !== asked) return
    if (r.ok && r.data.changes.length === 0) setNothing(true)
    else if (r.ok) setPreview({ ...snap, plan: r.data.changes, previewId: r.data.preview_id })
    else if (r.unavailable) toast.message("Data retention: not available on this server yet")
    else toast.error(`The change could not be checked: ${r.error}`)
    setBusy(false)
  }
  const apply = async () => {
    const p = preview
    if (!p) return
    if (ctx.demo) { toast.message("Demo data · nothing was sent"); return }
    const r = await putRetention(p.targets, p.changes, false, p.previewId)
    if (!r.ok && r.status === 409) {
      // Nothing was written. The dialog stays and shows a fresh preview, so
      // the admin decides again on what the server holds now.
      setStale(true)
      toast.error(STALE_PREVIEW)
      reload?.()
      const again = await putRetention(p.targets, p.changes, true)
      if (again.ok && again.data.changes.length > 0) {
        setPreview({ ...p, plan: again.data.changes, previewId: again.data.preview_id, refreshed: true })
        throw new Error(STALE_PREVIEW)
      }
      // The other change already did what this one asked: nothing is left.
      setPreview(null)
      if (again.ok) setNothing(true)
      return
    }
    if (!r.ok) { toast.error(`The change could not be saved: ${r.error}`); throw new Error(r.error) }
    toast.success("Retention saved")
    setStale(false)
    setDraft({})
    reload?.()
  }
  const plan = preview?.plan ?? []
  const swept = plan.reduce((a, c) => a + c.rows_affected, 0)
  const consequences: Consequence[] = [
    ...(preview?.refreshed ? [{ tone: "warn" as const, text: NEW_PREVIEW }] : []),
    ...plan.map((c) => ({ tone: (c.rows_affected ? "lost" : "warn") as Consequence["tone"], text: fmtChange(c, label(c.key)) })),
    // Deleting rows is a loss, said in the loss tone; only what stays is green.
    ...(swept ? [
      { tone: "lost" as const, text: `${swept.toLocaleString("en-GB")} row${swept === 1 ? "" : "s"} go at the next sweep.` },
      { tone: "kept" as const, text: "Older backups still hold them until they age out." },
    ] : []),
    { tone: "kept" as const, text: "New workspaces are not changed; they start from Defaults for new workspaces." },
  ]
  const mixedAny = editable.some((r) => r.mixed && !(r.key in draft))

  return (
    <>
      {stale && <WarnBar tone="bad" title={STALE_PREVIEW}>Nothing was written. The values below are still unsaved.</WarnBar>}
      <SettingsCard icon={Clock} title="Keep for" description="Saving shows a dry run of what would be deleted first">
        <div data-slot="retention-table">
          {editable.map((r) => {
            const value = r.key in draft ? draft[r.key] : r.days
            const mixed = r.mixed && !(r.key in draft)
            return (
              <div key={r.key} data-key={r.key}>
                <SettingsRow lead={keyIcon(r.key)} label={r.label}
                  description={mixed
                    ? <span className="text-warn">The chosen workspaces differ · a value here sets it for all of them</span>
                    : r.note ?? undefined}>
                  <SettingsSegmented<string> label={`Keep ${r.label} for`} value={mixed ? "" : value === null ? "forever" : String(value)}
                    onChange={(v) => setDraft((d) => ({ ...d, [r.key]: v === "forever" ? null : Number(v) }))} options={windowOptions(r, value)} />
                </SettingsRow>
              </div>
            )
          })}
        </div>
      </SettingsCard>
      {mixedAny && ctx.selected.size > 1 && (
        <p className="text-label text-muted-foreground">Choose workspaces above to change only some of them.</p>
      )}
      <SettingsSaveBar label="Data retention" count={diff.length} saving={busy} onDiscard={() => setDraft({})} onSave={review} />
      <ConfirmDialog open={preview !== null} onOpenChange={(o) => { if (!o) setPreview(null) }} destructive={swept > 0}
        title={preview ? `Apply ${preview.count} limit${preview.count === 1 ? "" : "s"} to ${preview.all ? "every existing workspace" : `${preview.size} workspace${preview.size === 1 ? "" : "s"}`}?` : ""}
        description="The dry run lists every value this overwrites."
        consequences={consequences}
        confirmLabel="Apply"
        onConfirm={apply} />
      {/* A dry run that changes nothing is a notice, not a decision: Close only. */}
      <ConfirmDialog open={nothing} onOpenChange={setNothing} closeOnly
        title="Nothing to change" description={NOTHING_TO_CHANGE} />
      <RetentionDefaultsCard rows={editable} demo={ctx.demo} />
      {housekeeping.length > 0 && (
        <SettingsCard icon={RefreshCw} title="Housekeeping" description="Runs by itself; not adjustable">
          <div data-slot="retention-housekeeping">
            {housekeeping.map((r) => (
              <div key={r.key} data-key={r.key}>
                <SettingsRow lead={keyIcon(r.key)} label={r.label} description={r.note ?? undefined}>
                  <span className="text-label text-muted-foreground">{r.fixed ?? retentionLabel(r.days)}</span>
                </SettingsRow>
              </div>
            ))}
          </div>
        </SettingsCard>
      )}
    </>
  )
}

/** How many defaults show before "Show all N". */
const DEFAULTS_FOLD = 4

/**
 * What a workspace created from now on starts with. A separate operation from
 * applying limits to existing workspaces, with its own confirmation: it never
 * touches an existing workspace or deletes a row.
 */
export function RetentionDefaultsCard({ rows, demo }: { rows: RetentionRow[]; demo: boolean }) {
  const res = useRetentionDefaults(true)
  const [draft, setDraft] = React.useState<Record<string, number | null>>({})
  const [showAll, setShowAll] = React.useState(false)
  // The preview and the exact windows it previewed; confirming sends those
  // with the server's preview_id, never the form as it is by then.
  const [preview, setPreview] = React.useState<{ changes: RetentionDefaultsPut["changes"]; windows: Record<string, number | null>; previewId?: string; refreshed?: boolean } | null>(null)
  const [stale, setStale] = React.useState(false)
  const current = res.data?.defaults ?? {}
  const edits = Object.entries(draft).filter(([k, v]) => current[k] !== v)
  const label = (key: string) => rows.find((r) => r.key === key)?.label ?? key
  // Any edit drops a preview, and an answer to an older request is dropped.
  const editsKey = JSON.stringify(edits)
  const epoch = React.useRef(0)
  const editsRef = React.useRef(editsKey)
  React.useEffect(() => {
    if (editsRef.current === editsKey) return
    editsRef.current = editsKey
    epoch.current += 1
    setPreview(null)
  }, [editsKey])

  const review = async () => {
    const asked = ++epoch.current
    const windows = Object.fromEntries(edits)
    setStale(false)
    if (demo) { setPreview({ windows, changes: edits.map(([key, to]) => ({ key, from: current[key] ?? null, to })) }); return }
    const r = await putRetentionDefaults(windows, true)
    if (epoch.current !== asked) return
    if (r.ok && r.data.changes.length === 0) toast.info("Nothing to change — new workspaces already start with these")
    else if (r.ok) setPreview({ windows, changes: r.data.changes, previewId: r.data.preview_id })
    else if (r.unavailable) toast.message("Defaults for new workspaces: not available on this server yet")
    else toast.error(`The defaults could not be checked: ${r.error}`)
  }
  const apply = async () => {
    const p = preview
    if (!p) return
    if (demo) { toast.message("Demo data · nothing was sent"); return }
    const r = await putRetentionDefaults(p.windows, false, p.previewId)
    if (!r.ok && r.status === 409) {
      // Nothing was written; the dialog stays with a fresh preview.
      setStale(true)
      toast.error(STALE_PREVIEW)
      res.reload()
      const again = await putRetentionDefaults(p.windows, true)
      if (again.ok && again.data.changes.length > 0) {
        setPreview({ ...p, changes: again.data.changes, previewId: again.data.preview_id, refreshed: true })
        throw new Error(STALE_PREVIEW)
      }
      setPreview(null)
      return
    }
    if (!r.ok) { toast.error(`The defaults could not be saved: ${r.error}`); throw new Error(r.error) }
    toast.success("Defaults for new workspaces saved")
    setStale(false)
    setDraft({})
    res.reload()
  }

  if (res.status === "unavailable") return null
  const shown = showAll ? rows : rows.slice(0, DEFAULTS_FOLD)
  return (
    <SettingsCard icon={Building} title="Defaults for new workspaces" description="A new workspace starts with these; change it later here">
      <div data-slot="retention-defaults">
        {stale && <div className="border-b border-border px-4 py-2.5"><WarnBar tone="bad" title={STALE_PREVIEW}>Nothing was written.</WarnBar></div>}
        {shown.map((r) => {
          const value = r.key in draft ? draft[r.key] : (current[r.key] ?? null)
          return (
            <div key={r.key} data-key={r.key}>
              <SettingsRow lead={keyIcon(r.key)} label={r.label}>
                <SettingsSegmented<string> label={`New workspaces keep ${r.label} for`} value={value === null ? "forever" : String(value)}
                  onChange={(v) => setDraft((d) => ({ ...d, [r.key]: v === "forever" ? null : Number(v) }))} options={windowOptions(r, value)} />
              </SettingsRow>
            </div>
          )
        })}
        {rows.length > DEFAULTS_FOLD && (
          <SettingsCardActions className="border-t border-border pt-2.5">
            <button type="button" onClick={() => setShowAll((v) => !v)} aria-expanded={showAll}
              className="text-label font-medium text-primary-hover hover:underline">
              {showAll ? "Show fewer" : `Show all ${rows.length}`}
            </button>
          </SettingsCardActions>
        )}
      </div>
      {/* The page's Save bar opens the preview below; the confirm writes. */}
      <SettingsSaveBar label="Defaults for new workspaces" count={edits.length} onDiscard={() => setDraft({})} onSave={review} />
      <ConfirmDialog open={preview !== null} onOpenChange={(o) => { if (!o) setPreview(null) }}
        title={preview ? `Change ${preview.changes.length} default${preview.changes.length === 1 ? "" : "s"} for new workspaces?` : ""}
        description="Only workspaces created from now on start with these."
        consequences={[
          ...(preview?.refreshed ? [{ tone: "warn" as const, text: "Someone changed these defaults while you were reviewing. This is the new preview; nothing was written." }] : []),
          ...(preview?.changes ?? []).map((c) => ({ tone: "warn" as const, text: `${label(c.key)}: ${retentionLabel(c.from)} → ${retentionLabel(c.to)}` })),
          { tone: "kept" as const, text: "No existing workspace changes." },
        ]}
        confirmLabel="Save defaults for new workspaces"
        onConfirm={apply} />
    </SettingsCard>
  )
}
