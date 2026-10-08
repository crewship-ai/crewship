"use client"

import { cn } from "@/lib/utils"
import type { InstanceGovRow } from "./use-instance-keeper"
import type { BulkSection } from "./bulk-governance"
import { SAMPLE_DEFAULT } from "./bulk-governance"
import { settingsTable, settingsTh, settingsTd } from "@/components/features/settings/shared"

/**
 * What each workspace has switched on, one row per workspace: the answer to
 * "is the watchdog on everywhere?" without opening every workspace. A cell
 * opens that setting for that workspace. Off is red; a workspace never set
 * says it runs on the defaults.
 */

function fourEyes(r: InstanceGovRow): { text: string; off?: boolean } {
  const e = r.effective_second_approver
  if (r.require_second_approver || e?.source === "workspace") return { text: "All levels" }
  if (e?.source === "tier") return { text: `${(e.min_security_level_label ?? `L${e.min_security_level}`).split(" ")[0]} required` }
  return { text: "Off", off: true }
}

const lease = (s: number) => (!s ? "Standing" : s % 3600 === 0 ? `${s / 3600} h` : `${Math.round(s / 60)} min`)

const COLS: { section: BulkSection; label: string; show: (r: InstanceGovRow) => { text: string; off?: boolean } }[] = [
  // The judge a workspace actually uses, and where that comes from: its own
  // setting, or the instance judge it inherits (review R8).
  { section: "workspace-judge", label: "Judge", show: (r) => ({ text: r.gov_model_provider ? `${r.gov_model_provider} · ${r.gov_model_id} (own)` : "Instance judge (inherited)" }) },
  { section: "watchdog", label: "Watchdog", show: (r) => ({ text: r.enabled ? "On" : "Off", off: !r.enabled }) },
  { section: "watchdog", label: "Review", show: (r) => ({ text: `1 in ${r.behavior_sample_every || SAMPLE_DEFAULT}` }) },
  { section: "alerts", label: "DENY alert ≥", show: (r) => ({ text: String(r.deny_notify_min_risk) }) },
  // Four-eyes as ENFORCED, not the toggle: the tier table requires a second
  // approver for the top level whatever the toggle says (review R8).
  { section: "alerts", label: "Four-eyes", show: (r) => fourEyes(r) },
  { section: "leases", label: "Lease", show: (r) => ({ text: lease(r.auto_lease_seconds) }) },
]

export function WhatsOnWhere({ rows, selected, onOpen, defaults, onOpenDefaults }: {
  rows: InstanceGovRow[]
  selected: Set<string>
  onOpen: (section: BulkSection, workspaceId: string) => void
  /** What a new workspace starts with, shown as the first row. */
  defaults?: InstanceGovRow
  onOpenDefaults?: () => void
}) {
  return (
    <div className="overflow-x-auto rounded-card border border-border bg-card" role="region" aria-label="What's on where">
      <table className={settingsTable}>
        <thead>
          <tr>
            <th className={settingsTh}>Workspace</th>
            {COLS.map((c) => <th key={c.label} className={settingsTh}>{c.label}</th>)}
          </tr>
        </thead>
        <tbody>
          {defaults && (
            <tr data-workspace="defaults" className="bg-muted/40">
              <td className={settingsTd}>
                <div className="font-medium">New workspaces</div>
                <div className="text-micro text-muted-foreground">copied when a workspace is created</div>
              </td>
              {COLS.map((c) => {
                const v = c.show(defaults)
                return (
                  <td key={c.label} className="border-b border-border p-0">
                    <button type="button" onClick={onOpenDefaults} aria-label={`${c.label} in New workspaces: ${v.text}`}
                      className="kit-tap w-full whitespace-nowrap px-3 py-2 text-left italic text-muted-foreground hover:bg-muted">
                      {v.text}
                    </button>
                  </td>
                )
              })}
            </tr>
          )}
          {rows.map((r) => (
            <tr key={r.workspace_id} data-workspace={r.workspace_slug} className={cn(selected.has(r.workspace_id) && "bg-accent/40")}>
              <td className={settingsTd}>
                <div className="font-medium">{r.workspace_name}</div>
                {!r.configured && <div className="text-micro text-muted-foreground" title="No settings of its own; runs on the built-in opt-out">built-in</div>}
              </td>
              {COLS.map((c) => {
                const v = c.show(r)
                return (
                  <td key={c.label} className="border-b border-border p-0">
                    <button type="button" onClick={() => onOpen(c.section, r.workspace_id)}
                      aria-label={`${c.label} in ${r.workspace_name}: ${v.text}`}
                      className="kit-tap w-full whitespace-nowrap px-3 py-2 text-left hover:bg-muted">
                      {v.off
                        ? <span className="rounded-full border border-destructive/40 px-1.5 font-mono text-micro text-destructive">{v.text}</span>
                        : v.text}
                    </button>
                  </td>
                )
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
