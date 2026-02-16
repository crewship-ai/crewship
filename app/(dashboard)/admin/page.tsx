"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import {
  LayoutDashboard, ScrollText, Building, Users, Server, Gauge,
  Globe, Archive, Brain, Lock, Key, ToggleRight, Activity, Shield
} from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { useOrg } from "@/hooks/use-org"
import { cn } from "@/lib/utils"

type TabKey =
  | "overview" | "logs" | "organizations" | "users"
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
  { key: "organizations", label: "Organizations", icon: Building },
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

const realTabs: TabKey[] = ["overview", "organizations", "users"]

interface Stats {
  organizations: number
  users: number
  agents: number
  running: number
}

interface AdminOrg {
  id: string
  name: string
  slug: string
  created_at: string
  _count: { members: number; agents: number; teams: number }
}

interface AdminUser {
  id: string
  email: string
  full_name: string | null
  created_at: string
  organization: { id: string; name: string } | null
  role: string | null
}

export default function AdminPage() {
  const router = useRouter()
  const { orgId, role, loading: orgLoading } = useOrg()
  const [tab, setTab] = useState<TabKey>("overview")
  const [stats, setStats] = useState<Stats | null>(null)
  const [orgs, setOrgs] = useState<AdminOrg[]>([])
  const [users, setUsers] = useState<AdminUser[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    if (orgLoading) return
    if (role !== "OWNER") {
      router.push("/")
      return
    }
  }, [orgLoading, role, router])

  useEffect(() => {
    if (!orgId || role !== "OWNER") return

    let cancelled = false

    async function fetchData() {
      setLoading(true)
      try {
        const [statsRes, orgsRes, usersRes] = await Promise.all([
          fetch(`/api/v1/admin/stats?org_id=${orgId}`),
          fetch(`/api/v1/admin/organizations?org_id=${orgId}`),
          fetch(`/api/v1/admin/users?org_id=${orgId}`),
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
  }, [orgId, role])

  if (orgLoading || role !== "OWNER") {
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
              { label: "Organizations", value: stats?.organizations ?? 0 },
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
                  { name: "PostgreSQL", status: true, desc: "Connected" },
                  { name: "crewshipd", status: false, desc: "Not running (MVP)" },
                  { name: "Docker Engine", status: false, desc: "Not configured (MVP)" },
                ].map((s) => (
                  <div key={s.name} className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <span className={cn("w-2 h-2 rounded-full", s.status ? "bg-emerald-500" : "bg-muted-foreground/30")} />
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

    if (tab === "organizations") {
      return (
        <div className="space-y-4">
          <div>
            <h3 className="text-sm font-medium">All Organizations</h3>
            <p className="text-xs text-muted-foreground">{orgs.length} organizations on this instance</p>
          </div>
          <Card>
            <CardContent className="p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Organization</TableHead>
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
                      <TableCell className="text-center text-xs">{o._count.members}</TableCell>
                      <TableCell className="text-center text-xs">{o._count.agents}</TableCell>
                      <TableCell className="text-center text-xs">{o._count.teams}</TableCell>
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
            <p className="text-xs text-muted-foreground">{users.length} users across all organizations</p>
          </div>
          <Card>
            <CardContent className="p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>User</TableHead>
                    <TableHead>Organization</TableHead>
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
                        {u.organization?.name ?? "—"}
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

    // Placeholder for infrastructure/security tabs
    return (
      <Card>
        <CardContent className="p-6 text-center space-y-2">
          <Badge variant="outline">Requires crewshipd</Badge>
          <p className="text-sm text-muted-foreground">
            This section will be available after the Go backend (crewshipd) is implemented.
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
