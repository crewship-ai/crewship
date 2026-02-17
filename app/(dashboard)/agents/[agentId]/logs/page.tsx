"use client"

import { use, useState, useEffect, useCallback } from "react"
import { Download, AlertCircle, Inbox } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Terminal,
  TerminalHeader,
  TerminalTitle,
  TerminalStatus,
  TerminalActions,
  TerminalCopyButton,
  TerminalContent,
} from "@/components/ai-elements/terminal"
import { useOrg } from "@/hooks/use-org"

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
    return d.toLocaleTimeString("en-GB", { hour12: false })
  } catch {
    return ts
  }
}

function logsToAnsi(logs: LogEntry[]): string {
  return logs.map((line) => {
    const level = line.level.toUpperCase()
    const time = formatLogTime(line.ts)
    const msg = line.content ?? line.event

    let levelAnsi: string
    let msgAnsi: string
    switch (level) {
      case "ERROR":
        levelAnsi = `\x1b[31m${level}\x1b[0m`
        msgAnsi = `\x1b[91m${msg}\x1b[0m`
        break
      case "WARN":
        levelAnsi = `\x1b[33m${level}\x1b[0m`
        msgAnsi = `\x1b[93m${msg}\x1b[0m`
        break
      default:
        levelAnsi = `\x1b[90m${level}\x1b[0m`
        msgAnsi = msg
    }

    return `\x1b[90m[${time}]\x1b[0m ${levelAnsi} ${msgAnsi}`
  }).join("\n")
}

export default function LogsPage({ params }: { params: Promise<{ agentId: string }> }) {
  const { agentId } = use(params)
  const { orgId, loading: orgLoading } = useOrg()
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [filter, setFilter] = useState<LogLevel>("ALL")
  const [autoRefresh, setAutoRefresh] = useState(false)

  const fetchLogs = useCallback(async () => {
    if (!orgId) return
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/logs?org_id=${orgId}`)
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
  }, [agentId, orgId])

  useEffect(() => {
    if (!orgId) return
    fetchLogs()
  }, [orgId, fetchLogs])

  useEffect(() => {
    if (!autoRefresh || !orgId) return
    const interval = setInterval(fetchLogs, 3000)
    return () => clearInterval(interval)
  }, [autoRefresh, orgId, fetchLogs])

  const filtered = filter === "ALL"
    ? logs
    : logs.filter((l) => l.level.toUpperCase() === filter)

  const terminalOutput = logsToAnsi(filtered)

  const handleDownload = useCallback(() => {
    const text = logs.map((l) => `[${formatLogTime(l.ts)}] ${l.level.toUpperCase()} ${l.content ?? l.event}`).join("\n")
    const blob = new Blob([text], { type: "text/plain" })
    const url = URL.createObjectURL(blob)
    const a = document.createElement("a")
    a.href = url
    a.download = `agent-${agentId}-logs.txt`
    a.click()
    URL.revokeObjectURL(url)
  }, [logs, agentId])

  if (orgLoading || loading) {
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
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Toolbar */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex items-center gap-1">
          {LEVELS.map((lvl) => (
            <Button
              key={lvl}
              variant={filter === lvl ? "default" : "outline"}
              size="sm"
              className="text-xs h-7 px-2.5"
              onClick={() => setFilter(lvl)}
            >
              {lvl}
            </Button>
          ))}
        </div>
        <div className="flex items-center gap-1 ml-auto">
          <Button
            variant={autoRefresh ? "default" : "outline"}
            size="sm"
            className="text-xs gap-1.5"
            onClick={() => setAutoRefresh((v) => !v)}
          >
            {autoRefresh ? "Polling..." : "Auto-refresh"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5 text-xs"
            onClick={handleDownload}
            disabled={logs.length === 0}
          >
            <Download className="h-3.5 w-3.5" /> Download
          </Button>
        </div>
      </div>

      {/* Log viewer */}
      {filtered.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <Inbox className="h-10 w-10 text-muted-foreground/50 mb-3" />
          <p className="text-sm font-medium text-muted-foreground">
            {logs.length === 0 ? "No logs yet" : "No matching logs"}
          </p>
          <p className="text-xs text-muted-foreground mt-1">
            {logs.length === 0
              ? "Logs will appear here when the agent runs."
              : "Try a different log level filter."}
          </p>
        </div>
      ) : (
        <Terminal
          output={terminalOutput}
          isStreaming={autoRefresh}
          className="max-h-[600px]"
        >
          <TerminalHeader>
            <TerminalTitle>Agent Logs</TerminalTitle>
            {autoRefresh && <TerminalStatus />}
            <TerminalActions>
              <TerminalCopyButton />
            </TerminalActions>
          </TerminalHeader>
          <TerminalContent className="max-h-[550px]" />
        </Terminal>
      )}

      {/* Footer */}
      <p className="text-xs text-muted-foreground">
        {filtered.length} log entr{filtered.length !== 1 ? "ies" : "y"}
        {filter !== "ALL" ? ` (${filter})` : ""}
        {autoRefresh ? " · Refreshing every 3s" : ""}
      </p>
    </div>
  )
}

function LogsSkeleton() {
  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <div className="flex items-center gap-2">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-7 w-14" />
        ))}
        <div className="ml-auto flex gap-1">
          <Skeleton className="h-7 w-24" />
          <Skeleton className="h-7 w-24" />
        </div>
      </div>
      <Skeleton className="h-[400px] w-full rounded-lg" />
      <Skeleton className="h-4 w-32" />
    </div>
  )
}
