"use client"

import * as React from "react"
import Link from "next/link"
import { Activity as ActivityIcon, AlertTriangle, Building2, ChevronRight, Gauge, Server, ShieldAlert, ShieldCheck } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { SettingsCard, SettingsRow, SettingsSummary, SummaryItem } from "@/components/features/settings/shared"
import { KeeperHealthCard } from "@/components/features/admin/keeper-health-card"
import { FINDING_ACTIONS } from "@/app/(dashboard)/admin/tabs/overview-tab"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import type { KeeperLogEntry, KeeperStatus } from "@/app/(dashboard)/admin/types"
import { DecisionChip } from "./decision-chip"
import { RequestSheet, subject, when } from "./request-sheet"
import { WholeInstanceMark } from "./security-kit"
import type { InstanceGovRow, InstanceHealthRow } from "./use-instance-keeper"
import { findings, firstSentence, healthToReport, judgeState, secondApprover, setupRows, watchdogFinding, type Posture, type Tone } from "./security-model"

const DOT: Record<Tone, string> = { ok: "bg-success", warn: "bg-warn", bad: "bg-destructive", muted: "bg-muted-foreground/50" }
const SUMMARY_TONE: Record<Tone, "success" | "warn" | "danger" | undefined> = { ok: "success", warn: "warn", bad: "danger", muted: undefined }

/** What the overview can open: a place in this page, found from a finding's link or a row. */
export interface OverviewGo {
  /** ?view= ?section= ?watch= ?judge= ?second= of a link that stays on this page. */
  params: (fn: (q: URLSearchParams) => void) => void
  workspace: (id: string) => void
  activity: () => void
}

/**
 * Admin › Security › Overview: one line of state, what needs a person (with a
 * fix for each), then four cards in two columns: how the workspaces are
 * watched, the server's read-only setup, the judge's health where it has
 * something to report, and the latest decisions. Each fact appears once.
 */
export function SecurityOverview({ status, posture, postureError, rows, entries, workspaceId, counts, health, healthError, activityError, go, onChanged }: {
  workspaceId: string
  status: KeeperStatus | null
  posture: Posture | null
  postureError: string | null
  /** Every workspace's governance row. */
  rows: InstanceGovRow[]
  entries: KeeperLogEntry[]
  /** Totals over every workspace (the instance log). Without them the line falls back to the Keeper status counts. */
  counts?: { total: number; allow: number; deny: number; escalate: number; pending: number }
  health?: InstanceHealthRow[]
  healthError?: string | null
  activityError?: string | null
  go: OverviewGo
  onChanged: () => void
}) {
  const [open, setOpen] = React.useState<KeeperLogEntry | null>(null)
  const judge = judgeState(status)
  const list = [...findings(posture, status, FINDING_ACTIONS), ...(watchdogFinding(rows) ? [watchdogFinding(rows)!] : [])]
  // Green is earned by a check that ran (review R5): the posture has to have
  // been read. A failed or missing read says what could not be checked.
  const unchecked = !posture ? (postureError ?? "The server's setup has not been read yet.") : null
  const waiting = counts ? counts.escalate + counts.pending : entries.filter((e) => e.decision === "ESCALATE" || e.decision == null || e.decision === "PENDING").length
  const recent = entries.slice(0, 5)

  const watched = rows.filter((r) => r.enabled).length
  const own = rows.filter((r) => r.gov_model_provider).length
  const needSecond = rows.filter((r) => secondApprover(r).required).length
  const standing = rows.filter((r) => !r.auto_lease_seconds).length
  const toWorkspaces = (extra: Record<string, string> = {}) => go.params((q) => { q.set("view", "workspaces"); for (const [k, v] of Object.entries(extra)) q.set(k, v) })
  const summaries: { key: string; text: string; hint: string; extra?: Record<string, string> }[] = [
    { key: "watched", text: `${watched} watched`, hint: `${rows.length - watched} off`, extra: { watch: "on" } },
    { key: "own", text: `${own} with their own judge`, hint: `${rows.length - own} use the instance judge`, extra: { judge: "own" } },
    { key: "second", text: `${needSecond} need a second approver`, hint: `${rows.length - needSecond} only from the level that requires one`, extra: { second: "required" } },
    { key: "standing", text: `${standing} keep access until revoked`, hint: `${rows.length - standing} time-boxed` },
  ]

  const report = health ? healthToReport(health, rows) : []

  return (
    <>
      <SettingsSummary slot="security-summary">
        <SummaryItem tone={status ? (status.enabled ? "success" : "danger") : undefined}>{status ? (status.enabled ? "Keeper on" : "Keeper off") : "Keeper status unknown"}</SummaryItem>
        {status?.enabled && <SummaryItem tone={SUMMARY_TONE[judge.tone]}>{judge.text}</SummaryItem>}
        {counts
          ? <SummaryItem n={counts.total}>requests</SummaryItem>
          : status && <SummaryItem n={status.total_requests}>requests</SummaryItem>}
        <SummaryItem n={waiting} tone={waiting ? "warn" : undefined}>waiting for a person</SummaryItem>
      </SettingsSummary>

      <SettingsCard icon={list.length || unchecked ? AlertTriangle : ShieldCheck} tint={unchecked ? "var(--destructive)" : list.length ? "var(--warn)" : "var(--success)"}
        title="Needs attention" description={unchecked ? "The server's setup could not be checked" : list.length ? "What to fix first, most pressing on top" : "Nothing in this server's setup stands out"}
        actions={<span className="text-micro tabular-nums text-muted-foreground">{list.length}</span>}>
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
          <div key={f.key} data-finding={f.key} className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0">
            {f.severity === "high"
              ? <ShieldAlert className="h-4 w-4 shrink-0 text-destructive" />
              : <AlertTriangle className={cn("h-4 w-4 shrink-0", f.severity === "medium" ? "text-warn" : "text-muted-foreground")} />}
            <div className="min-w-0 flex-[1_1_14rem]">
              <div className="text-control">{f.title}</div>
              <div className="truncate text-label text-muted-foreground-soft" title={f.detail}>{firstSentence(f.detail)}</div>
            </div>
            {f.action && <FindingFix action={f.action} go={go} />}
          </div>
        ))}
      </SettingsCard>

      <div className="grid gap-4 lg:grid-cols-2">
        <SettingsCard icon={Building2} title="How workspaces are watched" description={`${rows.length} ${rows.length === 1 ? "workspace" : "workspaces"}`}
          actions={<Button type="button" size="sm" variant="outline" className="h-7 px-2.5 text-xs" onClick={() => toWorkspaces()}>Change several<ChevronRight /></Button>}>
          <div data-slot="watch-summary">
            {summaries.map((s, i) => (
              <SettingsRow key={s.key} label={s.text} description={s.hint} border={i < summaries.length - 1}>
                <Button type="button" variant="ghost" size="sm" aria-label={`Show: ${s.text}`} onClick={() => toWorkspaces(s.extra)}>Show</Button>
              </SettingsRow>
            ))}
          </div>
        </SettingsCard>

        <SettingsCard icon={Server} tint="var(--purple)" title="Server setup" description="Read at start-up; change it in the server environment" actions={<WholeInstanceMark />}>
          {postureError ? (
            <p className="px-4 py-3 text-label text-muted-foreground">{postureError}</p>
          ) : !posture ? (
            <p className="px-4 py-3 text-label text-muted-foreground">Loading…</p>
          ) : (
            <dl data-slot="server-setup">
              {setupRows(posture).map((r) => (
                <div key={r.label} className="flex min-w-0 items-center gap-2 border-b border-border px-4 py-2 text-label last:border-b-0">
                  <dt className="min-w-0 flex-1 truncate text-control">{r.label}</dt>
                  <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", DOT[r.tone])} aria-hidden />
                  <dd className={cn("font-mono text-micro", r.tone === "bad" ? "font-medium text-destructive" : "text-muted-foreground")}>{r.value}</dd>
                </div>
              ))}
            </dl>
          )}
        </SettingsCard>

        {health ? (
          <SettingsCard icon={Gauge} tint={healthError || report.length ? "var(--destructive)" : "var(--success)"} title="Judge health"
            description={healthError ? "Could not be read" : "Only workspaces with something to report"}>
            {healthError ? (
              <p className="px-4 py-3 text-label text-destructive">The judge&apos;s health could not be read: {healthError}</p>
            ) : (
              <>
                {report.map((r) => (
                  <div key={r.workspace_id} data-health={r.workspace_slug} className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-2.5">
                    <WorkspaceTile id={r.workspace_id} name={r.workspace_name} logoUrl={rows.find((w) => w.workspace_id === r.workspace_id)?.workspace_logo_url} />
                    <div className="min-w-0 flex-[1_1_12rem]">
                      <div className="text-control font-medium">{r.workspace_name}</div>
                      <div className={cn("text-label", r.alarm ? "text-destructive" : "text-muted-foreground")}>
                        {r.alarm ? r.alarm.summary : "Its own judge fails more often than it should."}
                      </div>
                      <div className="mt-0.5 font-mono text-micro text-muted-foreground">
                        {r.samples} decisions · allow {r.allow ?? 0} · deny {r.deny ?? 0} · escalate {r.escalate ?? 0} · judge failed {r.judge_failures ?? 0} ({Math.round(r.judge_failure_rate * 100)} %) · p95 {r.p95_latency_ms} ms
                      </div>
                    </div>
                    <Button type="button" variant="ghost" size="sm" aria-label={`Open ${r.workspace_name}`} onClick={() => go.workspace(r.workspace_id)}>Open</Button>
                  </div>
                ))}
                <p className="px-4 py-2.5 text-label text-muted-foreground" data-slot="no-alarm">
                  {rows.length === 0 ? "No workspace yet."
                    : `${rows.length - report.length} ${rows.length - report.length === 1 ? "workspace has" : "workspaces have"} no alarm.`}
                  {health[0] && ` An alarm needs ${health[0].min_samples} decisions first.`}
                </p>
              </>
            )}
          </SettingsCard>
        ) : <KeeperHealthCard workspaceId={workspaceId} />}

        <SettingsCard icon={ActivityIcon} tint="var(--primary)" title="Latest decisions" description="Credential requests and background reviews"
          actions={<Button type="button" variant="ghost" size="sm" onClick={go.activity}>All activity<ChevronRight /></Button>}>
          {activityError ? (
            <p className="px-4 py-3 text-label text-destructive">Activity could not be read: {activityError}</p>
          ) : recent.length === 0 ? (
            <p className="px-4 py-3 text-label text-muted-foreground">Nothing here yet. Decisions appear as agents ask.</p>
          ) : recent.map((e) => (
            <button key={e.id} type="button" onClick={() => setOpen(e)} className="flex min-h-[44px] w-full items-center gap-3 border-b border-border px-4 py-2.5 text-left last:border-b-0 hover:bg-[var(--row-hover-bg)]">
              <span className="min-w-0 flex-1">
                <span className={cn("block truncate text-control", e.credential_name && "font-mono")}>{subject(e)}</span>
                <span className="block truncate text-label text-muted-foreground-soft">{e.agent_name}{e.workspace_name ? ` · ${e.workspace_name}` : ""} · {when(e.created_at)}</span>
              </span>
              <DecisionChip decision={e.decision} />
            </button>
          ))}
        </SettingsCard>
      </div>

      <RequestSheet entry={open} onClose={() => setOpen(null)} onResolved={onChanged} />
    </>
  )
}

/** A finding's fix: a place on this page when its link points here, a link otherwise. */
function FindingFix({ action, go }: { action: { label: string; href: string }; go: OverviewGo }) {
  const cls = "inline-flex h-7 shrink-0 items-center gap-1 rounded-md border border-control-border px-2.5 text-xs hover:bg-accent coarse:min-h-[44px]"
  if (action.href.startsWith("/admin/security")) {
    const target = new URL(action.href, "http://x")
    return (
      <button type="button" className={cls} onClick={() => go.params((q) => { for (const [k, v] of target.searchParams) q.set(k, v) })}>
        {action.label}<ChevronRight className="h-3 w-3" />
      </button>
    )
  }
  return <Link href={action.href} className={cls}>{action.label}<ChevronRight className="h-3 w-3" /></Link>
}
