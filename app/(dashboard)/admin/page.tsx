"use client"

import { useEffect, useState, useCallback } from "react"
import { useRouter } from "next/navigation"
import {
  LayoutDashboard, ScrollText, Building, Users, Server, Gauge,
  Globe, Archive, Brain, Lock, Key, ToggleRight, Activity, Shield,
  RefreshCw, CheckCircle2, AlertTriangle, Container, ExternalLink,
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
import { useWorkspace } from "@/hooks/use-workspace"
import { cn } from "@/lib/utils"

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

const realTabs: TabKey[] = ["overview", "workspaces", "users", "providers"]

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

  const checkRuntime = useCallback(async () => {
    setRuntimeChecking(true)
    try {
      const res = await fetch("/api/v1/system/runtime")
      if (!res.ok) return
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

  useEffect(() => {
    if (role === "OWNER") checkRuntime()
  }, [role, checkRuntime])

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
                  { name: "crewshipd", status: true, desc: "Running" },
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
                  <div className="grid grid-cols-2 gap-2">
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

    // Placeholder for infrastructure/security tabs
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
