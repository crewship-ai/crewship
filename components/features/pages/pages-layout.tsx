"use client"

/**
 * The /pages shell — three zones, exactly as PRD §9b.1 draws them:
 *
 *   [icon rail]  [filter rail 280px]  [main: overview · or a single page]
 *
 * Both routes render this: `/pages` with no slug is the overview, and
 * `/pages/<slug>` is that page. One shell rather than two means the rail keeps
 * its search, its facets and its scroll position when you open a page, and
 * that the header line reads the same on both.
 *
 * The header line is the Routines/Credentials idiom (§9b.2): icon + name + `·`
 * + a dense count summary — `38 routines · 0 runs`, `12 secrets · 2 waiting on
 * a tool`, and here `12 pages · 3 stale`. When nothing on the wire carries a
 * freshness verdict the second clause is dropped rather than guessed: `—`
 * means no basis to compute, and claiming "all fresh" without one is the
 * silent-old-numbers failure §4 exists to prevent.
 */

import * as React from "react"
import { useRouter } from "next/navigation"

import { SubBar } from "@/components/layout/sub-bar"
import { SidebarCollapseButton, SIDEBAR_WIDTH } from "@/components/layout/sidebar-kit"
import { useIsMobile } from "@/hooks/use-mobile"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { cn } from "@/lib/utils"
import {
  matchesPageFilters,
  summarisePages,
  togglePageFilter,
  usePage,
  usePages,
  EMPTY_PAGE_FILTERS,
  type PageFilters,
} from "@/hooks/use-pages"
import type { PanelState } from "@/components/features/pages/panels/types"
import { PagesRail } from "@/components/features/pages/pages-rail"
import { PagesOverview } from "@/components/features/pages/pages-overview"
import { PageView } from "@/components/features/pages/page-view"

export interface PagesLayoutProps {
  workspaceId: string
  /** Set by /pages/[slug]; absent on the index. */
  slug?: string
  /** Injected clock for tests — absolute ages and "today" are computed. */
  now?: Date
}

export function PagesLayout({ workspaceId, slug, now }: PagesLayoutProps) {
  const router = useRouter()
  const isMobile = useIsMobile()
  const [collapsed, setCollapsed] = React.useState(false)
  // On a phone the rail is 280px of a 390px screen — it does not sit BESIDE
  // the content, it replaces it. Collapse it when the viewport narrows and let
  // it open as an overlay instead of a column.
  React.useEffect(() => {
    if (isMobile) setCollapsed(true)
  }, [isMobile])

  const [search, setSearch] = React.useState("")
  const [filters, setFilters] = React.useState<PageFilters>(EMPTY_PAGE_FILTERS)

  const { pages, loading, error } = usePages(workspaceId)
  const detail = usePage(workspaceId, slug ?? null)

  const summary = React.useMemo(() => summarisePages(pages, now), [pages, now])
  const filtered = React.useMemo(
    () => pages.filter((p) => matchesPageFilters(p, filters, search)),
    [pages, filters, search],
  )

  const openPage = React.useCallback(
    (next: string) => {
      // Picking a page on a phone means "show me that", and the overlay
      // covering it would be the opposite.
      if (isMobile) setCollapsed(true)
      router.push(`/pages/${encodeURIComponent(next)}`)
    },
    [isMobile, router],
  )

  const description = (
    <>
      {pages.length} {pages.length === 1 ? "page" : "pages"}
      {summary.hasFreshnessBasis
        ? summary.stalePages > 0
          ? ` · ${summary.stalePages} stale`
          : " · all fresh"
        : ""}
    </>
  )

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      <SubBar
        icon={CONCEPT_ICON.pages}
        title="Pages"
        description={description}
        ariaLabel="Pages"
      />

      <div className="relative flex flex-1 overflow-hidden">
        {isMobile && !collapsed && (
          <button
            type="button"
            aria-label="Close page list"
            onClick={() => setCollapsed(true)}
            className="absolute inset-0 z-20 bg-black/50"
          />
        )}
        <aside
          className={cn(
            "shrink-0 overflow-hidden border-r border-white/[0.06] bg-card transition-all print:hidden",
            collapsed ? "w-9" : SIDEBAR_WIDTH,
            isMobile && !collapsed && "absolute inset-y-0 left-0 z-30 shadow-2xl",
          )}
        >
          {collapsed ? (
            <div className="flex h-full flex-col items-center pt-1.5">
              <SidebarCollapseButton collapsed onToggle={() => setCollapsed(false)} />
            </div>
          ) : (
            <PagesRail
              pages={pages}
              search={search}
              onSearchChange={setSearch}
              filters={filters}
              onFiltersChange={setFilters}
              selectedSlug={slug ?? null}
              onSelectPage={openPage}
              onToggleCollapse={() => setCollapsed(true)}
            />
          )}
        </aside>

        <div className="relative flex-1 overflow-hidden bg-background">
          {slug ? (
            <PageView
              page={detail.page}
              slug={slug}
              loading={detail.loading}
              error={detail.error}
              notFound={detail.notFound}
              onBack={() => router.push("/pages")}
              now={now}
            />
          ) : (
            <PagesOverview
              pages={filtered}
              allPages={pages}
              loading={loading}
              error={error}
              onSelect={openPage}
              onFilterState={(state: PanelState) =>
                setFilters((f) => ({ ...f, states: togglePageFilter(f.states, state) }))
              }
              now={now}
            />
          )}
        </div>
      </div>
    </div>
  )
}
