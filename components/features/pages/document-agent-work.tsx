"use client"
import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { documentRequest } from "@/hooks/use-page-document"
interface AgentWork { id: string; status: string; run_id?: string; result_revision?: number; checkpoint_id?: string; error?: string; instruction: string; base_rev: number }
interface WorkState { requests: AgentWork[]; can_dispatch: boolean; eligible_chats: { chat_id: string; agent_name: string; title?: string }[] }
export function DocumentAgentWork({ workspaceId, slug, rev, disabled, onViewResult }: { workspaceId: string; slug: string; rev: number; disabled: boolean; onViewResult?: (checkpoint: string) => void }) {
  const [state, setState] = useState<WorkState>()
  const [chat, setChat] = useState("")
  const [instruction, setInstruction] = useState("")
  const [source, setSource] = useState<number>()
  const [submission, setSubmission] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  useEffect(() => {
    let active = true
    async function refresh() { try { const result = await documentRequest<WorkState>(workspaceId, slug, "/requests"); if (active) setState(result) } catch (cause) { if (active) setError(cause instanceof Error ? cause.message : "Could not load agent requests.") } }
    void refresh(); const timer = setInterval(() => void refresh(), 5000)
    return () => { active = false; clearInterval(timer) }
  }, [workspaceId, slug])
  async function send() {
    const id = submission ?? crypto.randomUUID(); setSubmission(id); setBusy(true); setError(undefined)
    try { await documentRequest(workspaceId, slug, "/requests", { chat_id: chat, instruction, base_rev: source ?? rev, submission_id: id }); setInstruction(""); setSource(undefined); setSubmission(undefined); setState(await documentRequest<WorkState>(workspaceId, slug, "/requests")) } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not send request.") } finally { setBusy(false) }
  }
  return <section aria-label="Document agent work" className="space-y-3 border-t p-6"><h2 className="font-medium">Agent work</h2><p className="text-xs text-muted-foreground">Sending starts agent work using the saved document revision.</p>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}{state?.can_dispatch && <form className="space-y-2" onSubmit={event => { event.preventDefault(); void send() }}><label className="block text-sm">Private agent chat<select aria-label="Private agent chat" className="mt-1 w-full rounded border bg-background p-2" value={chat} disabled={busy || Boolean(submission)} onChange={event => setChat(event.target.value)}><option value="">Choose a chat</option>{state.eligible_chats.map(target => <option key={target.chat_id} value={target.chat_id}>{target.agent_name} · {target.title ?? "Private chat"}</option>)}</select></label><Textarea aria-label="Agent instructions" value={instruction} disabled={busy || Boolean(submission)} onChange={event => { setInstruction(event.target.value); if (source === undefined) setSource(rev) }} /><p className="text-xs text-muted-foreground">Source revision {source ?? rev}</p>{source !== undefined && source !== rev && <p role="alert" className="text-sm">The document changed. Discard this request draft and start again from the latest revision.</p>}<Button disabled={disabled || busy || !chat || !instruction.trim() || (source !== undefined && source !== rev)}>{busy ? "Sending…" : submission ? "Retry send" : "Send to agent"}</Button><Button type="button" variant="ghost" disabled={busy} onClick={() => { setInstruction(""); setSubmission(undefined); setSource(undefined) }}>Discard draft</Button></form>}{state?.requests.map(request => <article key={request.id} className="space-y-1 rounded border p-3 text-sm"><p className="font-medium">{request.status}{request.result_revision ? ` · Result revision ${request.result_revision}` : ""}</p><p>{request.instruction}</p><p className="text-xs text-muted-foreground">Source revision {request.base_rev}</p>{request.checkpoint_id && onViewResult && <Button size="sm" variant="outline" disabled={busy} onClick={() => onViewResult(request.checkpoint_id!)}>View result</Button>}{request.error && <p role="alert" className="text-destructive">{request.error}</p>}</article>)}</section>
}
