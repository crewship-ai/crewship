"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { MessageSquare, Terminal } from "lucide-react"
import { cn } from "@/lib/utils"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

export const MSG_TYPE_COLORS: Record<string, string> = {
  "task.updated": "text-blue-400",
  "mission.updated": "text-purple-400",
  "mission.started": "text-emerald-400",
  "agent.log": "text-cyan-400",
}
export const MSG_TYPE_LABELS: Record<string, string> = {
  "task.updated": "task",
  "mission.updated": "mission",
  "mission.started": "started",
  "agent.log": "log",
}

export interface LiveMessage { ts: string; type: string; agent: string; crew: string; content: string; status?: string }

/** Live messages panel — streams task.updated + mission.updated WebSocket events */
export function LiveMessagesPanel() {
  const [messages, setMessages] = useState<LiveMessage[]>([])
  const [autoScroll, setAutoScroll] = useState(true)
  const endRef = useRef<HTMLDivElement>(null)

  const handleTaskUpdate = useCallback((ev: RealtimeEvent) => {
    const p = ev.payload
    const agent = (p.agent_slug ?? p.agent ?? "") as string
    const title = (p.title ?? p.task_title ?? "") as string
    const status = (p.status ?? "") as string
    if (!title) return
    setMessages((prev) => [...prev.slice(-150), {
      ts: new Date().toISOString(), type: "task.updated", agent,
      crew: (p.crew_name ?? "") as string,
      content: `${title} → ${status}`, status,
    }])
  }, [])

  const handleMissionUpdate = useCallback((ev: RealtimeEvent) => {
    const p = ev.payload
    const title = (p.title ?? "") as string
    const status = (p.status ?? "") as string
    if (!title) return
    setMessages((prev) => [...prev.slice(-150), {
      ts: new Date().toISOString(), type: "mission.updated", agent: (p.lead_agent_slug ?? "") as string,
      crew: "", content: `${title} → ${status}`, status,
    }])
  }, [])

  useRealtimeEvent("task.updated", handleTaskUpdate)
  useRealtimeEvent("mission.updated", handleMissionUpdate)

  useEffect(() => {
    if (autoScroll) endRef.current?.scrollIntoView({ behavior: "smooth" })
  }, [messages, autoScroll])

  if (messages.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground/50">
        <MessageSquare className="h-5 w-5 mb-1.5" />
        <p className="text-[11px]">No messages yet</p>
        <p className="text-[10px] text-foreground/30 mt-0.5">Task and mission updates appear here in real-time</p>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full">
      <div className="flex items-center justify-between px-3 py-1 border-b border-white/[0.06] shrink-0">
        <span className="text-[10px] text-muted-foreground">{messages.length} messages</span>
        <button onClick={() => setAutoScroll(!autoScroll)} className={cn("text-[10px] px-1.5 py-0.5 rounded", autoScroll ? "text-blue-400 bg-blue-400/10" : "text-muted-foreground")}>
          Auto-scroll {autoScroll ? "ON" : "OFF"}
        </button>
      </div>
      <div className="flex-1 overflow-y-auto font-mono text-[11px] px-3 py-1">
        {messages.map((msg, i) => (
          <div key={i} className="flex items-start gap-2 py-0.5 hover:bg-white/[0.02]">
            <span className="text-foreground/40 tabular-nums shrink-0 w-[52px]">{msg.ts.slice(11, 19)}</span>
            <span className={cn("shrink-0 text-[10px] px-1 rounded", MSG_TYPE_COLORS[msg.type] || "text-muted-foreground", "bg-white/[0.03]")}>
              {MSG_TYPE_LABELS[msg.type] || msg.type}
            </span>
            {msg.agent && <img src={getAgentAvatarUrl(msg.agent)} alt="" className="w-3.5 h-3.5 rounded-full shrink-0 mt-0.5" />}
            {msg.agent && <span className="text-muted-foreground shrink-0 w-[50px] truncate">@{msg.agent}</span>}
            <span className="text-foreground/80 truncate">{msg.content}</span>
          </div>
        ))}
        <div ref={endRef} />
      </div>
    </div>
  )
}

const EVENT_COLORS: Record<string, string> = {
  text: "text-foreground", thinking: "text-muted-foreground", tool_call: "text-cyan-400",
  tool_result: "text-emerald-400", error: "text-red-400", status: "text-amber-400",
  result: "text-purple-400", system: "text-blue-400", rate_limit: "text-amber-400",
}

interface LogEntry { ts: string; agent: string; event: string; content: string }

/** Live exec log panel — streams agent.log WebSocket events */
export function ExecLogPanel() {
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [autoScroll, setAutoScroll] = useState(true)
  const endRef = useRef<HTMLDivElement>(null)

  const handleLog = useCallback((ev: RealtimeEvent) => {
    const agent = (ev.payload.agent ?? ev.payload.agent_slug ?? "") as string
    const content = (ev.payload.content ?? "") as string
    const event = (ev.payload.event ?? "text") as string
    if (!content) return
    setLogs((prev) => [...prev.slice(-200), { ts: new Date().toISOString(), agent, event, content: content.length > 200 ? content.slice(0, 197) + "..." : content }])
  }, [])

  useRealtimeEvent("agent.log", handleLog)

  useEffect(() => {
    if (autoScroll) endRef.current?.scrollIntoView({ behavior: "smooth" })
  }, [logs, autoScroll])

  if (logs.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground/50">
        <Terminal className="h-5 w-5 mb-1.5" />
        <p className="text-[11px]">Waiting for agent activity...</p>
        <p className="text-[10px] text-foreground/30 mt-0.5">Logs appear here when agents run</p>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full">
      <div className="flex items-center justify-between px-3 py-1 border-b border-white/[0.06] shrink-0">
        <span className="text-[10px] text-muted-foreground">{logs.length} entries</span>
        <button onClick={() => setAutoScroll(!autoScroll)} className={cn("text-[10px] px-1.5 py-0.5 rounded", autoScroll ? "text-blue-400 bg-blue-400/10" : "text-muted-foreground")}>
          Auto-scroll {autoScroll ? "ON" : "OFF"}
        </button>
      </div>
      <div className="flex-1 overflow-y-auto font-mono text-[11px] px-3 py-1">
        {logs.map((log, i) => (
          <div key={i} className="flex items-start gap-2 py-0.5 hover:bg-white/[0.02]">
            <span className="text-foreground/40 tabular-nums shrink-0 w-[52px]">{log.ts.slice(11, 19)}</span>
            <img src={getAgentAvatarUrl(log.agent)} alt="" className="w-3.5 h-3.5 rounded-full shrink-0 mt-0.5" />
            <span className="text-muted-foreground shrink-0 w-[60px] truncate">@{log.agent}</span>
            <span className={cn("truncate", EVENT_COLORS[log.event] || "text-foreground")}>{log.content}</span>
          </div>
        ))}
        <div ref={endRef} />
      </div>
    </div>
  )
}
