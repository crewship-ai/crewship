"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { Activity } from "lucide-react"
import { cn } from "@/lib/utils"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

interface AgentData {
  id: string
  slug: string
  avatar_seed?: string | null
  avatar_style?: string | null
}

interface ActivityEntry {
  ts: string
  agent: string
  avatarSeed: string
  avatarStyle?: string | null
  content: string
  type: "status" | "run" | "mission"
}

interface CrewsActivityFeedProps {
  agents: AgentData[]
}

export function CrewsActivityFeed({ agents }: CrewsActivityFeedProps) {
  const [entries, setEntries] = useState<ActivityEntry[]>([])
  const endRef = useRef<HTMLDivElement>(null)

  const handleAgentStatus = useCallback((ev: RealtimeEvent) => {
    const slug = (ev.payload.agent_slug ?? ev.payload.slug ?? "") as string
    const status = (ev.payload.status ?? "") as string
    if (!slug || !status) return
    const agent = agents.find((a) => a.slug === slug)
    setEntries((prev) => [...prev.slice(-100), {
      ts: new Date().toISOString(), agent: slug,
      avatarSeed: agent?.avatar_seed || slug, avatarStyle: agent?.avatar_style,
      content: `Status \u2192 ${status}`, type: "status",
    }])
  }, [agents])

  const handleRunEvent = useCallback((ev: RealtimeEvent) => {
    const slug = (ev.payload.agent_slug ?? "") as string
    const status = (ev.payload.status ?? ev.type?.split(".")[1] ?? "") as string
    if (!slug) return
    const agent = agents.find((a) => a.slug === slug)
    setEntries((prev) => [...prev.slice(-100), {
      ts: new Date().toISOString(), agent: slug,
      avatarSeed: agent?.avatar_seed || slug, avatarStyle: agent?.avatar_style,
      content: `Run ${status}`, type: "run",
    }])
  }, [agents])

  const handleMissionUpdate = useCallback((ev: RealtimeEvent) => {
    const title = (ev.payload.title ?? "") as string
    const status = (ev.payload.status ?? "") as string
    if (!title) return
    setEntries((prev) => [...prev.slice(-100), {
      ts: new Date().toISOString(), agent: (ev.payload.lead_agent_slug ?? "") as string,
      avatarSeed: (ev.payload.lead_agent_slug ?? "mission") as string, avatarStyle: null,
      content: `Mission "${title}" \u2192 ${status}`, type: "mission",
    }])
  }, [])

  useRealtimeEvent("agent.status", handleAgentStatus)
  useRealtimeEvent("run.started", handleRunEvent)
  useRealtimeEvent("run.completed", handleRunEvent)
  useRealtimeEvent("run.failed", handleRunEvent)
  useRealtimeEvent("mission.updated", handleMissionUpdate)

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth" })
  }, [entries])

  const typeColors: Record<string, string> = {
    status: "text-blue-400", run: "text-emerald-400", mission: "text-purple-400",
  }

  if (entries.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground/50">
        <Activity className="h-5 w-5 mb-1.5" />
        <p className="text-[11px]">Waiting for activity...</p>
        <p className="text-[10px] text-muted-foreground/30 mt-0.5">Agent status changes and run events appear here in real-time</p>
      </div>
    )
  }

  return (
    <div className="font-mono text-[11px] px-3 py-1 h-full overflow-y-auto">
      {entries.map((entry, i) => (
        <div key={i} className="flex items-center gap-2 py-0.5 hover:bg-white/[0.02]">
          <span className="text-muted-foreground/40 tabular-nums shrink-0 w-[52px]">{entry.ts.slice(11, 19)}</span>
          <span className={cn("text-[10px] px-1 rounded bg-white/[0.03] shrink-0", typeColors[entry.type] || "text-muted-foreground")}>
            {entry.type}
          </span>
          <img src={getAgentAvatarUrl(entry.avatarSeed, entry.avatarStyle)} alt="" className="w-3.5 h-3.5 rounded-full shrink-0" />
          <span className="text-muted-foreground shrink-0 w-[60px] truncate">@{entry.agent}</span>
          <span className="text-foreground/80 truncate">{entry.content}</span>
        </div>
      ))}
      <div ref={endRef} />
    </div>
  )
}
