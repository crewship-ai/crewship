"use client"

import { useCallback, useState } from "react"
import { Button } from "@/components/ui/button"
import { Users } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"

interface PeerMessage {
  id: string
  from_name: string
  from_slug: string
  to_name: string
  to_slug: string
  question: string
  response: string | null
  status: string
  created_at: string
}

interface TeamPayload {
  agentSlug: string | null
  crewId: string | null
  messages: PeerMessage[]
  agents: { id: string; slug: string; name: string; status: string }[]
  errors: string[]
  people: { user_id: string; user?: { full_name: string | null; email: string } }[]
}

export interface TeamTabProps {
  agentId: string
  workspaceId: string | null
}

export const teamKeys = { all: (workspaceId: string | null) => ["chat-team", workspaceId] as const }

export function TeamTab({ agentId, workspaceId }: TeamTabProps) {
  const queryClient = useQueryClient()
  const refresh = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: teamKeys.all(workspaceId) })
  }, [queryClient, workspaceId])
  useRealtimeEventSafe("peer_conversation.updated", refresh)
  useRealtimeEventSafe("crew.updated", refresh)
  useRealtimeEventSafe("agent.status", refresh)
  useRealtimeEventSafe("agent.created", refresh)
  useRealtimeEventSafe("agent.updated", refresh)
  useRealtimeEventSafe("agent.deleted", refresh)
  const [page, setPage] = useState({ scope: `${workspaceId}:${agentId}`, offset: 0 })
  const scope = `${workspaceId}:${agentId}`
  const offset = page.scope === scope ? page.offset : 0
  const { data, isPending, error, refetch } = useQuery<TeamPayload>({
    queryKey: [...teamKeys.all(workspaceId), { agentId, offset }],
    enabled: workspaceId !== null,
    queryFn: async ({ signal }) => {
      const r = await apiFetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`, { signal })
      if (!r.ok) throw new Error(`agent fetch HTTP ${r.status}`)
      const agent = await r.json()
      // Defensive shape check — if the API returns an unexpected payload
      // (e.g. an error object), don't try to read .slug / .crew_id off it.
      if (!agent || typeof agent !== "object" || typeof agent.slug !== "string") {
        throw new Error("agent response malformed")
      }
      const crewId: string | null = agent.crew_id ?? null
      const readList = async (url: string) => {
        const response = await apiFetch(url, { signal })
        if (!response.ok) throw new Error(`team fetch HTTP ${response.status}`)
        const value = await response.json()
        if (!Array.isArray(value)) throw new Error("team response malformed")
        return value
      }
      if (!crewId) return { agentSlug: agent.slug, crewId, messages: [], agents: [], people: [], errors: [] }
      const query = `workspace_id=${encodeURIComponent(workspaceId!)}`
      const results = await Promise.allSettled([
        readList(`/api/v1/crews/${crewId}/peer-conversations?${query}&agent_id=${encodeURIComponent(agentId)}&limit=20&offset=${offset}`),
        readList(`/api/v1/agents?${query}&crew_id=${encodeURIComponent(crewId)}&limit=500`),
        readList(`/api/v1/crews/${crewId}/members?${query}`),
      ])
      const [messages, agents, people] = results.map((result) => result.status === "fulfilled" ? result.value : [])
      const labels = ["agent collaboration", "crew agents", "crew people"]
      const errors = results.flatMap((result, index) => result.status === "rejected" ? [labels[index]] : [])
      return { agentSlug: agent.slug, crewId, messages, agents, people, errors }
    },
  })
  const loading = workspaceId !== null && isPending

  if (loading) return <div className="flex items-center justify-center h-full"><Spinner className="h-5 w-5 text-muted-foreground" /></div>

  // Workspace-specific empty state — reachable when useWorkspace() returns
  // null. Must come BEFORE the !crewId fall-through so users don't see
  // "Assign agent to a crew…" when the real problem is "no workspace".
  if (!workspaceId) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-center p-6">
        <Users className="h-8 w-8 text-muted-foreground/30 mb-2" />
        <p className="text-label text-muted-foreground">Select a workspace to view team conversations.</p>
      </div>
    )
  }

  if (error || !data) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-center p-6">
        <Users className="h-8 w-8 text-muted-foreground/30 mb-2" />
        <p role="alert" className="text-label text-muted-foreground">Unable to load team.</p>
        <Button variant="outline" size="sm" onClick={() => { void refetch() }}>Try again</Button>
      </div>
    )
  }

  const { crewId, agentSlug, messages } = data

  if (!crewId) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-center p-6">
        <Users className="h-8 w-8 text-muted-foreground/30 mb-2" />
        <p className="text-label text-muted-foreground">Assign agent to a crew to see team conversations.</p>
      </div>
    )
  }

  return (
    <div className="p-3 space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-medium">Crew team</h3>
        <Button variant="ghost" size="sm" onClick={() => { void refetch() }}>Refresh</Button>
      </div>
      {data.errors.length > 0 && <div role="alert" className="space-y-2 text-xs text-muted-foreground">
        <p>Unable to load {data.errors.join(", ")}.</p>
        <Button variant="outline" size="sm" onClick={() => { void refetch() }}>Try again</Button>
      </div>}
      <section aria-label="Crew agents" className="space-y-2">
        <h4 className="text-xs text-muted-foreground">Agents</h4>
        {/* The exported chat route reads its slug at mount. A client Link can
            preserve ChatClient and leave the previous agent selected. */}
        {data.agents.map((agent) => (
          <a key={agent.id} href={`/chat/${encodeURIComponent(agent.slug)}`} className="flex justify-between gap-2 rounded-lg border p-2 text-sm hover:bg-accent">
            <span>{agent.name}</span><span className="text-xs text-muted-foreground">{agent.status}</span>
          </a>
        ))}
        {data.agents.length === 0 && !data.errors.includes("crew agents") && <p className="text-xs text-muted-foreground">No agents in this crew.</p>}
        {data.agents.length === 500 && <p className="text-xs text-muted-foreground">Showing the first 500 agents.</p>}
      </section>
      <section aria-label="Crew people" className="space-y-2">
        <h4 className="text-xs text-muted-foreground">People assigned to this crew</h4>
        {data.people.map((person) => <p key={person.user_id} className="text-sm">{person.user?.full_name || person.user?.email || "Workspace member"}</p>)}
        {data.people.length === 0 && !data.errors.includes("crew people") && <p className="text-xs text-muted-foreground">No people assigned to this crew.</p>}
      </section>
      <section aria-label="Agent collaboration" className="space-y-2 border-t pt-3">
        <h4 className="text-sm font-medium">Agent collaboration</h4>
        <p className="text-xs text-muted-foreground">Requests between this agent and its teammates, across conversations.</p>
        {messages.length === 0 && !data.errors.includes("agent collaboration") && <p className="text-xs text-muted-foreground">{offset ? "No more requests." : "No agent collaboration yet."}</p>}
      {messages.map((msg) => {
        const isOutgoing = msg.from_slug === agentSlug
        return (
          <div key={msg.id} className="rounded-lg border border-border/50 p-2.5 space-y-1.5">
            <div className="flex items-center gap-1.5 text-micro">
              <span className={cn("font-medium", isOutgoing ? "text-info" : "text-success")}>
                {msg.from_name}
              </span>
              <span className="text-muted-foreground-soft">&rarr;</span>
              <span className="font-medium text-muted-foreground">{msg.to_name}</span>
              <span className="ml-auto text-muted-foreground-soft">
                {new Date(msg.created_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
              </span>
            </div>
            <details>
              <summary className="cursor-pointer text-xs text-foreground/80">{msg.question.length > 120 ? `${msg.question.slice(0, 120)}…` : msg.question}</summary>
              <p className="mt-2 text-xs text-foreground/80 whitespace-pre-wrap break-words">{msg.question}</p>
            {msg.response && (
              <div className="pl-2 border-l-2 border-success/30">
                <p className="text-xs text-muted-foreground whitespace-pre-wrap break-words">{msg.response}</p>
              </div>
            )}
            </details>
            {msg.status === "RUNNING" && (
              <div className="flex items-center gap-1 text-micro text-primary">
                <Spinner className="h-3 w-3" /> Processing...
              </div>
            )}
          </div>
        )
      })}
        <div className="flex justify-between gap-2">
          <Button variant="outline" size="sm" disabled={offset === 0} onClick={() => setPage({ scope, offset: Math.max(0, offset - 20) })}>Newer</Button>
          <Button variant="outline" size="sm" disabled={messages.length < 20} onClick={() => setPage({ scope, offset: offset + 20 })}>Older</Button>
        </div>
      </section>
    </div>
  )
}
