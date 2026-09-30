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

export function RetentionBody({ rows, ctx, reload }: { rows: RetentionRow[]; ctx: SectionCtx; reload?: () => void }) {
  const [draft, setDraft] = React.useState<Record<string, number | null>>({})
  const [plan, setPlan] = React.useState<RetentionChange[] | null>(null)
  const [busy, setBusy] = React.useState(false)
  const diff = retentionDiff(rows, draft)
  const editable = rows.filter((r) => !r.housekeeping)
  const housekeeping = rows.filter((r) => r.housekeeping)
  const ids = [...ctx.selected]
  // Every workspace ticked is sent as null: "every existing workspace", so one
  // created while the dialog is open is not left out. It is a selection only;
  // the defaults for new workspaces are a separate card and save below.
  const all = ctx.workspaces.length > 0 && ids.length === ctx.workspaces.length
  const target = all ? null : ids
  const label = (key: string) => rows.find((r) => r.key === key)?.label ?? key
  const changes = diff.map((d) => ({ key: d.key, days: d.to }))

  const review = async () => {
    setBusy(true)
    if (ctx.demo) {
      setPlan(ids.flatMap((id) => diff.map((d) => ({
        workspace_id: id, workspace_name: ctx.workspaces.find((w) => w.id === id)?.name ?? id, key: d.key, from: d.from, to: d.to,
        rows_affected: d.to !== null && (d.from === null || d.to < d.from) ? 120 : 0,
      }))))
    } else {
      const r = await putRetention(target, changes, true)
      if (r.ok) setPlan(r.data.changes)
      else if (r.unavailable) toast.message("Data retention: not available on this server yet")
      else toast.error(`The change could not be checked: ${r.error}`)
    }
    setBusy(false)
  }
  const swept = (plan ?? []).reduce((a, c) => a + c.rows_affected, 0)
  const consequences: Consequence[] = [
    ...(plan ?? []).map((c) => ({ tone: (c.rows_affected ? "lost" : "warn") as Consequence["tone"], text: fmtChange(c, label(c.key)) })),
    ...(swept ? [{ tone: "kept" as const, text: `${swept.toLocaleString("en-GB")} rows go at the next sweep; older backups still hold them until they age out.` }] : []),
    { tone: "kept" as const, text: "New workspaces are not changed; they start from Defaults for new workspaces." },
  ]

  return (
    <>
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
                      <SettingsSegmented<string> label={`Keep ${r.label} for`} value={r.mixed && !(r.key in draft) ? "" : value === null ? "forever" : String(value)}
                        onChange={(v) => setDraft((d) => ({ ...d, [r.key]: v === "forever" ? null : Number(v) }))} options={options} />
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
      <ConfirmDialog open={plan !== null} onOpenChange={(o) => { if (!o) setPlan(null) }} destructive={swept > 0}
        title={plan && plan.length ? `Apply ${diff.length} limit${diff.length === 1 ? "" : "s"} to ${all ? "every existing workspace" : `${ctx.selected.size} workspace${ctx.selected.size === 1 ? "" : "s"}`}?` : "Nothing to change"}
        description="The dry run lists every value this overwrites."
        consequences={consequences}
        confirmLabel="Apply"
        onConfirm={async () => {
          if (ctx.demo) { toast.message("Demo data · nothing was sent"); return }
          const r = await putRetention(target, changes, false)
          if (!r.ok) { toast.error(`The change could not be saved: ${r.error}`); throw new Error(r.error) }
          toast.success("Retention saved")
          setDraft({})
          reload?.()
        }} />
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
  const [preview, setPreview] = React.useState<RetentionDefaultsPut["changes"] | null>(null)
  const current = res.data?.defaults ?? {}
  const edits = Object.entries(draft).filter(([k, v]) => current[k] !== v)
  const label = (key: string) => rows.find((r) => r.key === key)?.label ?? key

  const review = async () => {
    if (demo) { setPreview(edits.map(([key, to]) => ({ key, from: current[key] ?? null, to }))); return }
    const r = await putRetentionDefaults(Object.fromEntries(edits), true)
    if (r.ok) setPreview(r.data.changes)
    else if (r.unavailable) toast.message("Defaults for new workspaces: not available on this server yet")
    else toast.error(`The defaults could not be checked: ${r.error}`)
  }

  if (res.status === "unavailable") return null
  return (
    <div className="overflow-hidden rounded-card border border-border bg-card" data-slot="retention-defaults">
      <div className="border-b border-border px-4 py-3">
        <div className="text-[14px] font-medium">Defaults for new workspaces</div>
        <div className="text-[13px] text-muted-foreground">What a workspace created from now on starts with. Existing workspaces are not changed.</div>
      </div>
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
                    <SettingsSegmented<string> label={`New workspaces keep ${r.label} for`} value={value === null ? "forever" : String(value)}
                      onChange={(v) => setDraft((d) => ({ ...d, [r.key]: v === "forever" ? null : Number(v) }))} options={options} />
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
        title={preview && preview.length ? `Change ${preview.length} default${preview.length === 1 ? "" : "s"} for new workspaces?` : "Nothing to change"}
        description="Only workspaces created from now on start with these."
        consequences={[
          ...(preview ?? []).map((c) => ({ tone: "warn" as const, text: `${label(c.key)}: ${retentionLabel(c.from)} → ${retentionLabel(c.to)}` })),
          { tone: "kept" as const, text: "No existing workspace changes." },
        ]}
        confirmLabel="Save defaults for new workspaces"
        onConfirm={async () => {
          if (demo) { toast.message("Demo data · nothing was sent"); return }
          const r = await putRetentionDefaults(Object.fromEntries(edits), false)
          if (!r.ok) { toast.error(`The defaults could not be saved: ${r.error}`); throw new Error(r.error) }
          toast.success("Defaults for new workspaces saved")
          setDraft({})
          res.reload()
        }} />
    </div>
  )
}
