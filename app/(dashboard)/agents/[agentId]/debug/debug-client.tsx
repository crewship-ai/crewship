"use client"

import { useParams } from "next/navigation"

import { useState, useEffect, useCallback } from "react"
import {
  RefreshCw, AlertCircle, CheckCircle2, XCircle, Loader2,
  Server, Cpu, ScrollText, Database, Wifi, Settings2,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { SectionCard } from "@/components/ui/section-card"
import { PropertyRow } from "@/components/layout/property-row"
import { StatusBadge } from "@/components/ui/status-badge"
import { ToolbarStrip } from "@/components/layout/toolbar-strip"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import type { DebugData } from "@/lib/types/agent"
import { cn } from "@/lib/utils"

type LogTab = "service" | "agent"

function StatusIcon({ ok }: { ok: boolean }) {
  return ok
    ? <CheckCircle2 className="h-4 w-4 text-muted-foreground" />
    : <XCircle className="h-4 w-4 text-destructive" />
}

function formatTime(ts: string): string {
  try {
    return new Date(ts).toLocaleTimeString("en-GB", { hour12: false })
  } catch {
    return ts
  }
}

function levelClass(level: string): string {
  switch (level.toUpperCase()) {
    case "ERROR": return "text-destructive"
    case "WARN":
    case "WARNING": return "text-muted-foreground font-medium"
    case "DEBUG": return "text-muted-foreground"
    default: return "text-foreground"
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

  // Real-time: refresh debug info when agent status or runs change
  useRealtimeEvent("agent.status", useCallback(() => { fetchDebug() }, [fetchDebug]))
  useRealtimeEvent("run.started", useCallback(() => { fetchDebug() }, [fetchDebug]))
  useRealtimeEvent("run.completed", useCallback(() => { fetchDebug() }, [fetchDebug]))
  useRealtimeEvent("run.failed", useCallback(() => { fetchDebug() }, [fetchDebug]))

  const handleRefresh = useCallback(() => {
    setRefreshing(true)
    fetchDebug()
  }, [fetchDebug])

  if (wsLoading || loading) {
    return <DebugSkeleton />
  }

  if (error || !data) {
    return (
      <div className="p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-body">{error ?? "Failed to load debug data"}</p>
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
    <div className="p-6 space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <div>
          <h2 className="text-title font-semibold">Debug & Diagnostics</h2>
          <p className="text-body text-muted-foreground mt-1">
            Engine service status, agent runtime, and live logs
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant={autoRefresh ? "default" : "outline"}
            size="sm"
            className="text-label"
            onClick={() => setAutoRefresh((v) => !v)}
          >
            {autoRefresh ? "Polling..." : "Auto-refresh"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5 text-label"
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
        <SectionCard
          title={
            <span className="flex items-center gap-2 text-label font-medium">
              <Server className="h-3.5 w-3.5 text-muted-foreground" />
              Engine
            </span>
          }
        >
          <div className="space-y-0">
            <PropertyRow label="Status">
              <div className="flex items-center gap-2">
                <StatusIcon ok={engineOk} />
                <span className="text-body font-medium">
                  {engineOk ? "Running" : "Unreachable"}
                </span>
              </div>
            </PropertyRow>
            {engineOk && data.crewshipd.uptime && (
              <PropertyRow label="Uptime">
                <span className="font-mono text-label">{data.crewshipd.uptime}</span>
              </PropertyRow>
            )}
            {engineOk && data.crewshipd.connections !== undefined && (
              <PropertyRow label="WS conns">
                <span className="font-mono text-label">{data.crewshipd.connections}</span>
              </PropertyRow>
            )}
            {engineOk && data.crewshipd.started_at && (
              <PropertyRow label="Started">
                <span className="font-mono text-micro">{formatTime(data.crewshipd.started_at)}</span>
              </PropertyRow>
            )}
            {!engineOk && (
              <p className="text-label text-destructive pt-2">
                Cannot connect to the Engine. Make sure it is running.
              </p>
            )}
          </div>
        </SectionCard>

        {/* Agent runtime */}
        <SectionCard
          title={
            <span className="flex items-center gap-2 text-label font-medium">
              <Cpu className="h-3.5 w-3.5 text-muted-foreground" />
              Agent Runtime
            </span>
          }
        >
          <div className="space-y-0">
            <PropertyRow label="Status">
              <StatusBadge status={runtimeStatus.toUpperCase()} label={runtimeStatus} />
            </PropertyRow>
            <PropertyRow label="DB">
              <StatusBadge status={data.agent.db_status.toUpperCase()} label={data.agent.db_status} />
            </PropertyRow>
            <PropertyRow label="Adapter">
              <span className="font-mono text-micro">{data.agent.cli_adapter}</span>
            </PropertyRow>
            {data.runtime.container_id && (
              <PropertyRow label="Container">
                <code className="font-mono text-micro truncate" title={data.runtime.container_id}>
                  {data.runtime.container_id.slice(0, 12)}
                </code>
              </PropertyRow>
            )}
            {data.runtime.exec_id && (
              <PropertyRow label="Exec ID">
                <code className="font-mono text-micro truncate">{data.runtime.exec_id.slice(0, 12)}</code>
              </PropertyRow>
            )}
            {data.runtime.session_id && (
              <PropertyRow label="Session">
                <code className="font-mono text-micro truncate">{data.runtime.session_id.slice(0, 8)}</code>
              </PropertyRow>
            )}
            {data.runtime.credential_id && (
              <PropertyRow label="Credential">
                <code className="font-mono text-micro truncate">{data.runtime.credential_id.slice(0, 8)}</code>
              </PropertyRow>
            )}
          </div>
        </SectionCard>

        {/* Providers & Config */}
        <SectionCard
          title={
            <span className="flex items-center gap-2 text-label font-medium">
              <Settings2 className="h-3.5 w-3.5 text-muted-foreground" />
              Providers
            </span>
          }
        >
          <div className="space-y-0">
            {engineOk && data.crewshipd.providers ? (
              <>
                {Object.entries(data.crewshipd.providers).map(([key, val]) => (
                  <PropertyRow key={key} label={<span className="capitalize">{key}</span>}>
                    <span className="font-mono text-label">{val}</span>
                  </PropertyRow>
                ))}
                <PropertyRow label="Container">
                  <StatusIcon ok={!!data.crewshipd.container_available} />
                </PropertyRow>
                <PropertyRow label="Storage">
                  <StatusIcon ok={!!data.crewshipd.storage_available} />
                </PropertyRow>
                <PropertyRow label="State">
                  <StatusIcon ok={!!data.crewshipd.state_available} />
                </PropertyRow>
                <PropertyRow label="JWT">
                  <StatusIcon ok={!!(data.crewshipd.config as Record<string, unknown>)?.jwt_configured} />
                </PropertyRow>
                <PropertyRow label="LLM Proxy">
                  <StatusIcon ok={!!data.crewshipd.llm_proxy_enabled} />
                </PropertyRow>
              </>
            ) : (
              <p className="text-label text-muted-foreground">Engine not reachable</p>
            )}
          </div>
        </SectionCard>

        {/* Last Run Stats */}
        <SectionCard
          title={
            <span className="flex items-center gap-2 text-label font-medium">
              <Cpu className="h-3.5 w-3.5 text-muted-foreground" />
              Last Run
            </span>
          }
        >
          <div className="space-y-0">
            {resultMeta ? (
              <>
                {resultMeta.total_cost_usd != null && (
                  <PropertyRow label="Cost">
                    <span className="font-mono text-label font-medium">
                      ${Number(resultMeta.total_cost_usd).toFixed(4)}
                    </span>
                  </PropertyRow>
                )}
                {resultMeta.duration_ms != null && (
                  <PropertyRow label="Duration">
                    <span className="font-mono text-label">{(Number(resultMeta.duration_ms) / 1000).toFixed(1)}s</span>
                  </PropertyRow>
                )}
                {resultMeta.num_turns != null && (
                  <PropertyRow label="Turns">
                    <span className="font-mono text-label">{String(resultMeta.num_turns)}</span>
                  </PropertyRow>
                )}
                {resultMeta.usage && (
                  <>
                    <PropertyRow label="Input tok">
                      <span className="font-mono text-label">
                        {String((resultMeta.usage as Record<string, number>).input_tokens ?? 0)}
                      </span>
                    </PropertyRow>
                    <PropertyRow label="Output tok">
                      <span className="font-mono text-label">
                        {String((resultMeta.usage as Record<string, number>).output_tokens ?? 0)}
                      </span>
                    </PropertyRow>
                  </>
                )}
                {initMeta?.model && (
                  <PropertyRow label="Model">
                    <span className="font-mono text-micro">{String(initMeta.model)}</span>
                  </PropertyRow>
                )}
                {initMeta?.tools && (
                  <PropertyRow label="Tools">
                    <span className="font-mono text-label">{(initMeta.tools as string[]).length}</span>
                  </PropertyRow>
                )}
              </>
            ) : (
              <p className="text-label text-muted-foreground">No run data yet</p>
            )}
          </div>
        </SectionCard>
      </div>

      {/* Log summary bar */}
      <div className="flex items-center gap-4 text-label flex-wrap">
        <div className="flex items-center gap-1">
          <ScrollText className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-muted-foreground">Service logs: {data.service_logs.length}</span>
        </div>
        {errorCount > 0 && (
          <StatusBadge status="FAILED" label={`${errorCount} error${errorCount !== 1 ? "s" : ""}`} />
        )}
        {warnCount > 0 && (
          <StatusBadge status="BLOCKED" label={`${warnCount} warning${warnCount !== 1 ? "s" : ""}`} />
        )}
        <div className="flex items-center gap-1 text-muted-foreground">
          <Database className="h-3.5 w-3.5" />
          <span>Agent logs: {data.agent_logs.length}</span>
        </div>
        {autoRefresh && (
          <span className="text-muted-foreground inline-flex items-center gap-1">
            <Wifi className="h-3 w-3" />
            Polling every 3s
          </span>
        )}
      </div>

      {/* Log viewer */}
      <div className="rounded-[var(--radius)] border border-border overflow-hidden bg-card">
        <ToolbarStrip<LogTab>
          tabs={[
            { id: "service", label: `Engine Logs (${data.service_logs.length})`, icon: Server },
            { id: "agent", label: `Agent Output (${data.agent_logs.length})`, icon: Cpu },
          ]}
          activeTab={logTab}
          onTabChange={setLogTab}
          ariaLabel="Log source"
        />
        <div className="bg-surface-subtle font-mono text-micro leading-relaxed overflow-x-auto max-h-[500px] overflow-y-auto p-4">
          {logTab === "service" ? (
            data.service_logs.length === 0 ? (
              <span className="text-muted-foreground">
                {engineOk ? "No log entries captured yet." : "Engine is not running. Start it to see logs."}
              </span>
            ) : (
              data.service_logs.map((entry, i) => {
                const lc = levelClass(entry.level)
                const attrStr = entry.attrs
                  ? Object.entries(entry.attrs).map(([k, v]) => `${k}=${v}`).join(" ")
                  : ""
                return (
                  <div key={i} className="hover:bg-muted/40 px-1 -mx-1 rounded">
                    <span className="text-muted-foreground">[{formatTime(entry.time)}]</span>{" "}
                    <span className={cn("font-mono font-medium", lc)}>{entry.level}</span>{" "}
                    <span className={lc}>{entry.msg}</span>
                    {attrStr && <span className="text-muted-foreground ml-2">{attrStr}</span>}
                  </div>
                )
              })
            )
          ) : (
            data.agent_logs.length === 0 ? (
              <span className="text-muted-foreground">No agent output logs. Run the agent to see output.</span>
            ) : (
              data.agent_logs.map((entry, i) => {
                const lc = levelClass(entry.level)
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
                return (
                  <div key={i} className="hover:bg-muted/40 px-1 -mx-1 rounded">
                    <span className="text-muted-foreground">[{formatTime(entry.ts)}]</span>{" "}
                    <span className={cn("font-mono font-medium", lc)}>{entry.level}</span>{" "}
                    {entry.event && <span className="text-muted-foreground mr-1">[{entry.event}]</span>}
                    <span className={lc}>{entry.content ?? entry.event}</span>
                    {extra && <span className="text-muted-foreground">{extra}</span>}
                  </div>
                )
              })
            )
          )}
        </div>
      </div>

      {/* Raw data collapsible */}
      <details className="group">
        <summary className="text-label font-medium uppercase tracking-wide text-muted-foreground cursor-pointer hover:text-foreground">
          Raw Debug Data
        </summary>
        <div className="mt-2 bg-surface-subtle border border-border rounded-lg p-4 font-mono text-micro leading-relaxed overflow-x-auto max-h-[400px] overflow-y-auto">
          <pre className="text-foreground whitespace-pre-wrap">
            {JSON.stringify(data, null, 2)}
          </pre>
        </div>
      </details>
    </div>
  )
}

function DebugSkeleton() {
  return (
    <div className="p-6 space-y-6">
      <div className="flex items-center justify-between">
        <Skeleton className="h-7 w-48" />
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
