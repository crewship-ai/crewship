"use client"

import * as React from "react"

import { cn } from "@/lib/utils"
import { SettingsCard } from "@/components/features/settings/shared"
import { Chip, Dot, Eyebrow, FieldRow, Gate, ItemRow, LocalOnlyBar, SmallButton, TD, TH, TONE_TEXT, WsName } from "./backups-kit"
import {
  formatAgo, formatSize, proofLabel, stripCells, verdictHeadline,
  type AttentionItem, type Night, type OverviewResponse, type SpaceInfo, type WorkspaceCoverage,
} from "./backups-model"
import { useBackupsOverview } from "./use-backups-overview"
import type { SectionCtx } from "./backups-console"

/**
 * Backups › Overview. Five questions a client asks — what is protected, how
 * often, where, how long, and was it really restored — then only concrete
 * problems, the last fourteen nights, and the room on this disk.
 */
export function BackupsOverview({ ctx }: { ctx: SectionCtx }) {
  const res = useBackupsOverview(ctx.scope, ctx.selected, ctx.workspaces)
  return (
    <Gate resource={res} what="The backup overview" skeleton="h-[320px]">
      {(data) => <OverviewBody data={data} ctx={ctx} />}
    </Gate>
  )
}

export function OverviewBody({ data, ctx, now = new Date() }: { data: OverviewResponse; ctx: SectionCtx; now?: Date }) {
  const s = data.status
  const head = verdictHeadline(s.verdict)
  return (
    <>
      <section data-slot="backup-status" aria-label="Backup status" className="overflow-hidden rounded-card border border-border bg-card">
        <div className="flex flex-col gap-1.5 px-4 pb-2.5 pt-3.5">
          <Eyebrow>{s.label}</Eyebrow>
          <span data-slot="backup-verdict" data-tone={head.tone} className={cn("text-[22px] font-semibold leading-tight", TONE_TEXT[head.tone])}>{head.text}</span>
          {s.summary && <p className="text-[13.5px]">{s.summary}</p>}
        </div>
        <div className="border-t border-border">
          <FieldRow label="Protects" detail={s.protects.detail} detailTone={s.protects.detail_tone}>{s.protects.value}</FieldRow>
          <FieldRow label="How often" detail={s.how_often.detail} detailTone={s.how_often.detail_tone}>{s.how_often.value}</FieldRow>
          <FieldRow label="Where" detail={s.where.detail} detailTone={s.where.detail_tone}>{s.where.value}</FieldRow>
          <FieldRow label="How long" detail={s.how_long.detail} detailTone={s.how_long.detail_tone}>{s.how_long.value}</FieldRow>
          <FieldRow label="Really restored?" detail={s.really_restored.detail} detailTone={s.really_restored.detail_tone}>{s.really_restored.value}</FieldRow>
        </div>
      </section>

      {!s.offsite_verified && <LocalOnlyBar onStorage={() => ctx.go("storage")} />}

      <NeedsAttention items={data.needs_attention} ctx={ctx} />

      <SettingsCard title="Last 14 nights" description="what each night's run did">
        <NightStrip nights={data.nights} today={localDay(now)} />
      </SettingsCard>

      {ctx.scope === "workspaces" && data.workspaces && <CoverageTable rows={data.workspaces} ctx={ctx} now={now} />}

      <SpaceCard space={data.space} />
    </>
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
  return (
    <SettingsCard title="Needs attention" actions={<span className="font-mono text-[11px] text-muted-foreground">{items.length}</span>}>
      {items.length === 0 ? (
        <p className="px-4 py-3 text-[12.5px] text-success">Nothing needs attention.</p>
      ) : items.map((it) => (
        <ItemRow key={it.id} lead={<Chip tone={it.severity}>!</Chip>} title={it.title} detail={it.detail}
          action={it.action && <SmallButton primary={it.action.kind === "back_up_now"} onClick={() => act(it.action!)}>{it.action.label}</SmallButton>} />
      ))}
    </SettingsCard>
  )
}

const NIGHT_BG: Record<Night["status"], string> = {
  ok: "color-mix(in oklch, var(--success) 55%, transparent)",
  failed: "var(--destructive)",
  skipped: "var(--warn)",
  late: "var(--warn)",
  none: "transparent",
}

/** Fourteen equal cells, date under each, ✓ contents checked, ◆ test restore. */
export function NightStrip({ nights, today }: { nights: Night[]; today: string }) {
  const cells = stripCells(nights, today)
  return (
    <>
      <div className="grid grid-cols-[repeat(14,minmax(0,1fr))] gap-1 px-3.5 pb-1 pt-3" data-slot="night-strip">
        {cells.map((c) => (
          <div key={c.date} className="flex min-w-0 flex-col items-center gap-1" data-status={c.status}>
            <div role="img" aria-label={c.title} title={c.title} className={cn("h-[30px] w-full rounded-[5px]", c.status === "none" && "border-[1.5px] border-dashed border-control-border")}
              style={{ background: NIGHT_BG[c.status] }} />
            <span className="text-[12px] text-muted-foreground">{c.day}</span>
            <span className="h-3.5 text-[12px]" aria-hidden>{c.mark}</span>
          </div>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 px-3.5 pb-3 text-[13px] text-muted-foreground">
        <span><Dot className="bg-success" />backup created</span>
        <span><Dot className="bg-warn" />skipped or late</span>
        <span><Dot className="bg-destructive" />failed</span>
        <span>✓ contents checked</span>
        <span>◆ test restore</span>
      </div>
    </>
  )
}

function CoverageTable({ rows, ctx, now }: { rows: WorkspaceCoverage[]; ctx: SectionCtx; now: Date }) {
  return (
    <SettingsCard title="Workspaces" description="the newest backup of each selected workspace">
      <div className="overflow-x-auto">
        <table className="w-full tabular-nums">
          <thead><tr><th className={TH}>Workspace</th><th className={TH}>Last backup</th><th className={TH}>Plan</th><th className={TH}>Checked to</th><th className={TH} /></tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.workspace_id} className="[&:last-child>td]:border-b-0">
                <td className={TD}><WsName name={r.name} here={r.workspace_id === ctx.currentWorkspaceId} /></td>
                <td className={TD}>{formatAgo(r.last_backup_at, now)}</td>
                <td className={cn(TD, !r.plan && "text-muted-foreground")}>{r.plan ?? "no plan covers it"}</td>
                <td className={TD}>{proofLabel(r.proof)}</td>
                <td className={cn(TD, "text-right")}>
                  {r.status === "ok" ? <Chip tone="ok">covered</Chip> : <SmallButton primary onClick={() => ctx.backUpNow([r.workspace_id])}>Back up now</SmallButton>}
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
  return (
    <SettingsCard title="Space" description="this server">
      <div className="flex flex-col gap-2 px-3.5 py-3">
        <div className="flex flex-wrap gap-x-4 gap-y-1.5 text-[13.5px] text-muted-foreground">
          <span>Backups use <b className="font-semibold tabular-nums text-foreground">{formatSize(space.backups_bytes)}</b></span>
          <span>disk <b className="font-semibold tabular-nums text-foreground">{formatSize(space.free_bytes)} free</b> of {formatSize(space.total_bytes)}</span>
        </div>
        <div className="text-[13.5px] text-muted-foreground">
          A backup run needs <b className="font-semibold tabular-nums text-foreground">{formatSize(space.staging_need_bytes)}</b> free for staging · restoring the largest backup needs <b className="font-semibold tabular-nums text-foreground">{formatSize(space.restore_need_bytes)}</b>
          {space.min_free_percent != null && <> · a run that would leave less than {space.min_free_percent} % free does not start</>}
        </div>
        {space.refusal && (
          <p data-slot="space-refusal" className="rounded-md border border-destructive/40 bg-destructive/10 px-2.5 py-1.5 text-[13px] text-destructive">
            <b className="font-semibold">New backup runs will not start.</b> {space.refusal}
          </p>
        )}
        <div className="flex h-2.5 overflow-hidden rounded-full bg-muted" role="img" aria-label={`Backups ${backups.toFixed(1)} %, everything else ${other.toFixed(1)} %, the rest free`}>
          <i className="block h-full bg-primary" style={{ width: `${backups}%` }} />
          <i className="block h-full bg-control-border" style={{ width: `${other}%` }} />
        </div>
        <div className="flex flex-wrap items-center gap-x-2.5 text-[13px] text-muted-foreground">
          <span><Dot className="bg-primary" />backups</span>
          <span><Dot className="bg-control-border" />everything else</span>
          <span><Dot className="border border-control-border bg-muted" />free</span>
        </div>
      </div>
    </SettingsCard>
  )
}
