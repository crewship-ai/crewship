"use client"

import { useEffect, useRef, useState } from "react"
import { Extension } from "@tiptap/core"
import { EditorContent, useEditor } from "@tiptap/react"
import StarterKit from "@tiptap/starter-kit"
import Collaboration from "@tiptap/extension-collaboration"
import { Table } from "@tiptap/extension-table"
import TableRow from "@tiptap/extension-table-row"
import TableHeader from "@tiptap/extension-table-header"
import TableCell from "@tiptap/extension-table-cell"
import { Plugin } from "@tiptap/pm/state"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { documentSchema, normalizeDocumentLink } from "@/lib/documents/schema"
import { useDocumentsAvailability } from "@/hooks/use-documents-availability"
import { DocumentSaveState } from "./document-save-state"
import { usePageDocument } from "@/hooks/use-page-document"
import { useSessionSafe } from "@/hooks/use-auth"
import { useDocumentHistory, useDocumentHistoryRestore } from "@/hooks/use-document-history"
import { DocumentHistory } from "./document-history/document-history"

const StableIDs = Extension.create({
  name: "documentBlockIDs",
  addOptions() { return {rich:false} },
  addGlobalAttributes() { return [{ types: ["paragraph", "heading", "bulletList", "orderedList", "listItem", "table", "tableRow", "tableCell", "tableHeader", ...(this.options.rich ? ["codeBlock"] : [])], attributes: { id: { default: null, parseHTML: element => element.getAttribute("data-block-id"), renderHTML: attributes => attributes.id ? { "data-block-id": attributes.id } : {} } } }] },
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
function download(name: string, content: string) {
  const url = URL.createObjectURL(new Blob([content], { type: "text/plain;charset=utf-8" }))
  const link = document.createElement("a"); link.href = url; link.download = name; link.click(); URL.revokeObjectURL(url)
}

type DocumentProps = { workspaceId: string; slug: string; pageId: string; name?: string; onDirtyChange?: (dirty: boolean) => void }
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
  if (!sync.state) return <p role={sync.denied ? "alert" : "status"} className="p-6">{sync.status}{sync.error ? ` · ${sync.error}` : ""}</p>
  return <DocumentWorkspace {...props} sync={sync} historyExpired={historyExpired} />
}
function DocumentWorkspace({ workspaceId, pageId, humanId, slug, name, available, historyExpired, onDirtyChange, sync, onRestored }: DocumentProps & { available:boolean; humanId:string; historyExpired:boolean; sync: ReturnType<typeof usePageDocument>; onRestored:()=>void }) {
  const [linkAddress, setLinkAddress] = useState("")
  const [actionError, setActionError] = useState<string>()
  const [restoring, setRestoring] = useState(false)
  const localVersion = useRef(0), restoreVersion = useRef(0)
  useEffect(() => {
    const changed = (_update: Uint8Array, origin: unknown) => { if (origin !== "remote") localVersion.current++ }
    sync.doc.on("update", changed)
    return () => { sync.doc.off("update", changed) }
  }, [sync.doc])
  const editor = useEditor({
    immediatelyRender: false, editable: false,
    extensions: [StarterKit.configure({ trailingNode: false, undoRedo: false, bold: false, italic: false, strike: false, code: false, codeBlock: false, blockquote: false, horizontalRule: false, hardBreak: false, link: false, underline: false, heading: { levels: [1, 2, 3] } }), Table.configure({ resizable: false }), TableRow, TableCell, TableHeader, StableIDs.configure({rich:sync.state?.schema===3}), ...(sync.state?.schema===3?documentSchema.extensionsFor(3).filter(extension=>["link","code","codeBlock"].includes(extension.name)):[]), Collaboration.configure({ document: sync.doc, field: "default" })],
    editorProps: { attributes: { class: "min-h-64 max-w-none p-6 outline-none [&_p]:my-3 [&_h1]:my-4 [&_h1]:text-3xl [&_h1]:font-semibold [&_h2]:my-4 [&_h2]:text-2xl [&_h2]:font-semibold [&_h3]:my-3 [&_h3]:text-xl [&_ul]:list-disc [&_ul]:pl-6 [&_ol]:list-decimal [&_ol]:pl-6 [&_table]:w-full [&_table]:border-collapse [&_td]:border [&_td]:p-2 [&_th]:border [&_th]:bg-muted [&_th]:p-2 [&_pre]:whitespace-pre-wrap [&_pre]:break-words [&_pre]:bg-muted [&_pre]:p-3 [&_code]:font-mono", "aria-label": "Document editor" } },
  }, [sync.doc,sync.state?.schema])
  useEffect(() => { editor?.setEditable(sync.editable && !restoring) }, [editor, sync.editable, restoring])
  useEffect(() => { onDirtyChange?.(sync.pending > 0 || sync.recovery); return () => onDirtyChange?.(false) }, [sync.pending, sync.recovery, onDirtyChange])
  return <section aria-label="Doc page" className="touch-form space-y-3 overflow-auto">
    <div className="flex flex-wrap items-center gap-2 border-b p-3"><h1 className="mr-auto font-semibold">{name ?? slug}</h1><DocumentSaveState status={restoring ? "Restore confirmation pending · current editor preserved" : !available && !sync.denied ? "Documents are unavailable · local changes retained" : sync.status} durability={sync.durability} /></div>
    {(sync.error || actionError) && <p role="alert" className="px-6 text-sm text-destructive">{actionError ?? sync.error}</p>}
    {!sync.denied && (sync.recovery || (!available && sync.pending > 0)) && <div role="alert" className="space-y-2 border p-4"><p>Your unsaved draft is retained privately in this browser. Download it before discarding it.</p><Button onClick={() => {
      try {
        const recovered = sync.recoveredDrafts.map(draft => {
          const local = documentSchema.load(draft.snapshot)
          try { return { schema: draft.schema, content: documentSchema.project(local) } } finally { local.destroy() }
        })
        download(`${slug}-recovery.json`, JSON.stringify(recovered.length ? recovered : editor?.getJSON(), null, 2))
      } catch { setActionError("Could not read the recovery draft. It remains retained in this browser.") }
    }}>Download unsaved draft</Button><Button variant="outline" onClick={() => {
      if (sync.recoveredDrafts.length) void sync.discardRecoveredDrafts().catch(() => setActionError("Could not discard the recovery draft."))
      else window.location.reload()
    }}>{sync.recoveredDrafts.length ? "Discard recovered drafts and reopen" : "Reopen saved document"}</Button></div>}
    <DocumentHistoryHost workspaceId={workspaceId} pageId={pageId} humanId={humanId} available={available} denied={sync.denied || historyExpired}
      head={sync.state} blocked={sync.pending > 0 || sync.recovery || !sync.editable || restoring}
      onStart={() => { if (!restoring) restoreVersion.current = localVersion.current; editor?.setEditable(false); setRestoring(true) }}
      onRefused={denied => { if (denied) sync.deny(); setRestoring(false) }}
      onRestored={() => {
        if (localVersion.current !== restoreVersion.current) { setActionError("Restore is confirmed. Recover your local draft before reopening the saved document."); return }
        onRestored()
      }} />
    {!sync.denied && <>
    {sync.editable && !restoring && editor && <div aria-label="Document formatting" className="flex flex-wrap gap-1 px-6">
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().setParagraph().run()}>Paragraph</Button>
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().toggleHeading({ level: 1 }).run()}>Heading 1</Button>
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().toggleHeading({ level: 2 }).run()}>Heading 2</Button>
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().toggleBulletList().run()}>Bullet list</Button>
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().toggleOrderedList().run()}>Numbered list</Button>
      {sync.state?.schema===3 && <><Button size="sm" variant="ghost" onClick={()=>editor.chain().focus().toggleMark("code").run()}>Inline code</Button><Button size="sm" variant="ghost" onClick={()=>editor.chain().focus().toggleNode("codeBlock","paragraph",{id:editor.state.selection.$from.parent.attrs.id}).run()}>Code block</Button><Input aria-label="Link address" className="w-64" value={linkAddress} onChange={event=>setLinkAddress(event.target.value)} placeholder="https://example.com" /><Button size="sm" variant="ghost" onClick={()=>{const href=normalizeDocumentLink(linkAddress);if(!href){setActionError("Enter an HTTP or HTTPS link without credentials.");return};editor.chain().focus().setMark("link",{href}).run();setActionError(undefined)}}>Apply link</Button><Button size="sm" variant="ghost" onClick={()=>editor.chain().focus().unsetMark("link").run()}>Remove link</Button></>}
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run()}>Table</Button>
    </div>}
    {sync.state ? <EditorContent editor={editor} /> : <p className="p-6">{sync.status}</p>}
    </>}
  </section>
}

function DocumentHistoryHost({ workspaceId, pageId, humanId, available, denied, head, blocked, onStart, onRestored, onRefused }: {
  workspaceId: string; pageId: string; humanId: string; available: boolean; denied: boolean
  head?: ReturnType<typeof usePageDocument>["state"]; blocked: boolean
  onStart:()=>void; onRestored:()=>void; onRefused:(denied:boolean)=>void
}) {
  const [open, setOpen] = useState(false)
  const [openEpoch, setOpenEpoch] = useState(0)
  const opener = useRef<HTMLButtonElement>(null)
  const eligible = available && !denied
  const visible = open && eligible
  const history = useDocumentHistory({ enabled: visible, workspaceId, humanId, pageId, activationKey: `history-open:${openEpoch}` })
  const restore = useDocumentHistoryRestore({ enabled: visible, workspaceId, humanId, pageId, activationKey: `history-open:${openEpoch}:denied:${denied}`,
    head, snapshot: history.snapshot, historyScopeKey: history.scopeKey, blocked, onStart, onRestored, onRefused })
  const restoring = restore.status === "pending" || restore.status === "uncertain"
  useEffect(() => { if (!eligible && !restoring) setOpen(false) }, [eligible, restoring])
  const close = () => { setOpen(false); opener.current?.focus() }
  return <div className="space-y-3 px-3">
    <Button ref={opener} type="button" variant="outline" disabled={!eligible || restoring} aria-expanded={visible} onClick={() => {
      if (!eligible || restoring) return
      if (open) close()
      else { setOpenEpoch(value => value + 1); setOpen(true) }
    }}>History</Button>
    {visible && <DocumentHistory pageId={pageId} scopeKey={history.scopeKey} list={history.list} snapshot={history.snapshot} selectedId={history.selectedId} onSelect={history.onSelect} onClose={close} restore={restore} />}
  </div>
}
