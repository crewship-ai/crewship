"use client"

import { useEffect, useState } from "react"
import { cn } from "@/lib/utils"

import type { BottomPanelContext } from "./types"
import { EmptyState, formatRelative } from "./shared"

// Mirror of internal/api/issue_handler.go activityResponse.
interface ActivityEntry {
  id: string
  mission_id: string
  actor_type: string
  actor_id?: string | null
  actor_name?: string | null
  action: string
  details?: string | null
  created_at: string
}

// Colour the timeline dot by the kind of actor that produced the event,
// so a glance separates human / agent / system activity.
function actorDot(actorType: string): string {
  switch (actorType) {
    case "user": return "border-violet-400"
    case "agent": return "border-emerald-400"
    case "system": return "border-blue-400"
    default: return "border-muted-foreground"
  }
}

/**
 * Activity — the timeline of everything that happened on the selected
 * issue/mission (status changes, assignments, escalations, runs started).
 * Reads the existing GET /api/v1/crews/{crewId}/issues/{identifier}/activity.
 */
export function ActivityTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelContext }) {
  const [entries, setEntries] = useState<ActivityEntry[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  const isMission = context?.kind === "mission"
  const crewId = isMission ? context.crewId : null
  const identifier = isMission ? context.identifier : null

  useEffect(() => {
    if (!isMission || !crewId || !identifier) return
    let cancelled = false
    setEntries(null)
    setError(null)
    fetch(`/api/v1/crews/${crewId}/issues/${encodeURIComponent(identifier)}/activity?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data) => {
        if (cancelled) return
        // Handler may return a bare array or { activity: [...] }.
        const list = Array.isArray(data) ? data : (data?.activity ?? [])
        setEntries(list)
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [isMission, crewId, identifier, workspaceId])

  if (!context) return <EmptyState>Select an issue to see its activity.</EmptyState>
  if (context.kind !== "mission") return <EmptyState>Activity is per-issue — select one.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (entries === null) return <EmptyState>Loading…</EmptyState>
  if (entries.length === 0) return <EmptyState>No activity recorded yet for {context.identifier}.</EmptyState>

  return (
    <div className="h-full overflow-y-auto p-4 text-xs">
      <div className="relative pl-5 before:absolute before:left-[5px] before:top-1 before:bottom-1 before:w-px before:bg-white/10">
        {entries.map((e) => (
          <div key={e.id} className="relative pb-4 pl-3.5">
            <span
              className={cn(
                "absolute -left-[15px] top-0.5 h-2.5 w-2.5 rounded-full bg-card border-2",
                actorDot(e.actor_type),
              )}
            />
            <div className="text-foreground">
              {e.actor_name && <span className="font-semibold">{e.actor_name}</span>}{" "}
              <span>{e.action}</span>
              {e.details && <span className="text-muted-foreground"> — {e.details}</span>}
            </div>
            <div className="text-muted-foreground-soft mt-0.5 text-[11px]">
              {formatRelative(e.created_at)} · {e.actor_type}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
