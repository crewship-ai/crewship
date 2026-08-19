"use client"

/**
 * The /pages landing pane — PRD §9b.2, "the main pane mirrors the Routines
 * overview".
 *
 * Recognisably the same page as Routines' Overview: a band of four stat tiles,
 * then paired cards below. Nothing here is a new shell — `StatCard`
 * (`components/layout/stat-card.tsx`), `DashboardCard` and `EmptyState` are
 * the components Routines and Dashboard already use, so a card on this page
 * and a card on that one cannot drift.
 *
 * The card header idiom is copied exactly: small icon + uppercase tracked label
 * on the left, right-aligned muted status word on the right — and the
 * right-hand word is always the ANSWER (`all fresh`, `nothing pending`,
 * `no pushes yet`), never a repeat of the label.
 *
 * Type is the Pages register (`app/globals.css`): the row a card lists is
 * `.type-page-value`, everything qualifying it is `.type-page-meta`, and a
 * count or an instant that has to line up column-wise is `.type-page-stamp`.
 * The rows here were 12px with 10px metadata under them, which is the fine
 * print the register was declared to end.
 *
 * The em-dash rule (§9b.4) is load-bearing on the tiles. `0` is a measured
 * zero — we looked and there was nothing. `—` is *no basis to compute*, which
 * is what an index that told us nothing about freshness leaves us with. There
 * is no third glyph, and this file does not invent one.
 */

import * as React from "react"
import { AlertTriangle, CircleCheck, Clock, Gauge } from "lucide-react"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { EmptyState } from "@/components/layout/empty-state"
import { StatCard } from "@/components/layout/stat-card"
import { Skeleton } from "@/components/ui/skeleton"
import { Appear } from "@/components/ui/detail"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { cn } from "@/lib/utils"
import { EM_DASH, formatInstant } from "@/components/features/pages/panels/freshness"
import { PAGE_STATE_META, PAGE_STATE_ORDER } from "@/components/features/pages/page-state"
import { summarisePages, type PageView } from "@/hooks/use-pages"
import type { PanelState } from "@/components/features/pages/panels/types"

const RECENT_LIMIT = 8
const ATTENTION_LIMIT = 6

export interface PagesOverviewProps {
  pages: PageView[]
  /** The full, unfiltered set — the tiles describe the workspace, not the view. */
  allPages: PageView[]
  loading: boolean
  error: string | null
  onSelect: (slug: string) => void
  /** Click-through from the freshness card into the rail's STATUS facet. */
  onFilterState?: (state: PanelState) => void
  /** Injected clock, so "updated today" is deterministic in a test. */
  now?: Date
}

export function PagesOverview({
  pages,
  allPages,
  loading,
  error,
  onSelect,
  onFilterState,
  now,
}: PagesOverviewProps) {
  // One clock for the whole render. Deriving `new Date()` inside each helper
  // would let two tiles disagree about what "today" is across a midnight
  // boundary — a bug that appears once a day for one second.
  const [tick, setTick] = React.useState(() => now ?? new Date())
  React.useEffect(() => {
    if (now) return
    const t = setInterval(() => setTick(new Date()), 30_000)
    return () => clearInterval(t)
  }, [now])
  const clock = now ?? tick

  const summary = React.useMemo(() => summarisePages(allPages, clock), [allPages, clock])
  const totalPanels = React.useMemo(
    () => allPages.reduce((n, p) => n + p.tally.total, 0),
    [allPages],
  )
  const attention = React.useMemo(
    () =>
      pages
        .filter((p) => p.tally.failed > 0 || p.tally.never_produced > 0)
        .slice(0, ATTENTION_LIMIT),
    [pages],
  )
  const recent = React.useMemo(
    () =>
      pages
        .filter((p) => p.lastProducedAt != null)
        .sort((a, b) => (b.lastProducedAt!.getTime() - a.lastProducedAt!.getTime()))
        .slice(0, RECENT_LIMIT),
    [pages],
  )
  const stateRows = React.useMemo(
    () =>
      PAGE_STATE_ORDER.map((state) => ({
        state,
        meta: PAGE_STATE_META[state],
        pages: allPages.filter((p) => p.tally[state] > 0).length,
        panels: allPages.reduce((n, p) => n + p.tally[state], 0),
      })),
    [allPages],
  )

  if (loading && allPages.length === 0) return <OverviewSkeleton />

  // Nothing to look at is not a blank screen — it is the sentence that says
  // how to make the first page exist (§9b.3).
  if (!loading && allPages.length === 0 && !error) {
    return (
      <div className="h-full overflow-auto">
        <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
          <EmptyState
            icon={CONCEPT_ICON.pages}
            title="No pages yet"
            description="A page is a named set of panels a producer pushes to. Write the spec, create it with crewship page create --file page.yaml, then send a first payload with crewship page set <page>/<panel> --data -."
          />
        </div>
      </div>
    )
  }

  return (
    <div className="h-full overflow-auto">
      <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
        <Appear order={0}>
          <div>
            <h1 className="text-lg font-semibold tracking-tight">Overview</h1>
            <p className="text-xs text-muted-foreground">
              {allPages.length} {allPages.length === 1 ? "page" : "pages"} in this workspace
            </p>
          </div>
        </Appear>

        {error && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
            {error}
          </div>
        )}

        {/* ── The band of four (§9b.2) ─────────────────────────────── */}
        <Appear order={1}>
          <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
            <StatCard
              title="Pages"
              value={summary.total}
              subtitle={
                totalPanels === 0
                  ? "no panels declared"
                  : `${totalPanels} ${totalPanels === 1 ? "panel" : "panels"}`
              }
              icon={CONCEPT_ICON.pages}
            />
            <StatCard
              title="Stale now"
              // An index that carried no freshness cannot be reported as
              // "0 stale" — that would be the Pushgateway lie §4 exists to
              // reject. No basis to compute is an em dash.
              value={summary.hasFreshnessBasis ? summary.stalePages : EM_DASH}
              subtitle={
                !summary.hasFreshnessBasis
                  ? "freshness not reported"
                  : summary.stalePanels === 0
                    ? "all within SLA"
                    : `${summary.stalePanels} ${summary.stalePanels === 1 ? "panel" : "panels"} past SLA`
              }
              icon={Clock}
              iconClassName={summary.stalePages > 0 ? "bg-warn/15 text-warn" : undefined}
            />
            <StatCard
              title="Updated today"
              value={summary.hasFreshnessBasis ? summary.updatedToday : EM_DASH}
              subtitle={
                !summary.hasFreshnessBasis
                  ? "no push timestamps"
                  : summary.updatedToday === 0
                    ? "nothing pushed today"
                    : "received a payload"
              }
              icon={CircleCheck}
            />
            <StatCard
              title="Needs attention"
              value={summary.hasFreshnessBasis ? summary.needsAttention : EM_DASH}
              subtitle={
                !summary.hasFreshnessBasis
                  ? "freshness not reported"
                  : summary.needsAttention === 0
                    ? "all clean"
                    : [
                        summary.failedPanels > 0 ? `${summary.failedPanels} failed` : null,
                        summary.neverProducedPanels > 0
                          ? `${summary.neverProducedPanels} never produced`
                          : null,
                      ]
                        .filter(Boolean)
                        .join(" · ")
              }
              icon={AlertTriangle}
              iconClassName={summary.needsAttention > 0 ? "bg-destructive/15 text-destructive" : undefined}
            />
          </div>
        </Appear>

        {/* ── What state the workspace is in, and what is broken ───── */}
        <Appear order={2}>
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <DashboardCard
              data-card="freshness"
              title="Freshness"
              icon={Gauge}
              hint={
                !summary.hasFreshnessBasis
                  ? EM_DASH
                  : summary.stalePages > 0
                    ? `${summary.stalePages} stale`
                    : "all fresh"
              }
            >
              {!summary.hasFreshnessBasis ? (
                <EmptyState
                  size="inline"
                  icon={Gauge}
                  title="No freshness to report"
                  description="No panel has a server-computed state yet. Push a payload to a panel and its page will start reporting fresh or stale here."
                />
              ) : (
                <div className="flex flex-col">
                  {stateRows.map((row) => {
                    const Icon = row.meta.icon
                    const empty = row.pages === 0
                    return (
                      <button
                        key={row.state}
                        type="button"
                        disabled={empty || !onFilterState}
                        onClick={() => onFilterState?.(row.state)}
                        className={cn(
                          "group flex items-center gap-2.5 rounded-md px-1.5 py-2 text-left transition-colors",
                          !empty && onFilterState && "hover:bg-white/[0.03]",
                          empty && "cursor-default",
                        )}
                      >
                        <Icon
                          className={cn("h-3.5 w-3.5 shrink-0", row.meta.tone, empty && "opacity-40")}
                        />
                        <span
                          className={cn(
                            "type-page-value min-w-0 flex-1 truncate",
                            empty ? "text-foreground/40" : "text-foreground/85",
                          )}
                        >
                          {row.meta.label}
                        </span>
                        <span className="type-page-meta shrink-0 text-muted-foreground-soft tabular-nums">
                          {row.panels} {row.panels === 1 ? "panel" : "panels"}
                        </span>
                        <span
                          className={cn(
                            "type-page-stamp w-8 shrink-0 text-right tabular-nums",
                            empty ? "text-muted-foreground-soft/50" : "text-foreground/85",
                          )}
                        >
                          {row.pages}
                        </span>
                      </button>
                    )
                  })}
                </div>
              )}
            </DashboardCard>

            {/* Deliberately NOT called "Needs attention": that is the tile
                above, and a card repeating a tile's label makes the reader
                check whether the two numbers are the same thing. This card is
                the list behind the number. */}
            <DashboardCard
              data-card="not-reporting"
              title="Not reporting"
              icon={AlertTriangle}
              hint={attention.length > 0 ? `${attention.length} pages` : "nothing pending"}
            >
              {attention.length === 0 ? (
                <EmptyState
                  size="inline"
                  icon={CircleCheck}
                  title="Nothing is broken"
                  description="No panel has failed and none is still waiting for its first payload. A page that stops updating opens an issue on its owning crew, so this card is the short version."
                />
              ) : (
                <div className="flex flex-col">
                  {attention.map((p) => (
                    <button
                      key={p.id || p.slug}
                      type="button"
                      onClick={() => onSelect(p.slug)}
                      className="group flex items-center gap-2.5 rounded-md px-1.5 py-2 text-left transition-colors hover:bg-white/[0.03]"
                    >
                      <span
                        aria-hidden
                        className={cn(
                          "h-2 w-2 shrink-0 rounded-full",
                          p.tally.failed > 0 ? "bg-destructive" : "bg-muted-foreground/40",
                        )}
                      />
                      <span className="min-w-0 flex-1">
                        <span className="type-page-value block truncate text-foreground/90">
                          {p.name}
                        </span>
                        <span className="type-page-meta block truncate text-muted-foreground">
                          {[
                            p.tally.failed > 0 ? `${p.tally.failed} failed` : null,
                            p.tally.never_produced > 0
                              ? `${p.tally.never_produced} never produced`
                              : null,
                          ]
                            .filter(Boolean)
                            .join(" · ")}
                        </span>
                      </span>
                      {p.ownerLabel && (
                        <span className="type-page-meta shrink-0 text-muted-foreground-soft">
                          {p.ownerLabel}
                        </span>
                      )}
                    </button>
                  ))}
                </div>
              )}
            </DashboardCard>
          </div>
        </Appear>

        {/* ── What arrived, and when exactly ───────────────────────── */}
        <Appear order={3}>
          <DashboardCard
            data-card="recently-updated"
            title="Recently updated"
            icon={Clock}
            hint={recent.length > 0 ? `last ${recent.length}` : "no pushes yet"}
          >
            {recent.length === 0 ? (
              <EmptyState
                size="inline"
                icon={Clock}
                title="Nothing has been pushed yet"
                description="Open a page on the left and run its producer, or send a payload yourself with crewship page set <page>/<panel> --data -."
              />
            ) : (
              <div className="flex flex-col">
                {recent.map((p) => {
                  const meta = p.state ? PAGE_STATE_META[p.state] : null
                  const Icon = meta?.icon ?? CONCEPT_ICON.pages
                  return (
                    <button
                      key={p.id || p.slug}
                      type="button"
                      onClick={() => onSelect(p.slug)}
                      className="group flex items-center gap-2.5 rounded-md px-1.5 py-2 text-left transition-colors hover:bg-white/[0.03]"
                    >
                      <Icon
                        className={cn(
                          "h-4 w-4 shrink-0",
                          meta?.tone ?? "text-muted-foreground-soft",
                        )}
                        aria-hidden
                      />
                      <span className="type-page-value min-w-0 flex-1 truncate text-foreground/90">
                        {p.name}
                      </span>
                      <span className="type-page-meta hidden shrink-0 text-muted-foreground sm:block">
                        {p.tally.total} {p.tally.total === 1 ? "panel" : "panels"}
                      </span>
                      {/* Absolute, never "a while ago" (§4 rule 3). */}
                      <span className="type-page-stamp shrink-0 text-right tabular-nums text-muted-foreground-soft">
                        {formatInstant(p.lastProducedAt!, clock)}
                      </span>
                    </button>
                  )
                })}
              </div>
            )}
          </DashboardCard>
        </Appear>
      </div>
    </div>
  )
}

/**
 * Skeleton in the final geometry — placeholders that do not match what
 * replaces them make the page reflow on load, which reads as a second,
 * unexplained render.
 */
function OverviewSkeleton() {
  return (
    <div className="h-full overflow-auto">
      <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
        <Skeleton className="h-9 w-48" />
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-[104px] rounded-xl" />
          ))}
        </div>
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <Skeleton className="h-[228px] rounded-xl" />
          <Skeleton className="h-[228px] rounded-xl" />
        </div>
        <Skeleton className="h-[240px] rounded-xl" />
      </div>
    </div>
  )
}
