"use client"

import * as React from "react"
import { ArrowLeft } from "lucide-react"

import {
  EDITOR_SECTIONS,
  EDITOR_SECTION_HINT,
  EDITOR_SECTION_LABEL,
  type EditorSection,
  type PageCapabilities,
} from "@/lib/pages/editor-contract"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import type { WirePageDetail } from "@/hooks/use-page-grants"
import type { EditorNavigation } from "@/components/features/pages/editor/use-editor-route"
import type { EditorSectionProps } from "@/components/features/pages/editor/section-props"
import { EditorContentSection } from "@/components/features/pages/editor/section-content"
import { EditorDataActionsSection } from "@/components/features/pages/editor/section-data-actions"
import { EditorAccessSection } from "@/components/features/pages/editor/section-access"
import { EditorHistorySection } from "@/components/features/pages/editor/section-history"

/**
 * The editor's own chrome, inside the content column.
 *
 * It replaces the page's content and nothing else: the Pages list stays where
 * it is on the desktop. Swapping the list for the editor's sections was the
 * riskiest idea in the first draft of the design, and the independent review
 * named it (U05) — the promise that a list restores its scroll and its filters
 * afterwards is exactly the promise that breaks.
 *
 * Four sections, in one order, on every Page. An ordinary panel Page is not a
 * reduced version of an application Page: each section decides for itself what
 * it has to show, and none of them renders an empty application heading or a
 * permanently disabled Publish (U02).
 *
 * The section nav is CSS-responsive rather than JavaScript-responsive — a row
 * of named tabs at ≥768px, one labelled picker below it — so the same DOM
 * serves both and there is no first-paint flash while a media query resolves.
 */

const SECTION_COMPONENT: Record<EditorSection, React.ComponentType<EditorSectionProps>> = {
  content: EditorContentSection,
  data: EditorDataActionsSection,
  access: EditorAccessSection,
  history: EditorHistorySection,
}

export interface PageEditorShellProps {
  workspaceId: string
  slug: string
  page: WirePageDetail | null
  loading: boolean
  capabilities: PageCapabilities
  navigation: EditorNavigation
}

export function PageEditorShell({ workspaceId, slug, page, loading, capabilities, navigation }: PageEditorShellProps) {
  const { section, pane, setSection, setPane, setMode, setDirty, pending, openPage } = navigation
  const Section = SECTION_COMPONENT[section]

  // Focus lands on the editor's heading when a section changes, so a keyboard
  // or screen-reader user is told where they now are instead of being left on
  // a control that has just been replaced.
  const heading = React.useRef<HTMLHeadingElement>(null)
  const first = React.useRef(true)
  React.useEffect(() => {
    if (first.current) {
      first.current = false
      return
    }
    heading.current?.focus()
  }, [section])

  const sectionProps: EditorSectionProps = {
    workspaceId,
    slug,
    page,
    capabilities,
    onNavigate: setSection,
    pane,
    onPaneChange: setPane,
    onLeaveEditor: () => setMode("view"),
    // A deleted Page has no view to go back to, so this is the overview and
    // not `setMode("view")`.
    onPageDeleted: () => openPage(null),
    onDirtyChange: setDirty,
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <header className="flex shrink-0 flex-wrap items-center gap-3 border-b border-white/[0.06] px-4 py-3">
        <Button
          variant="outline"
          size="sm"
          className="coarse:min-h-11"
          onClick={() => setMode("view")}
        >
          <ArrowLeft className="h-3.5 w-3.5" aria-hidden />
          View page
        </Button>
        <div className="min-w-0 flex-1">
          {/* The identity of the page being edited never leaves the screen,
              on any width — losing it is how someone edits the wrong Page. */}
          <h2 ref={heading} tabIndex={-1} className="truncate text-body font-medium outline-none">
            {page?.name ?? slug}
          </h2>
          <p className="truncate text-xs text-muted-foreground">
            Editing · {EDITOR_SECTION_LABEL[section]}
          </p>
        </div>
      </header>

      <nav aria-label="Editor sections" className="hidden shrink-0 gap-1 border-b border-white/[0.06] px-3 py-1.5 md:flex">
        {EDITOR_SECTIONS.map((id) => (
          <button
            key={id}
            type="button"
            onClick={() => setSection(id)}
            aria-current={id === section ? "page" : undefined}
            title={EDITOR_SECTION_HINT[id]}
            className={cn(
              "rounded-md px-3 py-1.5 text-xs transition-colors coarse:min-h-11",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              id === section
                ? "bg-white/[0.07] font-medium text-foreground"
                : "text-muted-foreground hover:bg-white/[0.04] hover:text-foreground",
            )}
          >
            {EDITOR_SECTION_LABEL[id]}
          </button>
        ))}
      </nav>

      <div className="shrink-0 border-b border-white/[0.06] px-4 py-2 md:hidden">
        {/* A named picker, not four tabs squeezed into 360px — the reviewer's
            §4 point, and the reason the label is visible rather than implied. */}
        <label htmlFor="pages-editor-section" className="mb-1 block text-xs text-muted-foreground">
          Editor section
        </label>
        <Select value={section} onValueChange={(next) => setSection(next as EditorSection)}>
          <SelectTrigger id="pages-editor-section" className="w-full coarse:min-h-11">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {EDITOR_SECTIONS.map((id) => (
              <SelectItem key={id} value={id}>
                {EDITOR_SECTION_LABEL[id]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {/* Only this column scrolls, and wide content scrolls inside its own
          container — the document itself must never move sideways. */}
      <div className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden">
        {loading && page == null ? (
          <p role="status" className="p-4 text-sm text-muted-foreground">
            Loading this Page…
          </p>
        ) : (
          <Section {...sectionProps} />
        )}
      </div>

      <UnsavedWorkDialog pending={pending} />
    </div>
  )
}

/**
 * Asked once, in one place, for every way out of a section: another section,
 * another Page, leaving the editor, and Back.
 *
 * It deliberately does not promise to keep anything. The editor has no
 * autosave and no global Save — each control writes its own thing at its own
 * moment — so the honest offer is "discard" or "stay", not "save everything".
 */
function UnsavedWorkDialog({ pending }: { pending: EditorNavigation["pending"] }) {
  return (
    <AlertDialog open={pending != null} onOpenChange={(open) => { if (!open) pending?.keep() }}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Leave without saving?</AlertDialogTitle>
          <AlertDialogDescription>
            This section holds changes that have not been written yet. Leaving discards them.
            Nothing on this Page has been changed for anyone else.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={() => pending?.keep()}>Stay here</AlertDialogCancel>
          <AlertDialogAction onClick={() => pending?.discard()}>Discard changes</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
