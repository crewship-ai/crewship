"use client"

import { useCallback, useEffect, useState } from "react"
import {
  Link2, Unlink2, ArrowLeftRight, ArrowRight, Loader2, Trash2,
} from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { EmptyState } from "@/components/layout/empty-state"
import { cn } from "@/lib/utils"
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

const crewColorMap: Record<string, string> = {
  blue: "#3b82f6",
  emerald: "#10b981",
  violet: "#8b5cf6",
  amber: "#f59e0b",
  rose: "#f43f5e",
  cyan: "#06b6d4",
  lime: "#84cc16",
  fuchsia: "#d946ef",
}

// ─── Row ────────────────────────────────────────────────────────────

function Row({ label, description, children, border = true }: {
  label?: string
  description?: string
  children: React.ReactNode
  border?: boolean
}) {
  return (
    <div className={cn(
      "flex items-center justify-between gap-4 px-5 py-3.5 min-h-[48px]",
      border && "border-b border-white/[0.04] last:border-b-0",
    )}>
      {label ? (
        <div className="shrink-0">
          <div className="text-[13px] text-foreground">{label}</div>
          {description && <div className="text-[11px] text-muted-foreground/30 mt-0.5">{description}</div>}
        </div>
      ) : <div />}
      <div className="flex items-center gap-2 min-w-0 justify-end">{children}</div>
    </div>
  )
}

// ─── Crew dot ───────────────────────────────────────────────────────

function CrewDot({ color }: { color?: string | null }) {
  const hex = crewColorMap[color ?? ""] ?? "#6b7280"
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
        <h4 className="text-[13px] font-medium text-foreground mb-3">Create Connection</h4>
        <Card className="border-white/[0.06] bg-white/[0.02]">
          <CardContent className="p-0">
            <Row label="From" description="Source crew">
              <Select value={fromCrewId} onValueChange={(v) => {
                setFromCrewId(v)
                if (v === toCrewId) setToCrewId("")
              }}>
                <SelectTrigger className="w-[200px] h-[30px] text-[12px] bg-white/[0.03] border-white/[0.06]">
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
              <div className="flex rounded-md overflow-hidden border border-white/[0.08]">
                <button
                  type="button"
                  onClick={() => setDirection("bidirectional")}
                  className={cn(
                    "flex items-center gap-1.5 px-3 h-[30px] text-[11px] font-medium transition-colors",
                    direction === "bidirectional"
                      ? "bg-white/[0.08] text-foreground"
                      : "bg-transparent text-muted-foreground/50 hover:text-muted-foreground/70",
                  )}
                >
                  <ArrowLeftRight className="size-3" />
                  Bidirectional
                </button>
                <button
                  type="button"
                  onClick={() => setDirection("unidirectional")}
                  className={cn(
                    "flex items-center gap-1.5 px-3 h-[30px] text-[11px] font-medium transition-colors border-l border-white/[0.08]",
                    direction === "unidirectional"
                      ? "bg-white/[0.08] text-foreground"
                      : "bg-transparent text-muted-foreground/50 hover:text-muted-foreground/70",
                  )}
                >
                  <ArrowRight className="size-3" />
                  One-way
                </button>
              </div>
            </Row>

            <Row label="To" description="Target crew">
              <Select value={toCrewId} onValueChange={setToCrewId} disabled={!fromCrewId}>
                <SelectTrigger className="w-[200px] h-[30px] text-[12px] bg-white/[0.03] border-white/[0.06]">
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
              <button
                type="button"
                disabled={!canConnect}
                onClick={handleConnect}
                className={cn(
                  "h-[26px] px-2.5 rounded-[4px] text-[11px] font-medium inline-flex items-center gap-1.5 transition-colors",
                  "bg-blue-500/15 border border-blue-500/35 text-blue-400",
                  "hover:bg-blue-500/25",
                  "disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:bg-blue-500/15",
                )}
              >
                {connecting ? (
                  <Loader2 className="size-3 animate-spin" />
                ) : (
                  <Link2 className="size-3" />
                )}
                Connect
              </button>
            </Row>
          </CardContent>
        </Card>
      </div>

      {/* Active Connections */}
      <div>
        <h4 className="text-[13px] font-medium text-foreground mb-3">Active Connections</h4>
        {connections.length === 0 ? (
          <EmptyState
            icon={Unlink2}
            title="No connections"
            description="Connect crews to enable cross-crew task dispatch in missions."
          />
        ) : (
          <Card className="border-white/[0.06] bg-white/[0.02]">
            <CardContent className="p-0">
              {connections.map((conn, i) => {
                const fromCrew = crewMap.get(conn.from_crew_id)
                const toCrew = crewMap.get(conn.to_crew_id)
                const isLast = i === connections.length - 1
                const isDisconnecting = disconnectingId === conn.id
                const isActive = conn.status === "active"

                return (
                  <Row key={conn.id} border={!isLast}>
                    <div className="flex items-center gap-2 text-[12px] text-foreground min-w-0">
                      <CrewDot color={fromCrew?.color} />
                      <span className="truncate">{conn.from_crew_name}</span>
                      {conn.direction === "bidirectional" ? (
                        <ArrowLeftRight className="size-3 text-muted-foreground/40 shrink-0" />
                      ) : (
                        <ArrowRight className="size-3 text-muted-foreground/40 shrink-0" />
                      )}
                      <CrewDot color={toCrew?.color} />
                      <span className="truncate">{conn.to_crew_name}</span>
                    </div>
                    <div className="flex items-center gap-2 shrink-0">
                      <Badge
                        variant="outline"
                        className={cn(
                          "text-[10px] px-1.5 py-0 h-[18px] border",
                          isActive
                            ? "border-emerald-500/30 text-emerald-400 bg-emerald-500/10"
                            : "border-white/[0.08] text-muted-foreground/50 bg-white/[0.02]",
                        )}
                      >
                        {conn.status}
                      </Badge>
                      <button
                        type="button"
                        disabled={isDisconnecting}
                        onClick={() => handleDisconnect(conn.id)}
                        className={cn(
                          "size-6 inline-flex items-center justify-center rounded-[4px] transition-colors",
                          "text-muted-foreground/40 hover:text-red-400 hover:bg-red-500/10",
                          "disabled:opacity-40 disabled:cursor-not-allowed",
                        )}
                      >
                        {isDisconnecting ? (
                          <Loader2 className="size-3 animate-spin" />
                        ) : (
                          <Trash2 className="size-3" />
                        )}
                      </button>
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
