"use client"

import { useEffect, useState, useCallback } from "react"
import {
  RefreshCw, Square, Play, Globe, Shield, ArrowRight,
  ArrowLeftRight, Plus, Trash2, Pencil, Check, X, Search,
  ChevronRight, Circle, Wifi, WifiOff,
} from "lucide-react"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { Textarea } from "@/components/ui/textarea"
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
    } catch { /* */ } finally { setLoading(false) }
  }, [workspaceId])

  const fetchStatuses = useCallback(async () => {
    for (const crew of crews) {
      try {
        const res = await fetch(`/api/v1/crews/${crew.id}/container/status?workspace_id=${workspaceId}`)
        if (res.ok) {
          const data = await res.json()
          setStatuses(prev => ({ ...prev, [crew.id]: data }))
        }
      } catch { /* */ }
    }
  }, [crews, workspaceId])

  const fetchConnections = useCallback(async () => {
    try {
      const res = await fetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`)
      if (res.ok) {
        const data = await res.json()
        setConnections(data.data || data || [])
      }
    } catch { /* */ }
  }, [workspaceId])

  useEffect(() => { fetchCrews() }, [fetchCrews])
  useEffect(() => { if (crews.length > 0) fetchStatuses() }, [crews.length, fetchStatuses])
  useEffect(() => { fetchConnections() }, [fetchConnections])

  async function containerAction(crewId: string, action: "start" | "stop" | "restart") {
    try {
      const res = await fetch(`/api/v1/crews/${crewId}/container/${action}?workspace_id=${workspaceId}`, { method: "POST" })
      if (res.ok) { toast.success(`Container ${action}ed`); fetchStatuses() }
      else { const b = await res.json().catch(() => null); toast.error(b?.error || `Failed to ${action}`) }
    } catch { toast.error(`Failed to ${action} container`) }
  }

  async function patchCrew(crewId: string, config: Record<string, unknown>) {
    const res = await fetch(`/api/v1/crews/${crewId}?workspace_id=${workspaceId}`, {
      method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify(config),
    })
    if (!res.ok) { const b = await res.json().catch(() => null); throw new Error(b?.error || "Failed to save") }
    await fetchCrews()
  }

  if (loading) return <div className="space-y-3"><Skeleton className="h-8 w-64" /><Skeleton className="h-[400px] w-full" /></div>

  if (section === "overview") return <CrewOverview crews={crews} statuses={statuses} connections={connections} canEdit={canEdit} onContainerAction={containerAction} onPatchCrew={patchCrew} />
  if (section === "connections") return <ConnectionsManager crews={crews} connections={connections} canEdit={canEdit} workspaceId={workspaceId} onRefresh={fetchConnections} />
  if (section === "audit") return <AuditLog workspaceId={workspaceId} crews={crews} />
  return null
}

// --- Settings Row (GitHub-style) ---

function SettingsRow({ label, description, children }: {
  label: string
  description?: string
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col sm:flex-row sm:items-start gap-1 sm:gap-8 py-4 border-b last:border-0">
      <div className="sm:w-48 shrink-0">
        <p className="text-sm font-medium">{label}</p>
        {description && <p className="text-xs text-muted-foreground mt-0.5">{description}</p>}
      </div>
      <div className="flex-1 min-w-0">{children}</div>
    </div>
  )
}

// --- Crew Overview ---

function CrewOverview({ crews, statuses, connections, canEdit, onContainerAction, onPatchCrew }: {
  crews: Crew[]; statuses: Record<string, ContainerStatus>; connections: Connection[]
  canEdit: boolean; onContainerAction: (id: string, a: "start" | "stop" | "restart") => void
  onPatchCrew: (id: string, c: Record<string, unknown>) => Promise<void>
}) {
  const [selectedId, setSelectedId] = useState<string | null>(crews[0]?.id || null)
  const [search, setSearch] = useState("")

  const filtered = crews.filter(c =>
    c.name.toLowerCase().includes(search.toLowerCase()) ||
    c.slug.toLowerCase().includes(search.toLowerCase())
  )
  const selected = crews.find(c => c.id === selectedId)

  function getConnections(crewId: string) {
    return connections.filter(c => c.from_crew_id === crewId || c.to_crew_id === crewId)
  }

  return (
    <div className="space-y-6">
      {/* Crew selector */}
      <div>
        <div className="flex items-center gap-3 mb-3">
          <h3 className="text-sm font-medium">Crews</h3>
          <span className="text-xs text-muted-foreground">{crews.length} total</span>
        </div>
        {crews.length > 4 && (
          <div className="relative mb-3">
            <Search className="absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
            <Input
              placeholder="Filter crews..."
              value={search}
              onChange={e => setSearch(e.target.value)}
              className="h-8 pl-8 text-xs"
            />
          </div>
        )}
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-2">
          {filtered.map(crew => {
            const status = statuses[crew.id]
            const isRunning = status?.status === "running"
            const isSelected = crew.id === selectedId
            const connCount = getConnections(crew.id).length

            return (
              <button
                key={crew.id}
                onClick={() => setSelectedId(crew.id)}
                className={cn(
                  "flex items-center gap-3 p-3 rounded-lg border text-left transition-all",
                  isSelected
                    ? "border-primary bg-primary/5 ring-1 ring-primary/20"
                    : "border-border hover:border-muted-foreground/30 hover:bg-muted/50"
                )}
              >
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-medium truncate">{crew.name}</span>
                    {isRunning ? (
                      <Circle className="h-2 w-2 fill-green-500 text-green-500 shrink-0" />
                    ) : (
                      <Circle className="h-2 w-2 fill-muted-foreground/30 text-muted-foreground/30 shrink-0" />
                    )}
                  </div>
                  <div className="flex items-center gap-2 mt-0.5">
                    <span className="text-[11px] text-muted-foreground font-mono">{crew.slug}</span>
                    {connCount > 0 && (
                      <span className="text-[10px] text-muted-foreground">&middot; {connCount} conn</span>
                    )}
                  </div>
                </div>
                <ChevronRight className={cn("h-4 w-4 shrink-0 transition-colors", isSelected ? "text-primary" : "text-muted-foreground/40")} />
              </button>
            )
          })}
        </div>
      </div>

      {/* Selected crew settings */}
      {selected && (
        <CrewSettingsPanel
          crew={selected}
          status={statuses[selected.id]}
          connections={getConnections(selected.id)}
          canEdit={canEdit}
          onContainerAction={onContainerAction}
          onPatchCrew={onPatchCrew}
        />
      )}
    </div>
  )
}

// --- Crew Settings Panel (GitHub-style form) ---

function CrewSettingsPanel({ crew, status, connections, canEdit, onContainerAction, onPatchCrew }: {
  crew: Crew; status?: ContainerStatus; connections: Connection[]; canEdit: boolean
  onContainerAction: (id: string, a: "start" | "stop" | "restart") => void
  onPatchCrew: (id: string, c: Record<string, unknown>) => Promise<void>
}) {
  const isRunning = status?.status === "running"
  const [mem, setMem] = useState(crew.container_memory_mb)
  const [cpu, setCpu] = useState(crew.container_cpus)
  const [ttl, setTtl] = useState(crew.container_ttl_hours?.toString() ?? "")
  const [netMode, setNetMode] = useState(crew.network_mode || "free")
  const [domains, setDomains] = useState((crew.allowed_domains || []).join("\n"))
  const [saving, setSaving] = useState(false)

  // Resync when crew changes
  useEffect(() => { setMem(crew.container_memory_mb) }, [crew.container_memory_mb])
  useEffect(() => { setCpu(crew.container_cpus) }, [crew.container_cpus])
  useEffect(() => { setTtl(crew.container_ttl_hours?.toString() ?? "") }, [crew.container_ttl_hours])
  useEffect(() => { setNetMode(crew.network_mode || "free") }, [crew.network_mode])
  useEffect(() => { setDomains((crew.allowed_domains || []).join("\n")) }, [crew.allowed_domains])

  const resourcesChanged = mem !== crew.container_memory_mb || cpu !== crew.container_cpus ||
    (ttl === "" ? crew.container_ttl_hours !== null : parseInt(ttl) !== crew.container_ttl_hours)
  const networkChanged = netMode !== (crew.network_mode || "free") ||
    domains.split("\n").map(d => d.trim()).filter(Boolean).join(",") !== (crew.allowed_domains || []).join(",")
  const hasChanges = resourcesChanged || networkChanged

  async function handleSave() {
    setSaving(true)
    try {
      const parsed = netMode === "restricted" ? domains.split(/[\n,]+/).map(d => d.trim()).filter(Boolean) : []
      await onPatchCrew(crew.id, {
        container_memory_mb: mem,
        container_cpus: cpu,
        container_ttl_hours: ttl === "" ? null : parseInt(ttl),
        network_mode: netMode,
        allowed_domains: parsed,
      })
      toast.success("Settings saved")
    } catch (err: any) {
      toast.error(err.message || "Failed to save")
    } finally { setSaving(false) }
  }

  return (
    <Card>
      <CardHeader className="pb-0">
        <div className="flex items-center justify-between">
          <div>
            <CardTitle className="text-base">{crew.name}</CardTitle>
            <CardDescription className="font-mono text-xs">{crew.slug}</CardDescription>
          </div>
          {canEdit && (
            <div className="flex items-center gap-2">
              {isRunning ? (
                <>
                  <Button variant="outline" size="sm" className="h-7 text-xs" onClick={() => onContainerAction(crew.id, "restart")}>
                    <RefreshCw className="h-3 w-3 mr-1.5" /> Restart
                  </Button>
                  <Button variant="outline" size="sm" className="h-7 text-xs text-destructive hover:text-destructive" onClick={() => onContainerAction(crew.id, "stop")}>
                    <Square className="h-3 w-3 mr-1.5" /> Stop
                  </Button>
                </>
              ) : (
                <Button variant="outline" size="sm" className="h-7 text-xs" onClick={() => onContainerAction(crew.id, "start")}>
                  <Play className="h-3 w-3 mr-1.5" /> Start Container
                </Button>
              )}
            </div>
          )}
        </div>
      </CardHeader>
      <CardContent className="pt-2">
        {/* Status bar */}
        <div className="flex items-center gap-4 py-3 mb-2 text-xs">
          <div className="flex items-center gap-1.5">
            <Circle className={cn("h-2 w-2", isRunning ? "fill-green-500 text-green-500" : "fill-muted-foreground/30 text-muted-foreground/30")} />
            <span className={isRunning ? "text-foreground" : "text-muted-foreground"}>{status?.status || "stopped"}</span>
          </div>
          {status?.uptime && <span className="text-muted-foreground">Uptime: {status.uptime}</span>}
        </div>

        {/* Resources */}
        <SettingsRow label="Memory" description="Container memory limit in MB">
          <Input type="number" value={mem} onChange={e => setMem(parseInt(e.target.value) || 0)}
            disabled={!canEdit} min={256} max={32768} step={256} className="h-8 w-32 text-sm font-mono" />
        </SettingsRow>

        <SettingsRow label="CPU" description="CPU core allocation">
          <Input type="number" value={cpu} onChange={e => setCpu(parseFloat(e.target.value) || 0)}
            disabled={!canEdit} min={0.5} max={32} step={0.5} className="h-8 w-32 text-sm font-mono" />
        </SettingsRow>

        <SettingsRow label="TTL" description="Auto-stop after idle hours. Empty = never stop.">
          <Input type="number" value={ttl} onChange={e => setTtl(e.target.value)}
            disabled={!canEdit} placeholder="Never" min={1} max={720} className="h-8 w-32 text-sm font-mono" />
        </SettingsRow>

        {/* Network */}
        <SettingsRow label="Network access" description="Control outbound internet access for agents">
          <div className="space-y-3">
            <div className="flex gap-2">
              <button
                onClick={() => canEdit && setNetMode("free")}
                className={cn(
                  "flex items-center gap-2 px-3 py-2 rounded-md border text-sm transition-all",
                  netMode === "free"
                    ? "border-primary bg-primary/5 text-foreground"
                    : "border-border text-muted-foreground hover:border-muted-foreground/50"
                )}
              >
                <Globe className="h-3.5 w-3.5" /> Unrestricted
              </button>
              <button
                onClick={() => canEdit && setNetMode("restricted")}
                className={cn(
                  "flex items-center gap-2 px-3 py-2 rounded-md border text-sm transition-all",
                  netMode === "restricted"
                    ? "border-primary bg-primary/5 text-foreground"
                    : "border-border text-muted-foreground hover:border-muted-foreground/50"
                )}
              >
                <Shield className="h-3.5 w-3.5" /> Restricted
              </button>
            </div>
            {netMode === "restricted" && (
              <div className="space-y-1.5">
                <Label className="text-xs text-muted-foreground">Allowed domains (one per line)</Label>
                <Textarea
                  value={domains}
                  onChange={e => setDomains(e.target.value)}
                  disabled={!canEdit}
                  placeholder={"api.anthropic.com\napi.openai.com"}
                  rows={3}
                  className="text-xs font-mono resize-none"
                />
              </div>
            )}
          </div>
        </SettingsRow>

        {/* Connections summary */}
        <SettingsRow label="Connections" description="Crews this container can communicate with">
          {connections.length === 0 ? (
            <p className="text-sm text-muted-foreground">No connections. This crew is fully isolated.</p>
          ) : (
            <div className="space-y-1.5">
              {connections.map(c => {
                const isSource = c.from_crew_id === crew.id
                const other = isSource ? { name: c.to_crew_name, slug: c.to_crew_slug } : { name: c.from_crew_name, slug: c.from_crew_slug }
                return (
                  <div key={c.id} className="flex items-center gap-2 text-sm">
                    {c.direction === "bidirectional" ? (
                      <ArrowLeftRight className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                    ) : (
                      <ArrowRight className={cn("h-3.5 w-3.5 text-muted-foreground shrink-0", !isSource && "rotate-180")} />
                    )}
                    <span>{other.name}</span>
                    <span className="text-xs text-muted-foreground font-mono">({other.slug})</span>
                    <Badge variant="outline" className="text-[10px] ml-auto">
                      {c.direction === "bidirectional" ? "both ways" : isSource ? "outbound" : "inbound"}
                    </Badge>
                  </div>
                )
              })}
              <p className="text-xs text-muted-foreground pt-1">Manage connections in the Connections tab.</p>
            </div>
          )}
        </SettingsRow>

        {/* Save */}
        {canEdit && hasChanges && (
          <div className="flex items-center justify-end gap-2 pt-4 border-t mt-2">
            <Button variant="ghost" size="sm" onClick={() => {
              setMem(crew.container_memory_mb); setCpu(crew.container_cpus)
              setTtl(crew.container_ttl_hours?.toString() ?? "")
              setNetMode(crew.network_mode || "free"); setDomains((crew.allowed_domains || []).join("\n"))
            }}>
              Reset
            </Button>
            <Button size="sm" onClick={handleSave} disabled={saving}>
              {saving ? "Saving..." : "Save changes"}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

// --- Connections Manager ---

function ConnectionsManager({ crews, connections, canEdit, workspaceId, onRefresh }: {
  crews: Crew[]; connections: Connection[]; canEdit: boolean; workspaceId: string; onRefresh: () => void
}) {
  const [adding, setAdding] = useState(false)
  const [fromId, setFromId] = useState("")
  const [toId, setToId] = useState("")
  const [direction, setDirection] = useState("bidirectional")

  async function handleCreate() {
    if (!fromId || !toId || fromId === toId) { toast.error("Select two different crews"); return }
    try {
      const res = await fetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ from_crew_id: fromId, to_crew_id: toId, direction }),
      })
      if (res.ok) { toast.success("Connection created"); setAdding(false); setFromId(""); setToId(""); onRefresh() }
      else { const b = await res.json().catch(() => null); toast.error(b?.error || "Failed to create") }
    } catch { toast.error("Network error") }
  }

  async function handleDelete(id: string) {
    try {
      const res = await fetch(`/api/v1/crew-connections/${id}?workspace_id=${workspaceId}`, { method: "DELETE" })
      if (res.ok) { toast.success("Connection removed"); onRefresh() }
      else toast.error("Failed to remove")
    } catch { toast.error("Network error") }
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-sm font-medium">Crew Connections</h3>
          <p className="text-xs text-muted-foreground mt-0.5">
            Define which crews can communicate with each other via messaging and file sharing.
          </p>
        </div>
        {canEdit && !adding && (
          <Button size="sm" onClick={() => setAdding(true)}>
            <Plus className="h-3.5 w-3.5 mr-1.5" /> New Connection
          </Button>
        )}
      </div>

      {/* Add connection form */}
      {adding && (
        <Card>
          <CardContent className="p-4">
            <p className="text-xs text-muted-foreground mb-3">Create a new connection between two crews.</p>
            <div className="grid grid-cols-1 sm:grid-cols-[1fr_auto_1fr] gap-3 items-end">
              <div className="space-y-1.5">
                <Label className="text-xs">Source crew</Label>
                <Select value={fromId} onValueChange={setFromId}>
                  <SelectTrigger className="h-9 text-sm"><SelectValue placeholder="Select crew..." /></SelectTrigger>
                  <SelectContent>
                    {crews.map(c => <SelectItem key={c.id} value={c.id}>{c.name} <span className="text-muted-foreground ml-1">({c.slug})</span></SelectItem>)}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5 sm:text-center">
                <Label className="text-xs">Direction</Label>
                <Select value={direction} onValueChange={setDirection}>
                  <SelectTrigger className="h-9 text-sm w-full sm:w-36"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="bidirectional">
                      <span className="flex items-center gap-1.5"><ArrowLeftRight className="h-3 w-3" /> Both ways</span>
                    </SelectItem>
                    <SelectItem value="unidirectional">
                      <span className="flex items-center gap-1.5"><ArrowRight className="h-3 w-3" /> One-way &rarr;</span>
                    </SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <Label className="text-xs">Target crew</Label>
                <Select value={toId} onValueChange={setToId}>
                  <SelectTrigger className="h-9 text-sm"><SelectValue placeholder="Select crew..." /></SelectTrigger>
                  <SelectContent>
                    {crews.filter(c => c.id !== fromId).map(c => <SelectItem key={c.id} value={c.id}>{c.name} <span className="text-muted-foreground ml-1">({c.slug})</span></SelectItem>)}
                  </SelectContent>
                </Select>
              </div>
            </div>
            <div className="flex gap-2 mt-4">
              <Button size="sm" onClick={handleCreate} disabled={!fromId || !toId}>Create Connection</Button>
              <Button size="sm" variant="ghost" onClick={() => { setAdding(false); setFromId(""); setToId("") }}>Cancel</Button>
            </div>
          </CardContent>
        </Card>
      )}

      {/* Connection list */}
      {connections.length === 0 && !adding ? (
        <Card>
          <CardContent className="py-12 text-center">
            <WifiOff className="h-8 w-8 mx-auto text-muted-foreground/40 mb-3" />
            <p className="text-sm font-medium">No connections</p>
            <p className="text-xs text-muted-foreground mt-1">All crews are fully isolated from each other.</p>
            {canEdit && (
              <Button size="sm" variant="outline" className="mt-4" onClick={() => setAdding(true)}>
                <Plus className="h-3.5 w-3.5 mr-1.5" /> Create first connection
              </Button>
            )}
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-2">
          {connections.map(conn => (
            <div
              key={conn.id}
              className="flex items-center gap-3 p-3 rounded-lg border bg-card hover:bg-muted/30 transition-colors"
            >
              {/* Source */}
              <div className="flex-1 min-w-0">
                <p className="text-sm font-medium truncate">{conn.from_crew_name}</p>
                <p className="text-[11px] text-muted-foreground font-mono">{conn.from_crew_slug}</p>
              </div>

              {/* Direction */}
              <div className="shrink-0 px-2">
                {conn.direction === "bidirectional" ? (
                  <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <ArrowLeftRight className="h-4 w-4" />
                    <span className="hidden sm:inline">both ways</span>
                  </div>
                ) : (
                  <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <ArrowRight className="h-4 w-4" />
                    <span className="hidden sm:inline">one-way</span>
                  </div>
                )}
              </div>

              {/* Target */}
              <div className="flex-1 min-w-0 text-right sm:text-left">
                <p className="text-sm font-medium truncate">{conn.to_crew_name}</p>
                <p className="text-[11px] text-muted-foreground font-mono">{conn.to_crew_slug}</p>
              </div>

              {/* Status + Actions */}
              <div className="shrink-0 flex items-center gap-2">
                <Badge variant={conn.status === "active" ? "default" : "secondary"} className="text-[10px]">
                  {conn.status}
                </Badge>
                {canEdit && (
                  <Button
                    variant="ghost" size="icon" className="h-7 w-7 text-muted-foreground hover:text-destructive"
                    onClick={() => handleDelete(conn.id)} title="Remove connection"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// --- Audit Log ---

function AuditLog({ workspaceId, crews }: { workspaceId: string; crews: Crew[] }) {
  const [entries, setEntries] = useState<AuditEntry[]>([])
  const [loading, setLoading] = useState(true)
  const crewMap = new Map(crews.map(c => [c.id, c]))

  useEffect(() => {
    (async () => {
      try {
        const res = await fetch(`/api/v1/crew-audit?workspace_id=${workspaceId}`)
        if (res.ok) { const d = await res.json(); setEntries(d.data || []) }
      } catch { /* */ } finally { setLoading(false) }
    })()
  }, [workspaceId])

  const labels: Record<string, string> = {
    message_sent: "Message sent", file_read: "File read", file_written: "File written",
    file_list: "Directory listed", connection_created: "Connection created", connection_deleted: "Connection deleted",
  }

  if (loading) return <Skeleton className="h-[300px] w-full" />

  return (
    <div className="space-y-4">
      <div>
        <h3 className="text-sm font-medium">Crew Audit Log</h3>
        <p className="text-xs text-muted-foreground mt-0.5">Activity log for cross-crew messaging, file sharing, and connection changes.</p>
      </div>

      {entries.length === 0 ? (
        <Card>
          <CardContent className="py-12 text-center">
            <p className="text-sm text-muted-foreground">No cross-crew activity yet.</p>
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-1">
          {entries.map(entry => {
            const from = entry.from_crew_id ? crewMap.get(entry.from_crew_id) : null
            const to = entry.to_crew_id ? crewMap.get(entry.to_crew_id) : null
            let details: Record<string, string> = {}
            try { if (entry.details) details = JSON.parse(entry.details) } catch { /* */ }

            return (
              <div key={entry.id} className="flex items-start gap-3 py-2.5 px-3 rounded-md hover:bg-muted/30 transition-colors text-sm">
                <span className="text-xs text-muted-foreground font-mono w-32 shrink-0 pt-0.5">
                  {new Date(entry.created_at).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })}
                </span>
                <div className="flex-1 min-w-0">
                  <span className="font-medium">{labels[entry.action] || entry.action}</span>
                  {from && <span className="text-muted-foreground"> from <span className="text-foreground">{from.name}</span></span>}
                  {to && <span className="text-muted-foreground"> to <span className="text-foreground">{to.name}</span></span>}
                  {Object.keys(details).length > 0 && (
                    <p className="text-xs text-muted-foreground font-mono mt-0.5 truncate">
                      {Object.entries(details).map(([k, v]) => `${k}=${v}`).join("  ")}
                    </p>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
