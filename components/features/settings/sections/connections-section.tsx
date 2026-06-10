"use client"

import { useCallback, useEffect, useState } from "react"
import {
  Link2, Unlink2, ArrowLeftRight, ArrowRight, Loader2, Trash2,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { StatusBadge } from "@/components/ui/status-badge"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import { resolveCrewColor } from "@/lib/colors"
import { toast } from "sonner"
import { SettingsCard, SettingsRow } from "../shared"

// ── Types ────────────────────────────────────────────────────────────

interface Crew {
  id: string
  name: string
  slug: string
  color?: string | null
}

interface Connection {
  id: string
  from_crew_id: string
  from_crew_name: string
  from_crew_slug: string
  to_crew_id: string
  to_crew_name: string
  to_crew_slug: string
  direction: string
  status: string
  created_at: string
}

interface ConnectionsSectionProps {
  workspaceId: string
}

// ── CrewDot ──────────────────────────────────────────────────────────

function CrewDot({ color }: { color?: string | null }) {
  const hex = resolveCrewColor(color)
  return (
    <span
      className="inline-block size-2 rounded-sm shrink-0"
      style={{ backgroundColor: hex }}
    />
  )
}

// ── Component ────────────────────────────────────────────────────────

export function ConnectionsSection({ workspaceId }: ConnectionsSectionProps) {
  const [crews, setCrews] = useState<Crew[]>([])
  const [connections, setConnections] = useState<Connection[]>([])
  const [loading, setLoading] = useState(true)

  // form state
  const [fromCrewId, setFromCrewId] = useState("")
  const [toCrewId, setToCrewId] = useState("")
  const [direction, setDirection] = useState<"bidirectional" | "unidirectional">("bidirectional")
  const [connecting, setConnecting] = useState(false)
  const [disconnectingId, setDisconnectingId] = useState<string | null>(null)

  const fetchData = useCallback(async () => {
    try {
      const [connsRes, crewsRes] = await Promise.all([
        fetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`),
        fetch(`/api/v1/crews?workspace_id=${workspaceId}`),
      ])
      if (connsRes.ok) setConnections(await connsRes.json())
      if (crewsRes.ok) setCrews(await crewsRes.json())
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => { fetchData() }, [fetchData])

  const handleConnect = useCallback(async () => {
    if (!fromCrewId || !toCrewId || fromCrewId === toCrewId) return
    setConnecting(true)
    try {
      const res = await fetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          from_crew_id: fromCrewId,
          to_crew_id: toCrewId,
          direction,
        }),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.error ?? "Failed to create connection")
        return
      }
      toast.success("Connection created")
      setFromCrewId("")
      setToCrewId("")
      setDirection("bidirectional")
      await fetchData()
    } catch {
      toast.error("Failed to create connection")
    } finally {
      setConnecting(false)
    }
  }, [fromCrewId, toCrewId, direction, workspaceId, fetchData])

  const handleDisconnect = useCallback(async (connId: string) => {
    if (!window.confirm("Disconnect these crews?")) return
    setDisconnectingId(connId)
    try {
      const res = await fetch(`/api/v1/crew-connections/${connId}?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })
      if (!res.ok) {
        toast.error("Failed to disconnect")
        return
      }
      toast.success("Connection removed")
      await fetchData()
    } catch {
      toast.error("Failed to disconnect")
    } finally {
      setDisconnectingId(null)
    }
  }, [workspaceId, fetchData])

  const crewMap = new Map(crews.map((c) => [c.id, c]))
  const toCrews = crews.filter((c) => c.id !== fromCrewId)
  const canConnect = fromCrewId && toCrewId && fromCrewId !== toCrewId && !connecting

  if (loading) {
    return (
      <div className="space-y-5">
        <Skeleton className="h-[200px] rounded-xl" />
        <Skeleton className="h-[120px] rounded-xl" />
      </div>
    )
  }

  return (
    <div className="space-y-5">
      {/* ── Create Connection ── */}
      <SettingsCard
        title="Create connection"
        description="Link two crews so agents on one can dispatch tasks to the other"
      >
        <SettingsRow label="From" description="Source crew">
          <Select value={fromCrewId} onValueChange={(v) => {
            setFromCrewId(v)
            if (v === toCrewId) setToCrewId("")
          }}>
            <SelectTrigger className="w-[200px] h-7 text-xs">
              <SelectValue placeholder="Select crew" />
            </SelectTrigger>
            <SelectContent>
              {crews.map((c) => (
                <SelectItem key={c.id} value={c.id} className="text-xs">
                  <span className="flex items-center gap-2">
                    <CrewDot color={c.color} />
                    {c.name}
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </SettingsRow>

        <SettingsRow label="Direction" description="How tasks can flow">
          <div
            className="flex rounded-md overflow-hidden border border-border/60"
            role="radiogroup"
            aria-label="Connection direction"
          >
            <button
              type="button"
              role="radio"
              aria-checked={direction === "bidirectional"}
              aria-pressed={direction === "bidirectional"}
              onClick={() => setDirection("bidirectional")}
              className={cn(
                "flex items-center gap-1.5 px-2.5 h-7 text-xs font-medium transition-colors",
                direction === "bidirectional"
                  ? "bg-accent text-foreground"
                  : "bg-transparent text-muted-foreground hover:text-foreground",
              )}
            >
              <ArrowLeftRight className="size-3" />
              Both ways
            </button>
            <button
              type="button"
              role="radio"
              aria-checked={direction === "unidirectional"}
              aria-pressed={direction === "unidirectional"}
              onClick={() => setDirection("unidirectional")}
              className={cn(
                "flex items-center gap-1.5 px-2.5 h-7 text-xs font-medium transition-colors border-l border-border/60",
                direction === "unidirectional"
                  ? "bg-accent text-foreground"
                  : "bg-transparent text-muted-foreground hover:text-foreground",
              )}
            >
              <ArrowRight className="size-3" />
              One-way
            </button>
          </div>
        </SettingsRow>

        <SettingsRow label="To" description="Target crew">
          <Select value={toCrewId} onValueChange={setToCrewId} disabled={!fromCrewId}>
            <SelectTrigger className="w-[200px] h-7 text-xs">
              <SelectValue placeholder={fromCrewId ? "Select crew" : "Select source first"} />
            </SelectTrigger>
            <SelectContent>
              {toCrews.map((c) => (
                <SelectItem key={c.id} value={c.id} className="text-xs">
                  <span className="flex items-center gap-2">
                    <CrewDot color={c.color} />
                    {c.name}
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </SettingsRow>

        <div className="flex items-center justify-end px-4 py-2.5">
          <Button
            type="button"
            size="sm"
            className="h-7 px-2.5 text-xs"
            disabled={!canConnect}
            onClick={handleConnect}
          >
            {connecting ? (
              <Loader2 className="mr-1.5 size-3 animate-spin" />
            ) : (
              <Link2 className="mr-1.5 size-3" />
            )}
            Connect
          </Button>
        </div>
      </SettingsCard>

      {/* ── Active Connections ── */}
      <SettingsCard
        title="Active connections"
        description={connections.length === 0 ? "No connections yet" : `${connections.length} active link${connections.length === 1 ? "" : "s"}`}
      >
        {connections.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-10 text-center">
            <div className="w-8 h-8 rounded-lg bg-muted/50 flex items-center justify-center mb-2">
              <Unlink2 className="h-3.5 w-3.5 text-muted-foreground" />
            </div>
            <div className="text-xs font-medium text-foreground/80">No connections</div>
            <div className="text-[11px] text-muted-foreground mt-0.5 max-w-xs">
              Connect crews to enable cross-crew task dispatch in missions.
            </div>
          </div>
        ) : (
          connections.map((conn, i) => {
            const fromCrew = crewMap.get(conn.from_crew_id)
            const toCrew = crewMap.get(conn.to_crew_id)
            const isLast = i === connections.length - 1
            const isDisconnecting = disconnectingId === conn.id
            const isActive = conn.status === "active"
            return (
              <div
                key={conn.id}
                className={cn(
                  "flex items-center justify-between gap-4 px-4 py-2.5",
                  !isLast && "border-b border-border/40",
                )}
              >
                <div className="flex items-center gap-2 text-xs text-foreground min-w-0">
                  <CrewDot color={fromCrew?.color} />
                  <span className="truncate">{conn.from_crew_name}</span>
                  {conn.direction === "bidirectional" ? (
                    <ArrowLeftRight className="size-3 text-muted-foreground shrink-0" />
                  ) : (
                    <ArrowRight className="size-3 text-muted-foreground shrink-0" />
                  )}
                  <CrewDot color={toCrew?.color} />
                  <span className="truncate">{conn.to_crew_name}</span>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  <StatusBadge
                    status={isActive ? "COMPLETED" : "PENDING"}
                    label={conn.status}
                    className="text-[10px]"
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    disabled={isDisconnecting}
                    onClick={() => handleDisconnect(conn.id)}
                    aria-label={`Disconnect ${conn.from_crew_name} ${conn.direction === "bidirectional" ? "↔" : "→"} ${conn.to_crew_name}`}
                    className="h-6 w-6 text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                  >
                    {isDisconnecting ? (
                      <Loader2 className="size-3 animate-spin" />
                    ) : (
                      <Trash2 className="size-3" />
                    )}
                  </Button>
                </div>
              </div>
            )
          })
        )}
      </SettingsCard>
    </div>
  )
}
