"use client"

/**
 * The Dashboard's PAGES strip — PRD §10b.5d.
 *
 * *"Pages earns a strip on the Dashboard — `DashboardCard` titled PAGES,
 * listing the most recently updated pages the viewer may see, with the stale
 * count as the right-hand status word (`3 stale` / `all fresh`, per the idiom
 * in §9b.2). It is the cheapest way for the feature to find people's hands,
 * and it is a read-only view over data the page index already returns."*
 *
 * Three things this file is careful about:
 *
 *  1. **No new fetch.** `usePages` (hooks/use-pages.ts) is the same read hook
 *     the /pages surface uses, so this strip and that surface can never
 *     disagree about what "3 stale" means — there is one normaliser, one
 *     `summarisePages`, used in both places.
 *  2. **The em-dash rule (§9b.4) is load-bearing on the hint.** An index that
 *     sent a panel count and no freshness rollup leaves `summarisePages`
 *     with `hasFreshnessBasis: false` — the strip renders `—`, never
 *     `0 stale` / `all fresh`, both of which would be claims this build has
 *     no basis for.
 *  3. **Empty state names the next action (§9b.3)**, not a blank card. Two
 *     distinct cases get two distinct sentences, because the correct next
 *     step is different: a workspace with zero pages needs one *created*; a
 *     workspace whose pages have never received data needs a producer *run*.
 */

import * as React from "react"
import Link from "next/link"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { EmptyState } from "@/components/layout/empty-state"
import { Skeleton } from "@/components/ui/skeleton"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { usePages, summarisePages } from "@/hooks/use-pages"
import { PAGE_STATE_META } from "@/components/features/pages/page-state"
import { EM_DASH, formatInstant } from "@/components/features/pages/panels/freshness"

const STRIP_LIMIT = 5

export interface PagesStripProps {
  /** Injected clock — tests pin "now" so an absolute age is deterministic. */
  now?: Date
}

export function PagesStrip({ now }: PagesStripProps) {
  const { workspaceId } = useWorkspace()
  const { pages, loading, error } = usePages(workspaceId)

  // One clock for the whole render, ticking on its own timer rather than
  // being re-derived on every render — otherwise "3 stale" and the row ages
  // could each read a different instant within the same paint. Same
  // rationale as `PagesOverview`.
  const [tick, setTick] = React.useState(() => now ?? new Date())
  React.useEffect(() => {
    if (now) return
    const t = setInterval(() => setTick(new Date()), 30_000)
    return () => clearInterval(t)
  }, [now])
  const clock = now ?? tick

  const summary = React.useMemo(() => summarisePages(pages, clock), [pages, clock])

  // Most recently updated, viewer-visible pages — `usePages` already scopes
  // to what the caller may see (§7.1), so nothing here re-derives visibility.
  const recent = React.useMemo(
    () =>
      pages
        .filter((p) => p.lastProducedAt != null)
        .sort((a, b) => b.lastProducedAt!.getTime() - a.lastProducedAt!.getTime())
        .slice(0, STRIP_LIMIT),
    [pages],
  )

  // The right-hand answer word (§9b.2): never a repeat of "Pages", always
  // what the reader actually wants to know. `—` when the index sent no
  // freshness rollup at all — a measured `0` and "no basis to compute" are
  // different claims (§9b.4).
  const hint = loading && pages.length === 0
    ? undefined
    : !summary.hasFreshnessBasis
      ? EM_DASH
      : summary.stalePages > 0
        ? `${summary.stalePages} stale`
        : "all fresh"

  return (
    <DashboardCard title="Pages" icon={CONCEPT_ICON.pages} hint={hint}>
      {loading && pages.length === 0 ? (
        <PagesStripSkeleton />
      ) : error ? (
        <div className="flex items-center justify-center h-[120px] text-[11px] text-destructive">
          {error}
        </div>
      ) : recent.length === 0 ? (
        <EmptyState
          size="inline"
          icon={CONCEPT_ICON.pages}
          title={pages.length === 0 ? "No pages yet" : "Nothing has run yet"}
          description={
            pages.length === 0
              ? "A page is a named set of panels a producer pushes to. Create one with crewship page create --file page.yaml, then give it a first payload."
              : "No page has received a payload yet. Open Pages and run a producer, or push one with crewship page set <page>/<panel> --data -."
          }
        />
      ) : (
        <div className="flex flex-col">
          {recent.map((p) => {
            const meta = p.state ? PAGE_STATE_META[p.state] : null
            const Icon = meta?.icon ?? CONCEPT_ICON.pages
            return (
              <Link
                key={p.id || p.slug}
                href={`/pages/${encodeURIComponent(p.slug)}`}
                className="group flex items-center gap-2.5 rounded-md px-1.5 py-2 text-left transition-colors hover:bg-white/[0.03]"
              >
                <Icon
                  className={cn("h-3.5 w-3.5 shrink-0", meta?.tone ?? "text-muted-foreground-soft")}
                  aria-hidden="true"
                />
                <span className="min-w-0 flex-1 truncate text-[12px] text-foreground/90">
                  {p.name}
                </span>
                {p.ownerLabel && (
                  <span className="hidden shrink-0 text-[10px] text-muted-foreground sm:block">
                    {p.ownerLabel}
                  </span>
                )}
                {/* Absolute, never "a while ago" (§4 rule 3). */}
                <span className="shrink-0 text-right font-mono text-[11px] tabular-nums text-muted-foreground-soft">
                  {formatInstant(p.lastProducedAt!, clock)}
                </span>
              </Link>
            )
          })}
        </div>
      )}
    </DashboardCard>
  )
}

/** Skeleton in the strip's final geometry — same rationale as the overview's:
 * a placeholder that does not match what replaces it makes the dashboard
 * reflow once the query resolves. */
function PagesStripSkeleton() {
  return (
    <div className="flex flex-col gap-2">
      {Array.from({ length: 3 }, (_, i) => (
        <Skeleton key={i} className="h-8 w-full rounded-md" />
      ))}
    </div>
  )
}
