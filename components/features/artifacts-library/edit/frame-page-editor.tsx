"use client"

import * as React from "react"
import { usePage, type PageView } from "@/hooks/use-pages"
import { usePageGrants } from "@/hooks/use-page-grants"
import { PageEditorShell } from "@/components/features/pages/editor/page-editor-shell"
import { usePageCapabilities } from "@/components/features/pages/editor/use-page-capabilities"
import type { EditorNavigation } from "@/components/features/pages/editor/use-editor-route"
import { DEFAULT_EDITOR_SECTION, type EditorMode, type EditorPane, type EditorSection } from "@/lib/pages/editor-contract"

/**
 * The Pages editor's navigation, kept in the frame instead of the address.
 * The frame owns `/artifacts/<id>?edit=1`; sections and the preview pane are
 * a place inside the editor, so they live in state. Leaving the editor or a
 * deleted Page hands back to the frame, after the same unsaved-work question
 * the Pages route asks.
 */
export function useFrameEditorNavigation(slug: string, onLeave: (reason: "done" | "deleted") => void): EditorNavigation {
  const [route, setRoute] = React.useState<{ mode: EditorMode; section: EditorSection; pane: EditorPane }>({ mode: "edit", section: DEFAULT_EDITOR_SECTION, pane: "section" })
  const [dirty, setDirty] = React.useState(false)
  const [pending, setPending] = React.useState<EditorNavigation["pending"]>(null)
  const dirtyRef = React.useRef(dirty)
  dirtyRef.current = dirty
  const leave = React.useRef(onLeave)
  leave.current = onLeave

  const guarded = React.useCallback((perform: () => void) => {
    if (!dirtyRef.current) { perform(); return }
    setPending(current => current ?? {
      route: { slug, mode: "view", section: DEFAULT_EDITOR_SECTION, pane: "section" },
      discard: () => { setPending(null); setDirty(false); perform() },
      keep: () => setPending(null),
    })
  }, [slug])

  // Reload and tab close: only the browser's own question, as on the Pages route.
  React.useEffect(() => {
    if (!dirty) return
    const warn = (event: BeforeUnloadEvent) => event.preventDefault()
    window.addEventListener("beforeunload", warn)
    return () => window.removeEventListener("beforeunload", warn)
  }, [dirty])

  return React.useMemo<EditorNavigation>(() => ({
    slug, ...route, dirty, pending,
    openPage: next => { if (next === null) guarded(() => leave.current("deleted")) },
    setMode: mode => { if (mode === "view") guarded(() => leave.current("done")) },
    openEditor: section => setRoute(current => ({ ...current, mode: "edit", section: section ?? current.section, pane: "section" })),
    setSection: section => guarded(() => setRoute(current => ({ ...current, section, pane: "section" }))),
    setPane: pane => setRoute(current => ({ ...current, pane })),
    setDirty,
  }), [slug, route, dirty, pending, guarded])
}

/**
 * Whether the Pages editor applies to this Page: the same rule the Pages
 * route uses before it opens `?mode=edit` (pages-layout.tsx `editing`). A
 * document, deck, workbook, Mooring or collaboration Page and a typed
 * artifact have their own editors and never open the panel editor.
 */
export function panelEditorApplies(page: PageView | null): boolean {
  if (!page) return false
  return !page.hasDocument && !page.hasSlides && !page.hasWorkbook && !page.hasMooring
    && page.hasCollaboration !== true && page.collaborationFormat === undefined && !page.artifactKind
}

/**
 * The Pages panel editor (content, data and actions, access, history) inside
 * the Artifacts frame at `/artifacts/<id>?edit=1`. It reads the Page by its
 * immutable ID and edits it under the slug the server reports for that ID,
 * with the capabilities and grants the Pages editor reads; every write is
 * still the server's to refuse.
 */
export function FramePageEditor({ workspaceId, pageId, onLeave }: { workspaceId: string; pageId: string; onLeave: (reason: "done" | "deleted") => void }) {
  const detail = usePage(workspaceId, "@" + pageId)
  const slug = detail.page?.slug || null
  if (detail.loading && !detail.page) return <p role="status" className="p-6 text-sm text-muted-foreground">Loading the editor…</p>
  if (detail.accessDenied || detail.notFound || detail.error || !slug) return <p role="alert" className="p-6 text-sm">This artifact can&apos;t be edited right now. Nothing was changed.</p>
  if (!panelEditorApplies(detail.page)) return <p role="status" className="p-6 text-sm">This artifact is edited in its own view, not here.</p>
  return <LoadedEditor key={`${workspaceId}:${pageId}`} workspaceId={workspaceId} slug={slug} detail={detail} onLeave={onLeave} />
}

function LoadedEditor({ workspaceId, slug, detail, onLeave }: { workspaceId: string; slug: string; detail: ReturnType<typeof usePage>; onLeave: (reason: "done" | "deleted") => void }) {
  const navigation = useFrameEditorNavigation(slug, onLeave)
  const grants = usePageGrants(workspaceId, slug, true)
  const capabilities = usePageCapabilities(detail.raw, { mayManageAccess: grants.refusal === null })
  return <div data-frame-editor="page" className="flex min-h-[520px] flex-1 flex-col overflow-hidden">
    <PageEditorShell workspaceId={workspaceId} slug={slug} page={detail.error ? null : detail.raw} loading={detail.loading}
      capabilities={capabilities} navigation={navigation} />
  </div>
}
