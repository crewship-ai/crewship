"use client"

import * as React from "react"
import { History, RotateCcw } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { SettingsCard } from "@/components/features/settings/shared"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import { Chip, Gate } from "./backups-kit"
import { formatAgo, formatWhen, proofLabel, runPlanLabel, runResult, type ScopeWorkspace, type WorkspaceCoverage } from "./backups-model"
import { useBackupsOverview } from "./use-backups-overview"
import { useBackupRuns } from "./use-backup-runs"
import { OverviewBody } from "./backups-overview"
import type { SectionCtx } from "./backups-scope"

/**
 * One workspace's backups, like a workspace's page in People & workspaces and
 * Security: who it is and how it stands in one line of chips, Back up now and
 * Restore for it alone, then the overview the server gives for that workspace
 * (what needs attention, the five questions, its nights) and its last runs.
 */
export function WorkspaceBackups({ ws, coverage, ctx, onOpenRuns }: {
  ws: ScopeWorkspace
  coverage?: WorkspaceCoverage
  /** The page's context; the scope is narrowed to this workspace here. */
  ctx: SectionCtx
  /** All of its runs, in the Runs view narrowed to it. */
  onOpenRuns: (runId?: string) => void
}) {
  const only = React.useMemo(() => new Set([ws.id]), [ws.id])
  const wsCtx: SectionCtx = { ...ctx, scope: "workspaces", selected: only }
  const overview = useBackupsOverview("workspaces", only, ctx.workspaces)
  const runs = useBackupRuns("workspaces", only, ctx.workspaces)
  const now = new Date()
  const tone = coverage?.status ?? "bad"
  const chip = (text: React.ReactNode, dot?: "ok" | "warn" | "bad") => (
    <span className="inline-flex items-center gap-1.5 rounded-full border border-border px-2 py-0.5 text-label text-muted-foreground">
      {dot && <span className={cn("h-1.5 w-1.5 rounded-full", dot === "ok" ? "bg-success" : dot === "warn" ? "bg-warn" : "bg-destructive")} aria-hidden />}{text}
    </span>
  )
  const recent = (runs.data ?? []).filter((r) => r.workspace_id === ws.id).slice(0, 5)

  return (
    <>
      <section className="flex flex-wrap items-start gap-3 rounded-card border border-border bg-card p-4" data-slot="workspace-backups-header">
        <WorkspaceTile id={ws.id} name={ws.name} size="lg" logoUrl={ws.logoUrl} />
        <div className="min-w-0 flex-1">
          <h2 className="text-lg font-semibold tracking-[-0.01em]">{ws.name}</h2>
          <p className="font-mono text-micro text-muted-foreground">{ws.slug}</p>
          <div className="mt-2.5 flex flex-wrap gap-1.5">
            {chip(coverage?.last_backup_at ? `Backed up ${formatAgo(coverage.last_backup_at, now)}` : "Never backed up", tone)}
            {coverage && coverage.proof > 0 && chip(proofLabel(coverage.proof))}
            {chip(coverage?.plan ?? "No plan covers it", coverage?.plan ? undefined : "warn")}
          </div>
        </div>
        <div className="flex gap-1.5">
          <Button size="sm" variant="outline" className="h-8 text-xs" onClick={() => ctx.go("recovery")}><RotateCcw />Restore…</Button>
          <Button size="sm" className="h-8 text-xs" onClick={() => ctx.backUpNow([ws.id])}><History />Back up now</Button>
        </div>
      </section>

      <Gate resource={overview} what="This workspace's backups" skeleton="h-[240px]">
        {/* Its own row is the header above: no table of one workspace. */}
        {(data) => <OverviewBody data={data} ctx={wsCtx} hideSpace coverage={[]} />}
      </Gate>

      <SettingsCard icon={History} title="Its runs" description="The last few · all of them in Runs, narrowed to it"
        actions={<Button type="button" size="sm" variant="outline" className="h-7 px-2.5 text-xs" onClick={() => onOpenRuns()}>All runs</Button>}>
        {recent.length === 0 ? (
          <p className="px-4 py-3 text-label text-muted-foreground">{runs.status === "loading" ? "Loading…" : "No run has backed it up yet."}</p>
        ) : recent.map((r) => {
          const res = runResult(r)
          return (
            <button key={r.id} type="button" onClick={() => onOpenRuns(r.id)} data-run={r.id}
              className="flex w-full items-center gap-3 border-b border-border px-4 py-2.5 text-left last:border-b-0 hover:bg-[var(--row-hover-bg)]">
              <span className="w-28 shrink-0 font-mono text-label text-muted-foreground">{formatWhen(r.started_at, now)}</span>
              <span className="min-w-0 flex-1 truncate text-control">{runPlanLabel(r)}</span>
              <Chip tone={res.tone}>{res.text}</Chip>
            </button>
          )
        })}
      </SettingsCard>
    </>
  )
}
