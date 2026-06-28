"use client"

import { useParams } from "next/navigation"
import { useState, useEffect } from "react"
import {
  Plus, Settings, Puzzle, KeyRound, CheckCircle2,
  AlertCircle, Inbox,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { EmptyState } from "@/components/layout/empty-state"
import { formatDateTime } from "@/lib/time"
import { useWorkspace } from "@/hooks/use-workspace"
import type { AuditEvent } from "@/lib/types/agent"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"

const CATEGORY_CONFIG: Record<
  string,
  { label: string; icon: typeof Settings }
> = {
  CONFIG: { label: "CONFIG", icon: Settings },
  SKILL: { label: "SKILL", icon: Puzzle },
  CRED: { label: "CRED", icon: KeyRound },
  CREATED: { label: "CREATED", icon: Plus },
}

function categorizeEvent(event: AuditEvent): keyof typeof CATEGORY_CONFIG {
  const action = event.action.toUpperCase()
  if (action === "CREATE") return "CREATED"
  if (event.entity_type === "SKILL" || action.includes("SKILL")) return "SKILL"
  if (event.entity_type === "CREDENTIAL" || action.includes("CREDENTIAL")) return "CRED"
  return "CONFIG"
}

function formatEventTitle(event: AuditEvent): string {
  const action = event.action.toLowerCase()
  if (action === "create") return "Agent created"
  if (action === "update" && event.changes) {
    const keys = Object.keys(event.changes)
    if (keys.length === 1) return `Changed ${keys[0].replace(/_/g, " ")}`
    return `Changed ${keys.join(", ").replace(/_/g, " ")}`
  }
  if (action === "delete") return "Deleted"
  return event.action.replace(/_/g, " ")
}

/** Agent configuration change history timeline. Rendered inline in Overview. */
export function HistorySection() {
  const { agentId } = useParams<{ agentId: string }>()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [events, setEvents] = useState<AuditEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!workspaceId) {
      setLoading(false)
      return
    }
    let cancelled = false

    async function fetchHistory() {
      if (!cancelled) {
        setLoading(true)
        setError(null)
      }
      try {
        const res = await apiFetch(`/api/v1/audit?workspace_id=${workspaceId}&entity_id=${agentId}&limit=50`)
        if (!res.ok) {
          if (!cancelled) setError("Failed to load history")
          return
        }
        const data = await res.json()
        const items = Array.isArray(data) ? data : (data.data ?? [])
        if (!cancelled) setEvents(items)
      } catch {
        if (!cancelled) setError("Network error")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchHistory()
    return () => { cancelled = true }
  }, [agentId, workspaceId])

  if (wsLoading || loading) {
    return <HistorySkeleton />
  }

  if (error) {
    return (
      <div className="p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-body">{error}</p>
        </div>
      </div>
    )
  }

  if (events.length === 0) {
    return (
      <div className="p-6 space-y-6 max-w-4xl">
        <div>
          <h2 className="text-title font-semibold">History</h2>
          <p className="text-body text-muted-foreground mt-1">
            Configuration changes for this agent.
          </p>
        </div>
        <EmptyState
          icon={Inbox}
          title="No history yet"
          description="Configuration changes will appear here as a timeline."
        />
      </div>
    )
  }

  return (
    <div className="p-6 space-y-6 max-w-4xl">
      <div>
        <h2 className="text-title font-semibold">History</h2>
        <p className="text-body text-muted-foreground mt-1">
          Configuration change history ({events.length} event{events.length !== 1 ? "s" : ""})
        </p>
      </div>

      {/* Timeline */}
      <div className="relative">
        {/* Vertical timeline line */}
        <div className="absolute left-5 top-4 bottom-4 w-px bg-border" />

        {events.map((event, i) => {
          const category = categorizeEvent(event)
          const config = CATEGORY_CONFIG[category]
          const Icon = config.icon
          const isFirst = i === 0

          return (
            <div key={event.id} className="relative flex gap-4 pb-6">
              <div className="relative z-10 mt-1.5">
                <div
                  className={cn(
                    "flex h-10 w-10 shrink-0 items-center justify-center rounded-full",
                    isFirst
                      ? "bg-primary text-primary-foreground"
                      : "bg-muted text-muted-foreground"
                  )}
                >
                  {isFirst ? (
                    <CheckCircle2 className="h-4 w-4" />
                  ) : (
                    <Icon className="h-4 w-4" />
                  )}
                </div>
              </div>

              <div
                className={cn(
                  "flex-1 border rounded-lg bg-card",
                  isFirst ? "border-primary/40" : "border-border"
                )}
              >
                <div className="px-5 py-3 border-b border-border flex items-center justify-between flex-wrap gap-2">
                  <div className="flex items-center gap-2 flex-wrap">
                    {isFirst && (
                      <Badge className="bg-primary/10 text-primary text-micro font-semibold">
                        CURRENT
                      </Badge>
                    )}
                    <Badge variant="outline" className="text-micro">
                      {config.label}
                    </Badge>
                    <span className="text-body font-medium">{formatEventTitle(event)}</span>
                  </div>
                  <div className="flex items-center gap-3 text-label text-muted-foreground">
                    {event.user_name && <span>{event.user_name}</span>}
                    <span>{formatDateTime(event.created_at)}</span>
                  </div>
                </div>

                {/* Diff view */}
                {event.changes && Object.keys(event.changes).length > 0 && (
                  <div className="px-5 py-3 space-y-2">
                    <div className="text-label text-muted-foreground font-medium">Changes:</div>
                    {Object.entries(event.changes).map(([key, change]) => (
                      <div key={key} className="font-mono text-micro space-y-1">
                        {change.old !== undefined && (
                          <div className="rounded border border-destructive/30 bg-destructive/10 text-destructive px-2 py-1">
                            - &quot;{key}&quot;: &quot;{String(change.old)}&quot;
                          </div>
                        )}
                        {change.new !== undefined && (
                          <div className="rounded border border-border bg-surface-subtle text-foreground px-2 py-1">
                            + &quot;{key}&quot;: &quot;{String(change.new)}&quot;
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}

function HistorySkeleton() {
  return (
    <div className="p-6 space-y-6 max-w-4xl">
      <div className="space-y-2">
        <Skeleton className="h-7 w-32" />
        <Skeleton className="h-4 w-64" />
      </div>
      {Array.from({ length: 4 }).map((_, i) => (
        <div key={i} className="flex gap-4">
          <Skeleton className="h-10 w-10 rounded-full shrink-0" />
          <Skeleton className="h-24 flex-1 rounded-lg" />
        </div>
      ))}
    </div>
  )
}
