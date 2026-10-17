"use client"

import { useEffect, useState } from "react"
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
import { usePageDocument } from "@/hooks/use-page-document"

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
  const availability = useDocumentsAvailability(props.workspaceId)
  const identity = `${props.workspaceId}:${props.pageId}`
  const [admitted,setAdmitted] = useState<string>()
  const available = availability.data === true
  useEffect(()=>{if(available)setAdmitted(identity)},[available,identity])
  if (!props.workspaceId || !props.pageId) return <p role="alert" className="p-6">The original Page identity is unavailable.</p>
  if (availability.isPending && admitted !== identity) return <p role="status" className="p-6">Checking document availability…</p>
  if (!available && admitted !== identity) return <p role="status" className="p-6">Documents are unavailable. Your Page remains in the list.</p>
  return <DocumentSession key={identity} {...props} available={available} />
}
function DocumentSession(props: DocumentProps & {available:boolean}) {
  const sync = usePageDocument(props.workspaceId, props.slug, props.pageId, props.available)
  // Bind ProseMirror only after hydration, so its empty default paragraph is
  // never merged into a document which already contains saved content.
  if (!sync.state) return <p role={sync.denied ? "alert" : "status"} className="p-6">{sync.status}{sync.error ? ` · ${sync.error}` : ""}</p>
  return <DocumentWorkspace {...props} sync={sync} />
}
function DocumentWorkspace({ slug, name, available, onDirtyChange, sync }: DocumentProps & { available:boolean; sync: ReturnType<typeof usePageDocument> }) {
  const [linkAddress, setLinkAddress] = useState("")
  const [actionError, setActionError] = useState<string>()
  const editor = useEditor({
    immediatelyRender: false, editable: false,
    extensions: [StarterKit.configure({ undoRedo: false, bold: false, italic: false, strike: false, code: false, codeBlock: false, blockquote: false, horizontalRule: false, hardBreak: false, link: false, underline: false, heading: { levels: [1, 2, 3] } }), Table.configure({ resizable: false }), TableRow, TableCell, TableHeader, StableIDs.configure({rich:sync.state?.schema===3}), ...(sync.state?.schema===3?documentSchema.extensionsFor(3).filter(extension=>["link","code","codeBlock"].includes(extension.name)):[]), Collaboration.configure({ document: sync.doc, field: "default" })],
    editorProps: { attributes: { class: "min-h-64 max-w-none p-6 outline-none [&_p]:my-3 [&_h1]:my-4 [&_h1]:text-3xl [&_h1]:font-semibold [&_h2]:my-4 [&_h2]:text-2xl [&_h2]:font-semibold [&_h3]:my-3 [&_h3]:text-xl [&_ul]:list-disc [&_ul]:pl-6 [&_ol]:list-decimal [&_ol]:pl-6 [&_table]:w-full [&_table]:border-collapse [&_td]:border [&_td]:p-2 [&_th]:border [&_th]:bg-muted [&_th]:p-2 [&_pre]:whitespace-pre-wrap [&_pre]:break-words [&_pre]:bg-muted [&_pre]:p-3 [&_code]:font-mono", "aria-label": "Document editor" } },
  }, [sync.doc,sync.state?.schema])
  useEffect(() => { editor?.setEditable(sync.editable) }, [editor, sync.editable])
  useEffect(() => { onDirtyChange?.(sync.pending > 0 || sync.recovery); return () => onDirtyChange?.(false) }, [sync.pending, sync.recovery, onDirtyChange])
  return <section aria-label="Doc page" className="touch-form space-y-3 overflow-auto">
    <div className="flex flex-wrap items-center gap-2 border-b p-3"><h1 className="mr-auto font-semibold">{name ?? slug}</h1><span role="status" aria-live="polite" className="text-sm">{!available && !sync.denied ? "Documents are unavailable · local changes retained" : sync.status}</span></div>
    {(sync.error || actionError) && <p role="alert" className="px-6 text-sm text-destructive">{actionError ?? sync.error}</p>}
    {(sync.recovery || (!available && sync.pending > 0)) && <div role="alert" className="space-y-2 border p-4"><p>Your local draft is retained in this tab. Download it before reopening the current document.</p><Button onClick={() => download(`${slug}-recovery.json`, JSON.stringify(editor?.getJSON(), null, 2))}>Download unsaved draft</Button><Button variant="outline" onClick={() => window.location.reload()}>Reopen saved document</Button></div>}
    {!sync.denied && <>
    {sync.editable && editor && <div aria-label="Document formatting" className="flex flex-wrap gap-1 px-6">
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
