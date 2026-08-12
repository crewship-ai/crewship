"use client"

/**
 * One page — the panel grid (PRD §9).
 *
 * | Concern | Choice                                                      |
 * |---------|-------------------------------------------------------------|
 * | Layout  | CSS Grid, `col-span-n` from the spec's `span`                |
 * | Reflow  | `@container` queries on the panel's own box, not the viewport|
 * | Mobile  | single column below the tablet breakpoint                    |
 * | Dispatch| `PanelRenderer` — validate, look up the closed enum, render   |
 *
 * Cost: 0 KB. No chart library, no grid library, no new dependency.
 *
 * Two details that are easy to get wrong and are deliberate here:
 *
 *  · **The span classes are static strings.** `col-span-${n}` is invisible to
 *    Tailwind's scanner and would ship a grid with no spans at all.
 *
 *  · **The single-column case is `grid-cols-1` plus `md:col-span-n`, never a
 *    bare `col-span-n`.** In a one-track grid, `grid-column: span 8` does not
 *    clamp — it creates seven implicit columns and the page scrolls sideways
 *    on a phone, which is the opposite of "mobile and tablet first".
 *
 * Each cell is a named `@container/panel`, which is what the panels' own
 * `@md/panel:` rules resolve against: a panel in a `span: 4` cell collapses
 * its table to a card list while the `span: 12` panel beside it keeps the
 * table, at one viewport width. That is the whole point of §9's container
 * queries.
 */

import * as React from "react"
import { ChevronLeft, ChevronRight } from "lucide-react"

import { EmptyState } from "@/components/layout/empty-state"
import { Skeleton } from "@/components/ui/skeleton"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { cn } from "@/lib/utils"
import { PanelRenderer } from "@/components/features/pages/panels"
import { PAGE_STATE_META } from "@/components/features/pages/page-state"
import type { PageView as PageRecord } from "@/hooks/use-pages"

/**
 * `span` → grid class. A literal map, because Tailwind reads source text: a
 * template literal here produces a page whose every panel is full width.
 */
const SPAN_CLASS: Record<number, string> = {
  1: "md:col-span-1",
  2: "md:col-span-2",
  3: "md:col-span-3",
  4: "md:col-span-4",
  5: "md:col-span-5",
  6: "md:col-span-6",
  7: "md:col-span-7",
  8: "md:col-span-8",
  9: "md:col-span-9",
  10: "md:col-span-10",
  11: "md:col-span-11",
  12: "md:col-span-12",
}

export function spanClass(span: number | undefined): string {
  const n = Math.min(12, Math.max(1, Math.trunc(span ?? 12) || 12))
  return SPAN_CLASS[n]
}

export interface PageViewProps {
  page: PageRecord | null
  slug: string
  loading: boolean
  error: string | null
  notFound: boolean
  onBack: () => void
  /** Injected clock — absolute ages are computed, so a test can pin `now`. */
  now?: Date
}

export function PageView({ page, slug, loading, error, notFound, onBack, now }: PageViewProps) {
  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Breadcrumb back-bar — inside the content area, matching /routines and
          /issues, so it does not compete with the global sub-bar above. */}
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-card/40 px-4 py-2 print:hidden">
        <button
          type="button"
          onClick={onBack}
          className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          <ChevronLeft className="h-3.5 w-3.5" />
          Back to pages
        </button>
        <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
        <span className="truncate text-xs font-medium text-foreground/85" title={page?.name ?? slug}>
          {page?.name ?? slug}
        </span>
        <span className="ml-1 truncate font-mono text-[11px] text-muted-foreground">{slug}</span>
        {page?.ownerLabel && (
          <span className="ml-auto shrink-0 text-[11px] text-muted-foreground-soft">
            {page.ownerLabel}
          </span>
        )}
      </div>

      <div className="flex-1 overflow-auto">
        <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
          <PageBody
            page={page}
            slug={slug}
            loading={loading}
            error={error}
            notFound={notFound}
            now={now}
          />
        </div>
      </div>
    </div>
  )
}

function PageBody({
  page,
  slug,
  loading,
  error,
  notFound,
  now,
}: Omit<PageViewProps, "onBack">) {
  if (notFound) {
    return (
      <EmptyState
        icon={CONCEPT_ICON.pages}
        title={`No page is addressed "${slug}"`}
        description="It may have been deleted, renamed, or it belongs to a crew you are not in. Pick a page from the list on the left, or run crewship page list to see what exists."
      />
    )
  }

  if (error) {
    return (
      <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
        {error}
      </div>
    )
  }

  if (loading && !page) {
    return (
      <div className="grid grid-cols-1 gap-4 md:grid-cols-12">
        <Skeleton className="h-[180px] rounded-xl md:col-span-6" />
        <Skeleton className="h-[180px] rounded-xl md:col-span-6" />
        <Skeleton className="h-[220px] rounded-xl md:col-span-12" />
      </div>
    )
  }

  if (!page) return null

  const panels = page.panels ?? []
  if (panels.length === 0) {
    return (
      <EmptyState
        icon={CONCEPT_ICON.pages}
        title="This page declares no panels"
        description={`A page with no panel has nothing to render and nothing to push to. Add a panel to the spec and save it with crewship page update ${slug} --file page.yaml.`}
      />
    )
  }

  const meta = page.state ? PAGE_STATE_META[page.state] : null

  return (
    <>
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <h1 className="truncate text-lg font-semibold tracking-tight">{page.name}</h1>
          <p className="text-xs text-muted-foreground">
            {panels.length} {panels.length === 1 ? "panel" : "panels"}
            {page.description ? ` · ${page.description}` : ""}
          </p>
        </div>
        {meta && (
          <span
            className={cn(
              "shrink-0 whitespace-nowrap text-[11px] uppercase tracking-wider",
              meta.tone,
            )}
          >
            {meta.label}
          </span>
        )}
      </div>

      <div
        data-slot="panel-grid"
        className="grid grid-cols-1 gap-4 md:grid-cols-12 print:grid-cols-1"
      >
        {panels.map((panel) => (
          <div
            key={panel.spec.id}
            data-slot="panel-cell"
            data-panel-span={panel.spec.span}
            // min-w-0 so a wide table inside a narrow cell scrolls in its own
            // box instead of stretching the track and shoving its neighbours
            // off the grid.
            className={cn(
              "@container/panel min-w-0 print:break-inside-avoid",
              spanClass(panel.spec.span),
            )}
          >
            <PanelRenderer panel={panel.spec} data={panel.snapshot} now={now} />
          </div>
        ))}
      </div>
    </>
  )
}
