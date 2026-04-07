"use client"

import { useCallback, useEffect, useState } from "react"
import {
  Link2, Unlink2, ArrowLeftRight, ArrowRight, Loader2, Users,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { Badge } from "@/components/ui/badge"
import { EmptyState } from "@/components/layout/empty-state"
import { cn } from "@/lib/utils"
import { toast } from "sonner"

interface Crew {
  id: string
  name: string
  slug: string
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

interface CrewConnectionsProps {
  workspaceId: string
}

function hashColor(slug: string): string {
  let h = 0
  for (let i = 0; i < slug.length; i++) h = ((h << 5) - h + slug.charCodeAt(i)) | 0
  const hue = Math.abs(h) % 360
  return `hsl(${hue}, 60%, 50%)`
}

function CrewBadge({ name, slug }: { name: string; slug: string }) {
  const color = hashColor(slug)
  return (
    <div className="flex items-center gap-2 px-3 py-2 rounded-lg bg-accent/50 border border-border">
      <div className="w-3 h-3 rounded-full shrink-0" style={{ backgroundColor: color }} />
      <div>
        <div className="text-sm font-medium text-foreground">{name}</div>
        <div className="text-[11px] text-muted-foreground/70 font-mono">{slug}</div>
      </div>
    </div>
  )
}

export function CrewConnections({ workspaceId }: CrewConnectionsProps) {
  const [connections, setConnections] = useState<Connection[]>([])
  const [crews, setCrews] = useState<Crew[]>([])
  const [loading, setLoading] = useState(true)
  const [fromCrew, setFromCrew] = useState("")
  const [toCrew, setToCrew] = useState("")
  const [direction, setDirection] = useState("bidirectional")
  const [connecting, setConnecting] = useState(false)
  const [disconnecting, setDisconnecting] = useState<string | null>(null)

  const fetchData = useCallback(async () => {
    try {
      const [connsRes, crewsRes] = await Promise.all([
        fetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`),
        fetch(`/api/v1/crews?workspace_id=${workspaceId}`),
      ])
      if (connsRes.ok) setConnections(await connsRes.json())
      if (crewsRes.ok) setCrews(await crewsRes.json())
    } catch { /* ignore */ }
    finally { setLoading(false) }
  }, [workspaceId])

  useEffect(() => { fetchData() }, [fetchData])

  const handleConnect = useCallback(async () => {
    if (!fromCrew || !toCrew || fromCrew === toCrew) {
      toast.error("Select two different crews")
      return
    }
    setConnecting(true)
    try {
      const res = await fetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ from_crew_id: fromCrew, to_crew_id: toCrew, direction }),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.detail ?? "Failed to connect crews")
        return
      }
      toast.success("Crews connected")
      setFromCrew("")
      setToCrew("")
      fetchData()
    } catch {
      toast.error("Failed to connect crews")
    } finally {
      setConnecting(false)
    }
  }, [fromCrew, toCrew, direction, workspaceId, fetchData])

  const handleDisconnect = useCallback(async (id: string) => {
    setDisconnecting(id)
    try {
      const res = await fetch(`/api/v1/crew-connections/${id}?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })
      if (!res.ok) {
        toast.error("Failed to disconnect crews")
        return
      }
      toast.success("Crews disconnected")
      fetchData()
    } catch {
      toast.error("Failed to disconnect crews")
    } finally {
      setDisconnecting(null)
    }
  }, [workspaceId, fetchData])

  if (loading) {
    return <Card><CardContent className="py-12 text-center text-muted-foreground">Loading...</CardContent></Card>
  }

  return (
    <div className="space-y-4">
      {/* Connect form */}
      <Card>
        <CardContent className="py-4">
          <h3 className="text-sm font-semibold mb-3">Connect Crews</h3>
          <div className="flex items-end gap-3">
            <div className="flex-1 space-y-1">
              <label className="text-xs text-muted-foreground">From</label>
              <Select value={fromCrew} onValueChange={setFromCrew}>
                <SelectTrigger className="h-9"><SelectValue placeholder="Select crew" /></SelectTrigger>
                <SelectContent>
                  {crews.map((c) => <SelectItem key={c.id} value={c.id}>{c.name}</SelectItem>)}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1">
              <label className="text-xs text-muted-foreground">Direction</label>
              <Select value={direction} onValueChange={setDirection}>
                <SelectTrigger className="h-9 w-[160px]"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="bidirectional">Bidirectional</SelectItem>
                  <SelectItem value="unidirectional">One-way</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex-1 space-y-1">
              <label className="text-xs text-muted-foreground">To</label>
              <Select value={toCrew} onValueChange={setToCrew}>
                <SelectTrigger className="h-9"><SelectValue placeholder="Select crew" /></SelectTrigger>
                <SelectContent>
                  {crews.filter((c) => c.id !== fromCrew).map((c) => (
                    <SelectItem key={c.id} value={c.id}>{c.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Button
              onClick={handleConnect}
              disabled={connecting || !fromCrew || !toCrew}
              className="gap-1.5 shrink-0"
              size="sm"
            >
              {connecting ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Link2 className="h-3.5 w-3.5" />}
              Connect
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* Existing connections */}
      {connections.length === 0 ? (
        <Card>
          <CardContent className="py-12">
            <EmptyState
              icon={Users}
              title="No crew connections"
              description="Connect crews to enable cross-crew task dispatch in missions"
            />
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-2">
          {connections.map((conn) => (
            <Card key={conn.id}>
              <CardContent className="py-3">
                <div className="flex items-center gap-4">
                  <CrewBadge name={conn.from_crew_name} slug={conn.from_crew_slug} />
                  <div className="flex flex-col items-center gap-1 shrink-0">
                    {conn.direction === "bidirectional" ? (
                      <ArrowLeftRight className="h-5 w-5 text-muted-foreground/70" />
                    ) : (
                      <ArrowRight className="h-5 w-5 text-muted-foreground/70" />
                    )}
                    <Badge variant="outline" className="text-[10px] px-1.5">
                      {conn.direction}
                    </Badge>
                  </div>
                  <CrewBadge name={conn.to_crew_name} slug={conn.to_crew_slug} />
                  <div className="flex-1" />
                  <Badge
                    variant="outline"
                    className={cn("text-[10px]",
                      conn.status === "active" ? "border-green-500/30 text-green-400" : "border-gray-500/30 text-gray-400"
                    )}
                  >
                    {conn.status}
                  </Badge>
                  <Button
                    size="sm"
                    variant="outline"
                    className="gap-1.5 h-7 text-xs border-red-500/30 text-red-400 hover:bg-red-500/10"
                    onClick={() => handleDisconnect(conn.id)}
                    disabled={disconnecting === conn.id}
                  >
                    {disconnecting === conn.id ? <Loader2 className="h-3 w-3 animate-spin" /> : <Unlink2 className="h-3 w-3" />}
                    Disconnect
                  </Button>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </div>
  )
}
