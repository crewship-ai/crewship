"use client"

import { useCallback, useEffect, useState } from "react"
import { useParams } from "next/navigation"
import {
  ChevronDown, ChevronRight, RefreshCw, Loader2, CheckCircle2, XCircle,
  Server, Cpu, Settings2, Wifi,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { SectionCard } from "@/components/ui/section-card"
import { PropertyRow } from "@/components/layout/property-row"
import { StatusBadge } from "@/components/ui/status-badge"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import type { DebugData } from "@/lib/types/agent"

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

/**
 * Collapsible engine + runtime + provider + last-run status banner. Lives at
 * the top of the Logs tab, absorbing the old standalone Debug page.
 */
export function EngineStatusBanner() {
  const { agentId } = useParams<{ agentId: string }>()
  const { workspaceId } = useWorkspace()
  const [data, setData] = useState<DebugData | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [autoRefresh, setAutoRefresh] = useState(false)
  const [expanded, setExpanded] = useState(false)

  const fetchDebug = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/debug?workspace_id=${workspaceId}`)
      if (!res.ok) return
      const d: DebugData = await res.json()
      setData(d)
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

  useRealtimeEvent("agent.status", useCallback(() => { fetchDebug() }, [fetchDebug]))
  useRealtimeEvent("run.started", useCallback(() => { fetchDebug() }, [fetchDebug]))
  useRealtimeEvent("run.completed", useCallback(() => { fetchDebug() }, [fetchDebug]))
  useRealtimeEvent("run.failed", useCallback(() => { fetchDebug() }, [fetchDebug]))

  const handleRefresh = useCallback(() => {
    setRefreshing(true)
    fetchDebug()
  }, [fetchDebug])

  if (loading || !data) return null

  const engineOk = data.crewshipd_reachable
  const runtimeStatus = data.runtime?.status ?? "unknown"
  const resultMeta = [...data.agent_logs].reverse().find((l) => l.event === "result")?.metadata as Record<string, unknown> | undefined
  const initMeta = [...data.agent_logs].reverse().find((l) => l.event === "system")?.metadata as Record<string, unknown> | undefined

  return (
    <div className="border-b border-border bg-card">
      <div className="w-full flex items-center gap-2 px-4 py-2 hover:bg-muted/40 transition-colors">
        {/* Left side: toggle button spans just the label cluster, not the
            whole row — this is why there are no nested <button>s even though
            the action group on the right contains <Button>s. */}
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          className="flex items-center gap-2 text-left flex-1 min-w-0"
          aria-expanded={expanded}
        >
          {expanded
            ? <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
            : <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />}
          <Server className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-label font-medium">Engine</span>
          <StatusBadge
            status={engineOk ? "COMPLETED" : "FAILED"}
            label={engineOk ? "online" : "unreachable"}
          />
          <span className="text-micro text-muted-foreground ml-2">
            runtime {runtimeStatus}
          </span>
        </button>
        <div className="flex items-center gap-2 shrink-0">
          <Button
            variant={autoRefresh ? "default" : "outline"}
            size="sm"
            className="h-6 text-micro"
            onClick={() => setAutoRefresh((v) => !v)}
          >
            {autoRefresh ? <><Wifi className="h-3 w-3 mr-1" />3s</> : "Auto"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="h-6 text-micro gap-1"
            onClick={handleRefresh}
            disabled={refreshing}
          >
            {refreshing ? <Loader2 className="h-3 w-3 animate-spin" /> : <RefreshCw className="h-3 w-3" />}
          </Button>
        </div>
      </div>

      {expanded && (
        <div className="px-4 pb-4">
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-3">
            <SectionCard
              title={<span className="flex items-center gap-2 text-label font-medium"><Server className="h-3.5 w-3.5 text-muted-foreground" />Engine</span>}
            >
              <PropertyRow label="Status">
                <div className="flex items-center gap-2">
                  <StatusIcon ok={engineOk} />
                  <span className="text-body font-medium">{engineOk ? "Running" : "Unreachable"}</span>
                </div>
              </PropertyRow>
              {engineOk && data.crewshipd.uptime && (
                <PropertyRow label="Uptime"><span className="font-mono text-label">{data.crewshipd.uptime}</span></PropertyRow>
              )}
              {engineOk && data.crewshipd.connections !== undefined && (
                <PropertyRow label="WS conns"><span className="font-mono text-label">{data.crewshipd.connections}</span></PropertyRow>
              )}
              {engineOk && data.crewshipd.started_at && (
                <PropertyRow label="Started"><span className="font-mono text-micro">{formatTime(data.crewshipd.started_at)}</span></PropertyRow>
              )}
            </SectionCard>

            <SectionCard
              title={<span className="flex items-center gap-2 text-label font-medium"><Cpu className="h-3.5 w-3.5 text-muted-foreground" />Runtime</span>}
            >
              <PropertyRow label="Status"><StatusBadge status={runtimeStatus.toUpperCase()} label={runtimeStatus} /></PropertyRow>
              <PropertyRow label="DB"><StatusBadge status={data.agent.db_status.toUpperCase()} label={data.agent.db_status} /></PropertyRow>
              <PropertyRow label="Adapter"><span className="font-mono text-micro">{data.agent.cli_adapter}</span></PropertyRow>
              {data.runtime.container_id && (
                <PropertyRow label="Container"><code className="font-mono text-micro truncate" title={data.runtime.container_id}>{data.runtime.container_id.slice(0, 12)}</code></PropertyRow>
              )}
              {data.runtime.session_id && (
                <PropertyRow label="Session"><code className="font-mono text-micro truncate">{data.runtime.session_id.slice(0, 8)}</code></PropertyRow>
              )}
            </SectionCard>

            <SectionCard
              title={<span className="flex items-center gap-2 text-label font-medium"><Settings2 className="h-3.5 w-3.5 text-muted-foreground" />Providers</span>}
            >
              {engineOk && data.crewshipd.providers ? (
                <>
                  {Object.entries(data.crewshipd.providers).map(([key, val]) => (
                    <PropertyRow key={key} label={<span className="capitalize">{key}</span>}>
                      <span className="font-mono text-label">{val}</span>
                    </PropertyRow>
                  ))}
                  <PropertyRow label="Container"><StatusIcon ok={!!data.crewshipd.container_available} /></PropertyRow>
                  <PropertyRow label="LLM Proxy"><StatusIcon ok={!!data.crewshipd.llm_proxy_enabled} /></PropertyRow>
                </>
              ) : (
                <p className="text-label text-muted-foreground">Engine not reachable</p>
              )}
            </SectionCard>

            <SectionCard
              title={<span className="flex items-center gap-2 text-label font-medium"><Cpu className="h-3.5 w-3.5 text-muted-foreground" />Last run</span>}
            >
              {resultMeta ? (
                <>
                  {resultMeta.total_cost_usd != null && (
                    <PropertyRow label="Cost"><span className="font-mono text-label font-medium">${Number(resultMeta.total_cost_usd).toFixed(4)}</span></PropertyRow>
                  )}
                  {resultMeta.duration_ms != null && (
                    <PropertyRow label="Duration"><span className="font-mono text-label">{(Number(resultMeta.duration_ms) / 1000).toFixed(1)}s</span></PropertyRow>
                  )}
                  {resultMeta.num_turns != null && (
                    <PropertyRow label="Turns"><span className="font-mono text-label">{String(resultMeta.num_turns)}</span></PropertyRow>
                  )}
                  {initMeta?.model && (
                    <PropertyRow label="Model"><span className="font-mono text-micro">{String(initMeta.model)}</span></PropertyRow>
                  )}
                </>
              ) : (
                <p className="text-label text-muted-foreground">No run data yet</p>
              )}
            </SectionCard>
          </div>
        </div>
      )}
    </div>
  )
}
