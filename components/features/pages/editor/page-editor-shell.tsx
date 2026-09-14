"use client"

import * as React from "react"

import {
  EDITOR_SECTIONS,
  EDITOR_SECTION_LABEL,
  type EditorSection,
  type PageCapabilities,
} from "@/lib/pages/editor-contract"
import { cn } from "@/lib/utils"
import { Appear } from "@/components/ui/detail"
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
import type { WirePageDetail } from "@/hooks/use-page-grants"
import type { EditorNavigation } from "@/components/features/pages/editor/use-editor-route"
import type { EditorSectionProps } from "@/components/features/pages/editor/section-props"
import { EditorContentSection } from "@/components/features/pages/editor/section-content"
import { EditorDataActionsSection } from "@/components/features/pages/editor/section-data-actions"
import { EditorAccessSection } from "@/components/features/pages/editor/section-access"
import { EditorHistorySection } from "@/components/features/pages/editor/section-history"
import { PageDetailsStrip, PageEditorHeader } from "@/components/features/pages/editor/editor-header"
import { PropertiesCard } from "@/components/features/pages/editor/properties-card"

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
 * The shape is the issue detail's, on purpose. The first draft (#2515) gave
 * the editor a rail of four sections and showed one at a time in a centred
 * column; beside an issue, a routine or a crew it read as a settings wizard
 * from another product. Now the header card names the Page, a strip behind a
 * disclosure holds its dates, and the four sections are cards on one page —
 * Content, Data & actions and History in the main column, Properties and
 * Access in the sidebar — the way `issue-card-detail.tsx` lays out an issue.
 * The Pages rail on the left is the shared element every surface keeps.
 *
 * `section` in the address survives: it is where the page scrolls and where
 * focus lands, so a pasted `?section=access` still opens on Access and
 * "Manage producer access" in Data & actions still lands on the right card.
 * What it no longer does is hide the other three.
 */

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

  // Focus lands on the editor's heading when the editor opens, and on the
  // section's card when the address names one.
  //
  // The open case used to be skipped deliberately — and that was the wrong
  // call. Pressing Edit unmounts the button focus was on, so a keyboard user
  // arrived at the editor with focus on `<body>`: the single most common way
  // into this surface dropped them at the top of the document. A measured
  // pass found the same at every other seam, which is why the dialog and
  // `Back to page` use the same heading.
  const heading = React.useRef<HTMLHeadingElement>(null)
  const frames = React.useRef<Record<EditorSection, HTMLElement | null>>({
    content: null,
    data: null,
    access: null,
    history: null,
  })
  const opened = React.useRef(false)
  React.useEffect(() => {
    // The first section on a fresh open is the page itself: the heading is
    // the honest target, and scrolling to the Content card would only move
    // the header out of view.
    if (!opened.current && section === "content") {
      opened.current = true
      heading.current?.focus()
      return
    }
    opened.current = true
    const frame = frames.current[section]
    if (!frame) {
      heading.current?.focus()
      return
    }
    // jsdom has no layout, so it has no `scrollIntoView`; the focus alone is
    // what a test can see, and what a screen reader hears.
    if (typeof frame.scrollIntoView === "function") frame.scrollIntoView({ block: "start" })
    frame.focus()
  }, [section])

  // Every section reports its own unsaved work, and the address is guarded on
  // the sum. With all four mounted at once a single flag would let History's
  // "nothing unwritten" clear what Content had just raised.
  const parts = React.useRef<Record<EditorSection, boolean>>({ content: false, data: false, access: false, history: false })
  const reporters = React.useMemo(() => {
    const out = {} as Record<EditorSection, (dirty: boolean) => void>
    for (const id of EDITOR_SECTIONS) {
      out[id] = (dirty: boolean) => {
        parts.current[id] = dirty
        setDirty(EDITOR_SECTIONS.some((s) => parts.current[s]))
      }
    }
    return out
  }, [setDirty])

  const shared = {
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
  }
  const propsFor = (id: EditorSection): EditorSectionProps => ({ ...shared, onDirtyChange: reporters[id] })

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

  const leave = () => {
    setMode("view")
    onLeft?.()
  }

  const frame = (id: EditorSection, node: React.ReactNode) => (
    <SectionFrame key={id} id={id} frameRef={(el) => (frames.current[id] = el)}>
      {node}
    </SectionFrame>
  )

  return (
    <div data-slot="page-editor" className="flex h-full min-h-0 flex-col bg-background">
      {/* Only this column scrolls, and wide content scrolls inside its own
          container — the document itself must never move sideways. */}
      <div className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden">
        <div className="flex flex-col gap-4 p-4">
          {loading && page == null ? (
            <p role="status" className="text-sm text-muted-foreground">
              Loading this Page…
            </p>
          ) : pane === "preview" ? (
            // The candidate's build gets the whole surface rather than a third
            // of it beside a form and a diff. Content owns the preview and the
            // way back from it.
            frame("content", <EditorContentSection {...propsFor("content")} />)
          ) : (
            <>
              <Appear order={0}>
                <PageEditorHeader
                  slug={slug}
                  page={page}
                  capabilities={capabilities}
                  headingRef={heading}
                  audience={audience}
                  onBack={leave}
                />
              </Appear>
              <Appear order={1}>
                <PageDetailsStrip slug={slug} page={page} />
              </Appear>

              <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
                <div className="flex min-w-0 flex-col gap-4 xl:col-span-2">
                  <Appear order={2}>{frame("content", <EditorContentSection {...propsFor("content")} />)}</Appear>
                  <Appear order={3}>{frame("data", <EditorDataActionsSection {...propsFor("data")} />)}</Appear>
                  <Appear order={4}>{frame("history", <EditorHistorySection {...propsFor("history")} />)}</Appear>
                </div>
                <div className="flex min-w-0 flex-col gap-4">
                  <Appear order={2}>
                    <PropertiesCard workspaceId={workspaceId} slug={slug} page={page} capabilities={capabilities} />
                  </Appear>
                  <Appear order={3}>{frame("access", <EditorAccessSection {...propsFor("access")} />)}</Appear>
                </div>
              </div>
            </>
          )}
        </div>
      </div>

      <UnsavedWorkDialog pending={pending} onReturnFocus={() => heading.current?.focus()} />
    </div>
  )
}

/**
 * The landmark a section lives in: what `?section=` scrolls to and what an
 * in-editor link ("Manage producer access", "Data & actions") hands focus to.
 * Programmatic focus only (`tabIndex={-1}`), so it adds no stop to the
 * ordinary tab order.
 */
function SectionFrame({
  id,
  frameRef,
  children,
}: {
  id: EditorSection
  frameRef: (el: HTMLElement | null) => void
  children: React.ReactNode
}) {
  return (
    <section
      ref={frameRef}
      id={`editor-section-${id}`}
      data-slot="editor-section"
      data-section={id}
      aria-label={EDITOR_SECTION_LABEL[id]}
      tabIndex={-1}
      className={cn(
        "flex min-w-0 flex-col gap-4 rounded-xl outline-none",
        "focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
      )}
    >
      {children}
    </section>
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
