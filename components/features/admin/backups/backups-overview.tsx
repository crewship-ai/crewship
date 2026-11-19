"use client"

import * as React from "react"
import { AlertTriangle, Building2, CalendarDays, HardDrive, ShieldCheck } from "lucide-react"

import { cn } from "@/lib/utils"
import { useShowMore } from "@/hooks/use-show-more"
import { Button } from "@/components/ui/button"
import { ShowMore } from "@/components/ui/show-more"
import { StatusPill } from "@/components/ui/status-pill"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { Dot, Gate, TONE_TEXT } from "./backups-kit"
import {
  coverageCounts, coverageRows, formatSize, stripCells, verdictHeadline,
  type AttentionItem, type CoverageState, type Night, type OverviewResponse, type SpaceInfo, type StatusRow, type WorkspaceCoverage,
} from "./backups-model"
import { useBackupsOverview } from "./use-backups-overview"
import type { Resource } from "./use-backups-data"
import type { SectionCtx } from "./backups-scope"

/**
 * Backups › Overview, in the Settings card grammar (like Security › Overview).
 *
 * The head says the server's verdict in one sentence. Then what needs a
 * person (a local-only copy is one of those items, said once), and a two-column
 * grid: what is protected (the five questions), the coverage in four counts
 * that each open Coverage filtered, the last fourteen nights and the room on
 * this disk. Each fact appears once: no strip repeating the panel's counts.
 * A workspace's own page uses the same body in one column, without Coverage
 * and Space.
 */
export function BackupsOverview({ ctx, resource, coverage, onOpenCoverage }: {
  ctx: SectionCtx
  /** The page's own read of the overview (it also feeds the head and the panel badge). */
  resource?: Resource<OverviewResponse>
  /** Each workspace's newest backup when the page covers the whole instance. */
  coverage?: WorkspaceCoverage[]
  /** A Coverage count opens Coverage narrowed to it. */
  onOpenCoverage?: (state?: CoverageState) => void
}) {
  if (resource) return <OverviewGate res={resource} ctx={ctx} coverage={coverage} onOpenCoverage={onOpenCoverage} />
  return <OwnOverview ctx={ctx} coverage={coverage} onOpenCoverage={onOpenCoverage} />
}

function OwnOverview(props: { ctx: SectionCtx; coverage?: WorkspaceCoverage[]; onOpenCoverage?: (state?: CoverageState) => void }) {
  const res = useBackupsOverview(props.ctx.scope, props.ctx.selected, props.ctx.workspaces)
  return <OverviewGate res={res} {...props} />
}

function OverviewGate({ res, ctx, coverage, onOpenCoverage }: { res: Resource<OverviewResponse>; ctx: SectionCtx; coverage?: WorkspaceCoverage[]; onOpenCoverage?: (state?: CoverageState) => void }) {
  return (
    <Gate resource={res} what="The backup overview" skeleton="h-[320px]">
      {(data) => <OverviewBody data={data} ctx={ctx} coverage={coverage} onOpenCoverage={onOpenCoverage} />}
    </Gate>
  )
}

/** The head's one sentence: how the restore stands, how often, where. */
export function overviewSentence(data: OverviewResponse): string {
  const s = data.status
  return [verdictHeadline(s.verdict).text, s.how_often.value, s.offsite_verified ? s.where.value : "this server only"].filter(Boolean).join(" · ")
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

export function OverviewBody({ data, ctx, now = new Date(), coverage, onOpenCoverage, hideSpace }: {
  data: OverviewResponse
  ctx: SectionCtx
  now?: Date
  /** Each workspace's newest backup when the page covers the whole instance
   *  (the server lists them for a workspaces scope only). */
  coverage?: WorkspaceCoverage[]
  onOpenCoverage?: (state?: CoverageState) => void
  /** A workspace's page: one column, no Coverage and no disk (both are the instance's). */
  hideSpace?: boolean
}) {
  const s = data.status
  const rows = coverage ?? data.workspaces
  const scoped = ctx.scope === "instance" ? ctx.workspaces : ctx.workspaces.filter((w) => ctx.selected.has(w.id))
  const counts = coverageCounts(coverageRows(rows, scoped))
  const head = verdictHeadline(s.verdict)
  const cards = (
    <>
      <SettingsCard icon={ShieldCheck} tint={`var(--${head.tone === "bad" ? "destructive" : head.tone === "warn" ? "warn" : "success"})`}
        title="What is protected" description={s.summary ?? s.label}>
        <ProtectionRow label="Protects" row={s.protects} />
        <ProtectionRow label="How often" row={s.how_often} />
        <ProtectionRow label="Where" row={s.where} quiet />
        <ProtectionRow label="How long" row={s.how_long} />
        <ProtectionRow label="Really restored?" row={s.really_restored} />
      </SettingsCard>
      {!hideSpace && rows && <CoverageCard counts={counts} crewless={data.crewless_workspaces} onOpen={onOpenCoverage} />}
      <SettingsCard icon={CalendarDays} tint="var(--success)" title="Last 14 nights" description="What each night’s run did"
        actions={!hideSpace ? <Button type="button" size="sm" variant="ghost" className="h-7 px-2.5 text-xs" onClick={() => ctx.go("history")}>All runs</Button> : undefined}>
        <NightStrip nights={data.nights} today={localDay(now)} />
      </SettingsCard>
      {!hideSpace && <SpaceCard space={data.space} onStorage={() => ctx.go("storage")} />}
    </>
  )
  return (
    <>
      <NeedsAttention items={attentionItems(data)} ctx={ctx} />
      {hideSpace ? cards : <div className="grid items-start gap-4 md:grid-cols-2" data-slot="overview-grid">{cards}</div>}
    </>
  )
}

/** One of the five questions: the question and its answer, once. */
function ProtectionRow({ label, row, quiet }: { label: string; row: StatusRow; quiet?: boolean }) {
  const tone = row.detail_tone ?? "ok"
  return (
    <SettingsRow label={label} description={<>
      {row.value}
      {row.detail && !quiet && <span className={cn("block", TONE_TEXT[tone])}>{row.detail}</span>}
    </>} data-testid={`question-${label}`}>
      {null}
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
  // Hundreds of workspaces can mean hundreds of findings: ten, then more.
  const page = useShowMore(items, { step: 10 })
  return (
    <SettingsCard icon={items.length ? AlertTriangle : ShieldCheck} tint={bad ? "var(--destructive)" : items.length ? "var(--warn)" : "var(--success)"}
      title="Needs attention" description={items.length ? "Most pressing first" : "Nothing needs attention"}
      actions={<span className="text-xs tabular-nums text-muted-foreground">{items.length}</span>}>
      {items.length === 0 ? (
        <p className="px-4 py-3 text-xs text-success">Nothing needs attention.</p>
      ) : page.shown.map((it) => (
        <div key={it.id} data-attention={it.id} className="flex items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0 max-sm:flex-wrap">
          {/* The severity words every Needs attention card uses (People, Security). */}
          <StatusPill tone={it.severity === "bad" ? "danger" : "warn"} label={it.severity === "bad" ? "High" : "Medium"} className="shrink-0" />
          <div className="min-w-0 flex-1">
            <div className="text-control">{it.title}</div>
            <div className="text-xs text-muted-foreground">{it.detail}</div>
          </div>
          {it.action && (
            <Button type="button" size="sm" variant={it.action.kind === "back_up_now" ? "default" : "outline"} className="h-7 shrink-0 text-xs coarse:h-[44px]" onClick={() => act(it.action!)}>
              {it.action.label}
            </Button>
          )}
        </div>
      ))}
      <ShowMore shown={page.shown.length} total={page.total} nextCount={page.nextCount} onMore={page.showMore} noun="findings" paged={page.paged} />
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

const COVERAGE_ROWS: { state: CoverageState; label: (n: number) => string; hint: string; dot: string }[] = [
  { state: "covered", label: (n) => `${n} covered`, hint: "Backed up in the last 36 h", dot: "bg-success" },
  { state: "stale", label: (n) => `${n} stale`, hint: "Last backup older than 36 h, incomplete, or no plan covers it", dot: "bg-warn" },
  { state: "never", label: (n) => `${n} never backed up`, hint: "No backup exists yet", dot: "bg-destructive" },
  { state: "none", label: (n) => `${n} with nothing to back up`, hint: "No crews yet", dot: "bg-muted-foreground/50" },
]

/** Four counts, each opening Coverage narrowed to it. */
function CoverageCard({ counts, crewless, onOpen }: { counts: ReturnType<typeof coverageCounts>; crewless?: number; onOpen?: (state?: CoverageState) => void }) {
  const none = Math.max(counts.none, crewless ?? 0)
  return (
    <SettingsCard icon={Building2} title="Coverage" description={`${counts.total} workspaces`}
      actions={onOpen && <Button type="button" size="sm" variant="outline" className="h-7 px-2.5 text-xs" onClick={() => onOpen()}>All workspaces</Button>}>
      {COVERAGE_ROWS.map((r) => {
        const n = r.state === "none" ? none : counts[r.state]
        return (
          <button key={r.state} type="button" disabled={!onOpen} onClick={() => onOpen?.(r.state)} data-coverage-count={r.state}
            className="flex w-full items-center gap-3 border-b border-border px-4 py-2.5 text-left last:border-b-0 enabled:hover:bg-[var(--row-hover-bg)] coarse:min-h-[44px]">
            <span className="min-w-0 flex-1">
              <span className="block text-control text-foreground">{r.label(n)}</span>
              <span className="block text-label text-muted-foreground">{r.hint}</span>
            </span>
            <span className={cn("h-2 w-2 shrink-0 rounded-full", r.dot)} aria-hidden />
          </button>
        )
      })}
    </SettingsCard>
  )
}

export function SpaceCard({ space, onStorage }: { space: SpaceInfo; onStorage?: () => void }) {
  const pct = (n: number) => (space.total_bytes > 0 ? Math.max(0, Math.min(100, (n / space.total_bytes) * 100)) : 0)
  const used = Math.max(0, space.total_bytes - space.free_bytes)
  const backups = pct(space.backups_bytes)
  const other = Math.max(0, pct(used) - backups)
  const fig = "text-control tabular-nums"
  return (
    <SettingsCard icon={HardDrive} tint="var(--purple)" title="Space"
      description={space.min_free_percent != null ? `A run that would leave less than ${space.min_free_percent} % free does not start` : "This server"}
      actions={onStorage && <Button type="button" size="sm" variant="ghost" className="h-7 px-2.5 text-xs" onClick={onStorage}>Storage</Button>}>
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
