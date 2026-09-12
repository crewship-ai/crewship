"use client"

import { PageApplicationView } from "./page-application"

/**
 * The /pages shell — three zones, exactly as PRD §9b.1 draws them:
 *
 *   [icon rail]  [filter rail 280px]  [main: overview · one page · the editor]
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
 * ── The third zone has two modes now ────────────────────────────────────────
 *
 * This bar used to carry five separate doors into one job: Settings, Edit, App
 * preview, Source history and Publications, side by side, three of them behind
 * the same icon. None of them was the place a person actually works, and for a
 * Page with a custom application the real work — deciding whether an agent's
 * change goes live — had no screen at all.
 *
 * So there is one door, `Edit`, and it opens a routed editor in the content
 * column: four named sections (Content / Data & actions / Access / History)
 * with the Pages list still beside them on the desktop. Everything the five
 * buttons reached is inside those sections; nothing was dropped, and the
 * address carries the section so a reload, Back, Forward and a pasted link all
 * land where they should.
 */

import * as React from "react"
import { AnimatePresence, motion } from "motion/react"

import { duration } from "@/lib/motion"
import { FilePlus2, Pencil, Share2, Upload } from "lucide-react"

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
import { PageEditor } from "@/components/features/pages/page-editor"
import { PageImportDialog } from "@/components/features/pages/page-import-dialog"
import { useEditorRoute } from "@/components/features/pages/editor/use-editor-route"
import { usePageCapabilities } from "@/components/features/pages/editor/use-page-capabilities"
import { usePageGrants } from "@/hooks/use-page-grants"
import { PageEditorShell } from "@/components/features/pages/editor/page-editor-shell"

export interface PagesLayoutProps {
  workspaceId: string
  /** Set by /pages/[slug]; absent on the index. */
  slug?: string
  /** Injected clock for tests — absolute ages and "today" are computed. */
  now?: Date
}

export function PagesLayout({ workspaceId, slug, now }: PagesLayoutProps) {
  const isMobile = useIsMobile()

  // WHICH PAGE IS OPEN, WHETHER THE EDITOR IS OPEN, AND WHICH SECTION IT SHOWS
  // ARE ONE PIECE OF STATE, AND THE ADDRESS BAR IS ITS MIRROR.
  //
  // `slug` still arrives as a prop, because /pages/[slug] is a real route and
  // has to keep working: a deep link, a refresh, a bookmark and a shared URL
  // all enter that way. What a CLICK does is different — routing to
  // /pages/<slug> made Next unmount this whole subtree and build it again, so
  // the rail blinked out and came back on every selection, taking its scroll
  // position and its filter state with it.
  //
  // `useEditorRoute` owns that: it writes the address with history.pushState,
  // reads it back on popstate, and is the single place that asks the
  // unsaved-work question before the address is allowed to move.
  const nav = useEditorRoute(slug ?? null)
  const selectedSlug = nav.slug

  const [collapsed, setCollapsed] = React.useState(false)
  // On a phone the rail is 280px of a 390px screen — it does not sit BESIDE
  // the content, it replaces it. Collapse it when the viewport narrows and let
  // it open as an overlay instead of a column.
  React.useEffect(() => {
    if (isMobile) setCollapsed(true)
  }, [isMobile])

  const [importing, setImporting] = React.useState(false)
  const [creating, setCreating] = React.useState(false)
  const [search, setSearch] = React.useState("")
  const [filters, setFilters] = React.useState<PageFilters>(EMPTY_PAGE_FILTERS)

  const { pages, loading, error } = usePages(workspaceId)
  const detail = usePage(workspaceId, selectedSlug)

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
      nav.openPage(next)
    },
    [isMobile, nav],
  )

  const closePage = React.useCallback(() => nav.openPage(null), [nav])

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
  // The panel owner most of this workspace already uses, so the template's
  // first placeholder is one someone actually has. Only a `crew/` reference —
  // a panel's permission anchor is always a crew (§7.1).
  const suggestedOwner = React.useMemo(
    () => pages.find((p) => p.ownerRef?.startsWith("crew/"))?.ownerRef ?? null,
    [pages],
  )

  // Per-section, not one boolean for the whole surface. The old gate hid Edit,
  // App preview, Source history and Publications together the moment a Page
  // carried one panel this viewer may not see — three of which the server
  // would have answered (V01). What a sealed panel actually makes unsafe is
  // replacing the DOCUMENT, and that is now the only thing it closes.
  // `?mode=edit` on a slug that does not exist used to open the whole editor:
  // the buttons were gated on the record, the address was not, so Access
  // rendered its grant and mint forms and fired reads against a Page that is
  // not there. The address is not allowed to reach a state the data does not
  // support.
  const editing = nav.mode === "edit" && selectedSlug != null && detail.page != null

  // One capability we can lower honestly without a second request: React
  // Query shares this key with the Access section's own read, so asking here
  // costs nothing extra, and only while the editor is open — a Page being
  // looked at has no reason to fetch its ACL. The rest stay optimistic and
  // the server's refusal renders at the control, which is the rule: a
  // refusal must be visible where the action was, not turned into an absence
  // that reads as "this product cannot do that".
  const grants = usePageGrants(workspaceId, selectedSlug, editing)
  const capabilities = usePageCapabilities(detail.raw, {
    mayManageAccess: grants.refusal === null,
  })

  // Closing the editor unmounts the control that had focus, and a keyboard
  // user was landing on `<body>`. This component renders both halves, so it
  // is the one that can hand focus from one to the other; the view's heading
  // is addressed by a ref rather than an id, because two page views can share
  // a document and a fixed id collides.
  // A pending request rather than a timed one. The first attempt focused on
  // the next animation frame and a live pass found it still landing on
  // `<body>`: `AnimatePresence mode="wait"` holds the incoming view until the
  // outgoing one has finished leaving, so the heading does not exist yet.
  // Guessing a longer delay would only move the race. The callback ref fires
  // when the node actually attaches, which is the moment that matters.
  const wantsFocus = React.useRef(false)
  const viewHeading = React.useCallback((node: HTMLHeadingElement | null) => {
    if (node === null || !wantsFocus.current) return
    wantsFocus.current = false
    node.focus()
  }, [])
  const focusTheView = React.useCallback(() => {
    wantsFocus.current = true
  }, [])

  return (
    <div className="flex h-[calc(100dvh-48px)] flex-col bg-background">
      <SubBar
        icon={CONCEPT_ICON.pages}
        title="Pages"
        section={editing ? "Edit" : undefined}
        description={description}
        ariaLabel="Pages"
        actions={
          <>
            {selectedSlug && !editing && (
              <SubBarSecondary
                icon={Share2}
                onClick={() => nav.openEditor("access")}
                disabled={detail.page == null}
                title="Who reaches this Page, who may send it data, and its public links"
              >
                Share
              </SubBarSecondary>
            )}
            {selectedSlug && !editing && (
              <SubBarSecondary
                icon={Pencil}
                onClick={() => nav.openEditor()}
                disabled={detail.page == null}
                title="Edit this Page's content, data, access and history"
              >
                Edit
              </SubBarSecondary>
            )}
            {/* Import sits beside New page because they are the same intent —
                "a page that is not here yet" — and it was the one authoring
                door that existed only as a CLI command. */}
            <SubBarSecondary icon={Upload} onClick={() => setImporting(true)}>
              Import
            </SubBarSecondary>
            <SubBarPrimary icon={FilePlus2} onClick={() => setCreating(true)}>
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
            className="fixed inset-0 z-40 bg-black/50 touch-none overscroll-contain"
          />
        )}
        {/* The list stays. Replacing it with the editor's sections was the
            first draft's riskiest idea and the review's U05: the promise that
            a list restores its scroll and filters afterwards is the promise
            that breaks. On a phone it is already an overlay, so the editor
            gets the full width there without anyone deciding it should. */}
        <aside
          className={cn(
            "shrink-0 overflow-hidden border-r border-white/[0.06] bg-card transition-all print:hidden",
            collapsed ? "w-9" : SIDEBAR_WIDTH,
            isMobile && !collapsed && "absolute inset-y-0 left-0 z-50 shadow-2xl",
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
              selectedSlug={selectedSlug}
              onSelectPage={openPage}
              onCreatePage={() => setCreating(true)}
              onToggleCollapse={() => setCollapsed(true)}
            />
          )}
        </aside>

        <div className="relative flex-1 overflow-hidden bg-background">
          {/* The same swap /routines does, with the same numbers: the outgoing
              view leaves to the left, the incoming one arrives from the right,
              and `mode="wait"` keeps them from overlapping. Only THIS column is
              inside it — the rail beside it is not, so it never participates in
              the transition and never blinks. */}
          <AnimatePresence mode="wait">
            {editing && selectedSlug ? (
              <motion.div
                key={`editor-${selectedSlug}`}
                initial={{ opacity: 0, x: 12 }}
                animate={{ opacity: 1, x: 0 }}
                exit={{ opacity: 0, x: -12 }}
                transition={{ duration: duration.short, ease: "easeOut" }}
                className="absolute inset-0 flex flex-col overflow-hidden"
              >
                {/* Entering the editor unmounts the live application frame:
                    the candidate under review and the running publication are
                    two different programs and must not share a surface. */}
                <PageEditorShell
                  key={`${workspaceId}:${selectedSlug}`}
                  workspaceId={workspaceId}
                  slug={selectedSlug}
                  page={detail.error ? null : detail.raw}
                  loading={detail.loading}
                  capabilities={capabilities}
                  navigation={nav}
                  onLeft={focusTheView}
                />
              </motion.div>
            ) : selectedSlug ? (
              <motion.div
                key={`page-${selectedSlug}`}
                initial={{ opacity: 0, x: 12 }}
                animate={{ opacity: 1, x: 0 }}
                exit={{ opacity: 0, x: -12 }}
                transition={{ duration: duration.short, ease: "easeOut" }}
                className="absolute inset-0 flex flex-col overflow-hidden"
              >
                <PageApplicationView key={`${workspaceId}:${selectedSlug}`} workspaceId={workspaceId} slug={selectedSlug} page={detail.error ? null : detail.raw} fallback={
                <PageView
                  headingRef={viewHeading}
                  page={detail.page}
                  slug={selectedSlug}
                  loading={detail.loading}
                  error={detail.error}
                  notFound={detail.notFound}
                  onBack={closePage}
                  now={now}
                  // Straight from the subscription `usePage` registered — the
                  // header indicator is lit only while this page's channel is
                  // on a live socket, never on a timer of its own (epic #1935).
                  live={detail.live}
                />
                } />
              </motion.div>
            ) : (
              <motion.div
                key="pages-overview"
                initial={{ opacity: 0, x: -12 }}
                animate={{ opacity: 1, x: 0 }}
                exit={{ opacity: 0, x: 12 }}
                transition={{ duration: duration.short, ease: "easeOut" }}
                className="absolute inset-0 overflow-auto"
              >
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
              </motion.div>
            )}
          </AnimatePresence>
        </div>
      </div>

      {importing && (
        <PageImportDialog
          workspaceId={workspaceId}
          onClose={() => setImporting(false)}
          onImported={(installed) => {
            setImporting(false)
            openPage(installed)
          }}
        />
      )}

      {/* Only creation opens the document editor from here now. Editing an
          existing Page's document belongs to the editor's Content section,
          beside the panels it changes. */}
      {creating && (
        <PageEditor
          workspaceId={workspaceId}
          mode="create"
          page={null}
          defaultOwner={suggestedOwner}
          onClose={() => setCreating(false)}
          onSaved={(saved) => {
            if (saved && saved !== selectedSlug) openPage(saved)
          }}
        />
      )}
    </div>
  )
}
