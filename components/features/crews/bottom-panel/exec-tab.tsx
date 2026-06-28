"use client"

import { useEffect, useState } from "react"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"

import type { BottomPanelContext, LogEntry } from "./types"
import { EmptyState, formatTime } from "./shared"

/**
 * Exec Log — proxy returns a JSON ARRAY of log entries (verified
 * 2026-04-28 in internal/api/proxy.go AgentLogs). No `tail=` param;
 * proxy uses `limit/offset`, default 100. We render whatever recognisable
 * timestamp + message pair each row has.
 */
export function ExecTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelContext }) {
  const [logs, setLogs] = useState<LogEntry[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!context || context.kind !== "agent") return
    let cancelled = false
    setLogs(null)
    setError(null)
    apiFetch(`/api/v1/agents/${context.agentId}/logs?workspace_id=${workspaceId}&limit=200`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data) => {
        if (cancelled) return
        setLogs(Array.isArray(data) ? data : [])
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [context, workspaceId])

  if (!context) return <EmptyState>Select an agent to see its exec log.</EmptyState>
  if (context.kind !== "agent") return <EmptyState>Exec logs are per-agent — select one in the explorer.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (logs === null) return <EmptyState>Loading…</EmptyState>
  if (logs.length === 0) return <EmptyState>No log output yet for {context.agentName}.</EmptyState>

  return (
    <div className="h-full overflow-y-auto p-3 text-[11px] leading-relaxed font-mono text-foreground/80">
      {logs.map((l, i) => {
        const ts = l.ts ?? l.timestamp ?? ""
        const msg = l.message ?? l.msg ?? l.text ?? JSON.stringify(l)
        const level = String(l.level ?? "").toLowerCase()
        const levelColor =
          level.includes("error") || level.includes("fatal") ? "text-red-300" :
          level.includes("warn") ? "text-amber-300" :
          level.includes("info") ? "text-blue-300" :
          "text-muted-foreground"
        return (
          <div key={i} className="flex gap-2 hover:bg-white/[0.03] px-1 -mx-1 rounded">
            {ts && <span className="text-muted-foreground shrink-0">{formatTime(String(ts))}</span>}
            {level && <span className={cn("shrink-0 uppercase", levelColor)}>{level}</span>}
            <span className="break-all">{String(msg)}</span>
          </div>
        )
      })}
    </div>
  )
}
