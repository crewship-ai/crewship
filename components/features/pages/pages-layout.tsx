"use client"

import { useRouter } from "next/navigation"
import { usePageCreationAccess, usePageFolderCreationAccess } from "@/hooks/use-page-creation-access"

import { PageApplicationView } from "./page-application"
import { PageDocument } from "./page-document"
import { PageSlides } from "./page-slides"
import { MooringEditorHost } from "./mooring-editor-host"
import { NativeDocumentProvenance } from "../chat/right-panel-tabs/chat-document-access"
import { WordCraftEditorHost } from "./wordcraft-editor-host"
import { WordCraftCommandEditorHost } from "./wordcraft-command-editor-host"
import { wordCraftRenderer } from "@/lib/mooring/wordcraft-renderer"
import { PageWorkbookSurface } from "./workbook-editor-host"
import { ArtifactProject } from "./artifact-project"
import { chatArtifactProjects, slidesEditorMount } from "@/lib/feature-flags"
import { DocumentCatalog } from "./document-catalog"
import { PersonalDocumentSpace } from "./personal-document-space"
import { NewDocumentDialog } from "./new-document-dialog"
import { UnsentEditsLeaveDialog } from "./unsent-edits-leave-dialog"
import { useDocumentsAvailability } from "@/hooks/use-documents-availability"

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
import { AppWindow, FilePlus2, FolderUser, LayoutGrid, MessageSquare, Pencil, Share2, Upload } from "lucide-react"

import { SubBar, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { SidebarCollapseButton, SIDEBAR_WIDTH } from "@/components/layout/sidebar-kit"
import { useIsMobile } from "@/hooks/use-mobile"
import { useSessionSafe } from "@/hooks/use-auth"
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
import { usePageFolderMutations, usePageFolders } from "@/hooks/use-page-folders"
import { FolderDeleteDialog, FolderEditDialog, MoveToFolderDialog } from "@/components/features/pages/folder-dialogs"
import { FolderSharingDialog } from "@/components/features/pages/folder-sharing-dialog"
import { PageChatHandoff } from "@/components/features/pages/page-chat-handoff"
import { NativeDocumentAccessDialog } from "./native-document-access-dialog"
import { toast } from "sonner"

export interface PagesLayoutProps {
  workspaceId: string
  /** Set by /pages/[slug]; absent on the index. */
  slug?: string
  /** A code pin from the incoming Page URL; used only after its navigation is admitted. */
  initialArtifactVersion?: number
  /** Injected clock for tests — absolute ages and "today" are computed. */
  now?: Date
}

export function PagesLayout({ workspaceId, slug, initialArtifactVersion, now }: PagesLayoutProps) {
  const [personalSpace, setPersonalSpace] = React.useState(false)
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
  const [chatHandoffOpen, setChatHandoffOpen] = React.useState(false)
  const [nativeAccessSlug, setNativeAccessSlug] = React.useState<string | null>(null)
  const [nativeAccessOpener, setNativeAccessOpener] = React.useState<HTMLElement | null>(null)
  const closeNativeAccess = React.useCallback(() => setNativeAccessSlug(null), [])
  React.useEffect(closeNativeAccess, [workspaceId, closeNativeAccess])

  const [collapsed, setCollapsed] = React.useState(false)
  // On a phone the rail is 280px of a 390px screen — it does not sit BESIDE
  // the content, it replaces it. Collapse it when the viewport narrows and let
  // it open as an overlay instead of a column.
  React.useEffect(() => {
    if (isMobile) setCollapsed(true)
  }, [isMobile])

  const canCreatePage = usePageCreationAccess(workspaceId)
  const canCreateFolder = usePageFolderCreationAccess(workspaceId)
  const [importing, setImporting] = React.useState(false)
  const [creating, setCreating] = React.useState(false)
  const [creatingDocument, setCreatingDocument] = React.useState(false)
  const documentAvailability = useDocumentsAvailability(workspaceId)
  const nativeRouter = useRouter()
  const [search, setSearch] = React.useState("")
  const [filters, setFilters] = React.useState<PageFilters>(EMPTY_PAGE_FILTERS)

  const { pages, loading, error } = usePages(workspaceId, personalSpace ? "personal" : undefined)
  const detail = usePage(workspaceId, selectedSlug)
  const selectedRailSlug = selectedSlug?.startsWith("@") ? detail.page?.slug ?? selectedSlug : selectedSlug

  // ── Folders (#2527) ──────────────────────────────────────────────────────
  // The rail groups by folder when the server has them. The dialogs live
  // here rather than in the rail, which stays a list that reports what was
  // asked of a row; the state is which dialog is open and for what. The
  // subject handed to the move dialog is derived from the LIVE list on every
  // render, so after a 409 re-read the version it sends is the one on screen.
  const folders = usePageFolders(workspaceId)
  const folderWrites = usePageFolderMutations(workspaceId)
  const [folderDialog, setFolderDialog] = React.useState<
    | { kind: "create" }
    | { kind: "edit"; slug: string }
    | { kind: "delete"; slug: string }
    | { kind: "share"; slug: string }
    | { kind: "move"; slug: string }
    | { kind: "move-many"; slugs: string[] }
    | null
  >(null)
  const closeFolderDialog = React.useCallback(() => setFolderDialog(null), [])
  const folderOf = (slug: string) => folders.folders.find((f) => f.slug === slug) ?? null
  const moveSubject = React.useMemo(() => {
    if (folderDialog?.kind !== "move") return null
    const page = pages.find((p) => p.slug === folderDialog.slug)
    if (!page) return null
    return { slug: page.slug, name: page.name, folder: page.folder ?? null, pagesVersion: page.pagesVersion }
  }, [folderDialog, pages])
  // Several at once (#2533). Derived from the live list too, so a 409's
  // re-read puts fresh versions into the next confirm for every page.
  const moveSubjects = React.useMemo(() => {
    if (folderDialog?.kind !== "move-many") return null
    return folderDialog.slugs
      .map((slug) => pages.find((p) => p.slug === slug))
      .filter((p): p is (typeof pages)[number] => p !== undefined)
      .map((p) => ({ slug: p.slug, name: p.name, folder: p.folder ?? null, pagesVersion: p.pagesVersion }))
  }, [folderDialog, pages])

  const removeFromFolder = React.useCallback(
    async (page: { slug: string; name: string; folder?: { slug: string; name: string } | null; pagesVersion: number | null }) => {
      if (!page.folder) return
      try {
        await folderWrites.removePage.mutateAsync({
          folder: page.folder.slug,
          page: page.slug,
          pagesVersion: page.pagesVersion,
        })
        toast.success(`${page.name} is no longer in ${page.folder.name}.`)
      } catch (err) {
        // The lists were re-read on settle; the refusal is said in the
        // server's words, where the action was.
        toast.error(err instanceof Error && err.message ? err.message : "The page could not be removed from its folder.")
      }
    },
    [folderWrites.removePage],
  )

  // Who "Mine" belongs to, and half of the key the rail's fold state is saved
  // under. The tolerant variant on purpose: this shell is also mounted in
  // tests and embeds with no provider, and a rail with no "Mine" group is a
  // rail, while a throw is a blank column.
  const session = useSessionSafe()
  const currentUserId = session.data?.user.id ?? null

  const summaryPages = React.useMemo(() => personalSpace
    ? pages.filter(page => page.hasDocument === true && page.privateAudience === true && page.ownerRef === `user/${currentUserId}`)
    : pages, [pages, personalSpace, currentUserId])
  const summary = React.useMemo(() => summarisePages(summaryPages, now), [summaryPages, now])
  const filtered = React.useMemo(
    () => pages.filter((p) => matchesPageFilters(p, filters, search)),
    [pages, filters, search],
  )

  const legacyPages = React.useMemo(() => pages.filter(page => !page.hasDocument), [pages])
  const filteredLegacyPages = React.useMemo(() => filtered.filter(page => !page.hasDocument), [filtered])

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
      {summaryPages.length} {summaryPages.length === 1 ? "page" : "pages"}
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
  const nativePageId = typeof detail.raw?.id === "string" ? detail.raw.id.trim() : ""
  const nativeCraftApplication = detail.page?.nativeCraftApplication
  React.useEffect(()=>{
    if(currentUserId && nativeCraftApplication && nativePageId && !detail.loading && !detail.error && !detail.accessDenied){
      // The server names an existing explicit native profile. Opening it is a
      // fresh read-only admission, never a conversion of this Page's content.
      nativeRouter.replace(`/pages/craft/editor?${new URLSearchParams({workspace_id:workspaceId,page_id:nativePageId})}`)
    }
  },[currentUserId,nativeCraftApplication,nativePageId,detail.loading,detail.error,detail.accessDenied,nativeRouter,workspaceId])
  const nativeScope = session.status === "authenticated" && currentUserId && selectedSlug
    ? JSON.stringify([workspaceId, currentUserId, selectedSlug]) : null
  // An original producer's identity is retained DATA, never cached read/write
  // permission. A failed current Page read stops it without deleting its draft.
  const [nativeOriginal, setNativeOriginal] = React.useState<{ scope: string; pageId: string; name?: string; format?: string | null; commandsMounted?: boolean } | null>(null)
  let original = nativeOriginal?.scope === nativeScope ? nativeOriginal : null
  if (nativeOriginal && !original) setNativeOriginal(null)
  if (!original && nativeScope && nativePageId && detail.page && (detail.page.hasCollaboration === true || detail.page.collaborationFormat !== undefined) && !detail.error && !detail.accessDenied) {
    original = { scope: nativeScope, pageId: nativePageId, name: detail.page.name, format: detail.page.collaborationFormat, commandsMounted: documentAvailability.wordcraftCommands === true }
    setNativeOriginal(original)
  }
  // Once mounted, retain the same producer even when availability is withdrawn;
  // stop its input/delivery without deleting an unknown original from memory.
  if (original?.format === "wordcraft-c1-v2" && !original.commandsMounted && documentAvailability.wordcraftCommands === true) {
    original = { ...original, commandsMounted: true }; setNativeOriginal(original)
  }
  const commandRenderer = wordCraftRenderer(original?.format, original?.commandsMounted === true)
  const nativeUnavailable = Boolean(original) && (detail.accessDenied || Boolean(detail.error) || detail.page?.hasCollaboration !== true || nativePageId !== original?.pageId || detail.page?.collaborationFormat !== original?.format || commandRenderer === "unavailable" || (original?.format === "wordcraft-c1-v2" && documentAvailability.wordcraftCommands !== true))
  React.useEffect(() => { if (nativeUnavailable) closeNativeAccess() }, [nativeUnavailable, closeNativeAccess])
  const nativeDocument = !original && detail.page?.hasDocument === true
  // Flag off: has_slides is never read and the Page renders exactly as before.
  const nativeSlides = !original && slidesEditorMount() && detail.page?.hasSlides === true && !nativeDocument
  // A GridCraft workbook is a read-only preview here: no panel editor, no panels fallback.
  const nativeCollaboration = original !== null
  const nativeMooring = detail.page?.hasMooring === true && !nativeDocument && !nativeSlides && !nativeCollaboration
  const nativeWorkbook = detail.page?.hasWorkbook === true && !nativeDocument && !nativeSlides && !nativeCollaboration
  // Resolve only the selected Page already admitted by the existing route guard.
  // Native readers keep their original producers and take precedence below.
  const artifactKind: unknown = detail.raw?.artifact_kind ?? detail.page?.artifactKind
  // Any typed artifact, also one whose detail read lost its kind but still has a version head:
  // it never falls through to the panels empty state.
  const typedArtifactKind = artifactKind === "project" || artifactKind === "dashboard" || artifactKind === "slides" || artifactKind === "word" || artifactKind === "sheet" ? artifactKind
    : detail.raw?.has_artifact_project === true ? "project" : undefined
  const selectedArtifactVersion = selectedSlug === slug && Number.isSafeInteger(initialArtifactVersion) && (initialArtifactVersion ?? 0) > 0 ? initialArtifactVersion : undefined
  const versionedArtifact = chatArtifactProjects() && !nativeDocument && !nativeSlides && !nativeCollaboration && !nativeMooring && !nativeWorkbook && (artifactKind === "project" || artifactKind === "dashboard" || artifactKind === "slides")
  const editing = nav.mode === "edit" && selectedSlug != null && detail.page != null && !detail.loading && !detail.error && !nativeDocument && !nativeSlides && !nativeWorkbook && !nativeMooring && !nativeCollaboration && !versionedArtifact

  // Application | Panels. The switch lives in the page header beside Edit,
  // because that is where the page is named and where a person looks for
  // what to do with it; it replaces a second bar under the header that read
  // "Stop application / show panels". Showing panels unmounts the
  // application frame — the host's direct way of ending sandboxed code in
  // this tab — and stops nothing for anyone else. The switch is drawn only
  // while an application is actually on screen (`applicationAvailable`,
  // reported by the view), and a newly opened Page starts on its application.
  const [showPanels, setShowPanels] = React.useState(false)
  const [applicationAvailable, setApplicationAvailable] = React.useState(false)
  React.useEffect(() => {
    setShowPanels(false)
  }, [selectedSlug])

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
  // Keep editing returns to the editor, where the caret was; the view itself if it has none.
  const nativeView = React.useRef<HTMLDivElement>(null)
  const focusNativeView = React.useCallback(() => {
    const view = nativeView.current
    ;(view?.querySelector<HTMLElement>('[contenteditable="true"]') ?? view)?.focus()
  }, [])
  const focusTheView = React.useCallback(() => {
    wantsFocus.current = true
  }, [])

  const pageFallback = selectedSlug ? (
    <PageView
      headingRef={viewHeading}
      page={detail.page}
      slug={selectedSlug}
      loading={detail.loading}
      error={detail.error}
      notFound={detail.notFound}
      onBack={closePage}
      artifactKind={typedArtifactKind}
      now={now}
      workspaceId={workspaceId}
      mayEditAvatar={capabilities.mayEditMetadata}
      // Straight from the subscription `usePage` registered — the
      // header indicator is lit only while this page's channel is
      // on a live socket, never on a timer of its own (epic #1935).
      live={detail.live}
    />
  ) : null

  return (
    <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col bg-background">
      <SubBar
        className="coarse:[&>div]:min-h-[44px]"
        icon={CONCEPT_ICON.pages}
        title="Artifacts"
        section={editing ? "Edit" : undefined}
        description={description}
        ariaLabel="Artifacts"
        actions={
          <>
            {!selectedSlug && documentAvailability.data === true && <SubBarSecondary
              icon={FolderUser}
              aria-pressed={personalSpace}
              className="coarse:min-h-[44px]"
              onClick={() => setPersonalSpace(value => !value)}
              title="Show your private Documents in this workspace"
            >My space</SubBarSecondary>}
            {selectedSlug && !editing && detail.raw?.has_artifact_project && <SubBarSecondary icon={AppWindow} onClick={() => { window.location.href = `/pages/${encodeURIComponent(selectedSlug)}?artifact=1&workspace_id=${encodeURIComponent(workspaceId)}` }}>Open artifact project</SubBarSecondary>}
            {selectedSlug && !editing && applicationAvailable && (
              <div
                role="group"
                aria-label="Show"
                className="mr-1 flex h-7 items-center rounded-md border border-foreground/[0.08] p-0.5"
              >
                <button
                  type="button"
                  aria-pressed={!showPanels}
                  onClick={() => setShowPanels(false)}
                  className={cn(
                    "inline-flex h-6 items-center gap-1.5 rounded px-2 text-xs transition-colors coarse:min-h-[44px]",
                    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    !showPanels ? "bg-foreground/[0.08] font-medium text-foreground" : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  <AppWindow className="h-3 w-3" aria-hidden />
                  Application
                </button>
                <button
                  type="button"
                  aria-pressed={showPanels}
                  onClick={() => setShowPanels(true)}
                  title="Show this Page's panels instead; the application is closed in this tab and keeps running for everyone else"
                  className={cn(
                    "inline-flex h-6 items-center gap-1.5 rounded px-2 text-xs transition-colors coarse:min-h-[44px]",
                    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    showPanels ? "bg-foreground/[0.08] font-medium text-foreground" : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  <LayoutGrid className="h-3 w-3" aria-hidden />
                  Panels
                </button>
              </div>
            )}
            {selectedSlug && !editing && !nativeDocument && (
              <SubBarSecondary icon={MessageSquare} onClick={() => setChatHandoffOpen(true)} disabled={detail.page == null}>
                Ask about this Page
              </SubBarSecondary>
            )}
            {selectedSlug && !editing && !nativeDocument && !nativeWorkbook && !nativeMooring && !versionedArtifact && (
              <SubBarSecondary
                icon={Share2}
                onClick={event => {
                  if (nativeCollaboration) { setNativeAccessOpener(event.currentTarget); setNativeAccessSlug(selectedSlug) }
                  else nav.openEditor("access")
                }}
                disabled={detail.page == null}
                title="Who reaches this Page, who may send it data, and its public links"
              >
                Share
              </SubBarSecondary>
            )}
            {selectedSlug && !editing && !nativeDocument && !nativeSlides && !nativeWorkbook && !nativeMooring && !nativeCollaboration && !versionedArtifact && (
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
            {canCreatePage && <SubBarSecondary icon={Upload} onClick={() => canCreatePage && setImporting(true)}>
              Import
            </SubBarSecondary>}
            {canCreatePage && (documentAvailability.data === true || documentAvailability.wordcraftNative || documentAvailability.gridcraftNative || documentAvailability.wordcraftSDKPrefix || documentAvailability.wordcraftCommands) && <SubBarSecondary icon={FilePlus2} className="coarse:min-h-[44px]" onClick={() => setCreatingDocument(true)}>New Doc</SubBarSecondary>}
            {canCreatePage && <SubBarPrimary icon={FilePlus2} onClick={() => canCreatePage && setCreating(true)}>
              New page
            </SubBarPrimary>}
          </>
        }
      />

      {chatHandoffOpen && selectedSlug && <PageChatHandoff
        open={chatHandoffOpen} onOpenChange={setChatHandoffOpen}
        workspaceId={workspaceId} pageSlug={selectedSlug}
        ownerCrewSlug={detail.raw?.owner_crew_slug}
      />}

      <div className="relative flex flex-1 overflow-hidden">
        {isMobile && !collapsed && (
          <button
            type="button"
            aria-label="Close page list"
            onClick={() => setCollapsed(true)}
            className="fixed inset-0 z-40 bg-black/50 touch-none overscroll-contain"
          />
        )}
        {/* The list stays MOUNTED and on screen while editing. Replacing it
            with the editor's sections was the first draft's riskiest idea and
            the review's U05: the promise that a list restores its scroll and
            filters afterwards is the promise that breaks. #2515 then slid it
            off-screen to give a second, section-only rail the width; that
            rail is gone — the sections are cards on one page, laid out like
            an issue — and the Pages list is the one rail every surface
            shares, so it stays exactly where it is. Choosing another Page
            here goes through the same unsaved-work question as Back does. */}
        <aside
          className={cn(
            "shrink-0 overflow-hidden border-r border-foreground/[0.06] bg-card transition-all print:hidden",
            collapsed ? "w-9" : SIDEBAR_WIDTH,
            isMobile && !collapsed && "fixed inset-y-0 left-0 z-50 pt-[env(safe-area-inset-top)] pb-[env(safe-area-inset-bottom)] shadow-2xl",
          )}
        >
          {collapsed ? (
            <div className="flex h-full flex-col items-center pt-1.5">
              <SidebarCollapseButton collapsed onToggle={() => setCollapsed(false)} />
            </div>
          ) : (
            <PagesRail
              pages={pages}
              folders={folders.supported ? folders.folders : null}
              workspaceId={workspaceId}
              currentUserId={currentUserId}
              search={search}
              onSearchChange={setSearch}
              filters={filters}
              onFiltersChange={setFilters}
              selectedSlug={selectedRailSlug}
              onSelectPage={openPage}
              onCreatePage={canCreatePage ? () => setCreating(true) : undefined}
              onToggleCollapse={() => setCollapsed(true)}
              onMovePage={(page) => setFolderDialog({ kind: "move", slug: page.slug })}
              onRemoveFromFolder={(page) => void removeFromFolder(page)}
              onSharePage={(page, opener) => { setNativeAccessOpener(opener ?? null); setNativeAccessSlug(page.slug) }}
              onCreateFolder={canCreateFolder ? () => setFolderDialog({ kind: "create" }) : undefined}
              onEditFolder={(slug) => setFolderDialog({ kind: "edit", slug })}
              onShareFolder={(slug) => setFolderDialog({ kind: "share", slug })}
              onDeleteFolder={(slug) => setFolderDialog({ kind: "delete", slug })}
              onMovePages={(list) => setFolderDialog({ kind: "move-many", slugs: list.map((p) => p.slug) })}
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
                // The editor arrives from the right edge and leaves the same
                // way: it is a mode laid over the page, not the next page in
                // a sequence, and the direction says so. MotionConfig
                // (`reducedMotion="user"`) turns the travel into a plain
                // fade for anyone who asked for less motion.
                initial={{ opacity: 0.4, x: "100%" }}
                animate={{ opacity: 1, x: 0 }}
                exit={{ opacity: 0.4, x: "100%" }}
                transition={{ duration: duration.base, ease: "easeOut" }}
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
                key={nativeDocument ? `document-${workspaceId}:${detail.page!.id}` : nativeSlides ? `slides-${workspaceId}:${detail.page!.id}` : nativeWorkbook ? `workbook-${workspaceId}:${detail.page!.id}` : `page-${selectedSlug}`}
                ref={nativeView}
                tabIndex={nativeDocument || nativeSlides || nativeWorkbook || nativeMooring || nativeCollaboration ? -1 : undefined}
                initial={{ opacity: 0, x: 12 }}
                animate={{ opacity: 1, x: 0 }}
                exit={{ opacity: 0, x: -12 }}
                transition={{ duration: duration.short, ease: "easeOut" }}
                className="absolute inset-0 flex flex-col overflow-hidden"
              >
                {nativeDocument ? (
                  detail.accessDenied || !nativePageId ? <p role="alert" className="p-6">{detail.accessDenied ? "Document access is unavailable." : "The original Page identity is unavailable."}</p> : <>
                  {detail.error && <p role="status" className="px-6 py-2 text-sm">Page details are temporarily unavailable. Your document remains open.</p>}
                  <PageDocument key={`${workspaceId}:${nativePageId}`} workspaceId={workspaceId} slug={selectedSlug} pageId={nativePageId} name={detail.page?.name} onDirtyChange={nav.setDirty} />
                  <UnsentEditsLeaveDialog pending={nav.pending} onReturnFocus={focusNativeView} />
                  </>
                ) : nativeSlides ? (
                  detail.accessDenied || !nativePageId ? <p role="alert" className="p-6">Slides access is unavailable.</p>
                    : <><PageSlides workspaceId={workspaceId} pageId={nativePageId} name={detail.page?.name} onDirtyChange={nav.setDirty} /><UnsentEditsLeaveDialog pending={nav.pending} onReturnFocus={focusNativeView} /></>
                ) : nativeCollaboration ? (
                  <>
                    {!nativeUnavailable && currentUserId && <NativeDocumentProvenance workspaceId={workspaceId} pageId={original!.pageId} viewerId={currentUserId} />}
                    {commandRenderer === "commands" ? <WordCraftCommandEditorHost key={`${original!.scope}:${original!.pageId}`} workspaceId={workspaceId} pageId={original!.pageId} name={detail.page?.name ?? original!.name} unavailable={nativeUnavailable} onDirtyChange={nav.setDirty} onUnavailable={closeNativeAccess} />
                      : commandRenderer === "legacy" ? <WordCraftEditorHost key={`${original!.scope}:${original!.pageId}`} workspaceId={workspaceId} pageId={original!.pageId} name={detail.page?.name ?? original!.name} unavailable={nativeUnavailable} onDirtyChange={nav.setDirty} onUnavailable={closeNativeAccess} />
                        : <p role="alert" className="p-6">This document format is unavailable. No alternate editor has been opened.</p>}
                    <UnsentEditsLeaveDialog pending={nav.pending} onReturnFocus={focusNativeView} />
                  </>
                ) : nativeMooring ? (
                  detail.accessDenied || !nativePageId ? <p role="alert" className="p-6">Application unavailable</p>
                    : <><MooringEditorHost key={`${workspaceId}:${nativePageId}`} workspaceId={workspaceId} pageId={nativePageId} name={detail.page?.name} onDirtyChange={nav.setDirty} /><UnsentEditsLeaveDialog pending={nav.pending} onReturnFocus={focusNativeView} /></>
                ) : nativeWorkbook ? (
                  detail.accessDenied || !nativePageId ? <p role="alert" className="p-6">Workbook unavailable</p>
                    : <><PageWorkbookSurface key={`${workspaceId}:${nativePageId}`} workspaceId={workspaceId} pageId={nativePageId} name={detail.page?.name} onDirtyChange={nav.setDirty} /><UnsentEditsLeaveDialog pending={nav.pending} onReturnFocus={focusNativeView} /></>
                ) : versionedArtifact && detail.page && !detail.loading && !detail.error && !detail.accessDenied ? (
                  <ArtifactProject key={`${workspaceId}:${selectedSlug}:${selectedArtifactVersion ?? "latest"}`} workspaceId={workspaceId} slug={selectedSlug} initialVersion={selectedArtifactVersion} />
                ) : versionedArtifact ? pageFallback : detail.page && !detail.loading && !detail.error && !detail.accessDenied ? (
                  <PageApplicationView key={`${workspaceId}:${selectedSlug}`} workspaceId={workspaceId} slug={selectedSlug} page={detail.raw} panels={showPanels} onAvailableChange={setApplicationAvailable} fallback={pageFallback} />
                ) : pageFallback}
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
                {personalSpace && documentAvailability.data === true ? <PersonalDocumentSpace
                  key={`${workspaceId}:${currentUserId ?? ""}`}
                  workspaceId={workspaceId}
                  onSelect={openPage}
                /> : <>
                <DocumentCatalog pages={filtered} onSelect={openPage} />
                {!loading && !error && pages.length > 0 && legacyPages.length === 0 && filtered.length === 0 && <p role="status" className="p-6 text-muted-foreground">No documents match these filters.</p>}
                {(loading || error || legacyPages.length > 0 || pages.length === 0) && <PagesOverview
                  pages={filteredLegacyPages}
                  allPages={legacyPages}
                  loading={loading}
                  error={error}
                  onSelect={openPage}
                  onFilterState={(state: PanelState) =>
                    setFilters((f) => ({ ...f, states: togglePageFilter(f.states, state) }))
                  }
                  now={now}
                />}
                </>}
              </motion.div>
            )}
          </AnimatePresence>
        </div>
      </div>

      {nativeAccessSlug && <NativeDocumentAccessDialog key={`${workspaceId}:${nativeAccessSlug}`} workspaceId={workspaceId} slug={nativeAccessSlug} onClose={closeNativeAccess} returnFocus={nativeAccessOpener} />}
      {creatingDocument && <NewDocumentDialog key={workspaceId} workspaceId={workspaceId} onClose={() => setCreatingDocument(false)} onCreated={(slug,nativePageId)=>{if(nativePageId)nativeRouter.push(`/pages/craft/editor?${new URLSearchParams({workspace_id:workspaceId,page_id:nativePageId})}`);else openPage(slug)}} />}

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

      {/* The folder dialogs (#2527). Mounted only while open so each opening
          starts from the folder or page as it is now. */}
      {(folderDialog?.kind === "create" || folderDialog?.kind === "edit") && (
        <FolderEditDialog
          workspaceId={workspaceId}
          open
          onOpenChange={(open) => !open && closeFolderDialog()}
          folder={folderDialog.kind === "edit" ? folderOf(folderDialog.slug) : null}
          defaultOwner={suggestedOwner}
        />
      )}
      {folderDialog?.kind === "delete" && (
        <FolderDeleteDialog
          workspaceId={workspaceId}
          open
          onOpenChange={(open) => !open && closeFolderDialog()}
          folder={folderOf(folderDialog.slug)}
        />
      )}
      {folderDialog?.kind === "share" && (
        <FolderSharingDialog
          workspaceId={workspaceId}
          open
          onOpenChange={(open) => !open && closeFolderDialog()}
          folder={folderOf(folderDialog.slug)}
          marker={folders.error !== null ? "unknown" : (folderOf(folderDialog.slug)?.shared ?? "unknown")}
          // A reader who may not see the table is shown their own paths to
          // the page that is open, when it is in this folder.
          pageSlug={
            selectedSlug && pages.find((p) => p.slug === selectedSlug)?.folder?.slug === folderDialog.slug ? selectedSlug : null
          }
        />
      )}
      {folderDialog?.kind === "move" && (
        <MoveToFolderDialog
          workspaceId={workspaceId}
          open
          onOpenChange={(open) => !open && closeFolderDialog()}
          subject={moveSubject}
        />
      )}
      {folderDialog?.kind === "move-many" && (
        <MoveToFolderDialog
          workspaceId={workspaceId}
          open
          onOpenChange={(open) => !open && closeFolderDialog()}
          subject={null}
          subjects={moveSubjects}
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
