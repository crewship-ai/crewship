"use client"

import { useEffect, useState } from "react"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"

import type { BottomPanelContext, ContainerStatus } from "./types"
import { EmptyState } from "./shared"

/**
 * Reads the `containers` envelope, and THROWS when it isn't there.
 *
 * This tab used to ask GET /api/v1/system/runtime — the host runtime
 * inventory — for a `containers` field that endpoint has never sent, and
 * guarded it with `Array.isArray(data?.containers) ? data.containers : []`.
 * That guard is the bug: `Array.isArray(undefined)` is false, so a response
 * with no such field became an empty list and the tab rendered "No containers
 * running." on every crew, forever, with every container up (#1697). The line
 * that would have surfaced the mismatch was the same line that swallowed it.
 *
 * So the fallback is gone. A response that does not carry a `containers`
 * array is a broken contract, not an empty crew, and the tab now says so.
 * Exported for the unit test — the failure mode this guards is exactly the
 * kind that renders fine and reports nothing.
 */
export function parseContainerList(data: unknown): ContainerStatus[] {
  if (data === null || typeof data !== "object" || Array.isArray(data)) {
    throw new Error(
      `expected an object with a \`containers\` array, got ${Array.isArray(data) ? "an array" : typeof data}`,
    )
  }
  const list = (data as { containers?: unknown }).containers
  if (!Array.isArray(list)) {
    throw new Error(
      `response has no \`containers\` array (got ${list === undefined ? "no such field" : typeof list}) — the API shape changed`,
    )
  }
  return list as ContainerStatus[]
}

/** The crew whose containers this tab shows, or null when nothing selected. */
function crewIdFor(context: BottomPanelContext): string | null {
  if (!context) return null
  switch (context.kind) {
    case "crew":
      return context.crewId
    // An agent (or one of its issues) runs inside its crew's container, so
    // the crew's containers are the right answer for both.
    case "agent":
    case "mission":
      return context.crewId
    default:
      return null
  }
}

/**
 * Docker — the crew's live containers: its agent runtime and every sidecar,
 * with state, CPU, memory and (for the runtime) how many agents run in it.
 *
 * Reads GET /api/v1/crews/{crewId}/containers, which is where per-crew
 * container facts live. /api/v1/system/runtime is the host runtime INVENTORY
 * ("which runtimes does this machine have") and answers a different question.
 */
export function DockerTab({
  workspaceId,
  context,
}: {
  workspaceId: string
  context: BottomPanelContext
}) {
  const [containers, setContainers] = useState<ContainerStatus[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const crewId = crewIdFor(context)

  useEffect(() => {
    if (!crewId) return
    let cancelled = false
    setContainers(null)
    setError(null)
    apiFetch(`/api/v1/crews/${crewId}/containers?workspace_id=${workspaceId}`)
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((data) => {
        if (cancelled) return
        setContainers(parseContainerList(data))
      })
      .catch((err) => {
        if (cancelled) return
        setError(err instanceof Error ? err.message : String(err))
        setContainers([])
      })
    return () => { cancelled = true }
  }, [crewId, workspaceId])

  if (!crewId) return <EmptyState>Select a crew to see its containers.</EmptyState>
  if (error) return <EmptyState><span className="text-destructive">Failed to load: {error}</span></EmptyState>
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
                  c.status?.toLowerCase().includes("running") ? "bg-success" : "bg-muted-foreground",
                )}
              />
              <code className="text-xs">{c.name}</code>
            </span>
            <code className="text-xs text-muted-foreground">{c.image}</code>
            <span
              className={cn(
                "text-xs",
                c.status?.toLowerCase().includes("running") ? "text-success" : "text-muted-foreground",
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
