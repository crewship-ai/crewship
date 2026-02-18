"use client"

import { use, useState, useEffect } from "react"
import {
  Plus, Settings, Puzzle, KeyRound, CheckCircle2,
  AlertCircle, Inbox,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"

interface AuditEvent {
  id: string
  action: string
  entity_type: string
  entity_id: string
  changes: Record<string, { old?: string; new?: string }> | null
  user_name: string | null
  created_at: string
}

const CATEGORY_CONFIG: Record<string, { label: string; icon: typeof Settings; className: string }> = {
  CONFIG: { label: "CONFIG", icon: Settings, className: "bg-amber-100 text-amber-600 dark:bg-amber-950 dark:text-amber-400" },
  SKILL: { label: "SKILL", icon: Puzzle, className: "bg-blue-100 text-blue-600 dark:bg-blue-950 dark:text-blue-400" },
  CRED: { label: "CRED", icon: KeyRound, className: "bg-purple-100 text-purple-600 dark:bg-purple-950 dark:text-purple-400" },
  CREATED: { label: "CREATED", icon: Plus, className: "bg-emerald-100 text-emerald-600 dark:bg-emerald-950 dark:text-emerald-400" },
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

function formatDate(dateStr: string): string {
  const d = new Date(dateStr)
  return d.toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" }) +
    " " + d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })
}

/** Agent configuration change history timeline. */
export default function HistoryPage({ params }: { params: Promise<{ agentId: string }> }) {
  const { agentId } = use(params)
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
        const res = await fetch(`/api/v1/audit?workspace_id=${workspaceId}&entity_id=${agentId}&limit=50`)
        if (!res.ok) {
          if (!cancelled) setError("Failed to load history")
          return
        }
        const data = await res.json()
        const items = Array.isArray(data) ? data : (data.items ?? [])
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
      <div className="p-4 sm:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-sm">{error}</p>
        </div>
      </div>
    )
  }

  if (events.length === 0) {
    return (
      <div className="p-4 sm:p-6">
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <Inbox className="h-10 w-10 text-muted-foreground/50 mb-3" />
          <p className="text-sm font-medium text-muted-foreground">No history yet</p>
          <p className="text-xs text-muted-foreground mt-1">Configuration changes will appear here as a timeline.</p>
        </div>
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 max-w-4xl space-y-0">
      <p className="text-sm text-muted-foreground mb-6">Configuration change history</p>

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
                <div className={`flex h-10 w-10 shrink-0 items-center justify-center rounded-full ${
                  isFirst ? "bg-primary text-primary-foreground" :
                  category === "CREATED" ? "bg-emerald-500 text-white" :
                  "bg-muted"
                }`}>
                  {isFirst ? (
                    <CheckCircle2 className="h-4 w-4" />
                  ) : category === "CREATED" ? (
                    <Plus className="h-4 w-4" />
                  ) : (
                    <Icon className="h-4 w-4" />
                  )}
                </div>
              </div>

              <div className={`flex-1 border rounded-lg ${isFirst ? "border-primary/30 border-2" : "border-border"}`}>
                <div className="px-5 py-3 border-b flex items-center justify-between flex-wrap gap-2">
                  <div className="flex items-center gap-2 flex-wrap">
                    {isFirst && (
                      <Badge className="bg-primary/10 text-primary text-[10px] font-semibold">CURRENT</Badge>
                    )}
                    <Badge variant="outline" className={`${config.className} text-[10px]`}>{config.label}</Badge>
                    <span className="text-sm font-medium">{formatEventTitle(event)}</span>
                  </div>
                  <div className="flex items-center gap-3 text-xs text-muted-foreground">
                    {event.user_name && <span>{event.user_name}</span>}
                    <span>{formatDate(event.created_at)}</span>

                  </div>
                </div>

                {/* Diff view */}
                {event.changes && Object.keys(event.changes).length > 0 && (
                  <div className="px-5 py-3 space-y-2">
                    <div className="text-xs text-muted-foreground font-medium">Changes:</div>
                    {Object.entries(event.changes).map(([key, change]) => (
                      <div key={key} className="font-mono text-xs space-y-1">
                        {change.old !== undefined && (
                          <div className="bg-red-50 dark:bg-red-950/30 text-red-700 dark:text-red-400 rounded px-2 py-1">
                            - &quot;{key}&quot;: &quot;{String(change.old)}&quot;
                          </div>
                        )}
                        {change.new !== undefined && (
                          <div className="bg-emerald-50 dark:bg-emerald-950/30 text-emerald-700 dark:text-emerald-400 rounded px-2 py-1">
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

      <p className="text-xs text-muted-foreground pt-4">{events.length} event{events.length !== 1 ? "s" : ""} total</p>
    </div>
  )
}

function HistorySkeleton() {
  return (
    <div className="p-4 sm:p-6 max-w-4xl space-y-6">
      <Skeleton className="h-5 w-48" />
      {Array.from({ length: 4 }).map((_, i) => (
        <div key={i} className="flex gap-4">
          <Skeleton className="h-10 w-10 rounded-full shrink-0" />
          <Skeleton className="h-24 flex-1 rounded-lg" />
        </div>
      ))}
    </div>
  )
}
