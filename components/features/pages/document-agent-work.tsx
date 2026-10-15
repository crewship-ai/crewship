"use client"
import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import type { Editor } from "@tiptap/core"
import { captureDocumentSelection, type DocumentSelection } from "./document-selection"
import type { DocumentState } from "@/hooks/use-page-document"
import { documentRequest } from "@/hooks/use-page-document"
interface AgentWork { id: string; status: string; run_id?: string; result_revision?: number; checkpoint_id?: string; error?: string; instruction: string; base_rev: number }
interface WorkState { requests: AgentWork[]; can_dispatch: boolean; eligible_chats: { chat_id: string; agent_name: string; title?: string }[] }
export function DocumentAgentWork({ workspaceId, slug, pageId, confirmed, editor, ready, disabled, onViewResult }: { workspaceId: string; slug: string; pageId?: string; confirmed: DocumentState; editor: Editor | null; ready: boolean; disabled: boolean; onViewResult?: (checkpoint: string) => void }) {
  const [state, setState] = useState<WorkState>()
  const [chat, setChat] = useState("")
  const [instruction, setInstruction] = useState("")
  const [selection, setSelection] = useState<DocumentSelection>()
  const [submission, setSubmission] = useState<{ page_id: string; base_rev: number; targets: DocumentSelection["targets"]; chat_id: string; instruction: string; submission_id: string }>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  useEffect(() => {
    let active = true
    async function refresh() { try { const result = await documentRequest<WorkState>(workspaceId, slug, "/requests"); if (active) setState(result) } catch (cause) { if (active) setError(cause instanceof Error ? cause.message : "Could not load agent requests.") } }
    void refresh(); const timer = setInterval(() => void refresh(), 5000)
    return () => { active = false; clearInterval(timer) }
  }, [workspaceId, slug])
  const stale = Boolean(selection && (selection.base_rev !== confirmed.rev || selection.epoch !== confirmed.epoch || selection.page_id !== (confirmed.page_id || pageId)))
  function capture() {
    try { setSelection(captureDocumentSelection(editor, confirmed, pageId, ready && !disabled)); setError(undefined) }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not capture selected text.") }
  }
  async function send() {
    if (!selection || !ready || disabled || (!submission && stale)) return
    const input = submission ?? { page_id: selection.page_id, base_rev: selection.base_rev, targets: selection.targets, chat_id: chat, instruction, submission_id: crypto.randomUUID() }
    setSubmission(input); setBusy(true); setError(undefined)
    try {
      await documentRequest(workspaceId, slug, "/requests", input)
      setInstruction(""); setSelection(undefined); setSubmission(undefined)
      try { setState(await documentRequest<WorkState>(workspaceId, slug, "/requests")) }
      catch (cause) { setError(`Request sent, but results could not be refreshed. ${cause instanceof Error ? cause.message : ""}`) }
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not send request.") }
    finally { setBusy(false) }
  }
  return <section aria-label="Document agent work" className="space-y-3 border-t p-6"><h2 className="font-medium">Agent work</h2><p className="text-xs text-muted-foreground">Select text within one saved paragraph or heading. Sending starts agent work; one paragraph or heading can be updated per request.</p>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}{state?.can_dispatch && <form className="space-y-2" onSubmit={event => { event.preventDefault(); void send() }}><Button type="button" variant="outline" disabled={disabled || !ready || busy || Boolean(selection)} onClick={capture}>Use selected text</Button>{selection && <div aria-label="Selected text for agent" className="space-y-1 rounded border p-3"><p className="text-xs text-muted-foreground">Source revision {selection.base_rev}</p>{selection.targets.map(target => <blockquote key={target.id} className="border-l-2 pl-2 text-sm">{target.quote}</blockquote>)}</div>}<label className="block text-sm">Private agent chat<select aria-label="Private agent chat" className="mt-1 w-full rounded border bg-background p-2" value={chat} disabled={busy || Boolean(submission)} onChange={event => setChat(event.target.value)}><option value="">Choose a chat</option>{state.eligible_chats.map(target => <option key={target.chat_id} value={target.chat_id}>{target.agent_name} · {target.title ?? "Private chat"}</option>)}</select></label><Textarea aria-label="Agent instructions" value={instruction} disabled={busy || Boolean(submission)} onChange={event => setInstruction(event.target.value)} />{stale && <p role="alert" className="text-sm">The document changed after this selection. {submission ? "Retry checks the original submission without changing its source." : "Discard this draft and select the saved text again."}</p>}<Button disabled={disabled || !ready || busy || !selection || !chat || !instruction.trim() || (!submission && stale)}>{busy ? "Sending…" : submission ? "Retry send" : "Send to agent"}</Button><Button type="button" variant="ghost" disabled={busy} onClick={() => { setInstruction(""); setSubmission(undefined); setSelection(undefined) }}>Discard draft</Button></form>}{state?.requests.map(request => <article key={request.id} className="space-y-1 rounded border p-3 text-sm"><p className="font-medium">{request.status}{request.result_revision ? ` · Result revision ${request.result_revision}` : ""}</p><p>{request.instruction}</p><p className="text-xs text-muted-foreground">Source revision {request.base_rev}</p>{request.checkpoint_id && onViewResult && <Button size="sm" variant="outline" disabled={busy} onClick={() => onViewResult(request.checkpoint_id!)}>View result</Button>}{request.error && <p role="alert" className="text-destructive">{request.error}</p>}</article>)}</section>
}
