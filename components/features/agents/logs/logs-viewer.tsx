"use client"

import { useAgentId } from "@/hooks/use-agent-id"
import { useState, useEffect, useCallback, useRef } from "react"
import { Download, AlertCircle, ScrollText, Search, Pause, Play } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { EmptyState } from "@/components/layout/empty-state"
import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"

import {
  redactSecrets,
  formatLogTime,
  LEVEL_COLORS,
  EVENT_COLORS,
  type LogEntry,
} from "@/lib/utils/log-format"

type LogLevel = "ALL" | "INFO" | "WARN" | "ERROR"

const LEVELS: LogLevel[] = ["ALL", "INFO", "WARN", "ERROR"]

/** Agent logs viewer with dark terminal style, filtering, and auto-refresh. */
export function LogsViewer() {
  const agentId = useAgentId()
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

  // Real-time: stream individual log entries when autoRefresh is on
  useRealtimeEvent("agent.log", useCallback((event: RealtimeEvent) => {
    if (!autoRefresh) return
    const payload = event.payload
    if (payload.agent_id === agentId) {
      const entry: LogEntry = {
        ts: String(payload.ts ?? ""),
        level: String(payload.level ?? ""),
        agent: String(payload.agent ?? ""),
        event: String(payload.event ?? ""),
        content: typeof payload.content === "string" ? payload.content : undefined,
        metadata: (payload.metadata && typeof payload.metadata === "object")
          ? (payload.metadata as Record<string, unknown>)
          : undefined,
      }
      setLogs((prev) => [...prev, entry])
    }
  }, [autoRefresh, agentId]))

  // Real-time: refetch full log when this agent's runs start/complete
  useRealtimeEvent("run.started", useCallback((event: RealtimeEvent) => {
    if (event.payload.agent_id === agentId) fetchLogs()
  }, [fetchLogs, agentId]))
  useRealtimeEvent("run.completed", useCallback((event: RealtimeEvent) => {
    if (event.payload.agent_id === agentId) fetchLogs()
  }, [fetchLogs, agentId]))

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
      <div className="p-4 sm:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-body">{error}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full p-4 sm:p-6 gap-4">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-title font-semibold">Logs</h2>
      </div>

      {/* Log controls */}
      <div className="bg-card border border-border rounded-t-lg px-4 py-2 flex flex-wrap items-center gap-3 shrink-0">
        <div className="flex items-center gap-1">
          <span className="text-label text-muted-foreground mr-1">Level:</span>
          {LEVELS.map((lvl) => (
            <button
              key={lvl}
              aria-pressed={filter === lvl}
              aria-label={`Filter by ${lvl}`}
              className={cn(
                "px-2 py-0.5 rounded text-label transition-colors",
                filter === lvl
                  ? "bg-accent text-foreground font-medium"
                  : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
              )}
              onClick={() => setFilter(lvl)}
            >
              {lvl}
            </button>
          ))}
        </div>

        {/* Search */}
        <div className="relative flex-1 max-w-xs ml-2">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
          <input
            aria-label="Search logs"
            type="text"
            placeholder="Search logs..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="w-full pl-8 pr-3 py-1.5 text-label bg-background border border-border rounded text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring"
          />
        </div>

        <div className="ml-auto flex items-center gap-2">
          {autoRefresh && (
            <div className="flex items-center gap-1.5 text-label text-primary">
              <span className="h-1.5 w-1.5 rounded-full bg-primary animate-pulse" />
              Streaming
            </div>
          )}
          <Button
            size="sm"
            variant={autoRefresh ? "default" : "outline"}
            className="h-7 gap-1"
            onClick={() => setAutoRefresh(!autoRefresh)}
          >
            {autoRefresh ? (
              <><Pause className="h-3 w-3" /> Pause</>
            ) : (
              <><Play className="h-3 w-3" /> Stream</>
            )}
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="h-7 gap-1"
            onClick={handleDownload}
          >
            <Download className="h-3 w-3" /> Download
          </Button>
        </div>
      </div>

      {/* Log viewer */}
      {filtered.length === 0 ? (
        <div className="flex-1 border border-t-0 border-border rounded-b-lg bg-surface-subtle flex items-center justify-center">
          <EmptyState
            icon={ScrollText}
            title={logs.length === 0 ? "No logs yet" : "No matching logs"}
            description={logs.length === 0 ? "Logs will appear here when the agent runs." : "Try a different filter."}
          />
        </div>
      ) : (
        <div
          ref={logContainerRef}
          className="flex-1 bg-surface-subtle border border-t-0 border-border rounded-b-lg overflow-y-auto px-4 py-4 font-mono text-label leading-6"
        >
          {filtered.map((log, i) => {
            const level = log.level.toUpperCase()
            const levelColor = LEVEL_COLORS[level] ?? "text-muted-foreground"
            const eventColor = EVENT_COLORS[log.event] ?? "text-muted-foreground"
            const contentColor = level === "ERROR" ? "text-destructive"
              : level === "WARN" ? "text-foreground" : "text-foreground/90"

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
              <div key={i} className="flex gap-0 hover:bg-muted/40">
                <span className="text-muted-foreground shrink-0">{formatLogTime(log.ts)}</span>
                <span className={`${levelColor} mx-2 shrink-0`}>[{level.toLowerCase()}]</span>
                <span className={`${eventColor} mr-2 shrink-0`}>{log.event}</span>
                <span className={contentColor}>{log.content ?? ""}</span>
                {extraInfo && <span className="text-muted-foreground ml-1">{extraInfo}</span>}
              </div>
            )
          })}
          {autoRefresh && (
            <div className="mt-2">
              <span className="inline-block w-2 h-4 bg-primary animate-pulse align-middle" />
            </div>
          )}
        </div>
      )}

      {/* Log status bar */}
      <div className="bg-card border border-t-0 border-border rounded-b-lg px-4 py-2 flex items-center shrink-0 -mt-4">
        <span className="ml-auto text-micro text-muted-foreground">
          {filtered.length} entr{filtered.length !== 1 ? "ies" : "y"}
          {filter !== "ALL" ? ` (${filter})` : ""}
        </span>
      </div>
    </div>
  )
}

function LogsSkeleton() {
  return (
    <div className="flex flex-col h-full p-4 sm:p-6 gap-4">
      <Skeleton className="h-6 w-32" />
      <div className="bg-card border border-border rounded-t-lg px-4 py-2 flex items-center gap-2">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-6 w-14" />
        ))}
        <div className="ml-auto flex gap-1">
          <Skeleton className="h-6 w-20" />
          <Skeleton className="h-6 w-20" />
        </div>
      </div>
      <div className="flex-1 bg-surface-subtle border border-t-0 border-border rounded-b-lg p-4 space-y-2 -mt-4">
        {Array.from({ length: 12 }).map((_, i) => (
          <Skeleton key={i} className={`h-5 ${["w-3/4", "w-2/3", "w-4/5", "w-3/5", "w-2/3", "w-[70%]", "w-4/5", "w-3/5", "w-[65%]", "w-3/4", "w-2/3", "w-4/5"][i]}`} />
        ))}
      </div>
    </div>
  )
}
