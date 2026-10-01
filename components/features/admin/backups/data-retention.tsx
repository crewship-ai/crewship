"use client"

import * as React from "react"
import { toast } from "sonner"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { SettingsSaveBar, SettingsSegmented } from "@/components/features/settings/shared"
import { ConfirmDialog, type Consequence } from "@/components/ui/confirm-dialog"
import { Gate, TD, TH, WarnBar } from "./backups-kit"
import { RETENTION_OPTIONS, retentionDiff, retentionLabel, type RetentionChange, type RetentionRow } from "./backups-model"
import { putRetention, putRetentionDefaults, useDataRetention, useRetentionDefaults, type RetentionDefaultsPut } from "./use-data-retention"
import type { SectionCtx } from "./backups-console"

/**
 * Admin › Data retention: how long the server keeps each kind of live data,
 * per workspace. A change is never written blind — a dry run first lists
 * every value it overwrites and how many rows the next sweep deletes, and
 * older backups still hold those rows until they age out. Housekeeping the
 * server runs on its own is shown read-only.
 */
export function DataRetention({ ctx }: { ctx: SectionCtx }) {
  const res = useDataRetention(ctx.selected)
  if (ctx.selected.size === 0) return <p className="text-[13px] text-muted-foreground">Tick a workspace in the bar above.</p>
  return (
    <Gate resource={res} what="Data retention" skeleton="h-[360px]">
      {(data) => <RetentionBody rows={data.rows} ctx={ctx} reload={res.reload} />}
    </Gate>
  )
}

function fmtChange(c: RetentionChange, label: string): string {
  const rows = c.rows_affected ? ` · ${c.rows_affected.toLocaleString("en-GB")} row${c.rows_affected === 1 ? "" : "s"} go at the next sweep` : ""
  return `${c.workspace_name} · ${label}: ${retentionLabel(c.from)} → ${retentionLabel(c.to)}${rows}`
}

/** What the confirmation says when the server refuses a stale preview (409). */
export const STALE_PREVIEW = "Something changed since the preview — review again"

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
}

export function RetentionBody({ rows, ctx, reload }: { rows: RetentionRow[]; ctx: SectionCtx; reload?: () => void }) {
  const [draft, setDraft] = React.useState<Record<string, number | null>>({})
  const [preview, setPreview] = React.useState<RetentionPreview | null>(null)
  const [busy, setBusy] = React.useState(false)
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
    if (r.ok) setPreview({ ...snap, plan: r.data.changes, previewId: r.data.preview_id })
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
      // Nothing was written. The draft stays so the admin can review again.
      setStale(true)
      toast.error(STALE_PREVIEW)
      reload?.()
      return
    }
    if (!r.ok) { toast.error(`The change could not be saved: ${r.error}`); throw new Error(r.error) }
    toast.success("Retention saved")
    setDraft({})
    reload?.()
  }
  const plan = preview?.plan ?? []
  const swept = plan.reduce((a, c) => a + c.rows_affected, 0)
  const consequences: Consequence[] = [
    ...plan.map((c) => ({ tone: (c.rows_affected ? "lost" : "warn") as Consequence["tone"], text: fmtChange(c, label(c.key)) })),
    ...(swept ? [{ tone: "kept" as const, text: `${swept.toLocaleString("en-GB")} rows go at the next sweep; older backups still hold them until they age out.` }] : []),
    { tone: "kept" as const, text: "New workspaces are not changed; they start from Defaults for new workspaces." },
  ]

  return (
    <>
      {stale && <WarnBar tone="bad" title={STALE_PREVIEW}>Nothing was written. The values below are still unsaved; Save shows the new preview.</WarnBar>}
      {ctx.selected.size > 1 && (
        <WarnBar title={`${ctx.selected.size} workspaces selected.`}>A change is written into each after a dialog lists what it overwrites. New workspaces are not affected; their defaults are set below.</WarnBar>
      )}
      <div className="overflow-hidden rounded-card border border-border bg-card">
        <div className="overflow-x-auto">
          <table className="w-full" data-slot="retention-table">
            <thead><tr><th className={TH}>Data</th><th className={TH}>Keep for</th></tr></thead>
            <tbody>
              {editable.map((r) => {
                const value = r.key in draft ? draft[r.key] : r.days
                const known = RETENTION_OPTIONS.some((o) => o.days === value)
                const options = [...RETENTION_OPTIONS.filter((o) => o.days !== null || r.foreverAllowed !== false).map((o) => ({ value: o.days === null ? "forever" : String(o.days), label: o.label })),
                  ...(!known && value !== null ? [{ value: String(value), label: `${value} d` }] : [])]
                return (
                  <tr key={r.key} data-key={r.key} className="[&:last-child>td]:border-b-0">
                    <td className={cn(TD, "whitespace-normal")}>
                      <div>{r.label}{r.mixed && !(r.key in draft) && <span className="ml-2 font-mono text-[10.5px] text-warn">mixed</span>}</div>
                      {r.note && <div className="text-[12.5px] text-muted-foreground">{r.note}</div>}
                    </td>
                    <td className={TD}>
                      <div className="w-max"><SettingsSegmented<string> label={`Keep ${r.label} for`} value={r.mixed && !(r.key in draft) ? "" : value === null ? "forever" : String(value)}
                        onChange={(v) => setDraft((d) => ({ ...d, [r.key]: v === "forever" ? null : Number(v) }))} options={options} /></div>
                    </td>
                  </tr>
                )
              })}
              {housekeeping.length > 0 && (
                <tr><td colSpan={2} className={cn(TH, "bg-muted/40")}>Housekeeping · runs on its own, not editable here</td></tr>
              )}
              {housekeeping.map((r) => (
                <tr key={r.key} data-key={r.key} className="text-muted-foreground [&:last-child>td]:border-b-0">
                  <td className={TD}>{r.label}</td>
                  <td className={cn(TD, "font-mono text-[12px]")}>{r.fixed ?? retentionLabel(r.days)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
      <p className="rounded-lg border border-border bg-card px-3 py-2 text-[13px] text-muted-foreground">
        Turning a limit on says how many rows go at the next sweep, and that older backups still hold them until they age out.
      </p>
      <SettingsSaveBar count={diff.length} saving={busy} onDiscard={() => setDraft({})} onSave={review} />
      <ConfirmDialog open={preview !== null} onOpenChange={(o) => { if (!o) setPreview(null) }} destructive={swept > 0}
        title={preview && plan.length ? `Apply ${preview.count} limit${preview.count === 1 ? "" : "s"} to ${preview.all ? "every existing workspace" : `${preview.size} workspace${preview.size === 1 ? "" : "s"}`}?` : "Nothing to change"}
        description="The dry run lists every value this overwrites."
        consequences={consequences}
        confirmLabel="Apply"
        onConfirm={apply} />
      <RetentionDefaultsCard rows={editable} demo={ctx.demo} />
    </>
  )
}

/**
 * What a workspace created from now on starts with. A separate operation from
 * applying limits to existing workspaces, with its own confirmation: it never
 * touches an existing workspace or deletes a row.
 */
export function RetentionDefaultsCard({ rows, demo }: { rows: RetentionRow[]; demo: boolean }) {
  const res = useRetentionDefaults(true)
  const [draft, setDraft] = React.useState<Record<string, number | null>>({})
  // The preview and the exact windows it previewed; confirming sends those
  // with the server's preview_id, never the form as it is by then.
  const [preview, setPreview] = React.useState<{ changes: RetentionDefaultsPut["changes"]; windows: Record<string, number | null>; previewId?: string } | null>(null)
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
    if (r.ok) setPreview({ windows, changes: r.data.changes, previewId: r.data.preview_id })
    else if (r.unavailable) toast.message("Defaults for new workspaces: not available on this server yet")
    else toast.error(`The defaults could not be checked: ${r.error}`)
  }
  const apply = async () => {
    const p = preview
    if (!p) return
    if (demo) { toast.message("Demo data · nothing was sent"); return }
    const r = await putRetentionDefaults(p.windows, false, p.previewId)
    if (!r.ok && r.status === 409) {
      setStale(true)
      toast.error(STALE_PREVIEW)
      res.reload()
      return
    }
    if (!r.ok) { toast.error(`The defaults could not be saved: ${r.error}`); throw new Error(r.error) }
    toast.success("Defaults for new workspaces saved")
    setDraft({})
    res.reload()
  }

  if (res.status === "unavailable") return null
  return (
    <div className="overflow-hidden rounded-card border border-border bg-card" data-slot="retention-defaults">
      <div className="border-b border-border px-4 py-3">
        <div className="text-[14px] font-medium">Defaults for new workspaces</div>
        <div className="text-[13px] text-muted-foreground">What a workspace created from now on starts with. Existing workspaces are not changed.</div>
      </div>
      {stale && <div className="border-b border-border px-4 py-2.5"><WarnBar tone="bad" title={STALE_PREVIEW}>Nothing was written.</WarnBar></div>}
      <div className="overflow-x-auto">
        <table className="w-full">
          <tbody>
            {rows.map((r) => {
              const value = r.key in draft ? draft[r.key] : (current[r.key] ?? null)
              const options = RETENTION_OPTIONS.filter((o) => o.days !== null || r.foreverAllowed !== false)
                .map((o) => ({ value: o.days === null ? "forever" : String(o.days), label: o.label }))
              if (value !== null && !options.some((o) => o.value === String(value))) options.push({ value: String(value), label: `${value} d` })
              return (
                <tr key={r.key} data-key={r.key} className="[&:last-child>td]:border-b-0">
                  <td className={cn(TD, "whitespace-normal")}>{r.label}</td>
                  <td className={TD}>
                    <div className="w-max"><SettingsSegmented<string> label={`New workspaces keep ${r.label} for`} value={value === null ? "forever" : String(value)}
                      onChange={(v) => setDraft((d) => ({ ...d, [r.key]: v === "forever" ? null : Number(v) }))} options={options} /></div>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      <div className="flex items-center gap-2 border-t border-border px-4 py-2.5">
        <Button size="sm" disabled={edits.length === 0} onClick={() => void review()}>Save defaults for new workspaces…</Button>
        {edits.length > 0 && <Button size="sm" variant="ghost" onClick={() => setDraft({})}>Discard</Button>}
      </div>
      <ConfirmDialog open={preview !== null} onOpenChange={(o) => { if (!o) setPreview(null) }}
        title={preview && preview.changes.length ? `Change ${preview.changes.length} default${preview.changes.length === 1 ? "" : "s"} for new workspaces?` : "Nothing to change"}
        description="Only workspaces created from now on start with these."
        consequences={[
          ...(preview?.changes ?? []).map((c) => ({ tone: "warn" as const, text: `${label(c.key)}: ${retentionLabel(c.from)} → ${retentionLabel(c.to)}` })),
          { tone: "kept" as const, text: "No existing workspace changes." },
        ]}
        confirmLabel="Save defaults for new workspaces"
        onConfirm={apply} />
    </div>
  )
}
