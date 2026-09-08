"use client"

import { ConversationIdentity } from "./conversation-identity"
import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"
import { conversationRequest } from "@/hooks/use-workspace-conversations"

export interface JoinedAgent { agent_id: string; name: string; slug: string; joined_at: string; avatar_url?: string | null; avatar_style?: string | null; avatar_seed?: string | null }
export function ConversationAgents({ workspaceId, conversationId, agents, canManage, refresh }: { workspaceId: string; conversationId: string; agents: JoinedAgent[]; canManage: boolean; refresh: () => void }) {
  const [selected, setSelected] = useState("")
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const roster = useQuery({ queryKey: ["conversation-agent-roster", workspaceId], enabled: canManage, queryFn: ({ signal }) => conversationRequest<{ id: string; name: string; slug: string }[]>(workspaceId, "agents?limit=500", undefined, signal) })
  async function change(removeId?: string) {
    if (busy) return
    setBusy(true); setError(null)
    try {
      if (removeId) {
        const response = await apiFetch(`/api/v1/conversations/${encodeURIComponent(conversationId)}/agents/${encodeURIComponent(removeId)}?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "DELETE" })
        if (!response.ok) throw new Error("Unable to remove agent.")
      } else await conversationRequest(workspaceId, `conversations/${encodeURIComponent(conversationId)}/agents`, { agent_id: selected })
      setSelected(""); refresh()
    } catch (e) { setError(e instanceof Error ? e.message : "Unable to update agents.") }
    finally { setBusy(false) }
  }
  return <section className="space-y-3 border-t pt-3"><h3 className="text-sm font-medium">Channel agents</h3><p className="text-xs text-muted-foreground">Adding an agent grants access to this channel’s history. Agent tasks and results are visible in workspace activity. Agents respond only when explicitly selected in a message.</p>
    {agents.map((agent) => <div key={agent.agent_id} className="flex items-center justify-between gap-2 text-sm"><ConversationIdentity id={agent.agent_id} name={agent.name} slug={agent.avatar_seed || agent.slug} avatarUrl={agent.avatar_url} avatarStyle={agent.avatar_style} agent /><span className="min-w-0 flex-1">{agent.name} <span className="text-muted-foreground">@{agent.slug || agent.name}</span></span>{canManage && <Button type="button" variant="ghost" size="sm" disabled={busy} onClick={() => { if (window.confirm(`Remove ${agent.name} from this channel?`)) void change(agent.agent_id) }}>Remove</Button>}</div>)}
    {canManage && <><div className="flex gap-2"><select aria-label="Agent to add" value={selected} onChange={(e) => setSelected(e.target.value)} className="min-w-0 flex-1 rounded-md border bg-background p-2 text-sm"><option value="">Choose an agent…</option>{roster.data?.filter((agent) => !agents.some((a) => a.agent_id === agent.id)).map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</select><Button type="button" disabled={!selected || busy} onClick={() => { void change() }}>Add agent</Button></div>{roster.error && <div role="alert" className="text-sm">Unable to load workspace agents. <Button type="button" variant="ghost" onClick={() => { void roster.refetch() }}>Retry</Button></div>}{roster.data?.length === 500 && <p className="text-xs text-muted-foreground">Showing the first 500 agents.</p>}</>}
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
  </section>
}
