"use client"

import * as React from "react"
import { AlertTriangle, CalendarDays, HardDrive, LayoutGrid, ShieldCheck } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { SettingsCard, SettingsRow, SettingsSummary, SummaryItem } from "@/components/features/settings/shared"
import { Dot, Gate, TONE_PILL, TONE_TEXT } from "./backups-kit"
import {
  formatAgo, formatSize, proofLabel, stripCells, verdictHeadline,
  type AttentionItem, type Night, type OverviewResponse, type SpaceInfo, type StatusRow, type WorkspaceCoverage,
} from "./backups-model"
import { useBackupsOverview } from "./use-backups-overview"
import type { SectionCtx } from "./backups-console"

/**
 * Backups › Overview, in the Settings card grammar (like Security › Overview).
 *
 * One line of state on top — the restore verdict, how often, where, and how
 * many workspaces are covered — then what needs a person (including a
 * local-only copy, which is a problem like any other), the
 * five questions a client asks, coverage per workspace, the last fourteen
 * nights, and the room on this disk.
 */
export function BackupsOverview({ ctx }: { ctx: SectionCtx }) {
  const res = useBackupsOverview(ctx.scope, ctx.selected, ctx.workspaces)
  return (
    <Gate resource={res} what="The backup overview" skeleton="h-[320px]">
      {(data) => <OverviewBody data={data} ctx={ctx} />}
    </Gate>
  )
}


/**
 * The attention list, with the local-only copy the page used to draw as a
 * banner. A space refusal needs nothing added: the server lists it itself.
 */
export function attentionItems(data: OverviewResponse): AttentionItem[] {
  const extra: AttentionItem[] = []
  if (!data.status.offsite_verified) {
    extra.push({
      id: "local-only", severity: "warn", title: "Local copy only. Losing this server is not covered.",
      detail: "Every backup sits on the same disk as the data it protects.", action: { kind: "storage", label: "Storage" },
    })
  }
  return [...extra, ...data.needs_attention]
}

export function OverviewBody({ data, ctx, now = new Date() }: { data: OverviewResponse; ctx: SectionCtx; now?: Date }) {
  const s = data.status
  const head = verdictHeadline(s.verdict)
  const cov = data.workspaces
  const covered = cov?.filter((w) => w.status === "ok").length ?? 0
  return (
    <>
      <SettingsSummary slot="backups-summary">
        <span className="inline-flex items-center">
          <span className={cn("mr-1.5 h-1.5 w-1.5 rounded-full", head.tone === "ok" ? "bg-success" : head.tone === "warn" ? "bg-warn" : head.tone === "bad" ? "bg-destructive" : "bg-muted-foreground/50")} aria-hidden />
          <span data-slot="backup-verdict" data-tone={head.tone} className="text-foreground">{head.text}</span>
        </span>
        <SummaryItem>{s.how_often.value}</SummaryItem>
        <SummaryItem tone={s.offsite_verified ? undefined : "danger"}>{s.offsite_verified ? s.where.value : "This server only"}</SummaryItem>
        {cov && <SummaryItem n={covered} tone={covered < cov.length ? "warn" : "success"}>{`of ${cov.length} workspaces covered`}</SummaryItem>}
        {!!data.crewless_workspaces && <SummaryItem n={data.crewless_workspaces}>{data.crewless_workspaces === 1 ? "workspace without crews" : "workspaces without crews"}</SummaryItem>}
      </SettingsSummary>

      <NeedsAttention items={attentionItems(data)} ctx={ctx} />

      <SettingsCard icon={ShieldCheck} tint={`var(--${head.tone === "bad" ? "destructive" : head.tone === "warn" ? "warn" : "success"})`}
        title={s.label} description={s.summary ?? "What is protected, how often, where and for how long"}>
        <ProtectionRow label="Protects" row={s.protects} />
        <ProtectionRow label="How often" row={s.how_often} />
        <ProtectionRow label="Where" row={s.where} />
        <ProtectionRow label="How long" row={s.how_long} />
        <ProtectionRow label="Really restored?" row={s.really_restored} />
      </SettingsCard>

      {cov && <CoverageCard rows={cov} ctx={ctx} now={now} />}

      <SettingsCard icon={CalendarDays} tint="var(--success)" title="Last 14 nights" description="What each night’s run did; hover a night for the details">
        <NightStrip nights={data.nights} today={localDay(now)} />
      </SettingsCard>

      <SpaceCard space={data.space} />
    </>
  )
}

function ProtectionRow({ label, row }: { label: string; row: StatusRow }) {
  return (
    <SettingsRow label={label}>
      <div className="flex max-w-[28rem] flex-col items-end gap-1 text-right">
        <span className="text-[13px]">{row.value}</span>
        {row.detail && (row.detail_tone && row.detail_tone !== "muted"
          ? <StatusPill tone={TONE_PILL[row.detail_tone]} label={row.detail} />
          : <span className="text-xs text-muted-foreground">{row.detail}</span>)}
      </div>
    </SettingsRow>
  )
}

function localDay(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`
}

function NeedsAttention({ items, ctx }: { items: AttentionItem[]; ctx: SectionCtx }) {
  const act = (a: NonNullable<AttentionItem["action"]>) => {
    switch (a.kind) {
      case "see_which": return ctx.go("history", { run: a.run_id ?? undefined })
      case "back_up_now": return ctx.backUpNow(a.workspace_id ? [a.workspace_id] : undefined)
      default: return ctx.go(a.kind)
    }
  }
  const bad = items.some((i) => i.severity === "bad")
  return (
    <SettingsCard icon={items.length ? AlertTriangle : ShieldCheck} tint={bad ? "var(--destructive)" : items.length ? "var(--warn)" : "var(--success)"}
      title="Needs attention" description={items.length ? "What to fix first, most pressing on top" : "Nothing needs attention"}
      actions={<span className="font-mono text-xs tabular-nums text-muted-foreground">{items.length}</span>}>
      {items.length === 0 ? (
        <p className="px-4 py-3 text-xs text-success">Nothing needs attention.</p>
      ) : items.map((it) => (
        <div key={it.id} data-attention={it.id} className="flex items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0">
          <StatusPill tone={it.severity === "bad" ? "danger" : "warn"} label={it.severity === "bad" ? "urgent" : "check"} className="shrink-0" />
          <div className="min-w-0 flex-1">
            <div className="text-[13px]">{it.title}</div>
            <div className="text-xs text-muted-foreground">{it.detail}</div>
          </div>
          {it.action && (
            <Button type="button" size="sm" variant={it.action.kind === "back_up_now" ? "default" : "outline"} className="h-7 shrink-0 text-xs" onClick={() => act(it.action!)}>
              {it.action.label}
            </Button>
          )}
        </div>
      ))}
    </SettingsCard>
  )
}

const NIGHT_BG: Record<Night["status"], string> = {
  ok: "color-mix(in oklch, var(--success) 55%, transparent)",
  // Hatched warn: created, but not everything is in it — distinct from a
  // skipped or late night, and never the green of a complete one.
  incomplete: "repeating-linear-gradient(135deg, var(--warn) 0 4px, color-mix(in oklch, var(--warn) 45%, transparent) 4px 8px)",
  failed: "var(--destructive)",
  skipped: "var(--warn)",
  late: "var(--warn)",
  none: "transparent",
}

const NIGHT_TONE: Record<Night["status"], "ok" | "warn" | "bad" | "none"> = {
  ok: "ok", incomplete: "warn", skipped: "warn", late: "warn", failed: "bad", none: "none",
}

/** Fourteen equal cells, date under each, ✓ contents checked, ◆ test restore. */
export function NightStrip({ nights, today }: { nights: Night[]; today: string }) {
  const cells = stripCells(nights, today)
  return (
    <>
      <div className="grid grid-cols-[repeat(14,minmax(0,1fr))] gap-1 px-4 pb-1 pt-3" data-slot="night-strip">
        {cells.map((c) => (
          <div key={c.date} className="flex min-w-0 flex-col items-center gap-1" data-status={c.status}>
            <div role="img" aria-label={c.title} title={c.title} data-tone={NIGHT_TONE[c.status]} className={cn("h-7 w-full rounded-md", c.status === "none" && "border border-dashed border-control-border")}
              style={{ background: NIGHT_BG[c.status] }} />
            <span className="text-xs tabular-nums text-muted-foreground">{c.day}</span>
            <span className="h-3.5 text-xs" aria-hidden>{c.mark}</span>
          </div>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 pb-3 text-xs text-muted-foreground">
        <span><Dot className="bg-success" />backup created</span>
        <span><Dot style={{ background: NIGHT_BG.incomplete }} />created · incomplete</span>
        <span><Dot className="bg-warn" />skipped or late</span>
        <span><Dot className="bg-destructive" />failed</span>
        <span>✓ contents checked</span>
        <span>◆ test restore</span>
      </div>
    </>
  )
}

const COVERAGE_LABEL: Record<WorkspaceCoverage["status"], string> = { ok: "covered", warn: "stale", bad: "not covered" }

function CoverageCard({ rows, ctx, now }: { rows: WorkspaceCoverage[]; ctx: SectionCtx; now: Date }) {
  const th = "border-b border-border px-4 py-2 text-left text-xs font-medium text-muted-foreground"
  const td = "border-b border-border px-4 py-2.5 text-[13px]"
  return (
    <SettingsCard icon={LayoutGrid} title="Coverage" description="The newest backup of each selected workspace">
      <div className="overflow-x-auto">
        <table className="w-full tabular-nums">
          <thead><tr><th className={th}>Workspace</th><th className={th}>Last backup</th><th className={th}>Plan</th><th className={th}>Checked to</th><th className={th} /></tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.workspace_id} data-coverage={r.status} className="[&:last-child>td]:border-b-0">
                <td className={td}>
                  {r.name}
                  {r.workspace_id === ctx.currentWorkspaceId && <span className="ml-1.5 text-xs text-muted-foreground">· here</span>}
                </td>
                <td className={cn(td, !r.last_backup_at && "text-muted-foreground")}>{formatAgo(r.last_backup_at, now)}</td>
                <td className={cn(td, !r.plan && "text-muted-foreground")}>{r.plan ?? "no plan covers it"}</td>
                <td className={cn(td, "text-muted-foreground")}>{proofLabel(r.proof)}</td>
                <td className={cn(td, "text-right")}>
                  <span className="inline-flex items-center gap-2">
                    <StatusPill tone={TONE_PILL[r.status]} label={COVERAGE_LABEL[r.status]} />
                    {r.status !== "ok" && (
                      <Button type="button" size="sm" className="h-7 text-xs" onClick={() => ctx.backUpNow([r.workspace_id])}>Back up now</Button>
                    )}
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </SettingsCard>
  )
}

export function SpaceCard({ space }: { space: SpaceInfo }) {
  const pct = (n: number) => (space.total_bytes > 0 ? Math.max(0, Math.min(100, (n / space.total_bytes) * 100)) : 0)
  const used = Math.max(0, space.total_bytes - space.free_bytes)
  const backups = pct(space.backups_bytes)
  const other = Math.max(0, pct(used) - backups)
  const fig = "font-mono text-[13px] tabular-nums"
  return (
    <SettingsCard icon={HardDrive} tint="var(--purple)" title="Space"
      description={space.min_free_percent != null ? `A run that would leave less than ${space.min_free_percent} % free does not start` : "This server"}>
      <SettingsRow label="Backups use"><span className={fig}>{formatSize(space.backups_bytes)}</span></SettingsRow>
      <SettingsRow label="Disk">
        <span className={cn(fig, space.refusal && TONE_TEXT.bad)}>{`${formatSize(space.free_bytes)} free`}</span>
        <span className="text-xs text-muted-foreground">of {formatSize(space.total_bytes)}</span>
      </SettingsRow>
      <SettingsRow label="A backup run needs" description="Free room for staging"><span className={fig}>{formatSize(space.staging_need_bytes)}</span></SettingsRow>
      <SettingsRow label="Restoring the largest backup needs"><span className={fig}>{formatSize(space.restore_need_bytes)}</span></SettingsRow>
      <div className="flex flex-col gap-2 px-4 py-3">
        <div className="flex h-2.5 overflow-hidden rounded-full bg-muted" role="img" aria-label={`Backups ${backups.toFixed(1)} %, everything else ${other.toFixed(1)} %, the rest free`}>
          <i className="block h-full bg-primary" style={{ width: `${backups}%` }} />
          <i className="block h-full bg-control-border" style={{ width: `${other}%` }} />
        </div>
        <div className="flex flex-wrap items-center gap-x-3 text-xs text-muted-foreground">
          <span><Dot className="bg-primary" />backups</span>
          <span><Dot className="bg-control-border" />everything else</span>
          <span><Dot className="border border-control-border bg-muted" />free</span>
        </div>
      </div>
    </SettingsCard>
  )
}
