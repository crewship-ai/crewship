"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { Terminal } from "lucide-react"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import { cn } from "@/lib/utils"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { EVENT_COLORS, formatLogTime, redactSecrets } from "@/lib/utils/log-format"

interface TaskLiveLogsProps {
  agentSlug: string | null
  taskStatus: string
}

interface CompactLogEntry {
  ts: string
  event: string
  content: string
}

const MAX_ENTRIES = 30
const SYNC_INTERVAL_MS = 500

export function TaskLiveLogs({ agentSlug, taskStatus }: TaskLiveLogsProps) {
  const rawRef = useRef<CompactLogEntry[]>([])
  const dirtyRef = useRef(false)
  const hasReceivedRef = useRef(false)
  const scrollRef = useRef<HTMLDivElement>(null)
  const [entries, setEntries] = useState<CompactLogEntry[]>([])

  // Reset buffer when agent changes (task switch)
  useEffect(() => {
    rawRef.current = []
    dirtyRef.current = false
    hasReceivedRef.current = false
    setEntries([])
  }, [agentSlug])

  // Subscribe to agent.log events
  useRealtimeEvent("agent.log", useCallback((event: RealtimeEvent) => {
    const p = event.payload
    const slug = (p.agent ?? p.agent_slug) as string | undefined
    if (!slug || slug !== agentSlug) return

    hasReceivedRef.current = true
    rawRef.current.push({
      ts: (p.ts as string) || new Date().toISOString(),
      event: (p.event as string) || "text",
      content: redactSecrets(String(p.content ?? "")),
    })
    if (rawRef.current.length > MAX_ENTRIES) {
      rawRef.current = rawRef.current.slice(-MAX_ENTRIES)
    }
    dirtyRef.current = true
  }, [agentSlug]))

  // Throttled sync to state
  useEffect(() => {
    const interval = setInterval(() => {
      if (dirtyRef.current) {
        dirtyRef.current = false
        setEntries([...rawRef.current])
      }
    }, SYNC_INTERVAL_MS)
    return () => clearInterval(interval)
  }, [])

  // Auto-scroll to bottom
  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [entries])

  // Hide if task is done and we never received logs
  if (taskStatus !== "IN_PROGRESS" && !hasReceivedRef.current && entries.length === 0) {
    return null
  }

  const isActive = taskStatus === "IN_PROGRESS"

  return (
    <Collapsible defaultOpen={isActive}>
      <CollapsibleTrigger className="flex items-center gap-2 w-full text-left group">
        <Terminal className="h-4 w-4 text-blue-400 shrink-0" />
        <h4 className="text-xs font-semibold text-white/50 uppercase tracking-wider flex-1">
          Live Logs
        </h4>
        {isActive && (
          <span className="h-1.5 w-1.5 rounded-full bg-blue-400 animate-pulse" />
        )}
        {entries.length > 0 && (
          <span className="text-[10px] text-white/20">{entries.length}</span>
        )}
      </CollapsibleTrigger>
      <CollapsibleContent>
        {entries.length === 0 ? (
          <div className="mt-2 p-3 rounded-lg bg-black/40 border border-white/[0.04] text-center">
            <span className="text-[11px] text-white/20">
              {isActive ? "Waiting for logs..." : "No logs captured"}
            </span>
          </div>
        ) : (
          <div
            ref={scrollRef}
            className="mt-2 p-2 rounded-lg bg-black/40 border border-white/[0.04] max-h-[200px] overflow-y-auto font-mono text-[11px] leading-5"
          >
            {entries.map((log, i) => (
              <div key={i} className="flex gap-1.5 hover:bg-white/[0.02] px-1">
                <span className="text-white/20 shrink-0">
                  {formatLogTime(log.ts).slice(11)}
                </span>
                <span className={cn("shrink-0 min-w-[60px]", EVENT_COLORS[log.event] ?? "text-white/40")}>
                  {log.event}
                </span>
                <span className="text-white/50 truncate">{log.content}</span>
              </div>
            ))}
          </div>
        )}
      </CollapsibleContent>
    </Collapsible>
  )
}
