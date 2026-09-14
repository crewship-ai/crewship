"use client"

import * as React from "react"
import { ArrowLeft, FileText, History, KeyRound, Workflow, type LucideIcon } from "lucide-react"

import { SIDEBAR_WIDTH, SidebarRow, SidebarSection } from "@/components/layout/sidebar-kit"

import {
  EDITOR_SECTIONS,
  EDITOR_SECTION_LABEL,
  EDITOR_SECTION_SUMMARY,
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
 * Pressing Edit hands the whole content column to this shell (#2515): a
 * header that names the Page, the way back and what viewers see meanwhile; a
 * rail of sections on the left with one line under each name; the open
 * section in a readable measure. The Pages list beside it stays mounted but
 * steps out of the way, so its scroll and filters are there on return.
 *
 * The section nav is CSS-responsive rather than JavaScript-responsive — the
 * rail at ≥768px, one labelled picker below it — so the same DOM serves both
 * and there is no first-paint flash while a media query resolves.
 */

// The same icons the rest of the product uses for these nouns, so the rail
// reads like the Settings nav and not like a second design.
const SECTION_ICON: Record<EditorSection, LucideIcon> = {
  content: FileText,
  data: Workflow,
  access: KeyRound,
  history: History,
}

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
  /** Called after the editor closes, so the caller can place focus. */
  onLeft?: () => void
}

export function PageEditorShell({ workspaceId, slug, page, loading, capabilities, navigation, onLeft }: PageEditorShellProps) {
  const { section, pane, setSection, setPane, setMode, setDirty, pending, openPage } = navigation
  const Section = SECTION_COMPONENT[section]

  // Focus lands on the editor's heading when a section changes, and when the
  // editor opens.
  //
  // The open case used to be skipped deliberately — and that was the wrong
  // call. Pressing Edit unmounts the button focus was on, so a keyboard user
  // arrived at the editor with focus on `<body>`: the single most common way
  // into this surface dropped them at the top of the document. A measured
  // pass found the same at every other seam, which is why `focusHeading` is
  // exported and the dialog and `Back to page` use it too.
  const heading = React.useRef<HTMLHeadingElement>(null)
  React.useEffect(() => {
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
    onLeaveEditor: () => {
      setMode("view")
      onLeft?.()
    },
    // A deleted Page has no view to go back to, so this is the overview and
    // not `setMode("view")`.
    onPageDeleted: () => openPage(null),
    onDirtyChange: setDirty,
  }

  // What viewers see while this person edits. Stated in the header because
  // the whole screen has just changed under them, and the first question is
  // whether anything they touch here is already live. It is not: an
  // application Page keeps serving its publication until Publish, and every
  // other section writes only when its own control is pressed.
  const audience = page?.has_application
    ? `Viewers still see publication ${page.publication_version}`
    : capabilities.hasApplicationDraft
      ? "The application draft is not published"
      : "Each section saves on its own"

  return (
    <div data-slot="page-editor" className="flex h-full min-h-0 flex-col bg-background">
      <header className="flex shrink-0 items-center gap-3 border-b border-white/[0.06] px-4 py-2.5">
        <Button
          variant="outline"
          size="sm"
          className="coarse:min-h-11"
          // Leaving unmounts this button, so something has to catch the
          // focus it drops. The view's own heading is the honest target and
          // the shell cannot reach it, so the caller — which renders both —
          // is handed the moment instead. An id would have been simpler and
          // was wrong: two page views can share a document, and a fixed id
          // collides.
          onClick={() => {
            setMode("view")
            onLeft?.()
          }}
        >
          <ArrowLeft className="h-3.5 w-3.5" aria-hidden />
          Back to page
        </Button>
        <div className="min-w-0 flex-1">
          {/* The identity of the page being edited never leaves the screen,
              on any width — losing it is how someone edits the wrong Page. */}
          {/* The focus this shell moves on every section change, on returning
              from the preview and after a discard must be visible to a
              sighted keyboard user — a correct, silent move is the same as
              not making it. The ring is on `:focus-visible`, not `:focus`:
              browsers carry the input modality across a scripted focus, so a
              person who arrived by keyboard sees the ring and a person who
              clicked the rail with a mouse does not. On `:focus` it lit up
              the page's name after every click, which read as a text field. */}
          <h2
            ref={heading}
            tabIndex={-1}
            className="truncate rounded-sm text-body font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
          >
            {page?.name ?? slug}
          </h2>
          <p className="truncate text-xs text-muted-foreground">
            Editing {EDITOR_SECTION_LABEL[section]} · {audience}
          </p>
        </div>
      </header>

      <div className="flex min-h-0 flex-1">
        {/* The rail is the map of the editor: every section, with one line
            saying what lives there, and the open one marked. It replaces the
            row of tabs that sat under the page header — tabs said where you
            were, never what the other three held (#2515). */}
        {/* Same vocabulary as the Settings nav and the Pages, Issues and
            Routines rails: `sidebar-kit`'s width, ground, section header and
            rows, with the selected row marked by the shared accent bar. The
            one-line summary under each name is composed inside the row, the
            way settings-nav composes its badges. */}
        <aside className={cn(SIDEBAR_WIDTH, "hidden shrink-0 flex-col border-r border-sidebar-border bg-sidebar md:flex")}>
          <nav aria-label="Editor sections" className="flex-1 overflow-y-auto py-2">
            <SidebarSection label="Sections">
              {EDITOR_SECTIONS.map((id) => {
                const Icon = SECTION_ICON[id]
                const selected = id === section
                return (
                  <SidebarRow
                    key={id}
                    selected={selected}
                    onSelect={() => setSection(id)}
                    aria-current={selected ? "page" : undefined}
                    aria-label={EDITOR_SECTION_LABEL[id]}
                    className="items-start py-1.5"
                  >
                    <Icon className={cn("mt-0.5 h-3.5 w-3.5 shrink-0", selected ? "opacity-100" : "opacity-60")} aria-hidden />
                    <span className="flex min-w-0 flex-1 flex-col">
                      <span className="truncate">{EDITOR_SECTION_LABEL[id]}</span>
                      <span className="type-nav-sub truncate text-sidebar-foreground/50">{EDITOR_SECTION_SUMMARY[id]}</span>
                    </span>
                  </SidebarRow>
                )
              })}
            </SidebarSection>
          </nav>
        </aside>

        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          <div className="shrink-0 border-b border-white/[0.06] px-4 py-2 md:hidden">
            {/* A named picker, not four tabs squeezed into 360px — the
                reviewer's §4 point, and the reason the label is visible rather
                than implied. */}
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
              container — the document itself must never move sideways.

              Padding and the readable measure live here rather than in each
              section, so there is one answer instead of four that drift: three
              sections had rendered flush against the container, and the review —
              the surface someone spends the most time on — had no maximum width
              at all, so its diff and its prose stretched the full span of an
              ultrawide monitor. Sections narrow further where a form wants it;
              none of them widens past this. */}
          <div className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden">
            <div className="mx-auto w-full max-w-5xl p-4 sm:p-6 lg:px-10">
              {loading && page == null ? (
                <p role="status" className="text-sm text-muted-foreground">
                  Loading this Page…
                </p>
              ) : (
                <Section {...sectionProps} />
              )}
            </div>
          </div>
        </div>
      </div>

      <UnsavedWorkDialog pending={pending} onReturnFocus={() => heading.current?.focus()} />
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
function UnsavedWorkDialog({
  pending,
  onReturnFocus,
}: {
  pending: EditorNavigation["pending"]
  /**
   * Where focus goes when the answer leaves you where you were.
   *
   * Radix restores focus to the trigger, and this dialog is `open`-controlled
   * with no trigger to restore to — so Escape and Stay here, the two answers
   * that keep you on this screen, both dropped focus onto `<body>`. Discard
   * was already fine, because the navigation that follows it moves focus
   * itself.
   */
  onReturnFocus: () => void
}) {
  // Radix closes the dialog after a Cancel or Action handler runs, and the
  // close calls `onOpenChange(false)` with the same captured `pending`. Left
  // ungated, Stay pushes the restored Back entry twice and Discard performs
  // the navigation twice.
  const answered = React.useRef(false)
  React.useEffect(() => {
    if (pending != null) answered.current = false
  }, [pending])
  const answer = (choice: "keep" | "discard") => {
    if (answered.current) return
    answered.current = true
    if (choice === "keep") {
      pending?.keep()
      // Staying means the screen did not change, so focus belongs back on it
      // rather than at the top of the document.
      window.requestAnimationFrame(onReturnFocus)
    } else {
      pending?.discard()
    }
  }
  return (
    <AlertDialog open={pending != null} onOpenChange={(open) => { if (!open) answer("keep") }}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Leave without saving?</AlertDialogTitle>
          <AlertDialogDescription>
            This section holds changes that have not been written yet. Leaving discards them.
            Nothing on this Page has been changed for anyone else.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={() => answer("keep")}>Stay here</AlertDialogCancel>
          <AlertDialogAction onClick={() => answer("discard")}>Discard changes</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
