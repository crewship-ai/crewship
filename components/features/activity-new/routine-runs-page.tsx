"use client"

// One routine, and its runs.
//
// This is the page a routine on a one-minute schedule needs and nothing else in
// the product provides. The Routines page shows a routine's DEFINITION with a
// RUNS dock listing the last few; the Activity rail shows workflows, and thirty
// runs of one routine are thirty rows that read the same. Neither answers
// "which run was the one at ten past two".
//
// Three decisions carry the page, all of them in lib/run-digest so they can be
// tested without a DOM:
//
//   · absolute time, not "9h ago" — on a per-minute routine the reader knows it
//     was recent and is asking WHICH ONE;
//   · the run's own result instead of its id — thirty ids differ in a way no
//     human reads, "3 tickets classified" versus "no tickets" is the difference
//     they came for;
//   · an hour header that summarises, so a quiet hour is skipped rather than
//     read.
//
// No new endpoint. GET .../pipelines/{slug}/run-records has carried `output`
// since v83; a summary column would be a second place for the same fact to be
// wrong.

import * as React from "react"
import { ArrowLeft, ChevronRight, ListTree, ScrollText } from "lucide-react"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { Appear } from "@/components/ui/detail"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { CrewIcon } from "@/components/ui/crew-icon"
import { usePipelineRunRecords } from "@/hooks/use-pipeline-run-records"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { routineHref } from "@/lib/routine-href"
import { formatDurationMs } from "@/lib/activity-stream"
import { groupRunsByHour, medianRunDuration, runHeadline, type DigestRun } from "@/lib/run-digest"
import type { SidebarRoutine } from "./activity-sidebar"

/** Tone token per headline tone — the four the rest of the page already reads. */
const TONE_TOKEN: Record<string, string> = {
  failed: "--destructive",
  slow: "--warn",
  running: "--primary",
  ok: "--success",
}

export interface RoutineRunsPageProps {
  workspaceId: string
  slug: string
  /** Human name, resolved by the shell. The slug is the fallback, never blank. */
  label: string
  routine?: SidebarRoutine
  onBack: () => void
  /** Open one run — the same drill-down the workflow page uses. */
  onOpenRun: (runID: string) => void
}

export function RoutineRunsPage({
  workspaceId,
  slug,
  label,
  routine,
  onBack,
  onOpenRun,
}: RoutineRunsPageProps) {
  const { records, loading, error, legacy, refresh } = usePipelineRunRecords(workspaceId, slug)

  const runs = React.useMemo<DigestRun[]>(
    () =>
      records.map((r) => ({
        id: r.id,
        status: r.status,
        started_at: r.started_at,
        duration_ms: r.duration_ms,
        triggered_via: r.triggered_via,
        output: r.output,
        error_message: r.error_message,
      })),
    [records],
  )

  // The peer median, computed ONCE over the whole page and handed to every row.
  // Computed per row it would be the same number derived N times; worse, a row
  // comparing itself only to its own hour would call the same run slow in a
  // quiet hour and normal in a busy one.
  const median = React.useMemo(() => medianRunDuration(runs), [runs])
  const buckets = React.useMemo(() => groupRunsByHour(runs), [runs])

  const failed = runs.filter((r) => r.status === "failed").length
  const ok = runs.length - failed
  const slowest = runs.reduce((m, r) => Math.max(m, r.duration_ms), 0)
  const newest = runs.find((r) => !Number.isNaN(Date.parse(r.started_at)))

  return (
    <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
      <Appear order={0}>
        <div className="flex flex-col gap-2">
          <button
            type="button"
            onClick={onBack}
            className="inline-flex w-fit items-center gap-1.5 rounded-md px-1.5 py-1 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            <ArrowLeft className="h-3.5 w-3.5" /> Back to routines
          </button>
          <div className="flex flex-wrap items-center gap-2">
            <CrewIcon
              icon={resolveRoutineIcon(routine ?? { slug })}
              color={resolveRoutineColor(routine ?? { slug })}
              size="sm"
              className="!h-5 !w-5 !rounded-md shrink-0"
            />
            <h1 className="min-w-0 text-lg font-semibold tracking-tight">{label}</h1>
            {/* The definition lives on /routines. Linked rather than duplicated:
                two places to edit one routine is how the two disagree. */}
            <a
              href={routineHref(slug)}
              className="font-mono text-[11px] text-primary hover:underline"
              title="Open the routine's definition"
            >
              {slug} ↗
            </a>
          </div>
          <p className="text-xs text-muted-foreground">
            {runs.length.toLocaleString()} {runs.length === 1 ? "run" : "runs"} recorded
            {newest && ` · newest ${new Date(newest.started_at).toLocaleString(undefined, { hour12: false })}`}
          </p>
        </div>
      </Appear>

      <Appear order={1}>
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <KpiCard label="Runs" value={runs.length} subtitle="in the loaded page" />
          <KpiCard
            label="Success"
            value={runs.length > 0 ? `${Math.round((ok / runs.length) * 100)}%` : "—"}
            subtitle={runs.length > 0 ? `${ok} of ${runs.length}` : "nothing recorded"}
            valueColor={
              runs.length === 0 ? undefined : failed > 0 ? "var(--warn)" : "var(--success)"
            }
          />
          <KpiCard
            label="Typical duration"
            // The MEDIAN, not the mean: one 40-second outlier among thirty
            // 200ms runs moves a mean past every run that actually happened.
            value={median != null ? formatDurationMs(median) : "—"}
            subtitle={median != null ? "median of the finished runs" : "nothing finished yet"}
          />
          <KpiCard
            label="Slowest"
            value={slowest > 0 ? formatDurationMs(slowest) : "—"}
            subtitle={slowest > 0 && median != null && slowest > median * 2 ? "well above typical" : "of this page"}
            valueColor={slowest > 0 && median != null && slowest > median * 2 ? "var(--warn)" : undefined}
          />
        </div>
      </Appear>

      <Appear order={2}>
        <DashboardCard
          role="region"
          aria-label="Runs"
          title="Runs"
          icon={ListTree}
          hint={runs.length > 0 ? "click a run to open its trace" : undefined}
        >
          {loading && runs.length === 0 && (
            <div className="flex items-center justify-center gap-2 py-10 text-xs text-muted-foreground">
              <Spinner className="h-3.5 w-3.5" /> Loading runs…
            </div>
          )}

          {legacy && !loading && (
            <div className="flex flex-col items-center gap-2 py-10 text-center">
              <ScrollText className="h-4 w-4 text-muted-foreground-soft" />
              <p className="max-w-[380px] text-[11px] leading-relaxed text-muted-foreground-soft">
                This server records runs in the journal only, so a per-run list is not available.
                The workflows in the rail still cover everything that ran.
              </p>
            </div>
          )}

          {error && !loading && !legacy && (
            <div className="flex flex-col items-center gap-2 py-10 text-center">
              <p className="text-[11px] text-muted-foreground">Could not load the runs: {error}</p>
              <Button size="sm" variant="outline" onClick={() => void refresh()}>
                Try again
              </Button>
            </div>
          )}

          {!loading && !error && !legacy && runs.length === 0 && (
            <div className="flex flex-col items-center gap-1.5 py-10 text-center">
              <ScrollText className="h-4 w-4 text-muted-foreground-soft" />
              <p className="text-[11px] text-muted-foreground-soft">
                This routine has not run yet.
              </p>
            </div>
          )}

          {buckets.length > 0 && (
            <div className="flex flex-col gap-0.5">
              {buckets.map((b) => (
                <React.Fragment key={b.label}>
                  {/* The header is what makes thirty rows skippable: a reader
                      passes "12 runs · all ok" without reading twelve rows to
                      learn it, and stops at the one that says "2 failed". */}
                  <div className="mt-2 flex items-center gap-2 px-1.5 pb-1 pt-1 first:mt-0">
                    <span className="font-mono text-[10px] uppercase tracking-wider text-muted-foreground-soft">
                      {b.label}
                    </span>
                    <span
                      className="font-mono text-[10px]"
                      style={{ color: b.failed ? "var(--destructive)" : "var(--muted-foreground-soft)" }}
                    >
                      {b.summary}
                    </span>
                    <span aria-hidden className="h-px flex-1 bg-white/[0.06]" />
                  </div>

                  {b.runs.map((run) => {
                    const head = runHeadline(run, { medianMs: median })
                    return (
                      <button
                        key={run.id}
                        type="button"
                        onClick={() => onOpenRun(run.id)}
                        className="group grid grid-cols-[8px_64px_1fr_56px_16px] items-center gap-2.5 rounded-md px-1.5 py-1.5 text-left transition-colors hover:bg-white/[0.03] md:grid-cols-[8px_64px_1fr_56px_72px_16px]"
                      >
                        <span
                          aria-hidden
                          className="h-1.5 w-1.5 rounded-full"
                          style={{ background: `var(${TONE_TOKEN[head.tone] ?? "--muted-foreground"})` }}
                        />
                        {/* Absolute, to the second: two runs a minute apart are
                            told apart here and nowhere else on the row. */}
                        <span className="font-mono text-[11px] tabular-nums text-foreground/85">
                          {clockOf(run.started_at)}
                        </span>
                        <span
                          className="min-w-0 truncate text-[11.5px]"
                          style={{
                            color:
                              head.tone === "failed"
                                ? "var(--destructive)"
                                : head.tone === "slow"
                                  ? "var(--warn)"
                                  : "var(--muted-foreground)",
                          }}
                        >
                          {/* Output and error text are model- and author-written;
                              React escapes them and nothing here renders HTML. */}
                          {head.text || <span className="text-muted-foreground-soft">—</span>}
                        </span>
                        <span className="text-right font-mono text-[10.5px] tabular-nums text-muted-foreground">
                          {formatDurationMs(run.duration_ms)}
                        </span>
                        <span className="hidden font-mono text-[9.5px] uppercase tracking-wider text-muted-foreground-soft md:block">
                          {run.triggered_via ?? ""}
                        </span>
                        <ChevronRight className="h-3.5 w-3.5 text-muted-foreground-soft transition-colors group-hover:text-foreground" />
                      </button>
                    )
                  })}
                </React.Fragment>
              ))}
            </div>
          )}
        </DashboardCard>
      </Appear>
    </div>
  )
}

/**
 * The wall clock a run started at, to the second.
 *
 * To the second because a per-minute routine's runs differ only there once two
 * of them land in the same minute — which happens on every catch-up after a
 * restart. An unreadable stamp renders as a dash rather than as 1970.
 */
function clockOf(raw: string): string {
  const t = Date.parse(raw)
  if (Number.isNaN(t)) return "—"
  return new Date(t).toLocaleTimeString(undefined, { hour12: false })
}
