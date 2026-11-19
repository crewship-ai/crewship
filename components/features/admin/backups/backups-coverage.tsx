"use client"

import * as React from "react"

import { useShowMore } from "@/hooks/use-show-more"
import { Button } from "@/components/ui/button"
import { ShowMore } from "@/components/ui/show-more"
import { StatusPill } from "@/components/ui/status-pill"
import type { StatusTone } from "@/lib/format-status"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import { BackupsTable, Gate, ProofSteps, type TableColumn } from "./backups-kit"
import { COVERAGE_LABEL, coverageCounts, coverageRows, formatAgo, type CoverageRow, type CoverageState, type OverviewResponse } from "./backups-model"
import type { Resource } from "./use-backups-data"
import type { SectionCtx } from "./backups-scope"

const TONE: Record<CoverageState, StatusTone> = { never: "danger", stale: "warn", covered: "success", none: "muted" }

/** "Never backed up" and the rest, as the four filters the panel and the Overview share. */
export const COVERAGE_FILTERS: CoverageState[] = ["covered", "stale", "never", "none"]

/**
 * Backups › Coverage: every workspace, least protected first, with when it was
 * last backed up, by which plan, how far that backup was checked and where it
 * stands. Paged in the browser (hundreds of workspaces); a row opens the
 * workspace's own page. The Status filter lives in the panel and the URL.
 */
export function BackupsCoverage({ resource, ctx, status, query, onOpenWorkspace, now = new Date() }: {
  resource: Resource<OverviewResponse>
  ctx: SectionCtx
  status: CoverageState | ""
  /** The panel's search over name and slug. */
  query: string
  onOpenWorkspace: (id: string) => void
  now?: Date
}) {
  return (
    <Gate resource={resource} what="Coverage" skeleton="h-[280px]">
      {(data) => <CoverageBody rows={coverageRows(data.workspaces, ctx.workspaces)} ctx={ctx} status={status} query={query} onOpenWorkspace={onOpenWorkspace} now={now} />}
    </Gate>
  )
}

export function CoverageBody({ rows, ctx, status, query, onOpenWorkspace, now = new Date() }: {
  rows: CoverageRow[]
  ctx: SectionCtx
  status: CoverageState | ""
  query: string
  onOpenWorkspace: (id: string) => void
  now?: Date
}) {
  const needle = query.trim().toLowerCase()
  const shown = React.useMemo(
    () => rows.filter((r) => (!status || r.state === status) && (!needle || r.name.toLowerCase().includes(needle) || r.slug.toLowerCase().includes(needle))),
    [rows, status, needle],
  )
  const page = useShowMore(shown, { step: 25, resetKey: `${status}|${needle}` })
  const n = coverageCounts(rows)
  const backUp = (r: CoverageRow) => r.state !== "none" && (
    <Button type="button" size="sm" variant="outline" className="h-7 px-2.5 text-xs coarse:h-[44px]" aria-label={`Back up ${r.name} now`}
      onClick={(e) => { e.stopPropagation(); ctx.backUpNow([r.id]) }}>
      Back up now
    </Button>
  )
  const stateLabel = (r: CoverageRow) => <StatusPill tone={TONE[r.state]} label={COVERAGE_LABEL[r.state]} />
  const columns: TableColumn<CoverageRow>[] = [
    { key: "ws", header: "Workspace", cell: (r) => (
      <span className="flex items-center gap-2.5">
        <WorkspaceTile id={r.id} name={r.name} logoUrl={r.logoUrl} />
        <span className="font-medium">{r.name}</span>
        {r.id === ctx.currentWorkspaceId && <span className="text-xs text-muted-foreground">· here</span>}
      </span>
    ) },
    { key: "last", header: "Last backup", cell: (r) => (r.last_backup_at ? formatAgo(r.last_backup_at, now) : <span className="text-muted-foreground">—</span>) },
    { key: "plan", header: "Plan", cell: (r) => (r.state === "none" ? <span className="text-muted-foreground">—</span> : r.plan ?? <span className="text-muted-foreground">No plan covers it</span>) },
    { key: "proof", header: "Checked to", cell: (r) => (r.last_backup_at ? <ProofSteps level={r.proof} /> : <span className="text-muted-foreground">—</span>) },
    { key: "status", header: "Status", cell: stateLabel },
    { key: "act", header: <span className="sr-only">Back up now</span>, right: true, cell: (r) => backUp(r) || null },
  ]
  return (
    <section aria-label="Coverage" className="overflow-hidden rounded-card border border-border bg-card" data-slot="coverage">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 border-b border-border px-4 py-3">
        <h3 className="text-sm font-semibold">{shown.length === rows.length ? `${rows.length} workspaces` : `${shown.length} of ${rows.length} workspaces`}</h3>
        <p className="text-label text-muted-foreground">{n.never} never backed up · {n.stale} stale · least protected first</p>
      </div>
      <BackupsTable
        rows={page.shown} rowKey={(r) => r.id} columns={columns} label="Workspaces" onOpen={(r) => onOpenWorkspace(r.id)}
        rowProps={(r) => ({ "data-coverage": r.state, "aria-label": r.name })}
        stack={(r) => ({
          title: r.name,
          sub: r.state === "none" ? "No crews" : r.state === "never" ? r.plan ?? "No plan covers it" : `${formatAgo(r.last_backup_at, now)}${r.plan ? "" : " · no plan"}`,
          end: <span className="flex items-center gap-2">{stateLabel(r)}{backUp(r)}</span>,
        })}
        empty={rows.length === 0 ? "No workspace yet." : "No workspace matches this filter."} />
      <ShowMore shown={page.shown.length} total={page.total} nextCount={page.nextCount} onMore={page.showMore} noun="workspaces" paged={page.paged} />
    </section>
  )
}
