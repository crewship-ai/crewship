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

import { DashCard, DashLine, DashNote } from "@/components/features/dashboard/dash-card"
import { ConceptIcon } from "@/components/ui/concept-icon"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusPill } from "@/components/ui/status-pill"
import type { StatusTone } from "@/lib/format-status"
import { useWorkspace } from "@/hooks/use-workspace"
import { usePages, summarisePages } from "@/hooks/use-pages"
import { PAGE_STATE_META } from "@/components/features/pages/page-state"
import type { PanelState } from "@/components/features/pages/panels/types"
import { EM_DASH, formatInstant } from "@/components/features/pages/panels/freshness"

/** Three rows: the card is a glance, the full list is one click away. */
const STRIP_LIMIT = 3

const STATE_TONE: Record<PanelState, StatusTone> = {
  fresh: "success",
  stale: "warn",
  failed: "danger",
  never_produced: "muted",
}

export interface PagesStripProps {
  /** Injected clock — tests pin "now" so an absolute age is deterministic. */
  now?: Date
}

export function PagesStrip({ now }: PagesStripProps) {
  const { workspaceId } = useWorkspace()
  const { pages, loading, error } = usePages(workspaceId)

  // One clock for the whole render, ticking on its own timer rather than
  // being re-derived on every render — otherwise "3 stale" and the row ages
  // could each read a different instant within the same paint.
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

  // The answer word (§9b.2): `—` when the index sent no freshness rollup at
  // all — a measured `0` and "no basis to compute" are different claims.
  const hint = loading && pages.length === 0
    ? undefined
    : !summary.hasFreshnessBasis
      ? EM_DASH
      : summary.stalePages > 0
        ? `${summary.stalePages} stale`
        : "all fresh"

  return (
    <DashCard title="Artifacts" concept="pages" meta={hint} link={{ label: "All artifacts", href: "/pages" }}>
      {loading && pages.length === 0 ? (
        <div className="space-y-2 border-t border-border px-4 py-3">
          {Array.from({ length: 3 }, (_, i) => <Skeleton key={i} className="h-8 w-full rounded-md" />)}
        </div>
      ) : error ? (
        <DashNote tone="error">{error}</DashNote>
      ) : recent.length === 0 ? (
        pages.length === 0 ? (
          <DashNote>
            <b className="font-medium text-foreground">No pages yet</b> · A page is a named set of panels a producer pushes to. Create one with <code className="font-mono">crewship page create --file page.yaml</code>, then give it a first payload.
          </DashNote>
        ) : (
          <DashNote>
            <b className="font-medium text-foreground">Nothing has run yet</b> · No page has received a payload yet. Open Artifacts and run a producer, or push one with <code className="font-mono">crewship page set &lt;page&gt;/&lt;panel&gt; --data -</code>.
          </DashNote>
        )
      ) : (
        <ul aria-label="Recently updated pages">
          {recent.map((p) => (
            <li key={p.id || p.slug}>
              <DashLine
                lead={<ConceptIcon concept="pages" variant="chip" className="h-7 w-7 rounded-md" />}
                title={p.name}
                // Absolute, never "a while ago" (§4 rule 3).
                sub={[p.ownerLabel, `updated ${formatInstant(p.lastProducedAt!, clock)}`].filter(Boolean).join(" · ")}
                href={`/pages/${encodeURIComponent(p.slug)}`}
                // No pill when the server sent no state: an unknown state is
                // never shown as fresh.
                right={p.state ? <StatusPill tone={STATE_TONE[p.state]} label={PAGE_STATE_META[p.state].label} /> : undefined}
              />
            </li>
          ))}
        </ul>
      )}
    </DashCard>
  )
}
