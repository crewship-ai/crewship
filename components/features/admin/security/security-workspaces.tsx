"use client"

import * as React from "react"
import { ChevronDown, ChevronRight, Sparkles, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { ShowMore } from "@/components/ui/show-more"
import { StatusPill } from "@/components/ui/status-pill"
import { useShowMore } from "@/hooks/use-show-more"
import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceHeader } from "@/components/layout/create-surface"
import { selectionBar, selectionBarAction, selectionBarButton, settingsTable, settingsTh, settingsTd, settingsTableRowLink } from "@/components/features/settings/shared"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import { BulkGovernanceForm, SECTION_TITLE, type BulkSection } from "./bulk-governance"
import { accessLabel, secondApprover } from "./security-model"
import { checkLabel, judgeKind, watchSummary } from "./watch-labels"
import type { InstanceGovRow } from "./use-instance-keeper"

/** The selection bar's buttons: each opens the form for that part of the watch. */
const BULK: { key: BulkSection; label: string }[] = [
  { key: "watchdog", label: "Watchdog" },
  { key: "alerts", label: "Alerts" },
  { key: "leases", label: "Access time" },
  { key: "workspace-judge", label: "Judge" },
]

/**
 * Admin › Security › Workspaces: how each workspace is watched, one row each,
 * 25 a step. A row opens that workspace; ticked rows change together from the
 * bar, through the same dry-run preview and confirmation as ever.
 */
export function SecurityWorkspaces({ rows, all, total, off, filterKey, defaults, onOpen, onOpenDefaults, picked, onPick, onSaved }: {
  /** The rows that match the filters, whole; this view pages them. */
  rows: InstanceGovRow[]
  /** Every workspace: a ticked one stays ticked while a filter hides it. */
  all: InstanceGovRow[]
  /** How many workspaces there are, and how many have the watchdog off. */
  total: number
  off: number
  filterKey: string
  defaults: boolean
  onOpen: (id: string) => void
  onOpenDefaults: () => void
  picked: Set<string>
  onPick: (ids: string[], on: boolean) => void
  onSaved: () => void
}) {
  const canAct = useIsInstanceAdmin() === true
  const paged = useShowMore(rows, { resetKey: filterKey })
  const [bulk, setBulk] = React.useState<BulkSection | null>(null)
  const pickedRows = all.filter((r) => picked.has(r.workspace_id))
  const allOn = rows.length > 0 && rows.every((r) => picked.has(r.workspace_id))
  const open = (fn: () => void) => ({ tabIndex: 0, onClick: fn, onKeyDown: (e: React.KeyboardEvent) => { if (e.key === "Enter") fn() } })
  const count = rows.length === total ? `${total} ${total === 1 ? "workspace" : "workspaces"}` : `${rows.length} of ${total} workspaces`

  return (
    <>
      <section aria-label="Workspaces" className="overflow-hidden rounded-card border border-border bg-card">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-border px-4 py-3">
          <div className="min-w-0 flex-[1_1_12rem]">
            <h3 className="text-sm font-semibold" data-slot="workspaces-count">{count}</h3>
            <p className="mt-0.5 text-label text-muted-foreground">{off} with the watchdog off</p>
          </div>
          {defaults && (
            <Button type="button" variant="outline" size="sm" onClick={onOpenDefaults}><Sparkles className="mr-1 size-3.5" />Defaults for new ones</Button>
          )}
        </div>

        <ul className="md:hidden" aria-label="Workspaces" data-slot="workspaces-list">
          {paged.shown.map((r) => (
            <li key={r.workspace_id} className="flex items-center gap-3 border-b border-border px-4 py-3 last:border-b-0">
              <input type="checkbox" aria-label={`Select ${r.workspace_name}`} checked={picked.has(r.workspace_id)} disabled={!canAct}
                onChange={(e) => onPick([r.workspace_id], e.target.checked)} className="size-5 accent-[var(--primary)]" />
              <button type="button" onClick={() => onOpen(r.workspace_id)} className="flex coarse:min-h-[44px] min-w-0 flex-1 items-center gap-2.5 text-left">
                <WorkspaceTile id={r.workspace_id} name={r.workspace_name} logoUrl={r.workspace_logo_url} />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-control font-medium">{r.workspace_name}</span>
                  <span className="block truncate text-label text-muted-foreground">{watchSummary(r)} · access {accessLabel(r.auto_lease_seconds).toLowerCase()} · {judgeKind(r).toLowerCase()} judge</span>
                </span>
                {!r.enabled && <StatusPill tone="warn" label="Off" />}
                <ChevronRight className="size-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
              </button>
            </li>
          ))}
        </ul>

        <div className="overflow-x-auto max-md:hidden">
          <table className={settingsTable} aria-label="Workspaces">
            <thead>
              <tr>
                <th className={cn(settingsTh, "w-9")}>
                  <input type="checkbox" aria-label="Select every workspace listed" checked={allOn} disabled={rows.length === 0 || !canAct}
                    onChange={(e) => onPick(rows.map((r) => r.workspace_id), e.target.checked)} className="size-3.5 accent-[var(--primary)]" />
                </th>
                <th className={settingsTh}>Workspace</th>
                <th className={settingsTh}>Watchdog</th>
                <th className={settingsTh}>Checks</th>
                <th className={settingsTh}>Alert from risk</th>
                <th className={settingsTh}>Second approver</th>
                <th className={settingsTh}>Access lasts</th>
                <th className={settingsTh}>Judge</th>
                <th className={cn(settingsTh, "w-6")} aria-hidden />
              </tr>
            </thead>
            <tbody>
              {paged.shown.map((r) => {
                const second = secondApprover(r)
                return (
                  <tr key={r.workspace_id} data-workspace={r.workspace_slug} aria-label={r.workspace_name}
                    className={cn(settingsTableRowLink, picked.has(r.workspace_id) && "bg-accent/40")} {...open(() => onOpen(r.workspace_id))}>
                    <td className={settingsTd} onClick={(e) => e.stopPropagation()}>
                      <input type="checkbox" aria-label={`Select ${r.workspace_name}`} checked={picked.has(r.workspace_id)} disabled={!canAct}
                        onChange={(e) => onPick([r.workspace_id], e.target.checked)} className="size-3.5 accent-[var(--primary)]" />
                    </td>
                    <td className={settingsTd}>
                      <span className="flex min-w-0 items-center gap-2.5">
                        <WorkspaceTile id={r.workspace_id} name={r.workspace_name} logoUrl={r.workspace_logo_url} />
                        <span className="truncate font-medium">{r.workspace_name}</span>
                      </span>
                    </td>
                    <td className={settingsTd} data-col="watchdog">{r.enabled ? <span className="text-label text-muted-foreground">On</span> : <StatusPill tone="warn" label="Off" />}</td>
                    <td className={cn(settingsTd, "whitespace-nowrap font-mono text-label text-muted-foreground")} data-col="checks">{r.enabled ? checkLabel(r) : "—"}</td>
                    <td className={cn(settingsTd, "whitespace-nowrap font-mono text-label text-muted-foreground")} data-col="alert">≥ {r.deny_notify_min_risk}</td>
                    <td className={cn(settingsTd, "whitespace-nowrap text-label", second.fromLevel === null ? "text-muted-foreground" : "text-foreground")} data-col="second-approver">{second.text}</td>
                    <td className={cn(settingsTd, "whitespace-nowrap text-label text-muted-foreground")} data-col="access">{accessLabel(r.auto_lease_seconds)}</td>
                    <td className={cn(settingsTd, "whitespace-nowrap text-label text-muted-foreground")} data-col="judge" title={r.gov_model_provider ? `${r.gov_model_provider} · ${r.gov_model_id}` : undefined}>{judgeKind(r)}</td>
                    <td className={settingsTd}><ChevronRight className="size-3.5 text-muted-foreground-soft" aria-hidden /></td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
        {rows.length === 0 && <p className="px-4 py-8 text-center text-label text-muted-foreground">No workspace matches.</p>}
        <ShowMore shown={paged.shown.length} total={paged.total} nextCount={paged.nextCount} onMore={paged.showMore} paged={paged.paged} noun="workspaces" />
      </section>

      {pickedRows.length > 0 && (
        <div role="toolbar" aria-label="Change the selected workspaces" data-slot="security-bulk"
          className={cn(selectionBar, "fixed bottom-6 left-1/2 -translate-x-1/2 whitespace-nowrap",
            // A phone: edge to edge above the tab bar, the count without its word.
            "max-md:inset-x-3 max-md:bottom-[calc(var(--mobile-tab-bar-h,0px)+0.75rem)] max-md:translate-x-0 max-md:justify-between max-md:gap-1 max-md:px-2")}>
          <span className="px-1"><span className="font-semibold tabular-nums">{pickedRows.length}</span><span className="max-md:hidden"> selected</span></span>
          {BULK.map((b, i) => (
            <Button key={b.key} type="button" size="sm" variant={i === 0 ? "default" : "outline"} className={cn(i === 0 ? selectionBarAction : selectionBarButton, "max-md:px-2")}
              onClick={() => setBulk(b.key)}>
              {b.label}<ChevronDown className="h-3 w-3 max-md:hidden" />
            </Button>
          ))}
          <Button type="button" variant="ghost" size="icon-sm" className={cn(selectionBarButton, "w-8 px-0")} aria-label="Clear selection" onClick={() => onPick(pickedRows.map((r) => r.workspace_id), false)}>
            <X className="h-3.5 w-3.5" />
          </Button>
        </div>
      )}

      <CreateSurface open={bulk !== null} onOpenChange={(o) => { if (!o) setBulk(null) }} size="md">
        <CreateSurfaceHeader
          title={<>{bulk ? SECTION_TITLE[bulk] : ""} for {pickedRows.length} {pickedRows.length === 1 ? "workspace" : "workspaces"}</>}
          description={pickedRows.length > 5 ? `${pickedRows.slice(0, 5).map((r) => r.workspace_name).join(", ")} and ${pickedRows.length - 5} more` : pickedRows.map((r) => r.workspace_name).join(", ")}
          onClose={() => setBulk(null)}
        />
        {/* The form brings its own Review/Cancel row (`inDialog`): saving goes
            through its change-review confirmation, not a one-click primary, so
            the surface adds no second footer. */}
        <CreateSurfaceBody>
          {bulk && (
            <BulkGovernanceForm section={bulk} rows={pickedRows} all={false} inDialog={{ onCancel: () => setBulk(null) }}
              onSaved={() => { setBulk(null); onPick(pickedRows.map((r) => r.workspace_id), false); onSaved() }} />
          )}
        </CreateSurfaceBody>
      </CreateSurface>
    </>
  )
}
