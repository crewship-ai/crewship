"use client"

import { useParams } from "next/navigation"
import { useState, useEffect, useCallback, useRef } from "react"
import { Download, AlertCircle, Inbox, Search, Pause, Play } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"

const SECRET_RE = /(?:sk-[a-zA-Z0-9_-]{10,}|ghp_[a-zA-Z0-9]{36,}|gho_[a-zA-Z0-9]{36,}|xoxb-[a-zA-Z0-9-]+|AIza[a-zA-Z0-9_-]{35}|eyJ[a-zA-Z0-9_-]{20,}\.[a-zA-Z0-9_-]+)/g
function redactSecrets(s: string): string { return s.replace(SECRET_RE, "***") }

interface LogEntry {
  ts: string
  level: string
  agent: string
  event: string
  content?: string
  metadata?: Record<string, unknown>
}

type LogLevel = "ALL" | "INFO" | "WARN" | "ERROR"

const LEVELS: LogLevel[] = ["ALL", "INFO", "WARN", "ERROR"]

function formatLogTime(ts: string): string {
  try {
    const d = new Date(ts)
    return d.toISOString().slice(0, 19).replace("T", " ")
  } catch {
    return ts
  }
}

const LEVEL_COLORS: Record<string, string> = {
  INFO: "text-neutral-500",
  WARN: "text-amber-500",
  ERROR: "text-red-500",
}

const EVENT_COLORS: Record<string, string> = {
  status: "text-yellow-400",
  thinking: "text-neutral-500",
  text: "text-white",
  tool_call: "text-cyan-400",
  tool_result: "text-emerald-400",
  rate_limit: "text-amber-400",
  failover: "text-yellow-400",
  error: "text-red-400",
  result: "text-purple-400",
  system: "text-blue-400",
  image: "text-pink-400",
}

/** Agent logs viewer with dark terminal style, filtering, and auto-refresh. */
export function LogsPageClient() {
  const { agentId } = useParams<{ agentId: string }>()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [filter, setFilter] = useState<LogLevel>("ALL")
  const [searchQuery, setSearchQuery] = useState("")
  const autoScroll = true
  const [autoRefresh, setAutoRefresh] = useState(false)
  const logContainerRef = useRef<HTMLDivElement>(null)

  const fetchLogs = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/logs?workspace_id=${workspaceId}&limit=1000`)
      if (!res.ok) {
        setError("Failed to load logs")
        return
      }
      const data: LogEntry[] = await res.json()
      // Merge/dedupe with existing logs to avoid clobbering streamed entries
      setLogs((prev) => {
        if (prev.length === 0) return data
        const existing = new Set(prev.map((l) => `${l.ts}|${l.agent}|${l.event}`))
        const merged = [...prev]
        for (const entry of data) {
          if (!existing.has(`${entry.ts}|${entry.agent}|${entry.event}`)) {
            merged.push(entry)
          }
        }
        merged.sort((a, b) => a.ts.localeCompare(b.ts))
        return merged
      })
      setError(null)
    } catch {
      setError("Network error. Is the engine running?")
    } finally {
      setLoading(false)
    }
  }, [agentId, workspaceId])

  useEffect(() => {
    if (wsLoading) return
    if (!workspaceId) {
      setLoading(false)
      setError("No workspace selected")
      return
    }
    fetchLogs()
  }, [workspaceId, wsLoading, fetchLogs])

  useEffect(() => {
    if (!autoRefresh || !workspaceId) return
    const interval = setInterval(fetchLogs, 5000) // Fallback polling, slower
    return () => clearInterval(interval)
  }, [autoRefresh, workspaceId, fetchLogs])

  useRealtimeEvent("agent.log", (event) => {
    if (!autoRefresh) return
    const payload = event.payload
    if (payload.agent_id === agentId) {
      setLogs((prev) => [...prev, {
        ts: payload.ts,
        level: payload.level,
        agent: payload.agent,
        event: payload.event,
        content: payload.content,
        metadata: payload.metadata,
      }])
    }
  })

  useEffect(() => {
    if (autoScroll && logContainerRef.current) {
      logContainerRef.current.scrollTop = logContainerRef.current.scrollHeight
    }
  }, [logs, autoScroll])

  const filtered = logs.filter((l) => {
    if (filter !== "ALL" && l.level.toUpperCase() !== filter) return false
    if (searchQuery) {
      const q = searchQuery.toLowerCase()
      return (l.content ?? l.event).toLowerCase().includes(q) || l.event.toLowerCase().includes(q)
    }
    return true
  })

  const handleDownload = useCallback(() => {
    const text = logs.map((l) => `[${formatLogTime(l.ts)}] ${l.level.toUpperCase()} ${l.event} ${l.content ?? ""}`).join("\n")
    const blob = new Blob([text], { type: "text/plain" })
    const url = URL.createObjectURL(blob)
    const a = document.createElement("a")
    a.href = url
    a.download = `agent-${agentId}-logs.txt`
    a.click()
    URL.revokeObjectURL(url)
  }, [logs, agentId])

  if (wsLoading || loading) {
    return <LogsSkeleton />
  }

  if (error) {
    return (
      <div className="p-4 md:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-sm">{error}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full">
      {/* Log controls -- dark bar */}
      <div className="bg-neutral-900 dark:bg-neutral-950 border-b border-neutral-700 px-4 md:px-6 py-2 flex flex-wrap items-center gap-3 shrink-0">
        <div className="flex items-center gap-1">
          <span className="text-xs text-neutral-400 mr-1">Level:</span>
          {LEVELS.map((lvl) => (
            <button
              key={lvl}
              aria-pressed={filter === lvl}
              aria-label={`Filter by ${lvl}`}
              className={`px-2 py-0.5 rounded text-xs transition-colors ${
                filter === lvl
                  ? "bg-neutral-700 text-white font-medium"
                  : lvl === "WARN"
                    ? "text-amber-500 hover:bg-neutral-700"
                    : lvl === "ERROR"
                      ? "text-red-500 hover:bg-neutral-700"
                      : "text-neutral-400 hover:text-white hover:bg-neutral-700"
              }`}
              onClick={() => setFilter(lvl)}
            >
              {lvl}
            </button>
          ))}
        </div>

        {/* Search */}
        <div className="relative flex-1 max-w-xs ml-2">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-neutral-500" />
          <input
            aria-label="Search logs"
            type="text"
            placeholder="Search logs..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="w-full pl-8 pr-3 py-1.5 text-xs bg-neutral-800 border border-neutral-700 rounded text-white placeholder-neutral-500 focus:outline-none focus:ring-1 focus:ring-primary"
          />
        </div>

        <div className="ml-auto flex items-center gap-2">
          {autoRefresh && (
            <div className="flex items-center gap-1.5 text-xs text-[#4ECDC4]">
              <span className="h-1.5 w-1.5 rounded-full bg-[#4ECDC4] animate-pulse" />
              Auto-scroll ON
            </div>
          )}
          <button
            className={`px-2 py-1 rounded text-xs transition-colors ${
              autoRefresh ? "bg-primary text-white" : "bg-neutral-700 text-neutral-300 hover:text-white"
            }`}
            onClick={() => setAutoRefresh(!autoRefresh)}
          >
            {autoRefresh ? (
              <span className="flex items-center gap-1"><Pause className="h-3 w-3" /> Pause</span>
            ) : (
              <span className="flex items-center gap-1"><Play className="h-3 w-3" /> Stream</span>
            )}
          </button>
          <button
            className="px-2 py-1 rounded text-xs bg-neutral-700 text-neutral-300 hover:text-white flex items-center gap-1"
            onClick={handleDownload}
          >
            <Download className="h-3 w-3" /> Download
          </button>
        </div>
      </div>

      {/* Log viewer -- dark terminal */}
      {filtered.length === 0 ? (
        <div className="flex-1 bg-neutral-950 flex flex-col items-center justify-center text-center">
          <Inbox className="h-10 w-10 text-neutral-600 mb-3" />
          <p className="text-sm font-medium text-neutral-400">
            {logs.length === 0 ? "No logs yet" : "No matching logs"}
          </p>
          <p className="text-xs text-neutral-500 mt-1">
            {logs.length === 0 ? "Logs will appear here when the agent runs." : "Try a different filter."}
          </p>
        </div>
      ) : (
        <div
          ref={logContainerRef}
          className="flex-1 bg-neutral-950 overflow-y-auto px-4 md:px-6 py-4 font-mono text-xs leading-6"
        >
          {filtered.map((log, i) => {
            const level = log.level.toUpperCase()
            const levelColor = LEVEL_COLORS[level] ?? "text-neutral-500"
            const eventColor = EVENT_COLORS[log.event] ?? "text-neutral-400"
            const contentColor = level === "ERROR" ? "text-red-300" :
              level === "WARN" ? "text-amber-300" : "text-neutral-300"

            let extraInfo = ""
            if (log.event === "result" && log.metadata) {
              const m = log.metadata
              const parts: string[] = []
              if (m.total_cost_usd != null) parts.push(`$${Number(m.total_cost_usd).toFixed(4)}`)
              if (m.duration_ms != null) parts.push(`${(Number(m.duration_ms) / 1000).toFixed(1)}s`)
              if (m.num_turns != null) parts.push(`${m.num_turns} turns`)
              const usage = m.usage as Record<string, number> | undefined
              if (usage) {
                if (usage.input_tokens != null) parts.push(`in:${usage.input_tokens}`)
                if (usage.output_tokens != null) parts.push(`out:${usage.output_tokens}`)
              }
              if (parts.length) extraInfo = ` [${parts.join(" | ")}]`
            }
            if (log.event === "system" && log.metadata) {
              const m = log.metadata
              const parts: string[] = []
              if (m.model) parts.push(String(m.model))
              const tools = m.tools as string[] | undefined
              if (tools?.length) parts.push(`${tools.length} tools`)
              if (parts.length) extraInfo = ` [${parts.join(" | ")}]`
            }
            if (log.event === "tool_call" && log.metadata) {
              const input = log.metadata.input as Record<string, unknown> | undefined
              const toolName = (log.metadata.tool_name as string) ?? log.content ?? ""
              if (input) {
                switch (toolName) {
                  case "WebFetch": if (input.url) extraInfo = ` ${redactSecrets(String(input.url))}`; break
                  case "WebSearch": if (input.query) extraInfo = ` "${redactSecrets(String(input.query))}"`; break
                  case "Bash": if (input.command) { const cmd = redactSecrets(String(input.command)); extraInfo = ` $ ${cmd.slice(0, 80)}${cmd.length > 80 ? "..." : ""}`; } break
                  case "Read": case "Write": if (input.file_path) extraInfo = ` ${redactSecrets(String(input.file_path))}`; break
                  case "Edit": if (input.file_path) extraInfo = ` ${redactSecrets(String(input.file_path))}`; break
                  case "Grep": {
                    const parts: string[] = []
                    if (input.pattern) parts.push(`"${redactSecrets(String(input.pattern))}"`)
                    if (input.path) parts.push(`in ${redactSecrets(String(input.path))}`)
                    if (parts.length) extraInfo = ` ${parts.join(" ")}`
                    break
                  }
                  case "Glob": if (input.pattern) extraInfo = ` ${redactSecrets(String(input.pattern))}`; break
                  case "Task": if (input.description) extraInfo = ` ${redactSecrets(String(input.description))}`; break
                  case "AskUserQuestion": {
                    const qs = input.questions as { header: string }[] | undefined
                    if (qs?.[0]?.header) extraInfo = ` [${redactSecrets(String(qs[0].header))}]`
                    break
                  }
                  case "TodoWrite": {
                    const todos = input.todos as { status: string }[] | undefined
                    if (todos) {
                      const done = todos.filter((t) => t.status === "completed").length
                      extraInfo = ` ${done}/${todos.length} done`
                    }
                    break
                  }
                }
              }
            }

            return (
              <div key={i} className="flex gap-0 hover:bg-neutral-900/50">
                <span className="text-neutral-600 shrink-0">{formatLogTime(log.ts)}</span>
                <span className={`${levelColor} mx-2 shrink-0`}>[{level.toLowerCase()}]</span>
                <span className={`${eventColor} mr-2 shrink-0`}>{log.event}</span>
                <span className={contentColor}>{log.content ?? ""}</span>
                {extraInfo && <span className="text-neutral-500 ml-1">{extraInfo}</span>}
              </div>
            )
          })}
          {autoRefresh && (
            <div className="mt-2">
              <span className="inline-block w-2 h-4 bg-[#4ECDC4] animate-pulse align-middle" />
            </div>
          )}
        </div>
      )}

      {/* Log status bar */}
      <div className="bg-neutral-900 dark:bg-neutral-950 border-t border-neutral-700 px-4 md:px-6 py-2 flex items-center shrink-0">
        <span className="ml-auto text-xs text-neutral-600">
          {filtered.length} entr{filtered.length !== 1 ? "ies" : "y"}
          {filter !== "ALL" ? ` (${filter})` : ""}
        </span>
      </div>
    </div>
  )
}

function LogsSkeleton() {
  return (
    <div className="flex flex-col h-full">
      <div className="bg-neutral-900 px-4 md:px-6 py-2 flex items-center gap-2">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-6 w-14 bg-neutral-800" />
        ))}
        <div className="ml-auto flex gap-1">
          <Skeleton className="h-6 w-20 bg-neutral-800" />
          <Skeleton className="h-6 w-20 bg-neutral-800" />
        </div>
      </div>
      <div className="flex-1 bg-neutral-950 p-4 space-y-2">
        {Array.from({ length: 12 }).map((_, i) => (
          <Skeleton key={i} className={`h-5 bg-neutral-900 ${["w-3/4", "w-2/3", "w-4/5", "w-3/5", "w-2/3", "w-[70%]", "w-4/5", "w-3/5", "w-[65%]", "w-3/4", "w-2/3", "w-4/5"][i]}`} />
        ))}
      </div>
    </div>
  )
}
