"use client"

import { useEffect, useState, useCallback } from "react"
import {
  RefreshCw, Square, Play, Globe, ShieldCheck, ArrowRight,
  ArrowLeftRight, Plus, Trash2, ChevronDown, ChevronRight,
  Pencil, Check, X,
} from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

// --- Types ---

interface Crew {
  id: string
  name: string
  slug: string
  icon: string
  color: string
  container_memory_mb: number
  container_cpus: number
  container_ttl_hours: number | null
  network_mode: string
  allowed_domains: string[]
  _count?: { agents: number }
}

interface ContainerStatus {
  crew_id: string
  status: string
  uptime: string
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

interface AuditEntry {
  id: string
  workspace_id: string
  action: string
  from_crew_id: string | null
  to_crew_id: string | null
  agent_id: string | null
  details: string | null
  created_at: string
}

interface CrewInfrastructureProps {
  workspaceId: string
  canEdit: boolean
  section: "overview" | "connections" | "audit"
}

// --- Main Component ---

export function CrewInfrastructure({ workspaceId, canEdit, section }: CrewInfrastructureProps) {
  const [crews, setCrews] = useState<Crew[]>([])
  const [statuses, setStatuses] = useState<Record<string, ContainerStatus>>({})
  const [connections, setConnections] = useState<Connection[]>([])
  const [loading, setLoading] = useState(true)

  const fetchCrews = useCallback(async () => {
    try {
      const res = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
      if (res.ok) {
        const data = await res.json()
        setCrews(data.data || data || [])
      }
    } catch {
      toast.error("Failed to load crews")
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  const fetchStatuses = useCallback(async () => {
    for (const crew of crews) {
      try {
        const res = await fetch(`/api/v1/crews/${crew.id}/container/status?workspace_id=${workspaceId}`)
        if (res.ok) {
          const data = await res.json()
          setStatuses(prev => ({ ...prev, [crew.id]: data }))
        }
      } catch {
        // Container not running
      }
    }
  }, [crews, workspaceId])

  const fetchConnections = useCallback(async () => {
    try {
      const res = await fetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`)
      if (res.ok) {
        const data = await res.json()
        setConnections(data.data || data || [])
      }
    } catch {
      // Connections may not be available
    }
  }, [workspaceId])

  useEffect(() => { fetchCrews() }, [fetchCrews])
  useEffect(() => { if (crews.length > 0) fetchStatuses() }, [crews.length, fetchStatuses])
  useEffect(() => { fetchConnections() }, [fetchConnections])

  async function handleContainerAction(crewId: string, action: "start" | "stop" | "restart") {
    try {
      const endpoint = `/api/v1/crews/${crewId}/container/${action}?workspace_id=${workspaceId}`
      const res = await fetch(endpoint, { method: "POST" })
      if (res.ok) {
        toast.success(`Container ${action}ed`)
        fetchStatuses()
      } else {
        const body = await res.json().catch(() => null)
        toast.error(body?.error || `Failed to ${action} container`)
      }
    } catch {
      toast.error(`Failed to ${action} container`)
    }
  }

  async function patchCrew(crewId: string, config: Record<string, unknown>) {
    const res = await fetch(`/api/v1/crews/${crewId}?workspace_id=${workspaceId}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(config),
    })
    if (!res.ok) {
      const body = await res.json().catch(() => null)
      throw new Error(body?.error || "Failed to save")
    }
    fetchCrews()
  }

  if (loading) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-[300px] w-full" />
      </div>
    )
  }

  if (section === "overview") {
    return (
      <CrewOverviewTable
        crews={crews}
        statuses={statuses}
        connections={connections}
        canEdit={canEdit}
        onContainerAction={handleContainerAction}
        onPatchCrew={patchCrew}
      />
    )
  }

  if (section === "connections") {
    return (
      <ConnectionsTable
        crews={crews}
        connections={connections}
        canEdit={canEdit}
        workspaceId={workspaceId}
        onRefresh={fetchConnections}
      />
    )
  }

  if (section === "audit") {
    return <AuditLog workspaceId={workspaceId} crews={crews} />
  }

  return null
}

// --- Crew Overview Table ---

function CrewOverviewTable({
  crews, statuses, connections, canEdit, onContainerAction, onPatchCrew,
}: {
  crews: Crew[]
  statuses: Record<string, ContainerStatus>
  connections: Connection[]
  canEdit: boolean
  onContainerAction: (id: string, action: "start" | "stop" | "restart") => void
  onPatchCrew: (id: string, config: Record<string, unknown>) => Promise<void>
}) {
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editValues, setEditValues] = useState<{ mem: number; cpu: number; ttl: string }>({ mem: 0, cpu: 0, ttl: "" })

  function getConnectionCount(crewId: string) {
    return connections.filter(
      c => c.from_crew_id === crewId || c.to_crew_id === crewId
    ).length
  }

  function startEdit(crew: Crew) {
    setEditingId(crew.id)
    setEditValues({
      mem: crew.container_memory_mb,
      cpu: crew.container_cpus,
      ttl: crew.container_ttl_hours?.toString() ?? "",
    })
  }

  async function saveEdit(crewId: string) {
    try {
      const ttlVal = editValues.ttl === "" ? null : parseInt(editValues.ttl)
      await onPatchCrew(crewId, {
        container_memory_mb: editValues.mem,
        container_cpus: editValues.cpu,
        container_ttl_hours: ttlVal,
      })
      toast.success("Resources updated")
      setEditingId(null)
    } catch (err: any) {
      toast.error(err.message || "Failed to save")
    }
  }

  async function toggleNetwork(crew: Crew) {
    const newMode = crew.network_mode === "free" ? "restricted" : "free"
    try {
      await onPatchCrew(crew.id, { network_mode: newMode })
      toast.success(`Network set to ${newMode}`)
    } catch (err: any) {
      toast.error(err.message || "Failed to update network")
    }
  }

  if (crews.length === 0) {
    return (
      <Card>
        <CardContent className="p-8 text-center">
          <p className="text-sm text-muted-foreground">No crews in this workspace yet.</p>
        </CardContent>
      </Card>
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-sm font-medium">Crew Fleet</h3>
          <p className="text-xs text-muted-foreground mt-0.5">
            {crews.length} crew{crews.length !== 1 ? "s" : ""} &middot; Click a row to expand details
          </p>
        </div>
      </div>

      <div className="border rounded-lg overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="bg-muted/50">
              <TableHead className="w-8"></TableHead>
              <TableHead className="text-xs">Crew</TableHead>
              <TableHead className="text-xs w-20 text-center">Status</TableHead>
              <TableHead className="text-xs w-20 text-right">Memory</TableHead>
              <TableHead className="text-xs w-16 text-right">CPU</TableHead>
              <TableHead className="text-xs w-20 text-center">Network</TableHead>
              <TableHead className="text-xs w-24 text-center">Connections</TableHead>
              <TableHead className="text-xs w-16 text-right">TTL</TableHead>
              <TableHead className="text-xs w-28 text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {crews.map((crew) => {
              const status = statuses[crew.id]
              const isRunning = status?.status === "running"
              const isExpanded = expandedId === crew.id
              const isEditing = editingId === crew.id
              const connCount = getConnectionCount(crew.id)

              return (
                <>
                  <TableRow
                    key={crew.id}
                    className={cn(
                      "cursor-pointer transition-colors hover:bg-muted/30",
                      isExpanded && "bg-muted/20"
                    )}
                    onClick={() => setExpandedId(isExpanded ? null : crew.id)}
                  >
                    <TableCell className="px-2">
                      {isExpanded
                        ? <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
                        : <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
                      }
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-medium">{crew.name}</span>
                        <span className="text-[10px] text-muted-foreground font-mono">{crew.slug}</span>
                      </div>
                    </TableCell>
                    <TableCell className="text-center">
                      <Badge
                        variant={isRunning ? "default" : "secondary"}
                        className={cn(
                          "text-[10px] px-1.5",
                          isRunning && "bg-emerald-500/15 text-emerald-400 border-emerald-500/30",
                          !isRunning && "bg-zinc-500/15 text-zinc-400"
                        )}
                      >
                        {status?.status || "stopped"}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-right text-xs font-mono">
                      {isEditing ? (
                        <Input
                          type="number"
                          value={editValues.mem}
                          onChange={e => setEditValues(v => ({ ...v, mem: parseInt(e.target.value) || 0 }))}
                          className="h-6 w-16 text-xs text-right ml-auto"
                          onClick={e => e.stopPropagation()}
                        />
                      ) : (
                        <span>{crew.container_memory_mb} MB</span>
                      )}
                    </TableCell>
                    <TableCell className="text-right text-xs font-mono">
                      {isEditing ? (
                        <Input
                          type="number"
                          value={editValues.cpu}
                          onChange={e => setEditValues(v => ({ ...v, cpu: parseFloat(e.target.value) || 0 }))}
                          className="h-6 w-14 text-xs text-right ml-auto"
                          step={0.5}
                          onClick={e => e.stopPropagation()}
                        />
                      ) : (
                        <span>{crew.container_cpus}</span>
                      )}
                    </TableCell>
                    <TableCell className="text-center">
                      <button
                        className={cn(
                          "inline-flex items-center gap-1 text-[10px] px-1.5 py-0.5 rounded-full border transition-colors",
                          crew.network_mode === "free"
                            ? "bg-emerald-500/10 text-emerald-400 border-emerald-500/30 hover:bg-emerald-500/20"
                            : "bg-amber-500/10 text-amber-400 border-amber-500/30 hover:bg-amber-500/20",
                        )}
                        onClick={(e) => { e.stopPropagation(); if (canEdit) toggleNetwork(crew) }}
                        title={canEdit ? "Click to toggle" : crew.network_mode}
                      >
                        {crew.network_mode === "free"
                          ? <><Globe className="h-2.5 w-2.5" /> Free</>
                          : <><ShieldCheck className="h-2.5 w-2.5" /> Restricted</>
                        }
                      </button>
                    </TableCell>
                    <TableCell className="text-center text-xs">
                      {connCount > 0 ? (
                        <Badge variant="outline" className="text-[10px]">{connCount}</Badge>
                      ) : (
                        <span className="text-muted-foreground">-</span>
                      )}
                    </TableCell>
                    <TableCell className="text-right text-xs font-mono">
                      {isEditing ? (
                        <Input
                          type="number"
                          value={editValues.ttl}
                          onChange={e => setEditValues(v => ({ ...v, ttl: e.target.value }))}
                          placeholder="--"
                          className="h-6 w-14 text-xs text-right ml-auto"
                          onClick={e => e.stopPropagation()}
                        />
                      ) : (
                        <span className="text-muted-foreground">
                          {crew.container_ttl_hours ? `${crew.container_ttl_hours}h` : "--"}
                        </span>
                      )}
                    </TableCell>
                    <TableCell className="text-right" onClick={e => e.stopPropagation()}>
                      <div className="flex items-center justify-end gap-0.5">
                        {isEditing ? (
                          <>
                            <Button variant="ghost" size="icon" className="h-6 w-6" onClick={() => saveEdit(crew.id)}>
                              <Check className="h-3 w-3 text-emerald-400" />
                            </Button>
                            <Button variant="ghost" size="icon" className="h-6 w-6" onClick={() => setEditingId(null)}>
                              <X className="h-3 w-3" />
                            </Button>
                          </>
                        ) : (
                          <>
                            {canEdit && (
                              <Button variant="ghost" size="icon" className="h-6 w-6" onClick={() => startEdit(crew)} title="Edit resources">
                                <Pencil className="h-3 w-3" />
                              </Button>
                            )}
                            {canEdit && isRunning && (
                              <Button variant="ghost" size="icon" className="h-6 w-6" onClick={() => onContainerAction(crew.id, "restart")} title="Restart">
                                <RefreshCw className="h-3 w-3" />
                              </Button>
                            )}
                            {canEdit && isRunning && (
                              <Button variant="ghost" size="icon" className="h-6 w-6 text-destructive hover:text-destructive" onClick={() => onContainerAction(crew.id, "stop")} title="Stop">
                                <Square className="h-3 w-3" />
                              </Button>
                            )}
                            {canEdit && !isRunning && (
                              <Button variant="ghost" size="icon" className="h-6 w-6" onClick={() => onContainerAction(crew.id, "start")} title="Start">
                                <Play className="h-3 w-3" />
                              </Button>
                            )}
                          </>
                        )}
                      </div>
                    </TableCell>
                  </TableRow>
                  {isExpanded && (
                    <TableRow key={`${crew.id}-detail`} className="bg-muted/10 hover:bg-muted/10">
                      <TableCell colSpan={9} className="p-4">
                        <ExpandedCrewDetail
                          crew={crew}
                          status={status}
                          connections={connections}
                          canEdit={canEdit}
                          onPatchCrew={onPatchCrew}
                        />
                      </TableCell>
                    </TableRow>
                  )}
                </>
              )
            })}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}

// --- Expanded Detail ---

function ExpandedCrewDetail({ crew, status, connections, canEdit, onPatchCrew }: {
  crew: Crew
  status?: ContainerStatus
  connections: Connection[]
  canEdit: boolean
  onPatchCrew: (id: string, config: Record<string, unknown>) => Promise<void>
}) {
  const crewConns = connections.filter(
    c => c.from_crew_id === crew.id || c.to_crew_id === crew.id
  )

  return (
    <div className="grid grid-cols-1 md:grid-cols-3 gap-4 text-xs">
      {/* Status */}
      <div className="space-y-2">
        <h4 className="font-medium text-muted-foreground uppercase tracking-wider text-[10px]">Container</h4>
        <div className="space-y-1">
          <div className="flex justify-between">
            <span className="text-muted-foreground">Status</span>
            <span>{status?.status || "stopped"}</span>
          </div>
          {status?.uptime && (
            <div className="flex justify-between">
              <span className="text-muted-foreground">Uptime</span>
              <span>{status.uptime}</span>
            </div>
          )}
          <div className="flex justify-between">
            <span className="text-muted-foreground">Resources</span>
            <span className="font-mono">{crew.container_memory_mb}MB / {crew.container_cpus} CPU</span>
          </div>
          <div className="flex justify-between">
            <span className="text-muted-foreground">TTL</span>
            <span>{crew.container_ttl_hours ? `${crew.container_ttl_hours}h` : "Never stop"}</span>
          </div>
        </div>
      </div>

      {/* Network */}
      <div className="space-y-2">
        <h4 className="font-medium text-muted-foreground uppercase tracking-wider text-[10px]">Network</h4>
        <div className="space-y-1">
          <div className="flex justify-between">
            <span className="text-muted-foreground">Mode</span>
            <span>{crew.network_mode === "free" ? "Unrestricted" : "Restricted"}</span>
          </div>
          {crew.network_mode === "restricted" && crew.allowed_domains?.length > 0 && (
            <div>
              <span className="text-muted-foreground block mb-1">Allowed domains:</span>
              <div className="flex flex-wrap gap-1">
                {crew.allowed_domains.map(d => (
                  <Badge key={d} variant="outline" className="text-[9px] font-mono">{d}</Badge>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>

      {/* Connections */}
      <div className="space-y-2">
        <h4 className="font-medium text-muted-foreground uppercase tracking-wider text-[10px]">Connections</h4>
        {crewConns.length === 0 ? (
          <p className="text-muted-foreground">No connections</p>
        ) : (
          <div className="space-y-1">
            {crewConns.map(c => {
              const isSource = c.from_crew_id === crew.id
              const otherName = isSource ? c.to_crew_name : c.from_crew_name
              const otherSlug = isSource ? c.to_crew_slug : c.from_crew_slug
              return (
                <div key={c.id} className="flex items-center gap-1.5">
                  {c.direction === "bidirectional" ? (
                    <ArrowLeftRight className="h-3 w-3 text-blue-400 shrink-0" />
                  ) : isSource ? (
                    <ArrowRight className="h-3 w-3 text-emerald-400 shrink-0" />
                  ) : (
                    <ArrowRight className="h-3 w-3 text-amber-400 rotate-180 shrink-0" />
                  )}
                  <span>{otherName}</span>
                  <span className="text-muted-foreground font-mono">({otherSlug})</span>
                </div>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}

// --- Connections Table ---

function ConnectionsTable({ crews, connections, canEdit, workspaceId, onRefresh }: {
  crews: Crew[]
  connections: Connection[]
  canEdit: boolean
  workspaceId: string
  onRefresh: () => void
}) {
  const [adding, setAdding] = useState(false)
  const [fromId, setFromId] = useState("")
  const [toId, setToId] = useState("")
  const [direction, setDirection] = useState("bidirectional")

  async function handleCreate() {
    if (!fromId || !toId || fromId === toId) {
      toast.error("Select two different crews")
      return
    }
    try {
      const res = await fetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ from_crew_id: fromId, to_crew_id: toId, direction }),
      })
      if (res.ok) {
        toast.success("Connection created")
        setAdding(false)
        setFromId("")
        setToId("")
        onRefresh()
      } else {
        const body = await res.json().catch(() => null)
        toast.error(body?.error || "Failed to create connection")
      }
    } catch {
      toast.error("Network error")
    }
  }

  async function handleDelete(id: string) {
    try {
      const res = await fetch(`/api/v1/crew-connections/${id}?workspace_id=${workspaceId}`, { method: "DELETE" })
      if (res.ok) {
        toast.success("Connection removed")
        onRefresh()
      } else {
        toast.error("Failed to remove connection")
      }
    } catch {
      toast.error("Network error")
    }
  }

  const crewMap = new Map(crews.map(c => [c.id, c]))

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-sm font-medium">Crew Connections</h3>
          <p className="text-xs text-muted-foreground mt-0.5">
            {connections.length} connection{connections.length !== 1 ? "s" : ""} &middot; Controls which crews can communicate with each other
          </p>
        </div>
        {canEdit && (
          <Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => setAdding(!adding)}>
            <Plus className="h-3 w-3 mr-1" />
            Add Connection
          </Button>
        )}
      </div>

      {adding && (
        <Card>
          <CardContent className="p-3">
            <div className="flex items-end gap-2 flex-wrap">
              <div className="space-y-1">
                <label className="text-[10px] text-muted-foreground uppercase tracking-wider">Source Crew</label>
                <Select value={fromId} onValueChange={setFromId}>
                  <SelectTrigger className="h-8 w-44 text-xs"><SelectValue placeholder="Select crew" /></SelectTrigger>
                  <SelectContent>
                    {crews.map(c => <SelectItem key={c.id} value={c.id} className="text-xs">{c.name}</SelectItem>)}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1">
                <label className="text-[10px] text-muted-foreground uppercase tracking-wider">Direction</label>
                <Select value={direction} onValueChange={setDirection}>
                  <SelectTrigger className="h-8 w-40 text-xs"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="bidirectional" className="text-xs">
                      <span className="flex items-center gap-1"><ArrowLeftRight className="h-3 w-3" /> Bidirectional</span>
                    </SelectItem>
                    <SelectItem value="unidirectional" className="text-xs">
                      <span className="flex items-center gap-1"><ArrowRight className="h-3 w-3" /> One-way</span>
                    </SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1">
                <label className="text-[10px] text-muted-foreground uppercase tracking-wider">Target Crew</label>
                <Select value={toId} onValueChange={setToId}>
                  <SelectTrigger className="h-8 w-44 text-xs"><SelectValue placeholder="Select crew" /></SelectTrigger>
                  <SelectContent>
                    {crews.filter(c => c.id !== fromId).map(c => <SelectItem key={c.id} value={c.id} className="text-xs">{c.name}</SelectItem>)}
                  </SelectContent>
                </Select>
              </div>
              <Button size="sm" className="h-8 text-xs" onClick={handleCreate}>Create</Button>
              <Button size="sm" variant="ghost" className="h-8 text-xs" onClick={() => setAdding(false)}>Cancel</Button>
            </div>
          </CardContent>
        </Card>
      )}

      <div className="border rounded-lg overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="bg-muted/50">
              <TableHead className="text-xs">Source</TableHead>
              <TableHead className="text-xs w-32 text-center">Direction</TableHead>
              <TableHead className="text-xs">Target</TableHead>
              <TableHead className="text-xs w-20 text-center">Status</TableHead>
              <TableHead className="text-xs w-40">Created</TableHead>
              {canEdit && <TableHead className="text-xs w-12"></TableHead>}
            </TableRow>
          </TableHeader>
          <TableBody>
            {connections.length === 0 ? (
              <TableRow>
                <TableCell colSpan={canEdit ? 6 : 5} className="text-center text-sm text-muted-foreground py-8">
                  No connections yet. Crews are fully isolated.
                </TableCell>
              </TableRow>
            ) : (
              connections.map((conn) => (
                <TableRow key={conn.id}>
                  <TableCell>
                    <div className="flex items-center gap-1.5">
                      <span className="text-sm font-medium">{conn.from_crew_name}</span>
                      <span className="text-[10px] text-muted-foreground font-mono">{conn.from_crew_slug}</span>
                    </div>
                  </TableCell>
                  <TableCell className="text-center">
                    {conn.direction === "bidirectional" ? (
                      <Badge variant="outline" className="text-[10px] gap-1 bg-blue-500/10 text-blue-400 border-blue-500/30">
                        <ArrowLeftRight className="h-2.5 w-2.5" /> Both ways
                      </Badge>
                    ) : (
                      <Badge variant="outline" className="text-[10px] gap-1 bg-amber-500/10 text-amber-400 border-amber-500/30">
                        <ArrowRight className="h-2.5 w-2.5" /> One-way
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1.5">
                      <span className="text-sm font-medium">{conn.to_crew_name}</span>
                      <span className="text-[10px] text-muted-foreground font-mono">{conn.to_crew_slug}</span>
                    </div>
                  </TableCell>
                  <TableCell className="text-center">
                    <Badge
                      variant="outline"
                      className={cn(
                        "text-[10px]",
                        conn.status === "active"
                          ? "bg-emerald-500/10 text-emerald-400 border-emerald-500/30"
                          : "bg-zinc-500/10 text-zinc-400"
                      )}
                    >
                      {conn.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {new Date(conn.created_at).toLocaleDateString(undefined, {
                      month: "short", day: "numeric", year: "numeric",
                    })}
                  </TableCell>
                  {canEdit && (
                    <TableCell>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-6 w-6 text-destructive hover:text-destructive"
                        onClick={() => handleDelete(conn.id)}
                        title="Remove connection"
                      >
                        <Trash2 className="h-3 w-3" />
                      </Button>
                    </TableCell>
                  )}
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}

// --- Audit Log ---

function AuditLog({ workspaceId, crews }: { workspaceId: string; crews: Crew[] }) {
  const [entries, setEntries] = useState<AuditEntry[]>([])
  const [loading, setLoading] = useState(true)

  const crewMap = new Map(crews.map(c => [c.id, c]))

  useEffect(() => {
    async function load() {
      try {
        const res = await fetch(`/api/v1/crew-audit?workspace_id=${workspaceId}`)
        if (res.ok) {
          const data = await res.json()
          setEntries(data.data || [])
        }
      } catch {
        // Audit endpoint may not exist yet
      } finally {
        setLoading(false)
      }
    }
    load()
  }, [workspaceId])

  const actionLabels: Record<string, string> = {
    message_sent: "Message sent",
    file_read: "File read",
    file_written: "File written",
    file_list: "Directory listed",
    connection_created: "Connection created",
    connection_deleted: "Connection deleted",
  }

  const actionColors: Record<string, string> = {
    message_sent: "text-blue-400",
    file_read: "text-emerald-400",
    file_written: "text-amber-400",
    file_list: "text-zinc-400",
    connection_created: "text-emerald-400",
    connection_deleted: "text-red-400",
  }

  if (loading) {
    return <Skeleton className="h-[300px] w-full" />
  }

  return (
    <div className="space-y-4">
      <div>
        <h3 className="text-sm font-medium">Crew Audit Log</h3>
        <p className="text-xs text-muted-foreground mt-0.5">
          Cross-crew communication and connection changes
        </p>
      </div>

      <div className="border rounded-lg overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="bg-muted/50">
              <TableHead className="text-xs w-44">Time</TableHead>
              <TableHead className="text-xs w-32">Action</TableHead>
              <TableHead className="text-xs">From</TableHead>
              <TableHead className="text-xs">To</TableHead>
              <TableHead className="text-xs">Details</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {entries.length === 0 ? (
              <TableRow>
                <TableCell colSpan={5} className="text-center text-sm text-muted-foreground py-8">
                  No cross-crew activity yet.
                </TableCell>
              </TableRow>
            ) : (
              entries.map((entry) => {
                const fromCrew = entry.from_crew_id ? crewMap.get(entry.from_crew_id) : null
                const toCrew = entry.to_crew_id ? crewMap.get(entry.to_crew_id) : null
                let details: Record<string, string> = {}
                try { if (entry.details) details = JSON.parse(entry.details) } catch {}

                return (
                  <TableRow key={entry.id}>
                    <TableCell className="text-xs text-muted-foreground font-mono">
                      {new Date(entry.created_at).toLocaleString(undefined, {
                        month: "short", day: "numeric", hour: "2-digit", minute: "2-digit",
                      })}
                    </TableCell>
                    <TableCell>
                      <span className={cn("text-xs font-medium", actionColors[entry.action] || "")}>
                        {actionLabels[entry.action] || entry.action}
                      </span>
                    </TableCell>
                    <TableCell className="text-xs">
                      {fromCrew ? fromCrew.name : entry.from_crew_id || "-"}
                    </TableCell>
                    <TableCell className="text-xs">
                      {toCrew ? toCrew.name : entry.to_crew_id || "-"}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground font-mono truncate max-w-[200px]">
                      {details.path || details.message_id || details.content_length
                        ? Object.entries(details).map(([k, v]) => `${k}=${v}`).join(", ")
                        : "-"
                      }
                    </TableCell>
                  </TableRow>
                )
              })
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}
