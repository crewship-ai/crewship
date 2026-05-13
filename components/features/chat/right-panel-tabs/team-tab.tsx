"use client"

import { Loader2, Users } from "lucide-react"
import { cn } from "@/lib/utils"
import { useAgentFetch } from "@/hooks/use-agent-fetch"

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
}

export interface TeamTabProps {
  agentId: string
  workspaceId: string | null
}

export function TeamTab({ agentId, workspaceId }: TeamTabProps) {
  const { data, loading, error } = useAgentFetch<TeamPayload>(
    async (signal) => {
      const r = await fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`, { signal })
      if (!r.ok) throw new Error(`agent fetch HTTP ${r.status}`)
      const agent = await r.json()
      // Defensive shape check — if the API returns an unexpected payload
      // (e.g. an error object), don't try to read .slug / .crew_id off it.
      if (!agent || typeof agent !== "object" || typeof agent.slug !== "string") {
        throw new Error("agent response malformed")
      }
      const crewId: string | null = agent.crew_id ?? null
      let messages: PeerMessage[] = []
      if (crewId) {
        const pr = await fetch(`/api/v1/crews/${crewId}/peer-conversations?workspace_id=${workspaceId}`, { signal })
        if (!pr.ok) throw new Error(`peer-conversations fetch HTTP ${pr.status}`)
        const all = await pr.json()
        // Filter to conversations involving this agent
        const list = Array.isArray(all) ? all : []
        messages = list.filter((m: PeerMessage) => m.from_slug === agent.slug || m.to_slug === agent.slug)
      }
      return { agentSlug: agent.slug, crewId, messages }
    },
    [agentId, workspaceId],
    { enabled: workspaceId !== null, logLabel: "TeamChatTab" },
  )

  if (loading) return <div className="flex items-center justify-center h-full"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div>

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
        <p className="text-label text-muted-foreground">Unable to load team conversations.</p>
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

  if (messages.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-center p-6">
        <Users className="h-8 w-8 text-muted-foreground/30 mb-2" />
        <p className="text-body font-medium text-muted-foreground">No conversations yet</p>
        <p className="text-label text-muted-foreground mt-1">Agent-to-agent conversations will appear here.</p>
      </div>
    )
  }

  return (
    <div className="p-2 space-y-2">
      {messages.map((msg) => {
        const isOutgoing = msg.from_slug === agentSlug
        return (
          <div key={msg.id} className="rounded-lg border border-border/50 p-2.5 space-y-1.5">
            <div className="flex items-center gap-1.5 text-micro">
              <span className={cn("font-medium", isOutgoing ? "text-blue-400" : "text-emerald-400")}>
                {msg.from_name}
              </span>
              <span className="text-muted-foreground/50">&rarr;</span>
              <span className="font-medium text-muted-foreground">{msg.to_name}</span>
              <span className="ml-auto text-muted-foreground/40">
                {new Date(msg.created_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
              </span>
            </div>
            <p className="text-xs text-foreground/80 whitespace-pre-wrap line-clamp-3">{msg.question}</p>
            {msg.response && (
              <div className="pl-2 border-l-2 border-emerald-500/30">
                <p className="text-xs text-muted-foreground whitespace-pre-wrap line-clamp-3">{msg.response}</p>
              </div>
            )}
            {msg.status === "RUNNING" && (
              <div className="flex items-center gap-1 text-micro text-blue-400">
                <Loader2 className="h-3 w-3 animate-spin" /> Processing...
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
