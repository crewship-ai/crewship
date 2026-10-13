"use client"

import type * as React from "react"
import { ChevronRight, Sparkles } from "lucide-react"

import { cn } from "@/lib/utils"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import type { InstanceGovRow } from "./use-instance-keeper"
import { SAMPLE_DEFAULT } from "./bulk-governance"
import { settingsTable, settingsTh, settingsTd, settingsTableRowLink } from "@/components/features/settings/shared"

/**
 * How each workspace is watched, one row per workspace: the answer to "is the
 * watchdog on everywhere?" without opening every workspace. A row opens that
 * workspace's security profile; with `selected` the rows can be ticked for a
 * change to several at once. "New workspaces" — what a workspace starts with —
 * closes the list and opens its own editor.
 */

export function fourEyes(r: Pick<InstanceGovRow, "require_second_approver" | "effective_second_approver">): { text: string; off?: boolean } {
  const e = r.effective_second_approver
  if (r.require_second_approver || e?.source === "workspace") return { text: "All levels" }
  // Four-eyes as ENFORCED, not the toggle: the tier table requires a second
  // approver for the top level whatever the toggle says (review R8).
  if (e?.source === "tier") return { text: `${(e.min_security_level_label ?? `L${e.min_security_level}`).split(" ")[0]} required` }
  return { text: "Off", off: true }
}

export const leaseLabel = (s: number) => (!s ? "Standing" : s % 3600 === 0 ? `${s / 3600} h` : `${Math.round(s / 60)} min`)
/** The judge a workspace actually uses, and where that comes from (review R8). */
export const judgeLabel = (r: Pick<InstanceGovRow, "gov_model_provider" | "gov_model_id">) =>
  r.gov_model_provider ? `${r.gov_model_provider} · ${r.gov_model_id} (own)` : "Instance judge (inherited)"
export const reviewLabel = (r: Pick<InstanceGovRow, "behavior_sample_every">) => `1 in ${r.behavior_sample_every || SAMPLE_DEFAULT}`

/** A workspace's watch in a few words, for the panel. */
export function watchSummary(r: InstanceGovRow): string {
  return r.enabled ? `Watchdog on · ${reviewLabel(r)}` : "Watchdog off"
}

function Cells({ r }: { r: InstanceGovRow }) {
  const eyes = fourEyes(r)
  return (
    <>
      <td className={settingsTd} data-col="watchdog">
        <span className={cn("rounded-full px-2 py-0.5 font-mono text-micro",
          r.enabled ? "bg-success/10 text-success" : "bg-muted text-muted-foreground")}>{r.enabled ? "On" : "Off"}</span>
      </td>
      <td className={cn(settingsTd, "whitespace-nowrap font-mono text-label text-muted-foreground")} data-col="review">{reviewLabel(r)}</td>
      <td className={cn(settingsTd, "whitespace-nowrap font-mono text-label text-muted-foreground")} data-col="deny">≥ {r.deny_notify_min_risk}</td>
      <td className={cn(settingsTd, "whitespace-nowrap text-label", eyes.off ? "text-muted-foreground" : "text-foreground")} data-col="four-eyes">{eyes.text}</td>
      <td className={cn(settingsTd, "whitespace-nowrap text-label text-muted-foreground")} data-col="lease">{leaseLabel(r.auto_lease_seconds)}</td>
      <td className={cn(settingsTd, "max-w-[14rem] truncate text-label text-muted-foreground")} data-col="judge" title={judgeLabel(r)}>{judgeLabel(r)}</td>
    </>
  )
}

export function WhatsOnWhere({ rows, onOpen, defaults, onOpenDefaults, selected, onSelect, label = "Workspaces" }: {
  rows: InstanceGovRow[]
  onOpen: (workspaceId: string) => void
  /** What a new workspace starts with, shown as the last row. */
  defaults?: InstanceGovRow
  onOpenDefaults?: () => void
  /** With these the rows can be ticked. */
  selected?: Set<string>
  onSelect?: (ids: string[], on: boolean) => void
  label?: string
}) {
  const selectable = !!selected && !!onSelect
  const allOn = selectable && rows.length > 0 && rows.every((r) => selected!.has(r.workspace_id))
  const open = (fn: () => void) => ({
    tabIndex: 0, onClick: fn,
    onKeyDown: (e: React.KeyboardEvent) => { if (e.key === "Enter") fn() },
  })
  // A phone gets one line per workspace instead of a table it would scroll.
  const phoneRow = (r: InstanceGovRow, isDefaults = false) => (
    <li key={r.workspace_id} className="flex items-center gap-3 border-b border-border px-4 py-3 last:border-b-0">
      {selectable && !isDefaults && (
        <input type="checkbox" aria-label={`Select ${r.workspace_name}`} checked={selected!.has(r.workspace_id)}
          onChange={(e) => onSelect!([r.workspace_id], e.target.checked)} className="size-4 accent-[var(--primary)]" />
      )}
      <button type="button" onClick={() => (isDefaults ? onOpenDefaults?.() : onOpen(r.workspace_id))}
        className="flex min-w-0 flex-1 items-center gap-2.5 text-left">
        {isDefaults
          ? <span className="grid h-7 w-7 shrink-0 place-items-center rounded-lg border border-border text-muted-foreground"><Sparkles className="h-3.5 w-3.5" /></span>
          : <WorkspaceTile id={r.workspace_id} name={r.workspace_name} />}
        <span className="min-w-0 flex-1">
          <span className={cn("flex items-center gap-1.5 text-control", isDefaults ? "text-muted-foreground" : "font-medium")}>
            <span className="truncate">{r.workspace_name}</span>
            {!isDefaults && <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", r.enabled ? "bg-success" : "bg-warn")} aria-hidden />}
          </span>
          <span className="block truncate text-label text-muted-foreground">
            {watchSummary(r)} · {leaseLabel(r.auto_lease_seconds).toLowerCase()} · {r.gov_model_provider ? "own judge" : "instance judge"}
          </span>
        </span>
        <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
      </button>
    </li>
  )
  return (
    <>
    <ul className="md:hidden" aria-label={label} data-slot="workspaces-list">
      {rows.map((r) => phoneRow(r))}
      {defaults && phoneRow(defaults, true)}
    </ul>
    <div className="overflow-x-auto max-md:hidden">
      <table className={settingsTable} aria-label={label}>
        <thead>
          <tr>
            {selectable && (
              <th className={cn(settingsTh, "w-9")}>
                <input type="checkbox" aria-label="Select every workspace" checked={allOn} disabled={rows.length === 0}
                  onChange={(e) => onSelect!(rows.map((r) => r.workspace_id), e.target.checked)} className="size-3.5 accent-[var(--primary)]" />
              </th>
            )}
            <th className={settingsTh}>Workspace</th>
            <th className={settingsTh}>Watchdog</th>
            <th className={settingsTh}>Review</th>
            <th className={settingsTh}>DENY alert</th>
            <th className={settingsTh}>Four-eyes</th>
            <th className={settingsTh}>Lease</th>
            <th className={settingsTh}>Judge</th>
            <th className={cn(settingsTh, "w-6")} aria-hidden />
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.workspace_id} data-workspace={r.workspace_slug} aria-label={r.workspace_name}
              className={cn(settingsTableRowLink, selected?.has(r.workspace_id) && "bg-accent/40")} {...open(() => onOpen(r.workspace_id))}>
              {selectable && (
                <td className={settingsTd} onClick={(e) => e.stopPropagation()}>
                  <input type="checkbox" aria-label={`Select ${r.workspace_name}`} checked={selected!.has(r.workspace_id)}
                    onChange={(e) => onSelect!([r.workspace_id], e.target.checked)} className="size-3.5 accent-[var(--primary)]" />
                </td>
              )}
              <td className={settingsTd}>
                <span className="flex min-w-0 items-center gap-2.5">
                  <WorkspaceTile id={r.workspace_id} name={r.workspace_name} />
                  <span className="min-w-0">
                    <span className="flex items-center gap-1.5 font-medium">
                      <span className="truncate">{r.workspace_name}</span>
                      <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", r.enabled ? "bg-success" : "bg-warn")} aria-hidden />
                    </span>
                    {!r.configured && <span className="block text-micro text-muted-foreground" title="No settings of its own; runs on the built-in opt-out">built-in</span>}
                  </span>
                </span>
              </td>
              <Cells r={r} />
              <td className={settingsTd}><ChevronRight className="h-3.5 w-3.5 text-muted-foreground-soft" aria-hidden /></td>
            </tr>
          ))}
          {defaults && (
            <tr data-workspace="defaults" aria-label="New workspaces" className={cn(settingsTableRowLink, "bg-muted/30")} {...open(() => onOpenDefaults?.())}>
              {selectable && <td className={settingsTd} />}
              <td className={settingsTd}>
                <span className="flex items-center gap-2.5">
                  <span className="grid h-7 w-7 shrink-0 place-items-center rounded-lg border border-border text-muted-foreground"><Sparkles className="h-3.5 w-3.5" /></span>
                  <span>
                    <span className="block text-muted-foreground">New workspaces</span>
                    <span className="block text-micro text-muted-foreground-soft">what a workspace starts with</span>
                  </span>
                </span>
              </td>
              <Cells r={defaults} />
              <td className={settingsTd}><ChevronRight className="h-3.5 w-3.5 text-muted-foreground-soft" aria-hidden /></td>
            </tr>
          )}
          {rows.length === 0 && !defaults && (
            <tr><td colSpan={9} className="px-3 py-6 text-center text-muted-foreground">No workspace matches.</td></tr>
          )}
        </tbody>
      </table>
    </div>
    </>
  )
}
