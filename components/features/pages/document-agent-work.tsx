"use client"
import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import type { Editor } from "@tiptap/core"
import { captureDocumentSelection, captureDocumentStructureSelection, documentRootBlocks, type DocumentSelection, type DocumentStructureSelectionSnapshot } from "./document-selection"
import type { DocumentState } from "@/hooks/use-page-document"
import { DocumentAPIError, documentRequest } from "@/hooks/use-page-document"
interface AgentWork { id: string; status: string; run_id?: string; result_revision?: number; checkpoint_id?: string; error?: string; instruction: string; base_rev: number }
interface WorkState { requests: AgentWork[]; can_dispatch: boolean; eligible_chats: { chat_id: string; agent_name: string; title?: string }[] }
export function DocumentAgentWork({ workspaceId, slug, pageId, confirmed, editor, ready, disabled, onViewResult, onDirtyChange, denied=false, onAccessDenied }: { workspaceId: string; slug: string; pageId?: string; confirmed: DocumentState; editor: Editor | null; ready: boolean; disabled: boolean; onViewResult?: (checkpoint: string) => void; onDirtyChange?:(dirty:boolean)=>void;denied?:boolean;onAccessDenied?:()=>void }) {
  const [state, setState] = useState<WorkState>()
  const [chat, setChat] = useState("")
  const [instruction, setInstruction] = useState("")
  const [selection, setSelection] = useState<DocumentSelection | DocumentStructureSelectionSnapshot>()
  const [scopeMode,setScopeMode] = useState<"text"|"blocks">("text")
  const [checked,setChecked] = useState<string[]>([])
  const roots = documentRootBlocks(confirmed.content)
  const [submission, setSubmission] = useState<{ page_id: string; base_rev: number; targets?: DocumentSelection["targets"]; structure?: DocumentStructureSelectionSnapshot["structure"]; chat_id: string; instruction: string; submission_id: string }>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  useEffect(()=>{onDirtyChange?.(Boolean(instruction || selection || submission || busy || checked.length));return()=>onDirtyChange?.(false)},[instruction,selection,submission,busy,checked.length,onDirtyChange])
  useEffect(() => {
    if(denied){setState(undefined);return}
    let active = true
    async function refresh() { try { const result = await documentRequest<WorkState>(workspaceId, slug, "/requests"); if (active) setState(result) } catch (cause) { if (active) {if(cause instanceof DocumentAPIError && [401,403,404].includes(cause.status)){active=false;setState(undefined);onAccessDenied?.()};setError(cause instanceof Error ? cause.message : "Could not load agent requests.")} } }
    void refresh(); const timer = setInterval(() => void refresh(), 5000)
    return () => { active = false; clearInterval(timer) }
  }, [workspaceId, slug,denied,onAccessDenied])
  const stale = Boolean(selection && (selection.base_rev !== confirmed.rev || selection.epoch !== confirmed.epoch || selection.page_id !== (confirmed.page_id || pageId)))
  function capture() {
    try { setSelection(captureDocumentSelection(editor, confirmed, pageId, ready && !disabled)); setError(undefined) }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not capture selected text.") }
  }
  function captureBlocks(ids: string[]) {
    try { setSelection(captureDocumentStructureSelection(editor,confirmed,pageId,ready && !disabled,ids));setError(undefined) }
    catch(cause) { setError(cause instanceof Error ? cause.message : "Could not capture whole blocks.") }
  }
  function discard() { setInstruction("");setSelection(undefined);setSubmission(undefined);setChecked([]) }
  async function send() {
    if (!selection || !ready || disabled || (!submission && stale)) return
    const input = submission ?? { page_id: selection.page_id, base_rev: selection.base_rev, ...("structure" in selection ? {structure:selection.structure} : {targets:selection.targets}), chat_id: chat, instruction, submission_id: crypto.randomUUID() }
    setSubmission(input); setBusy(true); setError(undefined)
    try {
      await documentRequest(workspaceId, slug, "/requests", input)
      discard()
      try { setState(await documentRequest<WorkState>(workspaceId, slug, "/requests")) }
      catch (cause) { setError(`Request sent, but results could not be refreshed. ${cause instanceof Error ? cause.message : ""}`) }
    } catch (cause) { if(cause instanceof DocumentAPIError && [401,403,404].includes(cause.status)){setState(undefined);onAccessDenied?.()};setError(cause instanceof Error ? cause.message : "Could not send request.") }
    finally { setBusy(false) }
  }
  if(denied) return <section aria-label="Document agent work" className="space-y-2 p-6"><p>Access is unavailable. Your local agent instructions remain here.</p><Textarea aria-label="Retained local agent instructions" readOnly value={instruction}/><Button variant="ghost" onClick={discard}>Discard draft</Button></section>
  return <section aria-label="Document agent work" className="space-y-3 border-t p-6"><h2 className="font-medium">Agent work</h2><p className="text-xs text-muted-foreground">Choose a saved text selection or explicitly allow structural edits to whole blocks. Sending starts agent work.</p>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}{state?.can_dispatch && <form className="space-y-2" onSubmit={event => { event.preventDefault(); void send() }}><label className="block text-sm">Editing scope<select aria-label="Editing scope" className="mt-1 w-full rounded border bg-background p-2 coarse:min-h-12" value={scopeMode} disabled={disabled || busy || Boolean(selection) || Boolean(submission)} onChange={event=>{setScopeMode(event.target.value as "text"|"blocks");setChecked([]);setError(undefined)}}><option value="text">Selected text only</option><option value="blocks" disabled={confirmed.schema!==3}>Whole block structure (schema3)</option></select></label>
{scopeMode==="text" ? <Button type="button" variant="outline" disabled={disabled || !ready || busy || Boolean(selection)} onClick={capture}>Use selected text</Button> : <div className="space-y-2">
<p className="text-sm">Agent may add blocks directly after this selection, delete selected blocks, and reorder blocks inside it. Surrounding content stays unchanged. Moves of blocks with comments are unavailable; deleting a block retains its comment threads as detached.</p>
{!selection && <><Button type="button" variant="outline" disabled={disabled || !ready || busy || roots.length===0 || roots.length>256} onClick={()=>captureBlocks(roots.map(block=>block.id))}>Select all blocks</Button>{roots.length>256 && <p role="status">Select all is unavailable: this document has {roots.length} root blocks; at most 256 are supported per request. Choose a smaller contiguous range.</p>}<div aria-label="Whole blocks for structural editing" className="max-h-48 overflow-auto rounded border p-2">{roots.map((block,index)=><label key={block.id} className="flex min-h-8 items-center gap-2 coarse:min-h-12"><input type="checkbox" aria-label={`Select block ${index+1}: ${block.type}`} checked={checked.includes(block.id)} disabled={disabled || !ready || busy} onChange={event=>{if(event.target.checked && checked.length>=256){setError("Select at most 256 whole blocks; nothing was truncated.");return}setChecked(previous=>event.target.checked?[...previous,block.id]:previous.filter(id=>id!==block.id))}}/><span className="truncate text-sm">{index+1}. {block.type} · {block.text || "Empty block"}</span></label>)}</div><Button type="button" variant="outline" disabled={disabled || !ready || busy || checked.length===0} onClick={()=>captureBlocks(roots.filter(block=>checked.includes(block.id)).map(block=>block.id))}>Use selected blocks</Button></>}
</div>}
{selection && <div aria-label={"structure" in selection ? "Selected whole blocks for agent" : "Selected text for agent"} className="space-y-1 rounded border p-3"><p className="text-xs text-muted-foreground">Source revision {selection.base_rev}</p>{"structure" in selection ? <><p>{selection.blocks.length} whole blocks · structural permission includes insertion directly after selection</p><div className="max-h-40 overflow-auto">{selection.blocks.map(block=><p key={block.id} className="truncate text-sm">{block.type} · {block.text || "Empty block"}</p>)}</div></> : selection.targets.map(target=><blockquote key={target.id} className="border-l-2 pl-2 text-sm">{target.quote}</blockquote>)}</div>}
<label className="block text-sm">Private agent chat<select aria-label="Private agent chat" className="mt-1 w-full rounded border bg-background p-2" value={chat} disabled={disabled || busy || Boolean(submission)} onChange={event => setChat(event.target.value)}><option value="">Choose a chat</option>{state.eligible_chats.map(target => <option key={target.chat_id} value={target.chat_id}>{target.agent_name} · {target.title ?? "Private chat"}</option>)}</select></label><Textarea aria-label="Agent instructions" value={instruction} disabled={disabled || busy || Boolean(submission)} onChange={event => setInstruction(event.target.value)} />{stale && <p role="alert" className="text-sm">The document changed after this selection. {submission ? "Retry checks the original submission without changing its source." : "Discard this draft and select the saved text again."}</p>}<Button disabled={disabled || !ready || busy || !selection || !chat || !instruction.trim() || (!submission && stale)}>{busy ? "Sending…" : submission ? "Retry send" : "Send to agent"}</Button><Button type="button" variant="ghost" disabled={busy} onClick={discard}>Discard draft</Button></form>}{state?.requests.map(request => <article key={request.id} className="space-y-1 rounded border p-3 text-sm"><p className="font-medium">{request.status}{request.result_revision ? ` · Result revision ${request.result_revision}` : ""}</p><p>{request.instruction}</p><p className="text-xs text-muted-foreground">Source revision {request.base_rev}</p>{request.checkpoint_id && onViewResult && <Button size="sm" variant="outline" disabled={busy} onClick={() => onViewResult(request.checkpoint_id!)}>View result</Button>}{request.error && <p role="alert" className="text-destructive">{request.error}</p>}</article>)}</section>
}
