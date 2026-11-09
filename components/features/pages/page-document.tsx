"use client"

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react"
import { Extension } from "@tiptap/core"
import { EditorContent, useEditor, useEditorState } from "@tiptap/react"
import StarterKit from "@tiptap/starter-kit"
import Collaboration from "@tiptap/extension-collaboration"
import { Table } from "@tiptap/extension-table"
import TableRow from "@tiptap/extension-table-row"
import TableHeader from "@tiptap/extension-table-header"
import TableCell from "@tiptap/extension-table-cell"
import { Plugin } from "@tiptap/pm/state"
import { Button } from "@/components/ui/button"
import { isEmptyDocument } from "@/lib/documents/empty"
import { Input } from "@/components/ui/input"
import { documentSchema, normalizeDocumentLink } from "@/lib/documents/schema"
import { useDocumentsAvailability } from "@/hooks/use-documents-availability"
import { DocumentSaveState } from "./document-save-state"
import { usePageDocument } from "@/hooks/use-page-document"
import { useSessionSafe } from "@/hooks/use-auth"
import { useDocumentHistory, useDocumentHistoryRestore } from "@/hooks/use-document-history"
import { DocumentHistory } from "./document-history/document-history"
import type { DocumentHistoryReview } from "./document-history/types"
import { DocumentProposals } from "./document-proposals/document-proposals"
import { createHttpHistoryActions } from "@/lib/documents/history-actions"
import { createHttpProposalClient } from "@/lib/documents/proposals"
import { documentReviewActions } from "@/lib/feature-flags"
import { CommentHighlights, documentEditorExtensions, isRichSchema, stableBlockTypes } from "./document-rich-extensions"

const StableIDs = Extension.create({
  name: "documentBlockIDs",
  addOptions() { return { types: stableBlockTypes(undefined) } },
  addGlobalAttributes() { return [{ types: this.options.types, attributes: { id: { default: null, parseHTML: element => element.getAttribute("data-block-id"), renderHTML: attributes => attributes.id ? { "data-block-id": attributes.id } : {} } } }] },
  addProseMirrorPlugins() { return [new Plugin({ appendTransaction: (transactions, _old, state) => {
    if (!transactions.some(transaction => transaction.docChanged)) return null
    const tr = state.tr, seen = new Set<string>()
    state.doc.descendants((node, pos) => {
      if (!node.isBlock || !("id" in node.attrs)) return
      let id = node.attrs.id
      if (!id || seen.has(id)) { id = crypto.randomUUID(); tr.setNodeMarkup(pos, undefined, { ...node.attrs, id }) }
      seen.add(id)
    })
    return tr.docChanged ? tr : null
  } })] },
})
/** Saves text as a local file: the draft downloads. Nothing is sent. */
export function downloadText(name: string, content: string) {
  const url = URL.createObjectURL(new Blob([content], { type: "text/plain;charset=utf-8" }))
  const link = document.createElement("a"); link.href = url; link.download = name; link.click(); URL.revokeObjectURL(url)
}

/** What a document workspace receives once the confirmed state is hydrated. */
export type DocumentWorkspaceProps = DocumentProps & { available: boolean; humanId: string; historyExpired: boolean; sync: ReturnType<typeof usePageDocument>; onRestored: () => void }
type DocumentProps = {
  workspaceId: string; slug: string; pageId: string; name?: string; onDirtyChange?: (dirty: boolean) => void
  /** Another layout for the same session (the Artifacts Docs view); every gate, sync and restore rule stays here. */
  renderWorkspace?: (props: DocumentWorkspaceProps) => ReactNode
  /** Wraps the message shown while there is no confirmed document (loading, refused, revoked), so a frame can still report its state. */
  renderUnhydrated?: (sync: ReturnType<typeof usePageDocument>, message: ReactNode) => ReactNode
}
export function PageDocument(props: DocumentProps) {
  const session = useSessionSafe()
  const human = session.status === "authenticated" ? session.data?.user.id : undefined
  const availability = useDocumentsAvailability(props.workspaceId)
  const identity = JSON.stringify([human, props.workspaceId, props.pageId])
  const [admitted,setAdmitted] = useState<string>()
  const available = availability.data === true
  useEffect(()=>{if(human && available)setAdmitted(identity)},[human,available,identity])
  if (!human) return <p role="alert" className="p-6">Document access is unavailable.</p>
  if (!props.workspaceId || !props.pageId) return <p role="alert" className="p-6">The original Page identity is unavailable.</p>
  if (availability.isPending && admitted !== identity) return <p role="status" className="p-6">Checking document availability…</p>
  if (!available && admitted !== identity) return <p role="status" className="p-6">Documents are unavailable. Your Page remains in the list.</p>
  return <DocumentSession key={identity} {...props} humanId={human} available={available} />
}
function DocumentSession(props: DocumentProps & {available:boolean; humanId:string}) {
  const [epoch, setEpoch] = useState(0)
  return <DocumentLiveSession key={epoch} {...props} onRestored={() => setEpoch(value => value + 1)} />
}
function DocumentLiveSession(props: DocumentProps & {available:boolean; humanId:string; onRestored:()=>void}) {
  const [historyExpired, setHistoryExpired] = useState(false)
  useEffect(() => {
    const expire = () => setHistoryExpired(true)
    window.addEventListener("auth:session-expired", expire)
    return () => window.removeEventListener("auth:session-expired", expire)
  }, [])
  const sync = usePageDocument(props.workspaceId, props.slug, props.pageId, props.available)
  // Bind ProseMirror only after hydration, so its empty default paragraph is
  // never merged into a document which already contains saved content.
  if (!sync.state) {
    const message = <p role={sync.denied ? "alert" : "status"} className="p-6">{sync.status}{sync.error ? ` · ${sync.error}` : ""}</p>
    return props.renderUnhydrated ? props.renderUnhydrated(sync, message) : message
  }
  const workspace = { ...props, sync, historyExpired }
  return props.renderWorkspace ? props.renderWorkspace(workspace) : <DocumentWorkspace {...workspace} />
}

const PAGES_EDITOR_CLASS = "min-h-64 max-w-none p-6 outline-none [&_p]:my-3 [&_h1]:my-4 [&_h1]:text-3xl [&_h1]:font-semibold [&_h2]:my-4 [&_h2]:text-2xl [&_h2]:font-semibold [&_h3]:my-3 [&_h3]:text-xl [&_ul]:list-disc [&_ul]:pl-6 [&_ol]:list-decimal [&_ol]:pl-6 [&_table]:w-full [&_table]:border-collapse [&_td]:border [&_td]:p-2 [&_th]:border [&_th]:bg-muted [&_th]:p-2 [&_pre]:whitespace-pre-wrap [&_pre]:break-words [&_pre]:bg-muted [&_pre]:p-3 [&_code]:font-mono"

/**
 * One document session's editor, restore guard and review clients, for any
 * layout. The editor binds the hydrated CRDT and is writable only while the
 * sync says so and no restore is pending; a restore confirmed while this tab
 * still holds local edits never reopens over them.
 */
export function useDocumentWorkspace({ workspaceId, pageId, slug, sync, onDirtyChange, onRestored }: Pick<DocumentWorkspaceProps, "workspaceId" | "pageId" | "slug" | "sync" | "onDirtyChange" | "onRestored">, editorClass = PAGES_EDITOR_CLASS) {
  const [actionError, setActionError] = useState<string>()
  const [restoring, setRestoring] = useState(false)
  const reviewEnabled = documentReviewActions()
  const revertActions = useMemo(() => createHttpHistoryActions(workspaceId, pageId), [workspaceId, pageId])
  const proposalClient = useMemo(() => createHttpProposalClient(workspaceId, pageId), [workspaceId, pageId])
  const hasLocalDraft = sync.pending > 0 || sync.recovery
  const downloadDraft = () => downloadText(`${slug}-draft.json`, JSON.stringify(editor?.getJSON(), null, 2))
  const localVersion = useRef(0), restoreVersion = useRef(0)
  useEffect(() => {
    const changed = (_update: Uint8Array, origin: unknown) => { if (origin !== "remote") localVersion.current++ }
    sync.doc.on("update", changed)
    return () => { sync.doc.off("update", changed) }
  }, [sync.doc])
  const editor = useEditor({
    immediatelyRender: false, editable: false,
    extensions: [StarterKit.configure({ trailingNode: false, undoRedo: false, bold: false, italic: false, strike: false, code: false, codeBlock: false, blockquote: false, horizontalRule: false, hardBreak: false, link: false, underline: false, heading: { levels: [1, 2, 3] } }), Table.configure({ resizable: false }), TableRow, TableCell, TableHeader, StableIDs.configure({ types: stableBlockTypes(sync.state?.schema) }), ...documentEditorExtensions(sync.state?.schema), CommentHighlights, Collaboration.configure({ document: sync.doc, field: "default" })],
    // The browser moves the caret (Ctrl+End, arrows, a tap) before its selectionchange reaches ProseMirror. A key the
    // editor handles itself, such as Enter, must act where the caret is shown, so the DOM selection is read first.
    // Only for real key presses: ProseMirror replays a browser-made Enter or Backspace (iOS, Android) as a plain Event
    // in the middle of its own flush, where reading the DOM again would split twice or recurse.
    editorProps: { handleKeyDown: (view, event) => { if (event instanceof KeyboardEvent && !view.composing) (view as unknown as { domObserver: { flush(): void } }).domObserver.flush(); return false }, attributes: { class: editorClass, "aria-label": "Document editor" } },
  }, [sync.doc,sync.state?.schema])
  useEffect(() => { editor?.setEditable(sync.editable && !restoring) }, [editor, sync.editable, restoring])
  const empty = useEditorState({ editor, selector: ({ editor }) => editor ? isEmptyDocument(editor.state.doc) : false })
  useEffect(() => { onDirtyChange?.(sync.pending > 0 || sync.recovery); return () => onDirtyChange?.(false) }, [sync.pending, sync.recovery, onDirtyChange])
  // Collaboration's Yjs undo tracks only this editor's own transactions, so it never takes back confirmed work from others.
  const history = useEditorState({ editor, selector: ({ editor }) => ({ undo: editor?.can().undo() ?? false, redo: editor?.can().redo() ?? false }) })
  const restoreGuard = {
    blocked: sync.pending > 0 || sync.recovery || !sync.editable || restoring,
    onStart: () => { if (!restoring) restoreVersion.current = localVersion.current; editor?.setEditable(false); setRestoring(true) },
    onRefused: (denied: boolean) => { if (denied) sync.deny(); setRestoring(false) },
    onRestored: () => {
      if (localVersion.current !== restoreVersion.current) { setActionError("Restore is confirmed. Recover your local draft before reopening the saved document."); return }
      onRestored()
    },
  }
  const review: DocumentHistoryReview = { currentContent: sync.state?.content, headRev: sync.state?.rev ?? 0, hasLocalDraft, actions: reviewEnabled && sync.editable && !restoring ? revertActions : undefined, onDownloadDraft: downloadDraft, onActionDone: () => window.location.reload() }
  return { editor, restoring, actionError, setActionError, reviewEnabled, proposalClient, hasLocalDraft, downloadDraft, empty, history, restoreGuard, review }
}

function DocumentWorkspace({ workspaceId, pageId, humanId, slug, name, available, historyExpired, onDirtyChange, sync, onRestored }: DocumentWorkspaceProps) {
  const [linkAddress, setLinkAddress] = useState("")
  const { editor, restoring, actionError, setActionError, reviewEnabled, proposalClient, hasLocalDraft, downloadDraft, empty, history, restoreGuard, review } = useDocumentWorkspace({ workspaceId, pageId, slug, sync, onDirtyChange, onRestored })
  return <section aria-label="Doc page" className="touch-form space-y-3 overflow-auto">
    <div className="flex flex-wrap items-center gap-2 border-b p-3"><h1 className="mr-auto font-semibold">{name ?? slug}</h1><DocumentSaveState status={restoring ? "Restore confirmation pending · current editor preserved" : !available && !sync.denied ? "Documents are unavailable · local changes retained" : sync.status} durability={sync.durability} storageUnavailable={sync.storageUnavailable} storageBusy={sync.storageBusy} unverifiedDraft={sync.unverifiedDrafts.length > 0} lockless={sync.storageLockless} /></div>
    {(sync.error || actionError) && <p role="alert" className="px-6 text-sm text-destructive">{actionError ?? sync.error}</p>}
    <DocumentRecoveryAlert sync={sync} available={available} slug={slug} editor={editor} onError={setActionError} className="space-y-2 border p-4" />
    <DocumentEarlierDraftNotice sync={sync} slug={slug} onError={setActionError} className="space-y-2 border p-4" />
    <DocumentHistoryHost workspaceId={workspaceId} pageId={pageId} humanId={humanId} available={available} denied={sync.denied || historyExpired}
      review={review} head={sync.state} blocked={restoreGuard.blocked}
      onStart={restoreGuard.onStart} onRefused={restoreGuard.onRefused} onRestored={restoreGuard.onRestored} />
    {reviewEnabled && sync.state && !sync.denied && <div className="px-3"><DocumentProposals client={proposalClient} headRev={sync.state.rev} canDecide={sync.editable && !restoring} hasLocalDraft={hasLocalDraft} onDownloadDraft={downloadDraft} onAccepted={() => window.location.reload()} /></div>}
    {!sync.denied && <>
    {sync.editable && !restoring && editor && <div aria-label="Document formatting" className="flex flex-wrap gap-1 px-6">
      <Button size="sm" variant="ghost" disabled={!history?.undo} onClick={() => editor.chain().focus().undo().run()}>Undo</Button>
      <Button size="sm" variant="ghost" disabled={!history?.redo} onClick={() => editor.chain().focus().redo().run()}>Redo</Button>
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().setParagraph().run()}>Paragraph</Button>
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().toggleHeading({ level: 1 }).run()}>Heading 1</Button>
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().toggleHeading({ level: 2 }).run()}>Heading 2</Button>
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().toggleBulletList().run()}>Bullet list</Button>
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().toggleOrderedList().run()}>Numbered list</Button>
      {isRichSchema(sync.state?.schema) && <><Button size="sm" variant="ghost" onClick={()=>editor.chain().focus().toggleMark("code").run()}>Inline code</Button><Button size="sm" variant="ghost" onClick={()=>editor.chain().focus().toggleNode("codeBlock","paragraph",{id:editor.state.selection.$from.parent.attrs.id}).run()}>Code block</Button><Input aria-label="Link address" className="w-64" value={linkAddress} onChange={event=>setLinkAddress(event.target.value)} placeholder="https://example.com" /><Button size="sm" variant="ghost" onClick={()=>{const href=normalizeDocumentLink(linkAddress);if(!href){setActionError("Enter an HTTP or HTTPS link without credentials.");return};editor.chain().focus().setMark("link",{href}).run();setActionError(undefined)}}>Apply link</Button><Button size="sm" variant="ghost" onClick={()=>editor.chain().focus().unsetMark("link").run()}>Remove link</Button></>}
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run()}>Table</Button>
    </div>}
    {sync.state && empty && <p className="px-6 text-sm text-muted-foreground">{sync.editable && !restoring ? "This document is empty. Start writing below, or ask an agent in a chat to draft it." : "This document is empty."}</p>}
    {sync.state ? <EditorContent editor={editor} /> : <p className="p-6">{sync.status}</p>}
    </>}
  </section>
}

type DocumentSync = ReturnType<typeof usePageDocument>
type DraftNoticeProps = { sync: DocumentSync; slug: string; onError: (message: string) => void; className?: string }

/**
 * Unsaved or recovered work this tab cannot send: download it, then discard
 * or reopen. Nothing here sends a draft to the server.
 */
export function DocumentRecoveryAlert({ sync, available, slug, editor, onError, className }: DraftNoticeProps & { available: boolean; editor: ReturnType<typeof useEditor> | null }) {
  if (sync.denied || !(sync.recovery || (!available && sync.pending > 0))) return null
  return <div role="alert" className={className}><p>{sync.storageUnavailable || sync.unverifiedDrafts.length ? "Your unsaved changes are held only in this tab, not stored in this browser. Download them before closing it." : "Your unsaved draft is retained privately in this browser. Download it before discarding it."}</p><Button onClick={() => {
      try {
        const recovered = sync.recoveredDrafts.map(draft => {
          const local = documentSchema.load(draft.snapshot)
          try { return { schema: draft.schema, content: documentSchema.project(local) } } finally { local.destroy() }
        })
        downloadText(`${slug}-recovery.json`, JSON.stringify(recovered.length ? recovered : editor?.getJSON(), null, 2))
      } catch { onError("Could not read the recovery draft. It remains retained in this browser.") }
    }}>Download unsaved draft</Button><Button variant="outline" onClick={() => {
      if (sync.recoveredDrafts.length) void sync.discardRecoveredDrafts().catch(() => onError("Could not discard the recovery draft."))
      else window.location.reload()
    }}>{sync.recoveredDrafts.length ? "Discard recovered drafts and reopen" : "Reopen saved document"}</Button></div>
}

/** An earlier draft this browser could not prove is this tab's: download only, never restored or deleted here. */
export function DocumentEarlierDraftNotice({ sync, slug, onError, className }: DraftNoticeProps) {
  if (sync.denied || sync.unverifiedDrafts.length === 0) return null
  return <div role="status" className={className}><p>An earlier unsent draft is stored in this browser. This browser cannot tell whether another tab of this document is using it, so it was not restored and your edits here are not stored locally. You can keep editing or download the earlier draft; it cannot be deleted from here.</p><Button onClick={() => {
      try {
        const earlier = sync.unverifiedDrafts.map(draft => {
          const local = documentSchema.load(draft.snapshot)
          try { return { schema: draft.schema, content: documentSchema.project(local) } } finally { local.destroy() }
        })
        downloadText(`${slug}-earlier-draft.json`, JSON.stringify(earlier, null, 2))
      } catch { onError("Could not read the earlier draft. It remains stored in this browser.") }
    }}>Download earlier draft</Button></div>
}

/**
 * Confirmed checkpoints and restore. Pages opens it with its own History
 * button; a host with its own place for it (the Artifacts drawer) passes
 * `control` and the button is left out. Every fetch, guard and restore rule
 * is the same either way.
 */
export function DocumentHistoryHost({ workspaceId, pageId, humanId, available, denied, head, blocked, review, onStart, onRestored, onRefused, control }: {
  workspaceId: string; pageId: string; humanId: string; available: boolean; denied: boolean
  head?: ReturnType<typeof usePageDocument>["state"]; blocked: boolean; review?: DocumentHistoryReview
  onStart:()=>void; onRestored:()=>void; onRefused:(denied:boolean)=>void
  control?: { open: boolean; onClose: () => void }
}) {
  const [ownOpen, setOwnOpen] = useState(false)
  const open = control ? control.open : ownOpen
  const setOpen = (value: boolean) => { if (control) { if (!value) control.onClose() } else setOwnOpen(value) }
  const [openEpoch, setOpenEpoch] = useState(0)
  // A controlled host reads afresh every time it is opened, as the button does.
  useEffect(() => { if (control?.open) setOpenEpoch(value => value + 1) }, [control?.open])
  const opener = useRef<HTMLButtonElement>(null)
  const eligible = available && !denied
  // Closing a controlled host's drawer during a restore must not cancel it: it stays visible until it settles.
  const [settling, setSettling] = useState(false)
  const visible = (open || settling) && eligible
  const history = useDocumentHistory({ enabled: visible, workspaceId, humanId, pageId, activationKey: `history-open:${openEpoch}` })
  const restore = useDocumentHistoryRestore({ enabled: visible, workspaceId, humanId, pageId, activationKey: `history-open:${openEpoch}:denied:${denied}`,
    head, snapshot: history.snapshot, historyScopeKey: history.scopeKey, blocked, onStart, onRestored, onRefused })
  const restoring = restore.status === "pending" || restore.status === "uncertain"
  const controlled = Boolean(control)
  useEffect(() => { if (controlled) setSettling(restoring) }, [controlled, restoring])
  useEffect(() => { if (!eligible && !restoring && !controlled) setOwnOpen(false) }, [eligible, restoring, controlled])
  const close = () => { setOpen(false); opener.current?.focus() }
  if (control) return !eligible ? <p role="status" className="text-sm text-muted-foreground">History is unavailable.</p>
    : visible ? <DocumentHistory pageId={pageId} scopeKey={history.scopeKey} list={history.list} snapshot={history.snapshot} selectedId={history.selectedId} onSelect={history.onSelect} onClose={close} restore={restore} review={review} headSchema={head?.schema} /> : null
  return <div className="space-y-3 px-3">
    <Button ref={opener} type="button" variant="outline" disabled={!eligible || restoring} aria-expanded={visible} onClick={() => {
      if (!eligible || restoring) return
      if (open) close()
      else { setOpenEpoch(value => value + 1); setOpen(true) }
    }}>History</Button>
    {visible && <DocumentHistory pageId={pageId} scopeKey={history.scopeKey} list={history.list} snapshot={history.snapshot} selectedId={history.selectedId} onSelect={history.onSelect} onClose={close} restore={restore} review={review} headSchema={head?.schema} />}
  </div>
}
