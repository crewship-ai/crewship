"use client"

import { useCallback, useEffect, useState } from "react"
import {
  Link2, Unlink2, ArrowLeftRight, ArrowRight, Loader2, Trash2,
} from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { StatusBadge } from "@/components/ui/status-badge"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { EmptyState } from "@/components/layout/empty-state"
import { cn } from "@/lib/utils"
import { resolveCrewColor } from "@/lib/colors"
import { toast } from "sonner"

// ─── Types ──────────────────────────────────────────────────────────

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

// ─── Constants ──────────────────────────────────────────────────────


// ─── Row ────────────────────────────────────────────────────────────

function Row({ label, description, children, border = true }: {
  label?: string
  description?: string
  children: React.ReactNode
  border?: boolean
}) {
  return (
    <div className={cn(
      "flex items-center justify-between gap-4 px-4 py-2.5",
      border && "border-b border-border/40 last:border-b-0",
    )}>
      {label ? (
        <div className="shrink-0">
          <div className="text-body text-foreground">{label}</div>
          {description && <div className="text-label text-muted-foreground mt-0.5">{description}</div>}
        </div>
      ) : <div />}
      <div className="flex items-center gap-2 min-w-0 justify-end">{children}</div>
    </div>
  )
}

// ─── Crew dot ───────────────────────────────────────────────────────

function CrewDot({ color }: { color?: string | null }) {
  const hex = resolveCrewColor(color)
  return (
    <span
      className="inline-block size-2 rounded-full shrink-0"
      style={{ backgroundColor: hex }}
    />
  )
}

// ─── Main ───────────────────────────────────────────────────────────

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

  // ── Fetch ─────────────────────────────────────────────────────────

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

  // ── Connect ───────────────────────────────────────────────────────

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

  // ── Disconnect ────────────────────────────────────────────────────

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

  // ── Helpers ───────────────────────────────────────────────────────

  const crewMap = new Map(crews.map((c) => [c.id, c]))
  const toCrews = crews.filter((c) => c.id !== fromCrewId)
  const canConnect = fromCrewId && toCrewId && fromCrewId !== toCrewId && !connecting

  // ── Loading ───────────────────────────────────────────────────────

  if (loading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-5 w-40" />
        <Skeleton className="h-[200px] w-full rounded-lg" />
        <Skeleton className="h-5 w-40 mt-4" />
        <Skeleton className="h-[120px] w-full rounded-lg" />
      </div>
    )
  }

  // ── Render ────────────────────────────────────────────────────────

  return (
    <div className="space-y-6">
      {/* Create Connection */}
      <div>
        <h4 className="text-body font-medium text-foreground/80 mb-3">Create Connection</h4>
        <Card>
          <CardContent className="p-0">
            <Row label="From" description="Source crew">
              <Select value={fromCrewId} onValueChange={(v) => {
                setFromCrewId(v)
                if (v === toCrewId) setToCrewId("")
              }}>
                <SelectTrigger className="w-[200px] h-7 text-label">
                  <SelectValue placeholder="Select crew" />
                </SelectTrigger>
                <SelectContent>
                  {crews.map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      <span className="flex items-center gap-2">
                        <CrewDot color={c.color} />
                        {c.name}
                      </span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Row>

            <Row label="Direction" description="How tasks can flow">
              <div className="flex rounded-md overflow-hidden border border-border">
                <button
                  type="button"
                  onClick={() => setDirection("bidirectional")}
                  className={cn(
                    "flex items-center gap-1.5 px-3 h-7 text-label font-medium transition-colors",
                    direction === "bidirectional"
                      ? "bg-accent text-foreground"
                      : "bg-transparent text-muted-foreground hover:text-foreground",
                  )}
                >
                  <ArrowLeftRight className="size-3" />
                  Bidirectional
                </button>
                <button
                  type="button"
                  onClick={() => setDirection("unidirectional")}
                  className={cn(
                    "flex items-center gap-1.5 px-3 h-7 text-label font-medium transition-colors border-l border-border",
                    direction === "unidirectional"
                      ? "bg-accent text-foreground"
                      : "bg-transparent text-muted-foreground hover:text-foreground",
                  )}
                >
                  <ArrowRight className="size-3" />
                  One-way
                </button>
              </div>
            </Row>

            <Row label="To" description="Target crew">
              <Select value={toCrewId} onValueChange={setToCrewId} disabled={!fromCrewId}>
                <SelectTrigger className="w-[200px] h-7 text-label">
                  <SelectValue placeholder={fromCrewId ? "Select crew" : "Select source first"} />
                </SelectTrigger>
                <SelectContent>
                  {toCrews.map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      <span className="flex items-center gap-2">
                        <CrewDot color={c.color} />
                        {c.name}
                      </span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Row>

            <Row border={false}>
              <Button
                type="button"
                size="sm"
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
            </Row>
          </CardContent>
        </Card>
      </div>

      {/* Active Connections */}
      <div>
        <h4 className="text-body font-medium text-foreground/80 mb-3">Active Connections</h4>
        {connections.length === 0 ? (
          <EmptyState
            icon={Unlink2}
            title="No connections"
            description="Connect crews to enable cross-crew task dispatch in missions."
          />
        ) : (
          <Card>
            <CardContent className="p-0">
              {connections.map((conn, i) => {
                const fromCrew = crewMap.get(conn.from_crew_id)
                const toCrew = crewMap.get(conn.to_crew_id)
                const isLast = i === connections.length - 1
                const isDisconnecting = disconnectingId === conn.id
                const isActive = conn.status === "active"

                return (
                  <Row key={conn.id} border={!isLast}>
                    <div className="flex items-center gap-2 text-body text-foreground min-w-0">
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
                      />
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        disabled={isDisconnecting}
                        onClick={() => handleDisconnect(conn.id)}
                        className="h-6 w-6 text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                      >
                        {isDisconnecting ? (
                          <Loader2 className="size-3 animate-spin" />
                        ) : (
                          <Trash2 className="size-3" />
                        )}
                      </Button>
                    </div>
                  </Row>
                )
              })}
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  )
}
