"use client"

import { useEffect, useState } from "react"
import { cn } from "@/lib/utils"

import type { ContainerStatus } from "./types"
import { EmptyState } from "./shared"

export function DockerTab() {
  const [containers, setContainers] = useState<ContainerStatus[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    let cancelled = false
    fetch("/api/v1/system/runtime")
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((data) => {
        if (cancelled) return
        const list: ContainerStatus[] = Array.isArray(data?.containers) ? data.containers : []
        setContainers(list)
      })
      .catch((err) => {
        if (cancelled) return
        setError(err instanceof Error ? err.message : String(err))
        setContainers([])
      })
    return () => { cancelled = true }
  }, [])

  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (containers === null) return <EmptyState>Loading container status…</EmptyState>
  if (containers.length === 0) return <EmptyState>No containers running.</EmptyState>

  return (
    <div className="h-full overflow-y-auto">
      <div className="grid grid-cols-[1fr_180px_120px_80px_80px_70px] gap-3 px-4 py-2 border-b border-white/8 text-[10px] uppercase tracking-wide text-muted-foreground">
        <span>Container</span>
        <span>Image</span>
        <span>Status</span>
        <span>CPU</span>
        <span>RAM</span>
        <span>Agents</span>
      </div>
      <div className="divide-y divide-white/5 text-sm">
        {containers.map((c) => (
          <div
            key={c.name}
            className="grid grid-cols-[1fr_180px_120px_80px_80px_70px] gap-3 px-4 py-2 items-center"
          >
            <span className="flex items-center gap-2">
              <span
                className={cn(
                  "w-1.5 h-1.5 rounded-full",
                  c.status?.toLowerCase().includes("running") ? "bg-emerald-400" : "bg-zinc-500",
                )}
              />
              <code className="text-xs">{c.name}</code>
            </span>
            <code className="text-xs text-muted-foreground">{c.image}</code>
            <span
              className={cn(
                "text-xs",
                c.status?.toLowerCase().includes("running") ? "text-emerald-400" : "text-muted-foreground",
              )}
            >
              {c.status}
            </span>
            <span className="text-xs">
              {c.cpu_percent !== null && c.cpu_percent !== undefined ? `${c.cpu_percent}%` : "—"}
            </span>
            <span className="text-xs">
              {c.memory_mb !== null && c.memory_mb !== undefined ? `${c.memory_mb} MB` : "—"}
            </span>
            <span className="text-xs">{c.agent_count ?? "—"}</span>
          </div>
        ))}
      </div>
    </div>
  )
}
