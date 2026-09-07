"use client"

import { useRef, useState } from "react"
import { ArrowRightLeft, UserRound, Bot } from "lucide-react"
import { Button } from "@/components/ui/button"
import { DetailCard } from "@/components/ui/detail"
import { hasIssueAgentDelegate } from "@/lib/issue-execution"
import { apiFetch } from "@/lib/api-fetch"
import { useIssuePeople } from "@/hooks/use-issue-people"
import type { MentionAgent } from "@/lib/mentions"
import type { Mission } from "@/lib/types/mission"

export function IssueWorkPanel({ issue, agents, editable, onChanged, latestOutcome }: {
  issue: Mission; agents: readonly MentionAgent[]; editable: boolean; onChanged: () => Promise<void>; latestOutcome?: string
}) {
  const [form, setForm] = useState<"handoff" | "submit" | null>(null)
  const [target, setTarget] = useState("")
  const [note, setNote] = useState("")
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const operation = useRef<{ body: string; id: string } | null>(null)
  const { people, error: peopleError } = useIssuePeople(issue.workspace_id, form === "handoff")
  const human = issue.work_mode === "human"
  const hasAgent = hasIssueAgentDelegate(issue)
  const needsInput = !human && latestOutcome === "NEEDS_HUMAN"
  const closed = ["DONE", "COMPLETED", "CANCELLED", "DUPLICATE"].includes(issue.status)
  const worker = human ? issue.worker_name || "Human worker" : issue.delegate?.name || issue.assignee_name || "Unassigned"
  async function act(action: string, targetId = "") {
    if (busy) return
    setBusy(true); setError(null)
    const body = JSON.stringify({ action, target_id: targetId, note, revision: issue.work_revision ?? 0 })
    if (operation.current?.body !== body) operation.current = { body, id: crypto.randomUUID() }
    try {
      const response = await apiFetch(`/api/v1/crews/${encodeURIComponent(issue.crew_id)}/issues/${encodeURIComponent(issue.identifier ?? issue.id)}/work?workspace_id=${encodeURIComponent(issue.workspace_id)}`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...JSON.parse(body), operation_id: operation.current.id }),
      })
      if (!response.ok) {
        const problem = await response.json().catch(() => null)
        if (response.status === 409) await onChanged()
        throw new Error(problem?.detail || problem?.error || "Could not transfer work")
      }
      await onChanged(); setForm(null); setNote(""); operation.current = null
    } catch (err) { setError(err instanceof Error ? err.message : "Could not transfer work") }
    finally { setBusy(false) }
  }
  async function retryStop() {
    if (busy) return
    setBusy(true); setError(null)
    try {
      const res = await apiFetch(`/api/v1/crews/${encodeURIComponent(issue.crew_id)}/issues/${encodeURIComponent(issue.identifier ?? issue.id)}/stop?hard=true&workspace_id=${encodeURIComponent(issue.workspace_id)}`, {method:"POST"})
      if (!res.ok) {const body = await res.json().catch(() => null); throw new Error(body?.detail || body?.error || "Could not stop agents")}
      await onChanged()
    } catch(err) {setError(err instanceof Error ? err.message : "Could not stop agents")} finally {setBusy(false)}
  }
  return <DetailCard title="Current work" icon={human ? UserRound : Bot}>
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0">
        <p className="text-sm font-medium">{issue.work_stopping ? "Taking over · stopping agent runs" : human ? "With a person" : needsInput ? "Needs your input" : hasAgent ? "With an agent" : "Ready to assign"} · {worker}</p>
        <p className="mt-1 text-xs text-muted-foreground">{issue.work_stopping ? "Work can be handed back once the running agents have stopped." : human ? "Automatic agent work is paused. Submit a result or hand the issue back when ready." : needsInput ? "The agent is waiting for an answer. Open the request in Inbox, or take over this issue." : hasAgent ? "The agent handles execution. Use a handoff to change who works next." : "Choose a person or agent to move this issue forward."}</p>
        {issue.owner && <p className="mt-1 text-xs text-muted-foreground">Accountable owner: {issue.owner.name || "Workspace member"}</p>}
        <a href="#issue-conversation" className="mt-2 inline-block text-xs text-primary hover:underline">Conversation and handoffs</a>
      </div>
      {editable && !closed && <div className="flex flex-wrap gap-2">
        {issue.work_stopping && <Button size="sm" variant="outline" disabled={busy} onClick={() => void retryStop()}>Retry stopping agents</Button>}
        {needsInput && <Button size="sm" asChild><a href="/inbox">Open request in Inbox</a></Button>}
        {!human && <Button size="sm" variant="outline" disabled={busy} onClick={() => void act("take_over")}>Take over</Button>}
        <Button size="sm" variant="outline" disabled={busy || issue.work_stopping} onClick={() => { setForm("handoff"); setError(null) }}><ArrowRightLeft className="mr-1 h-3.5 w-3.5" />Hand off</Button>
        {human && <Button size="sm" disabled={busy || issue.work_stopping} onClick={() => { setForm("submit"); setError(null) }}>Submit result</Button>}
      </div>}
    </div>
    {issue.work_note && <details className="mt-3 text-xs"><summary className="cursor-pointer text-muted-foreground">Latest handoff or result</summary><p className="mt-2 whitespace-pre-wrap break-words">{issue.work_note}</p></details>}
    {form && <form className="mt-4 space-y-3 border-t border-border pt-3" onSubmit={(event) => {
      event.preventDefault()
      if (form === "submit") void act("submit")
      else { const [type, id] = target.split(":"); void act(type === "agent" ? "handoff_agent" : "handoff_human", id) }
    }}>
      {form === "handoff" && <label className="block text-xs">Next worker
        <select required value={target} disabled={busy} onChange={(e) => setTarget(e.target.value)} className="mt-1 block h-9 w-full rounded-md border border-input bg-background px-2 text-sm">
          <option value="">Choose a person or agent</option>
          <optgroup label="Agents">{agents.map((agent) => <option key={agent.id} value={`agent:${agent.id}`}>{agent.name}</option>)}</optgroup>
          <optgroup label="People">{people.map((person) => <option key={person.id} value={`user:${person.id}`}>{person.name}</option>)}</optgroup>
        </select>
        {peopleError && <span role="alert" className="text-destructive">People could not be loaded. Reopen the handoff to retry.</span>}
      </label>}
      <label className="block text-xs">{form === "submit" ? "Result and how you verified it" : "What is done, what is needed next, and relevant files"}
        <textarea required maxLength={20000} rows={4} value={note} disabled={busy} onChange={(e) => setNote(e.target.value)} className="mt-1 block w-full rounded-md border border-input bg-background p-2 text-sm" />
      </label>
      <div className="flex gap-2"><Button size="sm" type="submit" disabled={busy || !note.trim()}>{busy ? "Saving…" : form === "submit" ? "Send for review" : "Hand off work"}</Button><Button size="sm" type="button" variant="ghost" disabled={busy} onClick={() => setForm(null)}>Cancel</Button></div>
      {form === "handoff" && <p className="text-xs text-muted-foreground">An agent handoff prepares the issue for Start work. Completed results stay in its history.</p>}
    </form>}
    {error && <p role="alert" className="mt-3 text-xs text-destructive">{error}</p>}
  </DetailCard>
}
