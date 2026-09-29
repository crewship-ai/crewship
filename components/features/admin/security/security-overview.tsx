"use client"

import * as React from "react"
import Link from "next/link"
import { AlertTriangle, ChevronRight, Server, ShieldAlert, ShieldCheck, Activity as ActivityIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { SettingsCard, SettingsSummary, SummaryItem } from "@/components/features/settings/shared"
import { KeeperHealthCard } from "@/components/features/admin/keeper-health-card"
import { FINDING_ACTIONS } from "@/app/(dashboard)/admin/tabs/overview-tab"
import type { KeeperLogEntry, KeeperStatus } from "@/app/(dashboard)/admin/types"
import { DecisionChip } from "./security-activity"
import { STREAMS, findings, firstSentence, judgeState, setupRows, streamOf, type Posture, type Tone } from "./security-model"

const DOT: Record<Tone, string> = { ok: "bg-success", warn: "bg-warn", bad: "bg-destructive", muted: "bg-muted-foreground/50" }
const SUMMARY_TONE: Record<Tone, "success" | "warn" | "danger" | undefined> = { ok: "success", warn: "warn", bad: "danger", muted: undefined }

/**
 * Admin › Security › Overview: one line of state, what needs a person (the
 * Keeper's own state first, then the server's posture), the server's
 * deploy-time setup read-only, the judge's recent record, and the last few
 * events. Everything else is one click away in the side panel.
 */
export function SecurityOverview({ status, posture, postureError, entries, workspaceId, onOpenActivity }: {
  workspaceId: string
  status: KeeperStatus | null
  posture: Posture | null
  postureError: string | null
  entries: KeeperLogEntry[]
  onOpenActivity: () => void
}) {
  const judge = judgeState(status)
  const list = findings(posture, status, FINDING_ACTIONS)
  const waiting = entries.filter((e) => e.decision === "ESCALATE" || e.decision === "PENDING").length
  const recent = entries.slice(0, 5)

  return (
    <>
      <SettingsSummary slot="security-summary">
        <SummaryItem tone={status ? (status.enabled ? "success" : "danger") : undefined}>{status ? (status.enabled ? "Keeper on" : "Keeper off") : "Keeper status unknown"}</SummaryItem>
        {status?.enabled && <SummaryItem tone={SUMMARY_TONE[judge.tone]}>{judge.text}</SummaryItem>}
        {status && <SummaryItem n={status.total_requests}>{`requests · ${status.allow_count} allowed · ${status.deny_count} denied`}</SummaryItem>}
        <SummaryItem n={waiting} tone={waiting ? "warn" : undefined}>waiting for a person</SummaryItem>
      </SettingsSummary>

      <SettingsCard icon={list.length ? AlertTriangle : ShieldCheck} tint={list.length ? "var(--warn)" : "var(--success)"}
        title="Needs attention" description={list.length ? "What to fix first, most pressing on top" : "Nothing in this server's setup stands out"}
        actions={<span className="font-mono text-[11px] text-muted-foreground">{list.length}</span>}>
        {list.length === 0 ? (
          <p className="px-4 py-3 text-[12px] text-success">Nothing in this instance&apos;s posture stands out.</p>
        ) : list.map((f) => (
          <div key={f.key} data-finding={f.key} className="flex items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0">
            {f.severity === "high"
              ? <ShieldAlert className="h-4 w-4 shrink-0 text-destructive" />
              : <AlertTriangle className={cn("h-4 w-4 shrink-0", f.severity === "medium" ? "text-warn" : "text-muted-foreground")} />}
            <div className="min-w-0 flex-1">
              <div className="text-[13px]">{f.title}</div>
              <div className="truncate text-[11px] text-muted-foreground-soft" title={f.detail}>{firstSentence(f.detail)}</div>
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
        actions={<span className="rounded-full bg-primary/10 px-2 font-mono text-[10.5px] text-primary-hover" title="Applies to every workspace on this server">Instance</span>}>
        {postureError ? (
          <p className="px-4 py-3 text-[12px] text-muted-foreground">{postureError}</p>
        ) : !posture ? (
          <p className="px-4 py-3 text-[12px] text-muted-foreground">Loading…</p>
        ) : (
          <dl className="grid sm:grid-cols-2" data-slot="server-setup">
            {setupRows(posture).map((r) => (
              <div key={r.label} className="flex min-w-0 items-center gap-2 border-b border-border px-4 py-2 text-[12.5px] sm:odd:border-r">
                <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", DOT[r.tone])} aria-hidden />
                <dt className="min-w-0 flex-1 truncate">{r.label}</dt>
                <dd className={cn("font-mono text-[11.5px]", r.tone === "bad" ? "font-medium text-destructive" : "text-muted-foreground")}>{r.value}</dd>
              </div>
            ))}
          </dl>
        )}
      </SettingsCard>

      <KeeperHealthCard workspaceId={workspaceId} />

      <SettingsCard icon={ActivityIcon} tint="var(--primary)" title="Recent activity" description="Credential decisions and background reviews"
        actions={<button type="button" onClick={onOpenActivity} className="inline-flex items-center gap-1 text-[12px] text-primary-hover hover:underline">All activity<ChevronRight className="h-3 w-3" /></button>}>
        {recent.length === 0 ? (
          <p className="px-4 py-3 text-[12px] text-muted-foreground">Nothing yet. Decisions appear here as agents ask for secrets.</p>
        ) : recent.map((e) => (
          <div key={e.id} className="flex items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0">
            <div className="min-w-0 flex-1">
              <div className="truncate text-[13px]">
                {e.agent_name} <span className="text-muted-foreground">→</span>{" "}
                <span className={cn(streamOf(e) === "requests" && "font-mono text-[12px]")}>
                  {streamOf(e) === "requests" ? e.credential_name : STREAMS.find((s) => s.key === streamOf(e))?.label}
                </span>
              </div>
              <div className="truncate text-[11px] text-muted-foreground-soft">{firstSentence(e.reason || e.intent || "")}</div>
            </div>
            <DecisionChip decision={e.decision} />
          </div>
        ))}
      </SettingsCard>
    </>
  )
}
