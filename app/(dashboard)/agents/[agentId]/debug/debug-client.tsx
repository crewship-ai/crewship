"use client"

import { useParams } from "next/navigation"

import { use, useState, useEffect, useCallback } from "react"
import {
  RefreshCw, AlertCircle, CheckCircle2, XCircle, Loader2,
  Server, Cpu, ScrollText, Database, Wifi, Settings2,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"

interface ServiceLogEntry {
  time: string
  level: string
  msg: string
  attrs?: Record<string, string>
}

interface AgentLogEntry {
  ts: string
  level: string
  agent: string
  event: string
  content?: string
  metadata?: Record<string, unknown>
}

interface DebugData {
  agent: {
    id: string
    name: string
    cli_adapter: string
    db_status: string
  }
  crewshipd_reachable: boolean
  crewshipd: {
    status?: string
    uptime?: string
    uptime_secs?: number
    connections?: number
    started_at?: string
    providers?: Record<string, string>
    container_available?: boolean
    storage_available?: boolean
    state_available?: boolean
    llm_proxy_enabled?: boolean
    config?: Record<string, unknown>
    error?: string
  }
  runtime: {
    agent_id?: string
    status: string
    started_at?: string
    container_id?: string
    exec_id?: string
    last_activity?: string
    credential_id?: string
    session_id?: string
  }
  service_logs: ServiceLogEntry[]
  agent_logs: AgentLogEntry[]
}

type LogTab = "service" | "agent"

function StatusIcon({ ok }: { ok: boolean }) {
  return ok
    ? <CheckCircle2 className="h-4 w-4 text-emerald-500" />
    : <XCircle className="h-4 w-4 text-destructive" />
}

function formatTime(ts: string): string {
  try {
    return new Date(ts).toLocaleTimeString("en-GB", { hour12: false })
  } catch {
    return ts
  }
}

function levelColor(level: string): { badge: string; text: string } {
  switch (level.toUpperCase()) {
    case "ERROR": return { badge: "text-red-400", text: "text-red-300" }
    case "WARN": case "WARNING": return { badge: "text-yellow-400", text: "text-yellow-200" }
    case "DEBUG": return { badge: "text-blue-400", text: "text-blue-200" }
    default: return { badge: "text-neutral-500", text: "text-neutral-300" }
  }
}

export function DebugPageClient() {
  const { agentId } = useParams<{ agentId: string }>()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [data, setData] = useState<DebugData | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [refreshing, setRefreshing] = useState(false)
  const [autoRefresh, setAutoRefresh] = useState(false)
  const [logTab, setLogTab] = useState<LogTab>("service")

  const fetchDebug = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/debug?workspace_id=${workspaceId}`)
      if (!res.ok) {
        setError("Failed to load debug info")
        return
      }
      const d: DebugData = await res.json()
      setData(d)
      setError(null)
    } catch {
      setError("Network error. Is the dev server running?")
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }, [agentId, workspaceId])

  useEffect(() => {
    if (!workspaceId) return
    fetchDebug()
  }, [workspaceId, fetchDebug])

  useEffect(() => {
    if (!autoRefresh || !workspaceId) return
    const interval = setInterval(fetchDebug, 3000)
    return () => clearInterval(interval)
  }, [autoRefresh, workspaceId, fetchDebug])

  const handleRefresh = useCallback(() => {
    setRefreshing(true)
    fetchDebug()
  }, [fetchDebug])

  if (wsLoading || loading) {
    return <DebugSkeleton />
  }

  if (error || !data) {
    return (
      <div className="p-4 sm:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-sm">{error ?? "Failed to load debug data"}</p>
        </div>
      </div>
    )
  }

  const engineOk = data.crewshipd_reachable
  const runtimeStatus = data.runtime?.status ?? "unknown"
  const errorCount = data.service_logs.filter((l) => l.level === "ERROR").length
  const warnCount = data.service_logs.filter((l) => l.level === "WARN" || l.level === "WARNING").length

  // Extract latest result and system/init from agent logs
  const lastResult = [...data.agent_logs].reverse().find((l) => l.event === "result")
  const lastInit = [...data.agent_logs].reverse().find((l) => l.event === "system")
  const resultMeta = lastResult?.metadata as Record<string, unknown> | undefined
  const initMeta = lastInit?.metadata as Record<string, unknown> | undefined

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-sm font-medium">Debug & Diagnostics</h2>
          <p className="text-xs text-muted-foreground mt-0.5">
            Engine service status, agent runtime, and live logs
          </p>
        </div>
        <div className="flex items-center gap-1">
          <Button
            variant={autoRefresh ? "default" : "outline"}
            size="sm"
            className="text-xs"
            onClick={() => setAutoRefresh((v) => !v)}
          >
            {autoRefresh ? "Polling..." : "Auto-refresh"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5 text-xs"
            onClick={handleRefresh}
            disabled={refreshing}
          >
            {refreshing ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />}
            Refresh
          </Button>
        </div>
      </div>

      {/* Status cards */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
        {/* Engine health */}
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-xs font-medium flex items-center gap-2">
              <Server className="h-3.5 w-3.5" />
              Engine
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            <div className="flex items-center gap-2">
              <StatusIcon ok={engineOk} />
              <span className="text-sm font-medium">
                {engineOk ? "Running" : "Unreachable"}
              </span>
            </div>
            {engineOk && data.crewshipd.uptime && (
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">Uptime</span>
                <span className="font-mono">{data.crewshipd.uptime}</span>
              </div>
            )}
            {engineOk && data.crewshipd.connections !== undefined && (
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">WS Connections</span>
                <span className="font-mono">{data.crewshipd.connections}</span>
              </div>
            )}
            {engineOk && data.crewshipd.started_at && (
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">Started</span>
                <span className="font-mono text-[10px]">{formatTime(data.crewshipd.started_at)}</span>
              </div>
            )}
            {!engineOk && (
              <p className="text-xs text-destructive">
                Cannot connect to the Engine. Make sure it is running.
              </p>
            )}
          </CardContent>
        </Card>

        {/* Agent runtime */}
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-xs font-medium flex items-center gap-2">
              <Cpu className="h-3.5 w-3.5" />
              Agent Runtime
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            <div className="flex items-center gap-2">
              <Badge
                variant="secondary"
                className={`text-xs ${runtimeStatus === "running" ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400" : runtimeStatus === "error" ? "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-400" : ""}`}
              >
                {runtimeStatus}
              </Badge>
            </div>
            <div className="flex justify-between text-xs">
              <span className="text-muted-foreground">DB Status</span>
              <Badge variant="outline" className="text-[10px]">{data.agent.db_status}</Badge>
            </div>
            <div className="flex justify-between text-xs">
              <span className="text-muted-foreground">CLI Adapter</span>
              <span className="font-mono text-[10px]">{data.agent.cli_adapter}</span>
            </div>
            {data.runtime.container_id && (
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">Container</span>
                <code className="text-[10px] truncate max-w-[140px]" title={data.runtime.container_id}>
                  {data.runtime.container_id.slice(0, 12)}
                </code>
              </div>
            )}
            {data.runtime.exec_id && (
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">Exec ID</span>
                <code className="text-[10px] truncate max-w-[140px]">{data.runtime.exec_id.slice(0, 12)}</code>
              </div>
            )}
            {data.runtime.session_id && (
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">Session</span>
                <code className="text-[10px] truncate max-w-[140px]">{data.runtime.session_id.slice(0, 8)}</code>
              </div>
            )}
            {data.runtime.credential_id && (
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">Credential</span>
                <code className="text-[10px] truncate max-w-[140px]">{data.runtime.credential_id.slice(0, 8)}</code>
              </div>
            )}
          </CardContent>
        </Card>

        {/* Providers & Config */}
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-xs font-medium flex items-center gap-2">
              <Settings2 className="h-3.5 w-3.5" />
              Providers
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            {engineOk && data.crewshipd.providers ? (
              <>
                {Object.entries(data.crewshipd.providers).map(([key, val]) => (
                  <div key={key} className="flex justify-between text-xs">
                    <span className="text-muted-foreground capitalize">{key}</span>
                    <span className="font-mono">{val}</span>
                  </div>
                ))}
                <div className="flex justify-between text-xs">
                  <span className="text-muted-foreground">Container</span>
                  <StatusIcon ok={!!data.crewshipd.container_available} />
                </div>
                <div className="flex justify-between text-xs">
                  <span className="text-muted-foreground">Storage</span>
                  <StatusIcon ok={!!data.crewshipd.storage_available} />
                </div>
                <div className="flex justify-between text-xs">
                  <span className="text-muted-foreground">State</span>
                  <StatusIcon ok={!!data.crewshipd.state_available} />
                </div>
                <div className="flex justify-between text-xs">
                  <span className="text-muted-foreground">JWT</span>
                  <StatusIcon ok={!!(data.crewshipd.config as Record<string, unknown>)?.jwt_configured} />
                </div>
                <div className="flex justify-between text-xs">
                  <span className="text-muted-foreground">LLM Proxy</span>
                  <StatusIcon ok={!!data.crewshipd.llm_proxy_enabled} />
                </div>
              </>
            ) : (
              <p className="text-xs text-muted-foreground">Engine not reachable</p>
            )}
          </CardContent>
        </Card>

        {/* Last Run Stats */}
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-xs font-medium flex items-center gap-2">
              <Cpu className="h-3.5 w-3.5" />
              Last Run
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            {resultMeta ? (
              <>
                {resultMeta.total_cost_usd != null && (
                  <div className="flex justify-between text-xs">
                    <span className="text-muted-foreground">Cost</span>
                    <span className="font-mono font-medium text-emerald-600 dark:text-emerald-400">${Number(resultMeta.total_cost_usd).toFixed(4)}</span>
                  </div>
                )}
                {resultMeta.duration_ms != null && (
                  <div className="flex justify-between text-xs">
                    <span className="text-muted-foreground">Duration</span>
                    <span className="font-mono">{(Number(resultMeta.duration_ms) / 1000).toFixed(1)}s</span>
                  </div>
                )}
                {resultMeta.num_turns != null && (
                  <div className="flex justify-between text-xs">
                    <span className="text-muted-foreground">Turns</span>
                    <span className="font-mono">{String(resultMeta.num_turns)}</span>
                  </div>
                )}
                {resultMeta.usage && (
                  <>
                    <div className="flex justify-between text-xs">
                      <span className="text-muted-foreground">Input Tokens</span>
                      <span className="font-mono">{String((resultMeta.usage as Record<string, number>).input_tokens ?? 0)}</span>
                    </div>
                    <div className="flex justify-between text-xs">
                      <span className="text-muted-foreground">Output Tokens</span>
                      <span className="font-mono">{String((resultMeta.usage as Record<string, number>).output_tokens ?? 0)}</span>
                    </div>
                  </>
                )}
                {initMeta?.model && (
                  <div className="flex justify-between text-xs">
                    <span className="text-muted-foreground">Model</span>
                    <span className="font-mono text-[10px]">{String(initMeta.model)}</span>
                  </div>
                )}
                {initMeta?.tools && (
                  <div className="flex justify-between text-xs">
                    <span className="text-muted-foreground">Tools</span>
                    <span className="font-mono">{(initMeta.tools as string[]).length}</span>
                  </div>
                )}
              </>
            ) : (
              <p className="text-xs text-muted-foreground">No run data yet</p>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Log summary bar */}
      <div className="flex items-center gap-4 text-xs">
        <div className="flex items-center gap-1">
          <ScrollText className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-muted-foreground">Service logs: {data.service_logs.length}</span>
        </div>
        {errorCount > 0 && (
          <Badge variant="secondary" className="text-[10px] bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-400">
            {errorCount} error{errorCount !== 1 ? "s" : ""}
          </Badge>
        )}
        {warnCount > 0 && (
          <Badge variant="secondary" className="text-[10px] bg-yellow-50 text-yellow-700 dark:bg-yellow-950 dark:text-yellow-400">
            {warnCount} warning{warnCount !== 1 ? "s" : ""}
          </Badge>
        )}
        <div className="flex items-center gap-1 text-muted-foreground">
          <Database className="h-3.5 w-3.5" />
          <span>Agent logs: {data.agent_logs.length}</span>
        </div>
        {autoRefresh && (
          <span className="text-muted-foreground">
            <Wifi className="h-3 w-3 inline mr-1" />
            Polling every 3s
          </span>
        )}
      </div>

      {/* Log viewer tabs */}
      <div>
        <div className="flex items-center gap-1 mb-2">
          <Button
            variant={logTab === "service" ? "default" : "outline"}
            size="sm"
            className="text-xs h-7 px-3"
            onClick={() => setLogTab("service")}
          >
            Engine Logs ({data.service_logs.length})
          </Button>
          <Button
            variant={logTab === "agent" ? "default" : "outline"}
            size="sm"
            className="text-xs h-7 px-3"
            onClick={() => setLogTab("agent")}
          >
            Agent Output ({data.agent_logs.length})
          </Button>
        </div>

        <div className="bg-neutral-950 rounded-lg p-3 sm:p-4 font-mono text-[11px] sm:text-xs leading-relaxed overflow-x-auto max-h-[500px] overflow-y-auto">
          {logTab === "service" ? (
            data.service_logs.length === 0 ? (
              <span className="text-neutral-600">
                {engineOk ? "No log entries captured yet." : "Engine is not running. Start it to see logs."}
              </span>
            ) : (
              data.service_logs.map((entry, i) => {
                const lc = levelColor(entry.level)
                const attrStr = entry.attrs
                  ? Object.entries(entry.attrs).map(([k, v]) => `${k}=${v}`).join(" ")
                  : ""
                return (
                  <div key={i} className="hover:bg-white/5 px-1 -mx-1 rounded">
                    <span className="text-neutral-600">[{formatTime(entry.time)}]</span>{" "}
                    <Badge variant="outline" className={`${lc.badge} border-current/20 text-[10px] px-1 py-0 font-mono`}>
                      {entry.level}
                    </Badge>{" "}
                    <span className={lc.text}>{entry.msg}</span>
                    {attrStr && <span className="text-neutral-600 ml-2">{attrStr}</span>}
                  </div>
                )
              })
            )
          ) : (
            data.agent_logs.length === 0 ? (
              <span className="text-neutral-600">No agent output logs. Run the agent to see output.</span>
            ) : (
              data.agent_logs.map((entry, i) => {
                const lc = levelColor(entry.level)
                let extra = ""
                if (entry.event === "result" && entry.metadata) {
                  const m = entry.metadata
                  const parts: string[] = []
                  if (m.total_cost_usd != null) parts.push(`$${Number(m.total_cost_usd).toFixed(4)}`)
                  if (m.duration_ms != null) parts.push(`${(Number(m.duration_ms) / 1000).toFixed(1)}s`)
                  if (m.num_turns != null) parts.push(`${m.num_turns} turns`)
                  if (parts.length) extra = ` [${parts.join(" | ")}]`
                }
                if (entry.event === "system" && entry.metadata?.model) {
                  extra = ` [${entry.metadata.model}]`
                }
                const eventColor = entry.event === "result" ? "text-purple-400" :
                  entry.event === "system" ? "text-blue-400" : ""
                return (
                  <div key={i} className="hover:bg-white/5 px-1 -mx-1 rounded">
                    <span className="text-neutral-600">[{formatTime(entry.ts)}]</span>{" "}
                    <Badge variant="outline" className={`${lc.badge} border-current/20 text-[10px] px-1 py-0 font-mono`}>
                      {entry.level}
                    </Badge>{" "}
                    {eventColor && <span className={`${eventColor} mr-1`}>[{entry.event}]</span>}
                    <span className={lc.text}>{entry.content ?? entry.event}</span>
                    {extra && <span className="text-neutral-500">{extra}</span>}
                  </div>
                )
              })
            )
          )}
        </div>
      </div>

      {/* Raw data collapsible */}
      <details className="group">
        <summary className="text-xs font-medium uppercase tracking-wide text-muted-foreground cursor-pointer hover:text-foreground">
          Raw Debug Data
        </summary>
        <div className="mt-2 bg-neutral-950 rounded-lg p-3 sm:p-4 font-mono text-[11px] leading-relaxed overflow-x-auto max-h-[400px] overflow-y-auto">
          <pre className="text-neutral-300 whitespace-pre-wrap">
            {JSON.stringify(data, null, 2)}
          </pre>
        </div>
      </details>
    </div>
  )
}

function DebugSkeleton() {
  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <div className="flex items-center justify-between">
        <Skeleton className="h-5 w-40" />
        <Skeleton className="h-8 w-20" />
      </div>
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-40 rounded-lg" />
        ))}
      </div>
      <Skeleton className="h-5 w-64" />
      <Skeleton className="h-[300px] rounded-lg" />
    </div>
  )
}
