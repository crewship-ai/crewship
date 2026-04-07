"use client"

import { useState, useRef, useEffect } from "react"
import { MessageSquare, ArrowDown } from "lucide-react"
import { cn } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"


export interface A2AMessage {
  id: string
  timestamp: string
  fromAgent: string
  fromCrew: string
  toAgent: string
  toCrew: string
  type: "@assign" | "@ask" | "@broadcast" | "@result"
  content: string
}

export interface A2AMessageStreamProps {
  messages: A2AMessage[]
  crewFilter: string | null
  onFilterChange: (crew: string | null) => void
}

const TYPE_COLORS: Record<A2AMessage["type"], string> = {
  "@assign": "bg-cyan-500/20 text-cyan-400",
  "@ask": "bg-violet-500/20 text-violet-400",
  "@result": "bg-emerald-500/20 text-emerald-400",
  "@broadcast": "bg-amber-500/20 text-amber-400",
}

function formatTimestamp(iso: string): string {
  try {
    const d = new Date(iso)
    return d.toLocaleTimeString("en-US", { hour12: false, hour: "2-digit", minute: "2-digit", second: "2-digit" })
  } catch {
    return "--:--:--"
  }
}

function getCrewPairs(messages: A2AMessage[]): string[] {
  const pairs = new Set<string>()
  for (const m of messages) {
    pairs.add(m.fromCrew)
    pairs.add(m.toCrew)
  }
  return Array.from(pairs).sort()
}

export function A2AMessageStream({ messages, crewFilter, onFilterChange }: A2AMessageStreamProps) {
  const [autoScroll, setAutoScroll] = useState(true)
  const scrollRef = useRef<HTMLDivElement>(null)

  const crewPairs = getCrewPairs(messages)

  const filtered = crewFilter
    ? messages.filter(m => m.fromCrew === crewFilter || m.toCrew === crewFilter)
    : messages

  // Newest first
  const sorted = [...filtered].reverse()

  useEffect(() => {
    if (autoScroll && scrollRef.current) {
      scrollRef.current.scrollTop = 0
    }
  }, [messages.length, autoScroll])

  if (messages.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full py-8 text-muted-foreground/70">
        <MessageSquare className="size-6 mb-2" />
        <p className="text-xs">No messages yet</p>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full">
      {/* Filter bar */}
      <div className="flex items-center gap-2 px-3 py-1.5 border-b border-border shrink-0">
        <select
          value={crewFilter ?? ""}
          onChange={e => onFilterChange(e.target.value || null)}
          aria-label="Filter by crew"
          className="bg-accent/50 border border-border rounded text-xs text-foreground/80 px-2 py-1 outline-none focus:border-white/20"
        >
          <option value="">All crews</option>
          {crewPairs.map(c => (
            <option key={c} value={c}>{c}</option>
          ))}
        </select>
        <div className="ml-auto flex items-center gap-1.5">
          <Button
            variant={autoScroll ? "secondary" : "ghost"}
            size="icon-xs"
            onClick={() => setAutoScroll(!autoScroll)}
            title={autoScroll ? "Auto-scroll on" : "Auto-scroll off"}
          >
            <ArrowDown className="size-3" />
          </Button>
          <span className="text-[10px] text-muted-foreground/70">{filtered.length} msgs</span>
        </div>
      </div>

      {/* Message list */}
      <div ref={scrollRef} className="flex-1 min-h-0 overflow-y-auto">
        <div className="divide-y divide-border">
          {sorted.map(msg => (
            <div key={msg.id} className="flex items-center gap-2 px-3 py-1.5 hover:bg-accent/30 transition-colors min-h-8">
              <span className="text-[10px] font-mono text-muted-foreground/70 shrink-0 w-16">
                {formatTimestamp(msg.timestamp)}
              </span>
              <span className="text-[11px] text-muted-foreground shrink-0 truncate max-w-[140px]">
                <span className="text-foreground/80">{msg.fromCrew}</span>
                <span className="text-muted-foreground/50 mx-1">{"\u2192"}</span>
                <span className="text-foreground/80">{msg.toCrew}</span>
              </span>
              <Badge className={cn("text-[9px] shrink-0", TYPE_COLORS[msg.type])}>
                {msg.type}
              </Badge>
              <span className="text-xs text-muted-foreground truncate min-w-0">
                {msg.content}
              </span>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
