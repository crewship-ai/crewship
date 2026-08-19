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
 *
 * The shell also owns the two AUTHORING affordances (§10b.1): New page, and
 * Edit on the page you are looking at. Both open the same YAML editor on the
 * same document — the third door beside the CLI and an agent — and the Edit
 * one lives here rather than in `page-view.tsx` because the header is the one
 * place both routes already share.
 */

import * as React from "react"
import { useRouter } from "next/navigation"
import { FilePlus2, Pencil, SlidersHorizontal } from "lucide-react"

import { SubBar, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
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
import { PageEditor, sealedPanelCount, type PageEditorMode } from "@/components/features/pages/page-editor"
import { PageSettings } from "@/components/features/pages/page-settings"

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

  // ── Authoring (§10b.1) ───────────────────────────────────────────────────
  const [editor, setEditor] = React.useState<PageEditorMode | null>(null)
  // The panel owner most of this workspace already uses, so the template's
  // first placeholder is one someone actually has. Only a `crew/` reference —
  // a panel's permission anchor is always a crew (§7.1).
  const suggestedOwner = React.useMemo(
    () => pages.find((p) => p.ownerRef?.startsWith("crew/"))?.ownerRef ?? null,
    [pages],
  )
  // A page carrying a panel this viewer may not see cannot be edited from a
  // document, because the document has no way to say "and one more I am not
  // allowed to describe" — saving it would delete that panel (§11b.14).
  const sealed = sealedPanelCount(detail.raw)
  const canEdit = Boolean(slug) && detail.page != null && sealed === 0

  // ── Settings (§7.1b, §10b.1) ─────────────────────────────────────────────
  // Who reaches this page, and what this page is. It sits beside Edit rather
  // than inside it: the editor owns one document, and grants and versions are
  // rows in two other tables no document can express. Unlike Edit it is NOT
  // gated on `sealed` — reading an ACL and rolling back a spec are exactly the
  // things an owner of a partially-sealed page still needs, and the server
  // gates both itself (§7.1 rule 3, and the version route's own refusal).
  const [settingsOpen, setSettingsOpen] = React.useState(false)
  // A slug change means a different page; a settings sheet left open over it
  // would be showing another page's ACL.
  React.useEffect(() => setSettingsOpen(false), [slug])

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      <SubBar
        icon={CONCEPT_ICON.pages}
        title="Pages"
        description={description}
        ariaLabel="Pages"
        actions={
          <>
            {slug && (
              <SubBarSecondary
                icon={SlidersHorizontal}
                onClick={() => setSettingsOpen(true)}
                disabled={detail.page == null}
                title="Who reaches this page, and what it is"
              >
                Settings
              </SubBarSecondary>
            )}
            {slug && (
              <SubBarSecondary
                icon={Pencil}
                onClick={() => setEditor("edit")}
                disabled={!canEdit}
                title={
                  sealed > 0
                    ? "This page has panels you may not see; editing it here would delete them."
                    : "Edit this page's YAML"
                }
              >
                Edit
              </SubBarSecondary>
            )}
            <SubBarPrimary icon={FilePlus2} onClick={() => setEditor("create")}>
              New page
            </SubBarPrimary>
          </>
        }
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
              onCreatePage={() => setEditor("create")}
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
              // Straight from the subscription `usePage` registered — the
              // header indicator is lit only while this page's channel is on a
              // live socket, never on a timer of its own (epic #1935).
              live={detail.live}
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

      {settingsOpen && slug && (
        <PageSettings
          workspaceId={workspaceId}
          slug={slug}
          page={detail.raw}
          onClose={() => setSettingsOpen(false)}
        />
      )}

      {editor && (
        <PageEditor
          workspaceId={workspaceId}
          mode={editor}
          page={editor === "edit" ? detail.raw : null}
          defaultOwner={suggestedOwner}
          onClose={() => setEditor(null)}
          // A created page is one you want to look at; an edited one you are
          // already looking at, and the invalidation the mutation performed
          // has already refetched it.
          onSaved={(saved) => {
            if (editor === "create" && saved && saved !== slug) openPage(saved)
          }}
        />
      )}
    </div>
  )
}
