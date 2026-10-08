"use client"

import * as React from "react"
import Link from "next/link"
import { AlertTriangle, ChevronRight, Server, ShieldAlert, ShieldCheck, Activity as ActivityIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { SettingsCard, SettingsSummary, SummaryItem, settingsTable, settingsTh, settingsTd } from "@/components/features/settings/shared"
import { KeeperHealthCard } from "@/components/features/admin/keeper-health-card"
import { FINDING_ACTIONS } from "@/app/(dashboard)/admin/tabs/overview-tab"
import type { KeeperLogEntry, KeeperStatus } from "@/app/(dashboard)/admin/types"
import { DecisionChip } from "./security-activity"
import type { InstanceHealthRow } from "./use-instance-keeper"
import { STREAMS, findings, firstSentence, judgeState, setupRows, streamOf, type Posture, type Tone } from "./security-model"

const DOT: Record<Tone, string> = { ok: "bg-success", warn: "bg-warn", bad: "bg-destructive", muted: "bg-muted-foreground/50" }
const SUMMARY_TONE: Record<Tone, "success" | "warn" | "danger" | undefined> = { ok: "success", warn: "warn", bad: "danger", muted: undefined }

/**
 * Admin › Security › Overview: one line of state, what needs a person (the
 * Keeper's own state first, then the server's posture), the server's
 * deploy-time setup read-only, the judge's recent record, and the last few
 * events. Everything else is one click away in the side panel.
 */
export function SecurityOverview({ status, posture, postureError, entries, workspaceId, onOpenActivity, counts, health, healthError, activityError, selectedCount }: {
  workspaceId: string
  status: KeeperStatus | null
  posture: Posture | null
  postureError: string | null
  entries: KeeperLogEntry[]
  onOpenActivity: () => void
  /** Totals over the ticked workspaces (the instance log). Without them the
   *  line falls back to the Keeper status counts. */
  counts?: { total: number; allow: number; deny: number; escalate: number; pending: number }
  /** Decision windows of the ticked workspaces. Without them, the current
   *  workspace's health card. */
  health?: InstanceHealthRow[]
  healthError?: string | null
  activityError?: string | null
  /** How many workspaces are ticked, so an empty health table can say why. */
  selectedCount?: number
}) {
  const judge = judgeState(status)
  const list = findings(posture, status, FINDING_ACTIONS)
  // Green is earned by a check that ran (review R5): the posture has to have
  // been read. A failed or missing read says what could not be checked.
  const unchecked = !posture ? (postureError ?? "The server's setup has not been read yet.") : null
  const waiting = counts ? counts.escalate + counts.pending : entries.filter((e) => e.decision === "ESCALATE" || e.decision === "PENDING").length
  const recent = entries.slice(0, 5)

  return (
    <>
      <SettingsSummary slot="security-summary">
        <SummaryItem tone={status ? (status.enabled ? "success" : "danger") : undefined}>{status ? (status.enabled ? "Keeper on" : "Keeper off") : "Keeper status unknown"}</SummaryItem>
        {status?.enabled && <SummaryItem tone={SUMMARY_TONE[judge.tone]}>{judge.text}</SummaryItem>}
        {counts
          ? <SummaryItem n={counts.total}>{`requests · ${counts.allow} allowed · ${counts.deny} denied`}</SummaryItem>
          : status && <SummaryItem n={status.total_requests}>{`requests · ${status.allow_count} allowed · ${status.deny_count} denied`}</SummaryItem>}
        <SummaryItem n={waiting} tone={waiting ? "warn" : undefined}>waiting for a person</SummaryItem>
      </SettingsSummary>

      <SettingsCard icon={list.length || unchecked ? AlertTriangle : ShieldCheck} tint={unchecked ? "var(--destructive)" : list.length ? "var(--warn)" : "var(--success)"}
        title="Needs attention" description={unchecked ? "The server's setup could not be checked" : list.length ? "What to fix first, most pressing on top" : "Nothing in this server's setup stands out"}
        actions={<span className="font-mono text-micro text-muted-foreground">{list.length}</span>}>
        {unchecked && (
          <div data-finding="unchecked" className="flex items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0">
            <ShieldAlert className="h-4 w-4 shrink-0 text-destructive" />
            <div className="min-w-0 flex-1">
              <div className="text-control">The server&apos;s setup could not be checked</div>
              <div className="truncate text-label text-muted-foreground-soft">{unchecked} Until it is read, nothing here says the setup is safe.</div>
            </div>
          </div>
        )}
        {list.length === 0 && !unchecked ? (
          <p className="px-4 py-3 text-label text-success">Nothing in this instance&apos;s posture stands out.</p>
        ) : list.map((f) => (
          <div key={f.key} data-finding={f.key} className="flex items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0">
            {f.severity === "high"
              ? <ShieldAlert className="h-4 w-4 shrink-0 text-destructive" />
              : <AlertTriangle className={cn("h-4 w-4 shrink-0", f.severity === "medium" ? "text-warn" : "text-muted-foreground")} />}
            <div className="min-w-0 flex-1">
              <div className="text-control">{f.title}</div>
              <div className="truncate text-label text-muted-foreground-soft" title={f.detail}>{firstSentence(f.detail)}</div>
            </div>
            {f.action && (
              <Link href={f.action.href} className="inline-flex h-7 shrink-0 items-center gap-1 rounded-md border border-control-border px-2.5 text-xs hover:bg-accent">
                {f.action.label}<ChevronRight className="h-3 w-3" />
              </Link>
            )}
          </div>
        ))}
      </SettingsCard>

      <SettingsCard icon={Server} tint="var(--purple)" title="Server setup" description="Read from the environment at deploy; change it there, not here"
        actions={<span className="rounded-full bg-primary/10 px-2 font-mono text-micro text-primary-hover" title="Applies to every workspace on this server">Instance</span>}>
        {postureError ? (
          <p className="px-4 py-3 text-label text-muted-foreground">{postureError}</p>
        ) : !posture ? (
          <p className="px-4 py-3 text-label text-muted-foreground">Loading…</p>
        ) : (
          <dl className="grid sm:grid-cols-2" data-slot="server-setup">
            {setupRows(posture).map((r) => (
              <div key={r.label} className="flex min-w-0 items-center gap-2 border-b border-border px-4 py-2 text-label sm:odd:border-r">
                <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", DOT[r.tone])} aria-hidden />
                <dt className="min-w-0 flex-1 truncate">{r.label}</dt>
                <dd className={cn("font-mono text-micro", r.tone === "bad" ? "font-medium text-destructive" : "text-muted-foreground")}>{r.value}</dd>
              </div>
            ))}
          </dl>
        )}
      </SettingsCard>

      {health ? <InstanceHealthCard rows={health} error={healthError ?? null} selectedCount={selectedCount} /> : <KeeperHealthCard workspaceId={workspaceId} />}

      <SettingsCard icon={ActivityIcon} tint="var(--primary)" title="Recent activity" description="Credential decisions and background reviews"
        actions={<button type="button" onClick={onOpenActivity} className="inline-flex items-center gap-1 text-label text-primary-hover hover:underline">All activity<ChevronRight className="h-3 w-3" /></button>}>
        {activityError ? (
          <p className="px-4 py-3 text-label text-destructive">Activity could not be read: {activityError}</p>
        ) : recent.length === 0 ? (
          <p className="px-4 py-3 text-label text-muted-foreground">Nothing yet. Decisions appear here as agents ask for secrets.</p>
        ) : recent.map((e) => (
          <div key={e.id} className="flex items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0">
            <div className="min-w-0 flex-1">
              <div className="truncate text-control">
                {e.workspace_name && <span className="mr-1.5 text-muted-foreground">{e.workspace_name} ·</span>}
                {e.agent_name} <span className="text-muted-foreground">→</span>{" "}
                <span className={cn(streamOf(e) === "requests" && "font-mono text-label")}>
                  {streamOf(e) === "requests" ? e.credential_name : STREAMS.find((s) => s.key === streamOf(e))?.label}
                </span>
              </div>
              <div className="truncate text-label text-muted-foreground-soft">{firstSentence(e.reason || e.intent || "")}</div>
            </div>
            <DecisionChip decision={e.decision} />
          </div>
        ))}
      </SettingsCard>
    </>
  )
}

/**
 * How the judge has been deciding, per ticked workspace: its recent window,
 * how much of it let work go on, how often the judge failed, and any standing
 * alarm. Below the sample minimum there is too little to judge, which is not
 * the same as healthy, and the row says so.
 */
function InstanceHealthCard({ rows, error, selectedCount }: { rows: InstanceHealthRow[]; error: string | null; selectedCount?: number }) {
  const alarms = rows.filter((r) => r.alarm).length
  return (
    <SettingsCard icon={ActivityIcon} tint={error || alarms ? "var(--destructive)" : "var(--success)"} title="Judge health"
      description={error ? "Could not be read" : alarms ? `${alarms} workspace${alarms === 1 ? "" : "s"} with a standing alarm` : "Recent decision window per workspace"}>
      {error ? (
        <p className="px-4 py-3 text-label text-destructive">The judge&apos;s health could not be read: {error}</p>
      ) : rows.length === 0 ? (
        <p className="px-4 py-3 text-label text-muted-foreground">{selectedCount === 0 ? "No workspace ticked." : "No decision window yet."}</p>
      ) : (
        <div className="overflow-x-auto">
          <table className={settingsTable}>
            <thead>
              <tr>
                <th className={settingsTh}>Workspace</th>
                <th className={cn(settingsTh, "text-right")}>Decisions</th>
                <th className={cn(settingsTh, "text-right")}>Work went on</th>
                <th className={cn(settingsTh, "text-right")}>Judge failed</th>
                <th className={cn(settingsTh, "text-right")}>p95</th>
                <th className={settingsTh}>State</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const thin = r.samples < r.min_samples
                return (
                  <tr key={r.workspace_id}>
                    <td className={settingsTd}>{r.workspace_name}</td>
                    <td className={cn(settingsTd, "text-right font-mono text-micro")}>{r.samples}</td>
                    <td className={cn(settingsTd, "text-right font-mono text-micro")}>{thin ? "—" : `${Math.round(r.progressed_rate * 100)} %`}</td>
                    <td className={cn(settingsTd, "text-right font-mono text-micro")}>{thin ? "—" : `${Math.round(r.judge_failure_rate * 100)} %`}</td>
                    <td className={cn(settingsTd, "text-right font-mono text-micro")}>{r.samples ? `${r.p95_latency_ms} ms` : "—"}</td>
                    <td className={settingsTd}>
                      {r.alarm
                        ? <span className="text-destructive">{r.alarm.summary}</span>
                        : <span className="text-muted-foreground">{thin ? `too few decisions (${r.min_samples} needed)` : "no alarm"}</span>}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </SettingsCard>
  )
}
