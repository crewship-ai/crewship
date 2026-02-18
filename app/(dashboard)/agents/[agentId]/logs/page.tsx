"use client"

import { use, useState, useEffect, useCallback, useRef } from "react"
import { Download, AlertCircle, Inbox, Search, Pause, Play } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"

interface LogEntry {
  ts: string
  level: string
  agent: string
  event: string
  content?: string
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
}

interface LogsPageProps {
  params: Promise<{ agentId: string }>
}

/** Agent logs viewer with dark terminal style, filtering, and auto-refresh. */
export default function LogsPage({ params }: LogsPageProps) {
  const { agentId } = use(params)
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [filter, setFilter] = useState<LogLevel>("ALL")
  const [searchQuery, setSearchQuery] = useState("")
  const [autoScroll] = useState(true)
  const [autoRefresh, setAutoRefresh] = useState(false)
  const logContainerRef = useRef<HTMLDivElement>(null)

  const fetchLogs = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/logs?workspace_id=${workspaceId}`)
      if (!res.ok) {
        setError("Failed to load logs")
        return
      }
      const data: LogEntry[] = await res.json()
      setLogs(data)
      setError(null)
    } catch {
      setError("Network error. Is crewshipd running?")
    } finally {
      setLoading(false)
    }
  }, [agentId, workspaceId])

  useEffect(() => {
    if (!workspaceId) return
    fetchLogs()
  }, [workspaceId, fetchLogs])

  useEffect(() => {
    if (!autoRefresh || !workspaceId) return
    const interval = setInterval(fetchLogs, 3000)
    return () => clearInterval(interval)
  }, [autoRefresh, workspaceId, fetchLogs])

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
          <p className="text-sm">{error}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full">
      {/* Log controls -- dark bar */}
      <div className="bg-neutral-900 dark:bg-neutral-950 border-b border-neutral-700 px-4 sm:px-6 py-2 flex flex-wrap items-center gap-3 shrink-0">
        <div className="flex items-center gap-1">
          <span className="text-xs text-neutral-400 mr-1">Level:</span>
          {LEVELS.map((lvl) => (
            <button
              key={lvl}
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
              Auto-scroll {autoScroll ? "ON" : "OFF"}
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
          className="flex-1 bg-neutral-950 overflow-y-auto px-4 sm:px-6 py-4 font-mono text-xs leading-6"
        >
          {filtered.map((log, i) => {
            const level = log.level.toUpperCase()
            const levelColor = LEVEL_COLORS[level] ?? "text-neutral-500"
            const eventColor = EVENT_COLORS[log.event] ?? "text-neutral-400"
            const contentColor = level === "ERROR" ? "text-red-300" :
              level === "WARN" ? "text-amber-300" : "text-neutral-300"

            return (
              <div key={i} className="flex gap-0 hover:bg-neutral-900/50">
                <span className="text-neutral-600 shrink-0">{formatLogTime(log.ts)}</span>
                <span className={`${levelColor} mx-2 shrink-0`}>[{level.toLowerCase()}]</span>
                <span className={`${eventColor} mr-2 shrink-0`}>{log.event}</span>
                <span className={contentColor}>{log.content ?? ""}</span>
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
      <div className="bg-neutral-900 dark:bg-neutral-950 border-t border-neutral-700 px-4 sm:px-6 py-2 flex items-center shrink-0">
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
      <div className="bg-neutral-900 px-4 sm:px-6 py-2 flex items-center gap-2">
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
          <Skeleton key={i} className="h-5 bg-neutral-900" style={{ width: `${60 + Math.random() * 30}%` }} />
        ))}
      </div>
    </div>
  )
}
