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
import { DocumentAgentWork } from "./document-agent-work"
import { DocumentComments } from "./document-comments"
import { Button } from "@/components/ui/button"
import { DocumentAPIError, documentRequest, usePageDocument } from "@/hooks/use-page-document"

const StableIDs = Extension.create({
  name: "documentBlockIDs",
  addGlobalAttributes() { return [{ types: ["paragraph", "heading", "bulletList", "orderedList", "listItem", "table", "tableRow", "tableCell", "tableHeader"], attributes: { id: { default: null, parseHTML: element => element.getAttribute("data-block-id"), renderHTML: attributes => attributes.id ? { "data-block-id": attributes.id } : {} } } }] },
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
function readableDocument(value: unknown): string {
  if (!value || typeof value !== "object") return ""
  const node = value as { type?: string; text?: string; attrs?: { level?: number }; content?: unknown[] }
  if (node.type === "text") return node.text ?? ""
  const children = (node.content ?? []).map(readableDocument)
  if (node.type === "heading") return `${"#".repeat(node.attrs?.level ?? 1)} ${children.join("")}`
  if (node.type === "paragraph") return children.join("")
  if (node.type === "tableRow") return children.join(" | ")
  if (node.type === "listItem") return `• ${children.join("\n")}`
  return children.join("\n\n")
}
interface Checkpoint { id: string; rev: number; created_at?: string; content?: unknown }
function download(name: string, content: string) {
  const url = URL.createObjectURL(new Blob([content], { type: "text/plain;charset=utf-8" }))
  const link = document.createElement("a"); link.href = url; link.download = name; link.click(); URL.revokeObjectURL(url)
}

type DocumentProps = { workspaceId: string; slug: string; onDirtyChange?: (dirty: boolean) => void }
export function PageDocument(props: DocumentProps) {
  const sync = usePageDocument(props.workspaceId, props.slug)
  // Bind ProseMirror only after hydration, so its empty default paragraph is
  // never merged into a document which already contains saved content.
  if (!sync.state) return <p role={sync.denied ? "alert" : "status"} className="p-6">{sync.status}{sync.error ? ` · ${sync.error}` : ""}</p>
  return <DocumentWorkspace {...props} sync={sync} />
}
function DocumentWorkspace({ workspaceId, slug, onDirtyChange, sync }: DocumentProps & { sync: ReturnType<typeof usePageDocument> }) {
  const [history, setHistory] = useState<Checkpoint[] | null>(null)
  const [comparison, setComparison] = useState<Checkpoint | null>(null)
  const [actionError, setActionError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [restoring, setRestoring] = useState(false)
  const [restoreUncertain, setRestoreUncertain] = useState(false)
  const [checkpointAttempt, setCheckpointAttempt] = useState<{ id: string; schema: number; epoch: string | undefined; ifRev: number | undefined }>()
  const [checkpointNotice, setCheckpointNotice] = useState<string>()
  const editor = useEditor({
    immediatelyRender: false, editable: false,
    extensions: [StarterKit.configure({ undoRedo: false, bold: false, italic: false, strike: false, code: false, codeBlock: false, blockquote: false, horizontalRule: false, hardBreak: false, link: false, underline: false, heading: { levels: [1, 2, 3] } }), Table.configure({ resizable: false }), TableRow, TableCell, TableHeader, StableIDs, Collaboration.configure({ document: sync.doc, field: "default" })],
    editorProps: { attributes: { class: "min-h-64 max-w-none p-6 outline-none [&_p]:my-3 [&_h1]:my-4 [&_h1]:text-3xl [&_h1]:font-semibold [&_h2]:my-4 [&_h2]:text-2xl [&_h2]:font-semibold [&_h3]:my-3 [&_h3]:text-xl [&_ul]:list-disc [&_ul]:pl-6 [&_ol]:list-decimal [&_ol]:pl-6 [&_table]:w-full [&_table]:border-collapse [&_td]:border [&_td]:p-2 [&_th]:border [&_th]:bg-muted [&_th]:p-2", "aria-label": "Document editor" } },
  }, [sync.doc])
  useEffect(() => { editor?.setEditable(sync.editable && !restoring) }, [editor, sync.editable, restoring])
  useEffect(() => { onDirtyChange?.(sync.pending > 0 || restoring); return () => onDirtyChange?.(false) }, [sync.pending, restoring, onDirtyChange])
  useEffect(() => {
    if (!restoring) return
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = "" }
    window.addEventListener("beforeunload", warn)
    return () => window.removeEventListener("beforeunload", warn)
  }, [restoring])
  async function saveCheckpoint() {
    const input = checkpointAttempt ?? { id: crypto.randomUUID(), schema: 2, epoch: sync.state?.epoch, ifRev: sync.state?.rev }
    setCheckpointAttempt(input); setBusy(true); setActionError(undefined); setCheckpointNotice(undefined)
    try {
      await documentRequest(workspaceId, slug, "/checkpoint", input)
      setCheckpointAttempt(undefined); setCheckpointNotice(`Checkpoint saved at revision ${input.ifRev}.`)
      try { await loadHistory() } catch (cause) { setActionError(`Checkpoint saved, but history could not be refreshed. Use History to retry. ${cause instanceof Error ? cause.message : ""}`) }
    } catch (cause) {
      if (cause instanceof DocumentAPIError && cause.status >= 400 && cause.status < 500 && ![408, 429].includes(cause.status)) setCheckpointAttempt(undefined)
      else setCheckpointNotice(`Checkpoint result is uncertain. Retry uses the original revision ${input.ifRev}.`)
      setActionError(cause instanceof Error ? cause.message : "Could not save checkpoint.")
    } finally { setBusy(false) }
  }
  async function restore(checkpoint: string) {
    // Freeze immediately, before the asynchronous request can replace history.
    editor?.setEditable(false); setRestoring(true); setBusy(true); setActionError(undefined)
    try {
      await documentRequest(workspaceId, slug, "/restore", { id: crypto.randomUUID(), schema: 2, epoch: sync.state?.epoch, ifRev: sync.state?.rev, checkpoint })
      window.location.reload()
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : "Could not restore checkpoint.")
      if (cause instanceof DocumentAPIError && cause.status >= 400 && cause.status < 500 && ![408, 429].includes(cause.status)) setRestoring(false)
      else setRestoreUncertain(true)
    } finally { setBusy(false) }
  }
  async function action(run: () => Promise<void>) { setBusy(true); setActionError(undefined); try { await run() } catch (cause) { setActionError(cause instanceof Error ? cause.message : "Request failed.") } finally { setBusy(false) } }
  async function loadHistory() { const result = await documentRequest<{ checkpoints: Checkpoint[] } | Checkpoint[]>(workspaceId, slug, "/checkpoints"); setHistory(Array.isArray(result) ? result : result.checkpoints) }
  if (sync.denied) return <div className="p-6" role="alert">{sync.status}{sync.recovery && <Button onClick={() => download(`${slug}-recovery.json`, JSON.stringify(editor?.getJSON()))}>Download unsaved draft</Button>}</div>
  return <section aria-label="Doc page" className="space-y-3">
    <div className="flex flex-wrap items-center gap-2 border-b p-3">
      <span role="status" aria-live="polite" className="mr-auto text-sm">{restoring ? restoreUncertain ? "Restore result is uncertain" : "Restoring…" : sync.status}</span>
      <Button size="sm" variant="outline" disabled={!sync.state || busy || sync.pending > 0} onClick={() => void action(loadHistory)}>History</Button>
      <Button size="sm" variant="outline" disabled={!sync.editable || restoring || busy || sync.pending > 0 || sync.status !== "Saved"} onClick={() => void saveCheckpoint()}>{checkpointAttempt ? "Retry checkpoint" : "Save checkpoint"}</Button>
      <Button size="sm" variant="outline" disabled={!sync.state || busy} onClick={() => void action(async () => {
        const response = await import("@/lib/api-fetch").then(({ apiFetch }) => apiFetch(`/api/v1/pages/${encodeURIComponent(slug)}/document/export?workspace_id=${encodeURIComponent(workspaceId)}`))
        if (!response.ok) throw new Error("Could not export saved document.")
        download(`${slug}.md`, await response.text())
      })}>Export Markdown</Button>
    </div>
    {checkpointNotice && <p role="status" className="px-6 text-sm">{checkpointNotice}</p>}
    {(sync.error || actionError) && <p role="alert" className="px-6 text-sm text-destructive">{actionError ?? sync.error}</p>}
    {restoreUncertain && <div role="alert" className="space-y-2 border p-4"><p>The restore outcome is uncertain. Your current copy remains here. Reopen the saved document to check the result.</p><Button onClick={() => download(`${slug}-restore-recovery.json`, JSON.stringify(editor?.getJSON(), null, 2))}>Download current copy</Button><Button variant="outline" onClick={() => window.location.reload()}>Reopen saved document</Button></div>}
    {sync.recovery && <div role="alert" className="space-y-2 border p-4"><p>Your local draft is retained in this tab. Download it before reopening the current document.</p><Button onClick={() => download(`${slug}-recovery.json`, JSON.stringify(editor?.getJSON(), null, 2))}>Download unsaved draft</Button><Button variant="outline" onClick={() => window.location.reload()}>Reopen saved document</Button></div>}
    {sync.editable && !restoring && editor && <div aria-label="Document formatting" className="flex flex-wrap gap-1 px-6">
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().setParagraph().run()}>Paragraph</Button>
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().toggleHeading({ level: 1 }).run()}>Heading 1</Button>
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().toggleHeading({ level: 2 }).run()}>Heading 2</Button>
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().toggleBulletList().run()}>Bullet list</Button>
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().toggleOrderedList().run()}>Numbered list</Button>
      <Button size="sm" variant="ghost" onClick={() => editor.chain().focus().insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run()}>Table</Button>
    </div>}
    {sync.state ? <EditorContent editor={editor} /> : <p className="p-6">{sync.status}</p>}
    {sync.state && !sync.recovery && <DocumentComments workspaceId={workspaceId} slug={slug} rev={sync.state.rev} epoch={sync.state.epoch} editor={editor} disabled={busy || restoring || sync.pending > 0} />}
    {sync.state && !sync.recovery && <DocumentAgentWork workspaceId={workspaceId} slug={slug} rev={sync.state.rev} disabled={busy || restoring || sync.pending > 0 || !sync.editable} onViewResult={checkpoint => void action(async () => setComparison(await documentRequest<Checkpoint>(workspaceId, slug, `/checkpoints/${encodeURIComponent(checkpoint)}`)))} />}
    {history && <section aria-label="Document history" className="space-y-3 border-t p-6"><h2 className="font-medium">Checkpoints</h2>{!history.length && <p>No checkpoints yet.</p>}{history.map(checkpoint => <div key={checkpoint.id} className="flex gap-2 items-center"><span className="mr-auto text-sm">Revision {checkpoint.rev} · {checkpoint.created_at}</span><Button variant="outline" size="sm" disabled={busy} onClick={() => void action(async () => setComparison(await documentRequest<Checkpoint>(workspaceId, slug, `/checkpoints/${encodeURIComponent(checkpoint.id)}`)))}>Compare</Button><Button variant="outline" size="sm" disabled={!sync.editable || restoring || sync.pending > 0 || busy || sync.status !== "Saved"} onClick={() => void restore(checkpoint.id)}>Restore</Button></div>)}</section>}
    {comparison && <section aria-label="Compare checkpoint" className="border-t p-6"><h2 className="font-medium">Checkpoint revision {comparison.rev} and current revision {sync.state?.rev}</h2><div className="grid gap-4 md:grid-cols-2"><pre className="overflow-auto whitespace-pre-wrap text-xs">{readableDocument(comparison.content)}</pre><pre className="overflow-auto whitespace-pre-wrap text-xs">{readableDocument(sync.state?.content)}</pre></div><Button variant="ghost" onClick={() => setComparison(null)}>Close comparison</Button></section>}
  </section>
}
