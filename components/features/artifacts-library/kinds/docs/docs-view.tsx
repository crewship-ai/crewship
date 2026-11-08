"use client"

import * as React from "react"
import { EditorContent, useEditorState } from "@tiptap/react"
import { Download, FileText, History, ListTree, PanelLeft, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useDocumentsAvailability } from "@/hooks/use-documents-availability"
import { durabilityLabel, DocumentSaveState } from "@/components/features/pages/document-save-state"
import { DocumentProposals } from "@/components/features/pages/document-proposals/document-proposals"
import { DocumentEarlierDraftNotice, DocumentHistoryHost, DocumentRecoveryAlert, PageDocument, useDocumentWorkspace, type DocumentWorkspaceProps } from "@/components/features/pages/page-document"
import { cn } from "@/lib/utils"
import { ArtifactDrawerSection, ArtifactSlot, ArtifactTool, ArtifactToolSeparator, useArtifactFrame, type FrameLayout, type FrameStatus } from "../../frame"
import type { LibraryItem } from "../../library-items"
import { docsFrameStatus, outlineOf, readingStats, readingText, type DocNode, type OutlineEntry } from "./docs-model"
import { DocsTools } from "./docs-tools"

/** What the Docs body tells the frame: the chip, whether this person can edit now, and the chip's popover. */
export interface DocsFrameReport {
  status: FrameStatus | null
  canEdit: boolean
  statusActions?: React.ReactNode
  statusFoot?: React.ReactNode
}

/** The reading column: centred, 700 px at most, clear of the floating view strip and tools bar. */
const COLUMN = "mx-auto grid w-full max-w-[700px] grid-cols-[minmax(0,1fr)] content-start gap-4 px-4 pb-28 pt-16 md:px-8 md:pb-20"

/** Reading typography (Harbor 2 v87 Docs page): a comfortable size on a phone, a calmer one on wider screens. */
const DOCS_EDITOR_CLASS = "min-h-64 max-w-none text-lg leading-relaxed outline-none md:text-base md:leading-[1.65] [&_[data-block-id]]:scroll-mt-20 [&_p]:my-3 [&_h1]:mb-3 [&_h1]:mt-8 [&_h1]:text-2xl [&_h1]:font-semibold [&_h1]:tracking-tight [&_h2]:mb-2 [&_h2]:mt-7 [&_h2]:text-xl [&_h2]:font-semibold [&_h3]:mb-2 [&_h3]:mt-5 [&_h3]:text-lg [&_h3]:font-semibold [&_ul]:list-disc [&_ul]:pl-6 [&_ol]:list-decimal [&_ol]:pl-6 [&_table]:my-4 [&_table]:w-full [&_table]:border-collapse [&_td]:border [&_td]:p-2 [&_th]:border [&_th]:bg-muted [&_th]:p-2 [&_th]:text-left [&_td_p]:my-0 [&_th_p]:my-0 [&_pre]:whitespace-pre-wrap [&_pre]:[overflow-wrap:anywhere] [&_pre]:rounded-md [&_pre]:bg-muted [&_pre]:p-3 [&_code]:font-mono [&_a]:text-primary-hover [&_a]:underline"

/**
 * A Docs page (a native Tiptap/Yjs document) inside the ArtifactFrame. The
 * session, its journal, receipts, recovery and restore guard are the
 * document editor's own (`PageDocument`); this only lays them out the Harbor
 * 2 way: a centred reading column, the outline in the panel slot, formatting
 * in the tools slot while editable, the counts in the viewport slot, History
 * in the drawer and one banner for unsaved work.
 *
 * Before the documents service admits this session the saved version shows
 * (`fallback`); once admitted, the editor stays even if the service later
 * withdraws, so unsent edits are never unmounted.
 */
export function DocsBody({ workspaceId, item, fallback, onFrame }: {
  workspaceId: string
  item: LibraryItem
  fallback: React.ReactNode
  onFrame: (report: DocsFrameReport | null) => void
}) {
  const availability = useDocumentsAvailability(workspaceId)
  const [admitted, setAdmitted] = React.useState(false)
  React.useEffect(() => { if (availability.data === true) setAdmitted(true) }, [availability.data])
  React.useEffect(() => () => onFrame(null), [onFrame])
  const pageId = item.pageId
  if (!pageId) return null
  if (!admitted && availability.data !== true) {
    if (availability.isPending) return <div className={COLUMN}><p role="status" className="text-sm text-muted-foreground">Checking document availability…</p></div>
    return <>{fallback}</>
  }
  return <PageDocument workspaceId={workspaceId} slug={"@" + pageId} pageId={pageId} name={item.name}
    renderWorkspace={props => <DocsWorkspace {...props} item={item} onFrame={onFrame} />} />
}

function DocsWorkspace(props: DocumentWorkspaceProps & { item: LibraryItem; onFrame: (report: DocsFrameReport | null) => void }) {
  const { workspaceId, pageId, humanId, slug, available, historyExpired, sync, item, onFrame } = props
  const frame = useArtifactFrame()
  const layout: FrameLayout = frame?.layout ?? "desktop"
  const phone = layout === "phone"
  const { editor, restoring, actionError, setActionError, reviewEnabled, proposalClient, hasLocalDraft, downloadDraft, empty, restoreGuard, review } = useDocumentWorkspace(props, DOCS_EDITOR_CLASS)
  // The outline is open by default only where it does not cover the page.
  const [outlineOpen, setOutlineOpen] = React.useState(layout === "desktop")
  const [sent, setSent] = React.useState(false)
  React.useEffect(() => { if (sync.pending > 0) setSent(true) }, [sync.pending])
  const summary = useEditorState({ editor, selector: ({ editor }) => {
    const json = editor?.getJSON() as DocNode | undefined
    return { outline: outlineOf(json), stats: readingStats(json) }
  } })
  const outline = summary?.outline ?? []
  const stats = summary?.stats ?? { words: 0, minutes: 0 }

  const editable = Boolean(sync.editable && !restoring && editor)
  const status = docsFrameStatus({
    hydrated: Boolean(sync.state), denied: sync.denied, available, restoring, status: sync.status, pending: sync.pending,
    recovery: sync.recovery, recoveredDrafts: sync.recoveredDrafts.length, canWrite: sync.state?.can_write === true,
    durability: sync.durability, sentThisSession: sent,
  })
  const durability = durabilityLabel(sync.durability, sync.status)
  const foot = [sync.status, durability, sync.error].filter(Boolean).join(" · ")
  const download = React.useRef(downloadDraft); download.current = downloadDraft
  const offerDownload = hasLocalDraft && !sync.denied
  const statusKey = JSON.stringify(status)
  React.useEffect(() => {
    onFrame({
      status: JSON.parse(statusKey) as FrameStatus | null, canEdit: editable, statusFoot: foot || undefined,
      statusActions: offerDownload ? <Button type="button" size="sm" variant="outline" className="coarse:min-h-[44px]" onClick={() => download.current()}><Download aria-hidden />Download my changes</Button> : undefined,
    })
  }, [onFrame, statusKey, editable, foot, offerDownload])
  React.useEffect(() => () => onFrame(null), [onFrame])

  const showRecovery = !sync.denied && (sync.recovery || (!available && sync.pending > 0))
  const showEarlier = !sync.denied && sync.unverifiedDrafts.length > 0
  const banner = showRecovery || showEarlier || actionError
    ? <div data-docs-banner className="grid gap-3 rounded-card border border-warn/40 bg-warn/8 p-3 text-sm">
      {actionError && <p role="alert" className="font-medium text-destructive">{actionError}</p>}
      <DocumentRecoveryAlert sync={sync} available={available} slug={slug} editor={editor} onError={setActionError} className="flex flex-wrap items-center gap-2 [&>p]:basis-full" />
      <DocumentEarlierDraftNotice sync={sync} slug={slug} onError={setActionError} className="flex flex-wrap items-center gap-2 [&>p]:basis-full" />
    </div>
    : null
  const owner = item.ownerLabel ? `owned by ${item.ownerLabel === "You" ? "you" : item.ownerLabel}` : null
  const meta = [item.folder?.name, owner].filter(Boolean).join(" · ")

  const jump = (entry: OutlineEntry) => {
    if (!entry.id || !editor) return
    const target = [...editor.view.dom.querySelectorAll<HTMLElement>("[data-block-id]")].find(node => node.dataset.blockId === entry.id)
    target?.scrollIntoView?.({ block: "start", behavior: "smooth" })
    if (phone) setOutlineOpen(false)
  }

  return <section aria-label="Doc page" data-docs-page data-layout={layout} className={cn("touch-form min-h-full", outlineOpen && !phone && "pl-[256px]")}>
    <article className={COLUMN}>
      {banner}
      <header className="grid gap-1">
        <h1 className="text-3xl font-semibold tracking-tight">{item.name}</h1>
        {meta && <p className="text-sm text-muted-foreground">{meta}</p>}
        {/* The chip is a button; this stays the one live region that announces save state. */}
        <div className="sr-only">
          <DocumentSaveState status={restoring ? "Restore confirmation pending · current editor preserved" : !available && !sync.denied ? "Documents are unavailable · local changes retained" : sync.status}
            durability={sync.durability} storageUnavailable={sync.storageUnavailable} storageBusy={sync.storageBusy} unverifiedDraft={sync.unverifiedDrafts.length > 0} lockless={sync.storageLockless} />
        </div>
      </header>
      {reviewEnabled && sync.state && !sync.denied && <DocumentProposals client={proposalClient} headRev={sync.state.rev} canDecide={sync.editable && !restoring} hasLocalDraft={hasLocalDraft} onDownloadDraft={downloadDraft} onAccepted={() => window.location.reload()} />}
      {!sync.denied && <>
        {sync.state && empty && <p className="text-sm text-muted-foreground">{editable ? "This document is empty. Start writing below, or ask an agent in a chat to draft it." : "This document is empty."}</p>}
        <EditorContent editor={editor} />
      </>}
    </article>

    <ArtifactSlot name="view" label="View">
      <ArtifactTool icon={PanelLeft} label={outlineOpen ? "Hide outline" : "Show outline"} on={outlineOpen} onClick={() => setOutlineOpen(open => !open)} />
      <ArtifactToolSeparator />
      {/* Page is the only mode with a backend; Canvas stays hidden until it has one. */}
      <span role="tablist" aria-label="Mode" className="inline-flex">
        <button type="button" role="tab" aria-selected="true" className="inline-flex h-[30px] items-center gap-1.5 rounded-[var(--radius-row)] bg-accent px-2.5 text-sm font-medium text-foreground coarse:min-h-[44px]">
          <FileText aria-hidden className="size-4" />Page
        </button>
      </span>
    </ArtifactSlot>

    {outlineOpen && <ArtifactSlot name="panel" label="Outline">
      <div className="flex items-center gap-1 border-b py-1.5 pl-3 pr-1.5">
        <b className="flex-1 text-sm font-semibold">Outline</b>
        <ArtifactTool icon={X} label="Close outline" onClick={() => setOutlineOpen(false)} />
      </div>
      <nav aria-label="Headings" className="min-h-0 flex-1 overflow-y-auto p-1.5">
        {outline.length === 0
          ? <p className="flex items-start gap-2 px-2 py-1.5 text-sm text-muted-foreground"><ListTree aria-hidden className="mt-0.5 size-4 shrink-0" />Headings you add show here.</p>
          : <ul className="grid gap-px">
            {outline.map((entry, index) => <li key={entry.id ?? `heading-${index}`}>
              <button type="button" disabled={!entry.id} onClick={() => jump(entry)} data-level={entry.level}
                className={cn("w-full truncate rounded-[var(--radius-row)] py-1.5 pr-2 text-left text-sm text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-60 coarse:min-h-[44px]",
                  entry.level === 1 ? "pl-2 font-medium text-foreground" : entry.level === 2 ? "pl-4" : "pl-6")}>
                {entry.text}
              </button>
            </li>)}
          </ul>}
      </nav>
    </ArtifactSlot>}

    {editable && editor && sync.state && <DocsTools editor={editor} schema={sync.state.schema} onError={setActionError} />}

    <ArtifactSlot name="viewport" label="Reading">
      <span className="px-2 font-mono text-xs tabular-nums text-muted-foreground">{readingText(stats)}</span>
    </ArtifactSlot>

    <ArtifactDrawerSection tab="history" label="History" icon={History}>
      <div className="grid content-start gap-3 p-3.5">
        <p className="text-xs text-muted-foreground">Restore creates a new version with this content. Later changes stay in History.</p>
        <DocumentHistoryHost workspaceId={workspaceId} pageId={pageId} humanId={humanId} available={available} denied={sync.denied || historyExpired}
          review={review} head={sync.state} blocked={restoreGuard.blocked}
          onStart={restoreGuard.onStart} onRefused={restoreGuard.onRefused} onRestored={restoreGuard.onRestored}
          control={{ open: frame?.drawerTab === "history", onClose: () => frame?.openDrawer(null) }} />
      </div>
    </ArtifactDrawerSection>
  </section>
}
