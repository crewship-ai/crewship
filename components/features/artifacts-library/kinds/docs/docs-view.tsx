"use client"

import * as React from "react"
import { EditorContent, useEditorState } from "@tiptap/react"
import { Download, FileText, GitPullRequestArrow, History, LayoutGrid, ListTree, MessageSquare, PanelLeft, Send, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { canEnableCanvas, enableCanvas, upgradeFailure, type UpgradeTarget } from "@/lib/documents/upgrade"
import { Textarea } from "@/components/ui/textarea"
import { proposalChatHref, proposalSchema } from "@/lib/documents/proposal-request"
import { useDocumentsAvailability } from "@/hooks/use-documents-availability"
import { durabilityLabel, DocumentSaveState } from "@/components/features/pages/document-save-state"
import { DocumentProposals } from "@/components/features/pages/document-proposals/document-proposals"
import { DocumentEarlierDraftNotice, DocumentHistoryHost, DocumentRecoveryAlert, PageDocument, useDocumentWorkspace, type DocumentWorkspaceProps } from "@/components/features/pages/page-document"
import { cn } from "@/lib/utils"
import { ArtifactAgentMode, ArtifactDrawerSection, ArtifactSlot, ArtifactTool, ArtifactToolSeparator, useArtifactFrame, type FrameLayout, type FrameStatus } from "../../frame"
import type { LibraryItem } from "../../library-items"
import { docsFrameStatus, outlineOf, readingStats, readingText, type DocNode, type OutlineEntry } from "./docs-model"
import { DocsTools } from "./docs-tools"
import { DocsComments } from "./docs-comments"
import { DocsCanvas, type MentionLabels } from "./docs-canvas"
import { documentSurface } from "@/lib/documents/surface"
import { DocsPresence } from "./docs-presence"
import { presentPeople } from "@/lib/documents/presence"
import { commentFailure, useDocumentComments, type CommentWrite, type DocumentComment } from "@/hooks/use-document-comments"
import { useMembers } from "@/components/features/settings/members/use-members"
import { documentActorName } from "@/lib/documents/actor-name"
import { commentTarget, setCommentHighlights, type CommentTarget } from "@/components/features/pages/document-rich-extensions"

/**
 * Documents whose Canvas a person has just enabled in this tab: the session
 * reopens at the new schema and shows Canvas, which they asked for. In memory
 * only; nothing about a document is stored in the browser for it.
 */
const openCanvasAfterReopen = new Set<string>()

/** What the Docs body tells the frame: the chip, whether this person can edit now, and the chip's popover. */
export interface DocsFrameReport {
  status: FrameStatus | null
  canEdit: boolean
  statusActions?: React.ReactNode
  statusFoot?: React.ReactNode
  /** Who else has the document open (#5166); nothing while the person is alone. */
  presence?: React.ReactNode
}

/** The reading column: centred, 700 px at most, clear of the floating view strip and tools bar. */
const COLUMN = "mx-auto grid w-full max-w-[700px] grid-cols-[minmax(0,1fr)] content-start gap-4 px-4 pb-28 pt-16 md:px-8 md:pb-20"

/** Reading typography (Harbor 2 v87 Docs page): a comfortable size on a phone, a calmer one on wider screens. */
const DOCS_EDITOR_CLASS = "min-h-64 max-w-none text-lg leading-relaxed outline-none md:text-base md:leading-[1.65] [&_[data-block-id]]:scroll-mt-20 [&_p]:my-3 [&_h1]:mb-3 [&_h1]:mt-8 [&_h1]:text-2xl [&_h1]:font-semibold [&_h1]:tracking-tight [&_h2]:mb-2 [&_h2]:mt-7 [&_h2]:text-xl [&_h2]:font-semibold [&_h3]:mb-2 [&_h3]:mt-5 [&_h3]:text-lg [&_h3]:font-semibold [&_ul]:list-disc [&_ul]:pl-6 [&_ol]:list-decimal [&_ol]:pl-6 [&_table]:my-4 [&_table]:w-full [&_table]:border-collapse [&_td]:border [&_td]:p-2 [&_th]:border [&_th]:bg-muted [&_th]:p-2 [&_th]:text-left [&_td_p]:my-0 [&_th_p]:my-0 [&_pre]:my-4 [&_pre]:overflow-x-auto [&_pre]:whitespace-pre [&_pre]:rounded-md [&_pre]:bg-muted [&_pre]:p-3 [&_pre]:text-sm [&_pre]:leading-normal [&_code]:font-mono [&_:not(pre)>code]:rounded-sm [&_:not(pre)>code]:bg-muted [&_:not(pre)>code]:px-1 [&_:not(pre)>code]:py-0.5 [&_:not(pre)>code]:text-[0.9em] [&_a]:text-primary-hover [&_a]:underline [&_strong]:font-semibold [&_em]:italic [&_s]:line-through [&_blockquote]:my-4 [&_blockquote]:border-l-2 [&_blockquote]:border-border [&_blockquote]:pl-4 [&_blockquote]:text-muted-foreground [&_ul[data-type=taskList]]:list-none [&_ul[data-type=taskList]]:pl-0.5 [&_li[data-type=taskItem]]:flex [&_li[data-type=taskItem]]:items-start [&_li[data-type=taskItem]]:gap-2 [&_li[data-type=taskItem]>label]:mt-[0.3em] [&_li[data-type=taskItem]>label]:flex [&_li[data-type=taskItem]>div]:min-w-0 [&_li[data-type=taskItem]>div]:flex-1 [&_li[data-type=taskItem]_p]:my-0.5 [&_li[data-checked=true]>div]:text-muted-foreground [&_li[data-checked=true]>div]:line-through [&_input[type=checkbox]]:size-4 [&_input[type=checkbox]]:cursor-pointer [&_input[type=checkbox]]:accent-primary [&_input[type=checkbox]:disabled]:cursor-default [&_.document-comment]:rounded-sm [&_.document-comment]:bg-warn/15 [&_.document-comment]:underline [&_.document-comment]:decoration-warn/60 [&_.document-comment]:underline-offset-4 [&_.document-comment-active]:bg-warn/35 [&_u]:underline [&_u]:underline-offset-[0.15em] selection:bg-primary/25"

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
    renderWorkspace={props => <DocsWorkspace {...props} onFrame={onFrame} />}
    renderUnhydrated={(sync, message) => <DocsUnhydrated denied={sync.denied} status={sync.status} onFrame={onFrame}>{message}</DocsUnhydrated>} />
}

/**
 * No confirmed document: the body shows the session's own message, and the
 * frame keeps a chip that says why — No access after a refusal or a revoke,
 * nothing while the first read is still on its way. Dropping the chip would
 * leave the last state (often Saved) unexplained.
 */
/**
 * The session's reader-scoped people lookup, as the Page editor's mention
 * support (DocumentMentions) carries it, so Canvas cards name people exactly as
 * the Page does. An editor without it (an older schema, or a build without that
 * support) has none, and every mention reads "@Unknown member".
 */
export function sessionPeople(editor: { storage: unknown } | null | undefined): MentionLabels | undefined {
  const people = (editor?.storage as Record<string, { people?: Partial<MentionLabels> } | undefined> | undefined)?.documentMentions?.people
  return typeof people?.label === "function" && typeof people.subscribe === "function" ? people as MentionLabels : undefined
}

function DocsUnhydrated({ denied, status, onFrame, children }: { denied: boolean; status: string; onFrame: (report: DocsFrameReport | null) => void; children: React.ReactNode }) {
  const chip = docsFrameStatus({ hydrated: false, denied, available: true, restoring: false, status, pending: 0, recovery: false, recoveredDrafts: 0, canWrite: false, durability: "confirmed", sentThisSession: false })
  const chipKey = JSON.stringify(chip)
  React.useEffect(() => { onFrame({ status: JSON.parse(chipKey) as FrameStatus | null, canEdit: false }) }, [onFrame, chipKey])
  React.useEffect(() => () => onFrame(null), [onFrame])
  return <div className={COLUMN}>{children}</div>
}

function DocsWorkspace(props: DocumentWorkspaceProps & { onFrame: (report: DocsFrameReport | null) => void }) {
  const { workspaceId, pageId, humanId, slug, available, historyExpired, sync, onFrame, onRestored } = props
  const frame = useArtifactFrame()
  const layout: FrameLayout = frame?.layout ?? "desktop"
  const phone = layout === "phone"
  const { editor, restoring, actionError, setActionError, reviewEnabled, proposalClient, hasLocalDraft, downloadDraft, empty, restoreGuard, review, peers } = useDocumentWorkspace(props, DOCS_EDITOR_CLASS)
  // The outline is open by default only where it does not cover the page.
  const [outlineOpen, setOutlineOpen] = React.useState(layout === "desktop")
  const [sent, setSent] = React.useState(false)
  React.useEffect(() => { if (sync.pending > 0) setSent(true) }, [sync.pending])
  const summary = useEditorState({ editor, selector: ({ editor }) => {
    const json = editor?.getJSON() as DocNode | undefined
    return { outline: outlineOf(json), stats: readingStats(json) }
  } })
  const outline = summary?.outline ?? []

  // Comments appear only once the discussion read succeeds; any refusal leaves the page as it was.
  const head = sync.state ? { schema: sync.state.schema, epoch: sync.state.epoch, rev: sync.state.rev } : undefined
  const comments = useDocumentComments({ workspaceId, slug, pageId, humanId, enabled: Boolean(sync.state) && available && !sync.denied, head })
  const discussion = comments.query.isSuccess && !sync.denied ? comments.query.data : null
  const members = useMembers(discussion ? workspaceId : null)
  const nameOf = React.useCallback((actor: string) => documentActorName(actor, { humanId, members: members.members }), [humanId, members.members])
  const [mode, setMode] = React.useState<"page" | "canvas">(() => openCanvasAfterReopen.delete(pageId) ? "canvas" : "page")
  const [draft, setDraft] = React.useState<CommentTarget | null>(null)
  const [commentNotice, setCommentNotice] = React.useState<string | null>(null)
  const [activeThread, setActiveThread] = React.useState<string | null>(null)
  const highlights = React.useMemo(() => (discussion?.comments ?? []).filter(comment => !comment.parent && !comment.resolved && comment.status === "attached" && comment.block && comment.from !== undefined && comment.to !== undefined)
    .map(comment => ({ id: comment.id, block: comment.block!, from: comment.from!, to: comment.to! })), [discussion])
  React.useEffect(() => { if (editor) setCommentHighlights(editor, [...highlights, ...(draft ? [{ id: "draft", ...draft }] : [])], activeThread ?? (draft ? "draft" : null)) }, [editor, highlights, draft, activeThread])
  const startComment = () => {
    if (!editor || !discussion?.canComment) return
    const target = commentTarget(editor)
    if (typeof target === "string") { setDraft(null); setCommentNotice(target) } else { setDraft(target); setCommentNotice(null) }
    frame?.openDrawer("comments")
  }
  const writeComment = async (change: CommentWrite) => {
    try { await comments.write.mutateAsync(change) } catch (cause) { throw new Error(commentFailure(cause)) }
  }
  const jumpToComment = (comment: DocumentComment) => {
    if (!editor || !comment.block) return
    setActiveThread(comment.id)
    // The thread's text lives on the Page: show it there.
    setMode("page")
    const target = [...editor.view.dom.querySelectorAll<HTMLElement>("[data-block-id]")].find(node => node.dataset.blockId === comment.block)
    target?.scrollIntoView?.({ block: "center", behavior: "smooth" })
  }
  const stats = summary?.stats ?? { words: 0, minutes: 0 }
  // Proposals exist only where the server says so and the Doc's own schema
  // supports them (4, 5 or 6); otherwise no Propose mode and no tab.
  const requestSchema = proposalSchema(sync.state?.schema)
  const proposalsServed = Boolean(sync.state?.proposals) && requestSchema !== undefined && available && !sync.denied
  const [proposalPrompt, setProposalPrompt] = React.useState("")

  // Canvas is a second view of the same Y.Doc (R71 §0.4), shown at a schema that admits the surface.
  // An older document is upgraded only when a person with edit access confirms "Enable Canvas"; never implicitly.
  const canvasReady = Boolean(sync.state && !sync.denied && sync.state.schema >= documentSurface.CANVAS_SCHEMA)
  const canvasOffer = !canvasReady && Boolean(sync.state && !sync.denied && available) && canEnableCanvas(sync.state?.schema, sync.state?.can_write === true)
  // An unknown outcome keeps its request ID AND its exact body: a retry is the same request, so a first attempt
  // that did succeed is answered with its original receipt rather than refused as stale.
  const [enabling, setEnabling] = React.useState<{ id: string; target?: UpgradeTarget; error: string | null } | null>(null)
  const confirmEnable = async () => {
    const state = sync.state
    if (!state || !enabling) return
    // Unsaved edits would be refused at the new schema: they are saved first, never dropped.
    // A frozen retry is allowed while the tab only needs reopening (a lost answer to an applied upgrade changes the epoch).
    if (sync.pending > 0 || (sync.recovery && !enabling.target)) { setEnabling({ ...enabling, error: "Wait until your changes are saved, then enable Canvas." }); throw new Error("pending") }
    const target = enabling.target ?? { workspaceId, pageId, schema: state.schema, rev: state.rev, epoch: state.epoch }
    const result = await enableCanvas(target, enabling.id)
    if (result.status !== "done") {
      // A changed document needs a fresh read and a fresh decision; an unknown outcome is retried as the same request.
      setEnabling(result.status === "unknown" ? { id: enabling.id, target, error: upgradeFailure(result) } : { id: crypto.randomUUID(), error: upgradeFailure(result) })
      if (result.status === "denied") sync.deny()
      throw new Error(result.status)
    }
    openCanvasAfterReopen.add(pageId)
    onRestored()
  }
  const view = canvasReady ? mode : "page"
  // Losing Canvas (schema or access changed) returns to the Page for good, never back on its own.
  React.useEffect(() => { if (!canvasReady) setMode("page") }, [canvasReady])
  // The person's own transaction origin for Canvas writes: saved like every local change, never mixed with remote ones.
  const canvasOrigin = React.useMemo(() => "canvas:" + crypto.randomUUID(), [])
  const openInPage = (blockId: string) => {
    setMode("page")
    requestAnimationFrame(() => {
      const target = [...(editor?.view.dom.querySelectorAll<HTMLElement>("[data-block-id]") ?? [])].find(node => node.dataset.blockId === blockId)
      target?.scrollIntoView?.({ block: "center", behavior: "smooth" })
    })
  }
  const openCanvasComments = (_block: string, threads: string[]) => {
    setActiveThread(threads[0] ?? null)
    frame?.openDrawer("comments")
  }

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
  const people = presentPeople(peers)
  const presenceKey = JSON.stringify(people.map(peer => [peer.userId, peer.name, peer.color]))
  React.useEffect(() => {
    onFrame({
      status: JSON.parse(statusKey) as FrameStatus | null, canEdit: editable, statusFoot: foot || undefined,
      presence: people.length ? <DocsPresence peers={people} /> : undefined,
      statusActions: offerDownload ? <Button type="button" size="sm" variant="outline" className="coarse:min-h-[44px]" onClick={() => download.current()}><Download aria-hidden />Download my changes</Button> : undefined,
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps -- presenceKey holds the people reported
  }, [onFrame, statusKey, editable, foot, offerDownload, presenceKey])
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

  const jump = (entry: OutlineEntry) => {
    if (!entry.id || !editor) return
    const target = [...editor.view.dom.querySelectorAll<HTMLElement>("[data-block-id]")].find(node => node.dataset.blockId === entry.id)
    target?.scrollIntoView?.({ block: "start", behavior: "smooth" })
    if (phone) setOutlineOpen(false)
  }

  const outlineShown = outlineOpen && view === "page"
  const modeTab = (value: "page" | "canvas", Icon: typeof FileText, label: string) => <button type="button" role="tab" aria-selected={view === value}
    id={`docs-mode-${value}`} aria-controls={view === value ? `docs-view-${value}` : undefined}
    aria-haspopup={value === "canvas" && !canvasReady ? "dialog" : undefined}
    onClick={() => { if (value === "canvas" && !canvasReady) setEnabling({ id: crypto.randomUUID(), error: null }); else setMode(value) }} data-docs-mode={value}
    className={cn("inline-flex h-[30px] items-center gap-1.5 rounded-[var(--radius-row)] px-2.5 text-sm font-medium coarse:min-h-[44px]",
      view === value ? "bg-accent text-foreground" : "text-muted-foreground hover:bg-accent/60 hover:text-foreground")}>
    <Icon aria-hidden className="size-4" />{label}
  </button>

  return <section aria-label="Doc page" data-docs-page data-layout={layout} data-docs-view={view} className={cn("touch-form min-h-full", outlineShown && !phone && "pl-[256px]")}>
    {/* The Page editor stays mounted under Canvas, so unsent edits and its undo history are never unmounted. */}
    <article id="docs-view-page" role="tabpanel" aria-labelledby="docs-mode-page" className={COLUMN} hidden={view !== "page"}>
      {view === "page" && banner}
      {/* The frame names the artifact and its Info drawer the owner and folder (Harbor 2 v87 Docs): the column starts with the document. */}
      {/* The chip is a button; this stays the one live region that announces save state. */}
      <div className="sr-only">
        <DocumentSaveState status={restoring ? "Restore confirmation pending · current editor preserved" : !available && !sync.denied ? "Documents are unavailable · local changes retained" : sync.status}
          durability={sync.durability} storageUnavailable={sync.storageUnavailable} storageBusy={sync.storageBusy} unverifiedDraft={sync.unverifiedDrafts.length > 0} lockless={sync.storageLockless} />
      </div>
      {reviewEnabled && !proposalsServed && sync.state && !sync.denied && <DocumentProposals client={proposalClient} headRev={sync.state.rev} canDecide={sync.editable && !restoring} hasLocalDraft={hasLocalDraft} onDownloadDraft={downloadDraft} onAccepted={() => window.location.reload()} />}
      {!sync.denied && <>
        {sync.state && empty && <p className="text-sm text-muted-foreground">{editable ? "This document is empty. Start writing below, or ask an agent in a chat to draft it." : "This document is empty."}</p>}
        <EditorContent editor={editor} />
      </>}
    </article>

    {view === "canvas" && <div id="docs-view-canvas" role="tabpanel" aria-labelledby="docs-mode-canvas" className="h-[calc(100dvh-8rem)] min-h-[480px] w-full px-2 pb-16 pt-14 md:px-4 md:pb-4">
      {banner}
      <DocsCanvas doc={sync.doc} editable={editable} origin={canvasOrigin} onOpenInPage={openInPage}
        comments={discussion?.comments} onOpenComments={discussion ? openCanvasComments : undefined} people={sessionPeople(editor)} />
    </div>}

    <ArtifactSlot name="view" label="View">
      {view === "page" && <>
        <ArtifactTool icon={PanelLeft} label={outlineOpen ? "Hide outline" : "Show outline"} on={outlineOpen} onClick={() => setOutlineOpen(open => !open)} />
        <ArtifactToolSeparator />
      </>}
      {/* Canvas shows for documents whose schema admits the surface, and as "Enable Canvas" to editors of older ones. */}
      <span role="tablist" aria-label="Mode" className="inline-flex">
        {modeTab("page", FileText, "Page")}
        {(canvasReady || canvasOffer) && modeTab("canvas", LayoutGrid, "Canvas")}
      </span>
    </ArtifactSlot>

    {outlineShown && <ArtifactSlot name="panel" label="Outline">
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

    {view === "page" && editable && editor && sync.state && <DocsTools editor={editor} schema={sync.state.schema} onError={setActionError} onComment={discussion?.canComment ? startComment : undefined} />}

    <ArtifactSlot name="viewport" label="Reading">
      <span className="px-2 font-mono text-xs tabular-nums text-muted-foreground">{readingText(stats)}</span>
    </ArtifactSlot>

    {discussion && <ArtifactDrawerSection tab="comments" label="Comments" icon={MessageSquare}>
      {discussion.canComment && !editable && view === "page" && <div className="px-3.5 pt-3.5">
        <Button type="button" size="sm" variant="outline" className="coarse:min-h-[44px]" onClick={startComment}>Comment on selection</Button>
      </div>}
      <DocsComments data={discussion} nameOf={nameOf} draft={draft} notice={commentNotice} active={activeThread}
        onCancelDraft={() => setDraft(null)} onWrite={writeComment} onJump={jumpToComment} />
    </ArtifactDrawerSection>}

    {proposalsServed && sync.state && <>
      {sync.state.can_write && <ArtifactAgentMode mode="propose">
        <div className="grid gap-2" data-docs-propose>
          <Textarea aria-label="What should the agent change?" placeholder="What should the agent change?" value={proposalPrompt}
            onChange={event => setProposalPrompt(event.target.value)} className="min-h-20 text-sm" />
          <p className="text-xs text-muted-foreground">The agent proposes changes in a private chat. On a shared document it needs edit access granted to it. Nothing changes until someone with edit access accepts them under Proposals.</p>
          <div className="flex justify-end">
            <Button asChild size="sm"><a href={proposalChatHref(workspaceId, { pageId, rev: sync.state.rev, generation: sync.state.generation, schema: requestSchema! }, proposalPrompt.trim())}>
              <Send aria-hidden />Ask for a proposal</a></Button>
          </div>
        </div>
      </ArtifactAgentMode>}
      <ArtifactDrawerSection tab="proposals" label="Proposals" icon={GitPullRequestArrow}>
        <div className="grid content-start gap-3 p-3.5">
          <DocumentProposals client={proposalClient} headRev={sync.state.rev} canDecide={sync.editable && !restoring} hasLocalDraft={hasLocalDraft} onDownloadDraft={downloadDraft} onAccepted={() => window.location.reload()} />
        </div>
      </ArtifactDrawerSection>
    </>}

    <ArtifactDrawerSection tab="history" label="History" icon={History}>
      <div className="grid content-start gap-3 p-3.5">
        <p className="text-xs text-muted-foreground">Restore creates a new version with this content. Later changes stay in History.</p>
        <DocumentHistoryHost workspaceId={workspaceId} pageId={pageId} humanId={humanId} available={available} denied={sync.denied || historyExpired}
          review={review} head={sync.state} blocked={restoreGuard.blocked}
          onStart={restoreGuard.onStart} onRefused={restoreGuard.onRefused} onRestored={restoreGuard.onRestored}
          control={{ open: frame?.drawerTab === "history", onClose: () => frame?.openDrawer(null) }} />
      </div>
    </ArtifactDrawerSection>
    <ConfirmDialog open={enabling !== null} onOpenChange={open => { if (!open) setEnabling(null) }}
      title="Enable Canvas for this document?"
      description="Canvas shows this document's blocks on a board you can arrange, next to notes that live only on the canvas."
      consequences={[
        { tone: "kept", text: "The Page, its text and formatting stay exactly as they are." },
        { tone: "kept", text: "Versions and comments are kept." },
        { tone: "warn", text: "Open proposals can no longer be accepted; they can still be rejected, and an agent can propose again." },
        { tone: "warn", text: "Everyone with edit access can use Canvas from now on. Restoring a version from before Canvas keeps Canvas enabled but empties its layout." },
        { tone: "warn", text: "People who have this document open must reload before they can save again." },
      ]}
      confirmLabel="Enable Canvas" onConfirm={confirmEnable}>
      {enabling?.error && <p role="alert" className="text-sm text-destructive">{enabling.error}</p>}
    </ConfirmDialog>
  </section>
}
