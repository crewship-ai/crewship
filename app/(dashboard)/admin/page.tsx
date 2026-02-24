"use client"

import { useEffect, useState, useCallback, useRef } from "react"
import { useRouter } from "next/navigation"
import {
  LayoutDashboard, ScrollText, Building, Users, Server, Gauge,
  Globe, Archive, Brain, Lock, Key, ToggleRight, Activity, Shield,
  RefreshCw, CheckCircle2, AlertTriangle, Container, ExternalLink,
  Radio,
} from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { useWorkspace } from "@/hooks/use-workspace"
import { cn } from "@/lib/utils"

const SECRET_PATTERNS = [
  /sk-ant-[a-zA-Z0-9_-]{20,}/g,
  /sk-[a-zA-Z0-9]{20,}/g,
  /AIza[a-zA-Z0-9_-]{35}/g,
  /AKIA[A-Z0-9]{16}/g,
  /Bearer\s+[a-zA-Z0-9._-]{20,}/g,
  /-----BEGIN[A-Z ]*PRIVATE KEY-----[\s\S]*?-----END[A-Z ]*PRIVATE KEY-----/g,
  /ghp_[a-zA-Z0-9]{36}/g,
  /gho_[a-zA-Z0-9]{36}/g,
]

function redactSecrets(text: string): string {
  let result = text
  for (const pattern of SECRET_PATTERNS) {
    result = result.replace(pattern, (m) => m.slice(0, 8) + "***REDACTED***")
  }
  return result
}

type TabKey =
  | "overview" | "logs" | "workspaces" | "users"
  | "providers" | "resources" | "networking" | "backups"
  | "gateway" | "security" | "auth" | "flags" | "ratelimits"

interface TabDef {
  type?: "section"
  key?: TabKey
  label: string
  icon?: React.ElementType
}

const sections: TabDef[] = [
  { type: "section", label: "PLATFORM" },
  { key: "overview", label: "Overview", icon: LayoutDashboard },
  { key: "logs", label: "System Logs", icon: ScrollText },
  { type: "section", label: "ORGANIZATIONS" },
  { key: "workspaces", label: "Workspaces", icon: Building },
  { key: "users", label: "All Users", icon: Users },
  { type: "section", label: "INFRASTRUCTURE" },
  { key: "providers", label: "Providers", icon: Server },
  { key: "resources", label: "Resources", icon: Gauge },
  { key: "networking", label: "Networking", icon: Globe },
  { key: "backups", label: "Backups", icon: Archive },
  { type: "section", label: "AI & ORCHESTRATION" },
  { key: "gateway", label: "LLM Gateway", icon: Brain },
  { type: "section", label: "SECURITY & ACCESS" },
  { key: "security", label: "Security", icon: Lock },
  { key: "auth", label: "Auth & SSO", icon: Key },
  { key: "flags", label: "Feature Flags", icon: ToggleRight },
  { key: "ratelimits", label: "Rate Limits", icon: Activity },
]

const realTabs: TabKey[] = ["overview", "workspaces", "users", "providers", "security"]

interface Stats {
  workspaces: number
  users: number
  agents: number
  running: number
}

interface AdminOrg {
  id: string
  name: string
  slug: string
  created_at: string
  _count_members: number
  _count_agents: number
  _count_crews: number
}

interface AdminUser {
  id: string
  email: string
  full_name: string | null
  created_at: string
  workspace: { id: string; name: string } | null
  role: string | null
}

interface KeeperStatus {
  enabled: boolean
  ollama_url: string
  model: string
  ollama_online: boolean
  gatekeeper_configured: boolean
  total_requests: number
  allow_count: number
  deny_count: number
  escalate_count: number
}

interface KeeperLogEntry {
  id: string
  agent_id: string
  agent_name: string
  crew_id: string
  credential_id: string
  credential_name: string
  intent: string
  request_type: string
  command: string | null
  decision: string | null
  reason: string | null
  risk_score: number | null
  exit_code: number | null
  ollama_prompt: string | null
  ollama_raw_response: string | null
  created_at: string
  decided_at: string | null
}

interface KeeperLiveEvent {
  request_id: string
  request_type: string
  agent_name: string
  credential_name: string
  intent: string
  command?: string
  decision: string
  reason: string
  risk_score: number
  exit_code?: number
  decided_at: string
}

function redactUrl(raw: string): string {
  try {
    const url = new URL(raw)
    if (url.username || url.password) {
      url.username = url.username ? "****" : ""
      url.password = ""
    }
    if (url.search) url.search = "?…"
    return url.toString()
  } catch {
    return raw
  }
}

export default function AdminPage() {
  const router = useRouter()
  const { workspaceId, role, loading: wsLoading } = useWorkspace()
  const [tab, setTab] = useState<TabKey>("overview")
  const [stats, setStats] = useState<Stats | null>(null)
  const [orgs, setOrgs] = useState<AdminOrg[]>([])
  const [users, setUsers] = useState<AdminUser[]>([])
  const [loading, setLoading] = useState(true)

  const [runtimeAvailable, setRuntimeAvailable] = useState<boolean | null>(null)
  const [runtimeInfo, setRuntimeInfo] = useState<{ runtime: string; version: string; socket: string } | null>(null)
  const [runtimeInstallLinks, setRuntimeInstallLinks] = useState<Record<string, string>>({})
  const [runtimeChecking, setRuntimeChecking] = useState(false)

  const [keeperStatus, setKeeperStatus] = useState<KeeperStatus | null>(null)
  const [keeperLog, setKeeperLog] = useState<KeeperLogEntry[]>([])
  const [keeperLoading, setKeeperLoading] = useState(false)

  const [keeperLiveEvents, setKeeperLiveEvents] = useState<KeeperLiveEvent[]>([])
  const keeperWsRef = useRef<WebSocket | null>(null)
  const [keeperWsStatus, setKeeperWsStatus] = useState<"disconnected" | "connecting" | "connected">("disconnected")
  const [selectedKeeperEntry, setSelectedKeeperEntry] = useState<KeeperLogEntry | null>(null)

  const checkRuntime = useCallback(async () => {
    setRuntimeChecking(true)
    try {
      const res = await fetch("/api/v1/system/runtime")
      if (!res.ok) {
        setRuntimeAvailable(false)
        return
      }
      const data = await res.json()
      setRuntimeAvailable(data.available)
      if (data.available) {
        setRuntimeInfo({ runtime: data.runtime, version: data.version, socket: data.socket })
      } else {
        setRuntimeInstallLinks(data.install_links ?? {})
      }
    } catch {
      setRuntimeAvailable(false)
    } finally {
      setRuntimeChecking(false)
    }
  }, [])

  useEffect(() => {
    if (wsLoading) return
    if (role !== "OWNER") {
      router.push("/")
      return
    }
  }, [wsLoading, role, router])

  useEffect(() => {
    if (!workspaceId || role !== "OWNER") return

    let cancelled = false

    async function fetchData() {
      setLoading(true)
      try {
        const [statsRes, orgsRes, usersRes] = await Promise.all([
          fetch(`/api/v1/admin/stats?workspace_id=${workspaceId}`),
          fetch(`/api/v1/admin/workspaces?workspace_id=${workspaceId}`),
          fetch(`/api/v1/admin/users?workspace_id=${workspaceId}`),
        ])

        if (statsRes.ok && !cancelled) setStats(await statsRes.json())
        if (orgsRes.ok && !cancelled) setOrgs(await orgsRes.json())
        if (usersRes.ok && !cancelled) setUsers(await usersRes.json())
      } catch {
        // silently fail
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchData()
    return () => { cancelled = true }
  }, [workspaceId, role])

  const fetchKeeperData = useCallback(async () => {
    setKeeperLoading(true)
    try {
      const statusRes = await fetch("/api/v1/system/keeper")
      if (statusRes.ok) setKeeperStatus(await statusRes.json())

      if (workspaceId) {
        const logRes = await fetch(`/api/v1/admin/keeper/requests?workspace_id=${workspaceId}&limit=50`)
        if (logRes.ok) setKeeperLog(await logRes.json())
      }
    } catch {
      // silently fail
    } finally {
      setKeeperLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    if (role === "OWNER") checkRuntime()
  }, [role, checkRuntime])

  useEffect(() => {
    if (role === "OWNER" && tab === "security") fetchKeeperData()
  }, [role, tab, fetchKeeperData])

  // Keeper live WebSocket: connect when Security tab is active
  useEffect(() => {
    if (role !== "OWNER" || tab !== "security" || !workspaceId) return

    let ws: WebSocket | null = null
    let cancelled = false

    const connect = async () => {
      try {
        setKeeperWsStatus("connecting")
        const tokenRes = await fetch("/api/v1/ws-token", { credentials: "include" })
        if (!tokenRes.ok || cancelled) return
        const { token } = await tokenRes.json()
        if (!token || cancelled) return

        const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
        const host = window.location.port === "3011"
          ? window.location.hostname + ":8081"
          : window.location.port === "3001"
            ? window.location.hostname + ":8080"
            : window.location.host
        const wsUrl = `${proto}//${host}/ws?token=${encodeURIComponent(token)}`
        ws = new WebSocket(wsUrl)
        keeperWsRef.current = ws

        ws.onopen = () => {
          if (cancelled) { ws?.close(); return }
          setKeeperWsStatus("connected")
          ws?.send(JSON.stringify({ type: "subscribe", channel: `keeper:${workspaceId}` }))
        }
        ws.onmessage = (event) => {
          try {
            const msg = JSON.parse(event.data)
            if (msg.type === "keeper_event" && msg.payload) {
              setKeeperLiveEvents((prev) => [msg.payload as KeeperLiveEvent, ...prev].slice(0, 100))
            }
          } catch { /* ignore non-JSON */ }
        }
        ws.onclose = () => {
          if (!cancelled) setKeeperWsStatus("disconnected")
        }
      } catch {
        setKeeperWsStatus("disconnected")
      }
    }

    connect()
    return () => {
      cancelled = true
      ws?.close()
      keeperWsRef.current = null
      setKeeperWsStatus("disconnected")
    }
  }, [role, tab, workspaceId])

  if (wsLoading || role !== "OWNER") {
    return (
      <div className="p-6">
        <Skeleton className="h-8 w-48 mb-4" />
        <Skeleton className="h-[300px] rounded-xl" />
      </div>
    )
  }

  function renderContent() {
    if (loading && realTabs.includes(tab)) {
      return <Skeleton className="h-[200px] rounded-xl" />
    }

    if (tab === "overview") {
      return (
        <div className="space-y-6">
          <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
            {[
              { label: "Workspaces", value: stats?.workspaces ?? 0 },
              { label: "Total Users", value: stats?.users ?? 0 },
              { label: "Total Agents", value: stats?.agents ?? 0 },
              { label: "Running", value: stats?.running ?? 0, color: "text-emerald-600" },
            ].map((s) => (
              <Card key={s.label}>
                <CardContent className="p-4">
                  <div className="text-[10px] text-muted-foreground uppercase font-medium">{s.label}</div>
                  <div className={cn("text-2xl font-bold mt-1", s.color)}>{s.value}</div>
                </CardContent>
              </Card>
            ))}
          </div>
          <Card>
            <CardContent className="p-5 space-y-4">
              <div className="text-xs font-medium">System Status</div>
              <div className="space-y-3">
                {[
                  { name: "Database", status: true, desc: "SQLite (connected)" },
                  { name: "Engine", status: true, desc: "Running" },
                  {
                    name: "Container Runtime",
                    status: runtimeAvailable === true,
                    desc: runtimeAvailable === null ? "Checking..."
                      : runtimeAvailable ? `${runtimeInfo?.runtime ?? "Unknown"} ${runtimeInfo?.version ?? ""}`
                      : "Not detected",
                  },
                ].map((s) => (
                  <div key={s.name} className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <span className={cn("w-2 h-2 rounded-full", s.status ? "bg-emerald-500" : "bg-amber-400")} />
                      <span className="text-xs">{s.name}</span>
                    </div>
                    <span className="text-xs text-muted-foreground">{s.desc}</span>
                  </div>
                ))}
              </div>
            </CardContent>
          </Card>
        </div>
      )
    }

    if (tab === "workspaces") {
      return (
        <div className="space-y-4">
          <div>
            <h3 className="text-sm font-medium">All Workspaces</h3>
            <p className="text-xs text-muted-foreground">{orgs.length} workspaces on this instance</p>
          </div>
          <Card>
            <CardContent className="p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Workspace</TableHead>
                    <TableHead className="text-center">Members</TableHead>
                    <TableHead className="text-center">Agents</TableHead>
                    <TableHead className="text-center">Teams</TableHead>
                    <TableHead>Created</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {orgs.map((o) => (
                    <TableRow key={o.id}>
                      <TableCell>
                        <div className="flex items-center gap-3">
                          <div className="w-8 h-8 rounded-lg bg-primary flex items-center justify-center text-primary-foreground text-xs font-bold">
                            {o.name[0]?.toUpperCase()}
                          </div>
                          <div>
                            <div className="text-sm font-medium">{o.name}</div>
                            <div className="text-[10px] text-muted-foreground font-mono">{o.slug}</div>
                          </div>
                        </div>
                      </TableCell>
                      <TableCell className="text-center text-xs">{o._count_members ?? 0}</TableCell>
                      <TableCell className="text-center text-xs">{o._count_agents ?? 0}</TableCell>
                      <TableCell className="text-center text-xs">{o._count_crews ?? 0}</TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {new Date(o.created_at).toLocaleDateString()}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </CardContent>
          </Card>
        </div>
      )
    }

    if (tab === "users") {
      return (
        <div className="space-y-4">
          <div>
            <h3 className="text-sm font-medium">All Users</h3>
            <p className="text-xs text-muted-foreground">{users.length} users across all workspaces</p>
          </div>
          <Card>
            <CardContent className="p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>User</TableHead>
                    <TableHead>Workspace</TableHead>
                    <TableHead>Role</TableHead>
                    <TableHead>Joined</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {users.map((u) => (
                    <TableRow key={u.id}>
                      <TableCell>
                        <div>
                          <div className="text-sm font-medium">{u.full_name ?? "—"}</div>
                          <div className="text-[10px] text-muted-foreground">{u.email}</div>
                        </div>
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {u.workspace?.name ?? "—"}
                      </TableCell>
                      <TableCell>
                        {u.role && <Badge variant="outline" className="text-[10px]">{u.role}</Badge>}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {new Date(u.created_at).toLocaleDateString()}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </CardContent>
          </Card>
        </div>
      )
    }

    if (tab === "providers") {
      return (
        <div className="space-y-5">
          <div className="pb-3 border-b">
            <h3 className="text-sm font-medium">Container Runtime</h3>
            <p className="text-xs text-muted-foreground">
              Manage the container runtime used to run AI agents.
            </p>
          </div>

          <Card>
            <CardContent className="p-5 space-y-4">
              {runtimeChecking && (
                <div className="flex items-center gap-3">
                  <RefreshCw className="h-4 w-4 animate-spin text-muted-foreground" />
                  <span className="text-sm">Detecting runtime...</span>
                </div>
              )}

              {!runtimeChecking && runtimeAvailable && runtimeInfo && (
                <div className="space-y-4">
                  <div className="flex items-center gap-3">
                    <CheckCircle2 className="h-5 w-5 text-emerald-500" />
                    <div>
                      <div className="text-sm font-medium">
                        {runtimeInfo.runtime.charAt(0).toUpperCase() + runtimeInfo.runtime.slice(1)} {runtimeInfo.version}
                      </div>
                      <p className="text-xs text-muted-foreground font-mono">{runtimeInfo.socket}</p>
                    </div>
                    <Badge variant="outline" className="ml-auto bg-emerald-50 text-emerald-700 border-emerald-200">
                      Connected
                    </Badge>
                  </div>
                </div>
              )}

              {!runtimeChecking && !runtimeAvailable && (
                <div className="space-y-4">
                  <div className="flex items-center gap-3">
                    <AlertTriangle className="h-5 w-5 text-amber-500" />
                    <div>
                      <div className="text-sm font-medium text-amber-700">No runtime detected</div>
                      <p className="text-xs text-muted-foreground">
                        Install a Docker-compatible runtime to enable agent containers.
                      </p>
                    </div>
                  </div>
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
                    {Object.entries(runtimeInstallLinks).map(([key, url]) => (
                      <a
                        key={key}
                        href={url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="flex items-center gap-2 rounded-lg border p-3 hover:bg-accent transition-colors text-sm"
                      >
                        <Container className="h-4 w-4 text-muted-foreground" />
                        <span className="font-medium">{key.charAt(0).toUpperCase() + key.slice(1)}</span>
                        <ExternalLink className="h-3 w-3 text-muted-foreground ml-auto" />
                      </a>
                    ))}
                  </div>
                </div>
              )}

              <Button variant="outline" size="sm" onClick={checkRuntime} disabled={runtimeChecking}>
                <RefreshCw className={cn("mr-2 h-3.5 w-3.5", runtimeChecking && "animate-spin")} />
                Re-detect Runtime
              </Button>
            </CardContent>
          </Card>
        </div>
      )
    }

    if (tab === "security") {
      return (
        <div className="space-y-6">
          <div className="pb-3 border-b">
            <h3 className="text-sm font-medium">Keeper — Credential Access Control</h3>
            <p className="text-xs text-muted-foreground">
              Keeper evaluates credential access requests using a local AI model (Ollama).
              Agents never see raw credentials — Keeper decides ALLOW / DENY / ESCALATE.
            </p>
          </div>

          {keeperLoading && <Skeleton className="h-[200px] rounded-xl" />}

          {!keeperLoading && keeperStatus && (
            <>
              {/* Status card */}
              <Card>
                <CardContent className="p-5 space-y-4">
                  <div className="text-xs font-medium">System Status</div>
                  <div className="space-y-3">
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <span className={cn("w-2 h-2 rounded-full", keeperStatus.enabled ? "bg-emerald-500" : "bg-amber-400")} />
                        <span className="text-xs">Keeper</span>
                      </div>
                      <span className="text-xs text-muted-foreground">
                        {keeperStatus.enabled ? "Enabled" : "Disabled"}
                      </span>
                    </div>
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <span className={cn("w-2 h-2 rounded-full", keeperStatus.gatekeeper_configured ? "bg-emerald-500" : "bg-red-400")} />
                        <span className="text-xs">Gatekeeper</span>
                      </div>
                      <span className="text-xs text-muted-foreground">
                        {keeperStatus.gatekeeper_configured ? "Configured" : "Not configured"}
                      </span>
                    </div>
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <span className={cn("w-2 h-2 rounded-full", keeperStatus.ollama_online ? "bg-emerald-500" : "bg-red-400")} />
                        <span className="text-xs">Ollama LLM</span>
                      </div>
                      <span className="text-xs text-muted-foreground">
                        {keeperStatus.ollama_online
                          ? `Online — ${keeperStatus.model}`
                          : keeperStatus.enabled
                            ? "Offline"
                            : "Not configured"}
                      </span>
                    </div>
                  </div>

                  {keeperStatus.enabled && (
                    <div className="pt-3 border-t text-xs text-muted-foreground space-y-1">
                      <div>Ollama URL: <span className="font-mono">{redactUrl(keeperStatus.ollama_url)}</span></div>
                      <div>Model: <span className="font-mono">{keeperStatus.model}</span></div>
                    </div>
                  )}

                  {!keeperStatus.enabled && (
                    <div className="pt-3 border-t">
                      <p className="text-xs text-muted-foreground">
                        To enable Keeper, set <code className="bg-muted px-1 py-0.5 rounded text-[10px]">KEEPER_OLLAMA_URL=http://localhost:11434</code> in
                        your <code className="bg-muted px-1 py-0.5 rounded text-[10px]">.env.local</code> and restart the server.
                      </p>
                    </div>
                  )}

                  <Button variant="outline" size="sm" onClick={fetchKeeperData} disabled={keeperLoading}>
                    <RefreshCw className={cn("mr-2 h-3.5 w-3.5", keeperLoading && "animate-spin")} />
                    Refresh
                  </Button>
                </CardContent>
              </Card>

              {/* Stats */}
              <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
                {[
                  { label: "Total Requests", value: keeperStatus.total_requests },
                  { label: "Allowed", value: keeperStatus.allow_count, color: "text-emerald-600" },
                  { label: "Denied", value: keeperStatus.deny_count, color: "text-red-600" },
                  { label: "Escalated", value: keeperStatus.escalate_count, color: "text-amber-600" },
                ].map((s) => (
                  <Card key={s.label}>
                    <CardContent className="p-4">
                      <div className="text-[10px] text-muted-foreground uppercase font-medium">{s.label}</div>
                      <div className={cn("text-2xl font-bold mt-1", s.color)}>{s.value}</div>
                    </CardContent>
                  </Card>
                ))}
              </div>

              {/* Live keeper events */}
              <div className="space-y-3">
                <div className="flex items-center gap-2">
                  <Radio className={cn("h-3.5 w-3.5", keeperWsStatus === "connected" ? "text-emerald-500" : "text-muted-foreground")} />
                  <h4 className="text-xs font-medium">Live Activity</h4>
                  <span className={cn("text-[10px]",
                    keeperWsStatus === "connected" ? "text-emerald-600" : "text-muted-foreground"
                  )}>
                    {keeperWsStatus === "connected" ? "Streaming" : keeperWsStatus === "connecting" ? "Connecting..." : "Disconnected"}
                  </span>
                </div>
                <Card>
                  <CardContent className="p-3 max-h-[240px] overflow-y-auto">
                    {keeperLiveEvents.length === 0 ? (
                      <div className="text-center text-xs text-muted-foreground py-6">
                        Waiting for keeper events... Send a credential request from an agent to see it here in real time.
                      </div>
                    ) : (
                      <div className="space-y-2">
                        {keeperLiveEvents.map((evt) => (
                          <div key={evt.request_id} className="flex items-start gap-2 py-1.5 border-b last:border-0">
                            <Badge
                              variant="outline"
                              className={cn("text-[10px] shrink-0 mt-0.5",
                                evt.decision === "ALLOW" && "bg-emerald-50 text-emerald-700 border-emerald-200",
                                evt.decision === "DENY" && "bg-red-50 text-red-700 border-red-200",
                                evt.decision === "ESCALATE" && "bg-amber-50 text-amber-700 border-amber-200",
                              )}
                            >
                              {evt.decision}
                            </Badge>
                            <div className="min-w-0 flex-1">
                              <div className="text-xs">
                                <span className="font-medium">{evt.agent_name}</span>
                                <span className="text-muted-foreground"> requested </span>
                                <span className="font-mono text-[10px]">{evt.credential_name}</span>
                                {evt.request_type === "execute" && (
                                  <Badge variant="outline" className="ml-1 text-[9px] py-0">exec</Badge>
                                )}
                              </div>
                              <div className="text-[10px] text-muted-foreground truncate">{evt.intent}</div>
                              {evt.reason && (
                                <div className="text-[10px] text-muted-foreground/70 truncate italic">{evt.reason}</div>
                              )}
                            </div>
                            <div className="text-[10px] text-muted-foreground shrink-0">
                              {evt.risk_score}/10
                            </div>
                          </div>
                        ))}
                      </div>
                    )}
                  </CardContent>
                </Card>
              </div>

              {/* Request log */}
              <div className="space-y-3">
                <h4 className="text-xs font-medium">Recent Requests</h4>
                <Card>
                  <CardContent className="p-0">
                    <Table>
                      <TableHeader>
                        <TableRow>
                          <TableHead>Agent</TableHead>
                          <TableHead>Credential</TableHead>
                          <TableHead>Type</TableHead>
                          <TableHead>Decision</TableHead>
                          <TableHead>Risk</TableHead>
                          <TableHead>Time</TableHead>
                        </TableRow>
                      </TableHeader>
                      <TableBody>
                        {keeperLog.length === 0 && (
                          <TableRow>
                            <TableCell colSpan={6} className="text-center text-xs text-muted-foreground py-8">
                              No keeper requests yet
                            </TableCell>
                          </TableRow>
                        )}
                        {keeperLog.map((entry) => (
                          <TableRow key={entry.id} className="cursor-pointer hover:bg-muted/50" onClick={() => setSelectedKeeperEntry(entry)}>
                            <TableCell className="text-xs font-medium">{entry.agent_name}</TableCell>
                            <TableCell className="text-xs text-muted-foreground">{entry.credential_name}</TableCell>
                            <TableCell>
                              <Badge variant="outline" className="text-[10px]">
                                {entry.request_type === "execute" ? "Execute" : "Access"}
                              </Badge>
                            </TableCell>
                            <TableCell>
                              <Badge
                                variant="outline"
                                className={cn("text-[10px]",
                                  entry.decision === "ALLOW" && "bg-emerald-50 text-emerald-700 border-emerald-200",
                                  entry.decision === "DENY" && "bg-red-50 text-red-700 border-red-200",
                                  entry.decision === "ESCALATE" && "bg-amber-50 text-amber-700 border-amber-200",
                                  entry.decision === "PENDING" && "bg-blue-50 text-blue-700 border-blue-200",
                                )}
                              >
                                {entry.decision ?? "PENDING"}
                              </Badge>
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground">
                              {entry.risk_score != null ? `${entry.risk_score}/10` : "—"}
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground">
                              {new Date(entry.created_at).toLocaleString()}
                            </TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  </CardContent>
                </Card>
              </div>

              {/* Keeper request detail sheet */}
              <Sheet open={!!selectedKeeperEntry} onOpenChange={(open) => { if (!open) setSelectedKeeperEntry(null) }}>
                <SheetContent side="right" className="sm:max-w-2xl w-full overflow-y-auto">
                  <SheetHeader>
                    <SheetTitle className="flex items-center gap-2 text-sm">
                      <Shield className="h-4 w-4" />
                      Keeper Decision Detail
                    </SheetTitle>
                  </SheetHeader>
                  {selectedKeeperEntry && (
                    <div className="space-y-5 px-1">
                      {/* Summary */}
                      <div className="grid grid-cols-2 gap-3">
                        <div>
                          <div className="text-[10px] text-muted-foreground uppercase font-medium">Agent</div>
                          <div className="text-xs font-medium mt-0.5">{selectedKeeperEntry.agent_name}</div>
                        </div>
                        <div>
                          <div className="text-[10px] text-muted-foreground uppercase font-medium">Credential</div>
                          <div className="text-xs font-mono mt-0.5">{selectedKeeperEntry.credential_name}</div>
                        </div>
                        <div>
                          <div className="text-[10px] text-muted-foreground uppercase font-medium">Decision</div>
                          <Badge
                            variant="outline"
                            className={cn("text-[10px] mt-0.5",
                              selectedKeeperEntry.decision === "ALLOW" && "bg-emerald-50 text-emerald-700 border-emerald-200",
                              selectedKeeperEntry.decision === "DENY" && "bg-red-50 text-red-700 border-red-200",
                              selectedKeeperEntry.decision === "ESCALATE" && "bg-amber-50 text-amber-700 border-amber-200",
                            )}
                          >
                            {selectedKeeperEntry.decision ?? "PENDING"}
                          </Badge>
                        </div>
                        <div>
                          <div className="text-[10px] text-muted-foreground uppercase font-medium">Risk Score</div>
                          <div className="text-xs font-medium mt-0.5">{selectedKeeperEntry.risk_score != null ? `${selectedKeeperEntry.risk_score}/10` : "—"}</div>
                        </div>
                        <div>
                          <div className="text-[10px] text-muted-foreground uppercase font-medium">Type</div>
                          <div className="text-xs mt-0.5">{selectedKeeperEntry.request_type === "execute" ? "Execute" : "Access"}</div>
                        </div>
                        <div>
                          <div className="text-[10px] text-muted-foreground uppercase font-medium">Time</div>
                          <div className="text-xs text-muted-foreground mt-0.5">{new Date(selectedKeeperEntry.created_at).toLocaleString()}</div>
                        </div>
                      </div>

                      {/* Intent */}
                      <div>
                        <div className="text-[10px] text-muted-foreground uppercase font-medium mb-1">Intent</div>
                        <div className="text-xs bg-muted/50 rounded-md p-3">{selectedKeeperEntry.intent}</div>
                      </div>

                      {/* Reason */}
                      {selectedKeeperEntry.reason && (
                        <div>
                          <div className="text-[10px] text-muted-foreground uppercase font-medium mb-1">Reason</div>
                          <div className="text-xs bg-muted/50 rounded-md p-3">{selectedKeeperEntry.reason}</div>
                        </div>
                      )}

                      {/* Command (execute requests) */}
                      {selectedKeeperEntry.command && (
                        <div>
                          <div className="text-[10px] text-muted-foreground uppercase font-medium mb-1">Command</div>
                          <pre className="text-[11px] bg-zinc-900 text-zinc-100 rounded-md p-3 overflow-x-auto font-mono">{selectedKeeperEntry.command}</pre>
                        </div>
                      )}

                      {/* Ollama Prompt */}
                      {selectedKeeperEntry.ollama_prompt ? (
                        <div>
                          <div className="text-[10px] text-muted-foreground uppercase font-medium mb-1">Ollama Prompt (sent to LLM)</div>
                          <pre className="text-[11px] bg-zinc-900 text-zinc-100 rounded-md p-3 overflow-x-auto whitespace-pre-wrap font-mono max-h-[300px] overflow-y-auto">{redactSecrets(selectedKeeperEntry.ollama_prompt)}</pre>
                        </div>
                      ) : (
                        <div>
                          <div className="text-[10px] text-muted-foreground uppercase font-medium mb-1">Ollama Prompt</div>
                          <div className="text-xs text-muted-foreground italic bg-muted/50 rounded-md p-3">Not available (L1 auto-allow or pre-observability request)</div>
                        </div>
                      )}

                      {/* Ollama Raw Response */}
                      {selectedKeeperEntry.ollama_raw_response ? (
                        <div>
                          <div className="text-[10px] text-muted-foreground uppercase font-medium mb-1">Ollama Raw Response</div>
                          <pre className="text-[11px] bg-zinc-900 text-zinc-100 rounded-md p-3 overflow-x-auto whitespace-pre-wrap font-mono max-h-[300px] overflow-y-auto">{redactSecrets(selectedKeeperEntry.ollama_raw_response)}</pre>
                        </div>
                      ) : (
                        <div>
                          <div className="text-[10px] text-muted-foreground uppercase font-medium mb-1">Ollama Raw Response</div>
                          <div className="text-xs text-muted-foreground italic bg-muted/50 rounded-md p-3">Not available (L1 auto-allow or pre-observability request)</div>
                        </div>
                      )}

                      {/* Request ID */}
                      <div className="pt-3 border-t">
                        <div className="text-[10px] text-muted-foreground">Request ID: <span className="font-mono">{selectedKeeperEntry.id}</span></div>
                      </div>
                    </div>
                  )}
                </SheetContent>
              </Sheet>
            </>
          )}
        </div>
      )
    }

    // Placeholder for other tabs
    return (
      <Card>
        <CardContent className="p-6 text-center space-y-2">
          <Badge variant="outline">Coming Soon</Badge>
          <p className="text-sm text-muted-foreground">
            This section will be available in a future release.
          </p>
        </CardContent>
      </Card>
    )
  }

  return (
    <div className="flex h-full">
      {/* Admin left nav */}
      <div className="w-52 border-r bg-background flex flex-col flex-shrink-0 overflow-y-auto">
        <div className="p-3 border-b">
          <div className="flex items-center gap-2">
            <Shield className="w-4 h-4 text-amber-700" />
            <span className="text-xs font-semibold">Admin Console</span>
            <Badge variant="outline" className="text-[9px] ml-auto bg-amber-50 text-amber-700 border-amber-200">
              OWNER
            </Badge>
          </div>
        </div>
        <nav className="flex-1 p-2 space-y-0.5 overflow-y-auto">
          {sections.map((s, i) => {
            if (s.type === "section") {
              return (
                <div key={i} className="text-[9px] font-semibold text-muted-foreground uppercase tracking-wider px-3 pt-3 pb-1">
                  {s.label}
                </div>
              )
            }
            const Icon = s.icon!
            const isActive = s.key === tab
            return (
              <button
                key={s.key}
                className={cn(
                  "flex items-center gap-2.5 w-full px-3 py-2 rounded-md text-xs transition-colors",
                  isActive ? "bg-primary/10 text-primary font-medium" : "text-muted-foreground hover:bg-muted"
                )}
                onClick={() => setTab(s.key!)}
              >
                <Icon className="h-4 w-4 flex-shrink-0" />
                <span className="truncate">{s.label}</span>
              </button>
            )
          })}
        </nav>
      </div>

      {/* Admin content */}
      <div className="flex-1 overflow-y-auto">
        <div className="max-w-3xl mx-auto px-8 py-6">
          {renderContent()}
        </div>
      </div>
    </div>
  )
}
