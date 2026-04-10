"use client"

import { useEffect, useState, useCallback } from "react"
import { useRouter } from "next/navigation"
import {
  LayoutDashboard, ScrollText, Building, Users, Server, Gauge,
  Globe, Archive, Brain, Lock, Key, ToggleRight, Activity, Shield,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { PageHeader } from "@/components/layout/page-header"
import { cn } from "@/lib/utils"

import type { TabKey, Stats, AdminOrg, AdminUser, KeeperStatus, KeeperLogEntry } from "./types"
import { useAdminWebSocket } from "./hooks/use-admin-websocket"
import { OverviewTab } from "./tabs/overview-tab"
import { RuntimeTab } from "./tabs/runtime-tab"
import { KeeperTab } from "./tabs/keeper-tab"
import { WorkspacesTab } from "./tabs/workspaces-tab"
import { UsersTab } from "./tabs/users-tab"

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
  const [allRuntimes, setAllRuntimes] = useState<{ runtime: string; version: string; socket: string }[]>([])
  const [runtimeInstallLinks, setRuntimeInstallLinks] = useState<Record<string, string>>({})
  const [runtimeChecking, setRuntimeChecking] = useState(false)

  const [keeperStatus, setKeeperStatus] = useState<KeeperStatus | null>(null)
  const [keeperLog, setKeeperLog] = useState<KeeperLogEntry[]>([])
  const [keeperLoading, setKeeperLoading] = useState(false)
  const [selectedKeeperEntry, setSelectedKeeperEntry] = useState<KeeperLogEntry | null>(null)

  const { keeperLiveEvents, keeperWsStatus } = useAdminWebSocket({
    enabled: role === "OWNER" && tab === "security",
    workspaceId,
  })

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
        setAllRuntimes(data.runtimes ?? [])
      } else {
        setRuntimeInstallLinks(data.install_links ?? {})
        setAllRuntimes([])
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
        <OverviewTab
          stats={stats}
          runtimeAvailable={runtimeAvailable}
          runtimeInfo={runtimeInfo}
        />
      )
    }

    if (tab === "workspaces") {
      return <WorkspacesTab orgs={orgs} />
    }

    if (tab === "users") {
      return <UsersTab users={users} />
    }

    if (tab === "providers") {
      return (
        <RuntimeTab
          runtimeChecking={runtimeChecking}
          runtimeAvailable={runtimeAvailable}
          runtimeInfo={runtimeInfo}
          allRuntimes={allRuntimes}
          runtimeInstallLinks={runtimeInstallLinks}
          onCheckRuntime={checkRuntime}
        />
      )
    }

    if (tab === "security") {
      return (
        <KeeperTab
          keeperLoading={keeperLoading}
          keeperStatus={keeperStatus}
          keeperLog={keeperLog}
          keeperLiveEvents={keeperLiveEvents}
          keeperWsStatus={keeperWsStatus}
          selectedKeeperEntry={selectedKeeperEntry}
          onSelectKeeperEntry={setSelectedKeeperEntry}
          onRefresh={fetchKeeperData}
        />
      )
    }

    // Placeholder for other tabs
    return (
      <Card>
        <CardContent className="p-6 text-center space-y-2">
          <Badge variant="outline">Coming Soon</Badge>
          <p className="text-body text-muted-foreground">
            This section will be available in a future release.
          </p>
        </CardContent>
      </Card>
    )
  }

  const activeSection = sections.find((s) => s.key === tab)

  return (
    <div className="flex h-full">
      {/* Admin left nav */}
      <aside className="w-52 border-r border-border bg-card flex flex-col flex-shrink-0 overflow-y-auto">
        <div className="p-3 border-b border-border">
          <div className="flex items-center gap-2">
            <Shield className="h-4 w-4 text-muted-foreground" />
            <span className="text-label font-semibold">Admin Console</span>
            <Badge variant="outline" className="text-micro ml-auto">
              OWNER
            </Badge>
          </div>
        </div>
        <nav className="flex-1 p-2 space-y-0.5 overflow-y-auto" aria-label="Admin sections">
          {sections.map((s, i) => {
            if (s.type === "section") {
              return (
                <div
                  key={i}
                  className="text-micro font-semibold text-muted-foreground uppercase tracking-wider px-3 pt-3 pb-1"
                >
                  {s.label}
                </div>
              )
            }
            const Icon = s.icon!
            const isActive = s.key === tab
            return (
              <button
                key={s.key}
                aria-current={isActive ? "page" : undefined}
                className={cn(
                  "flex items-center gap-2.5 w-full px-3 py-2 rounded-md text-label transition-colors",
                  isActive
                    ? "bg-accent text-foreground font-medium"
                    : "text-muted-foreground hover:text-foreground hover:bg-muted"
                )}
                onClick={() => setTab(s.key!)}
              >
                <Icon className="h-4 w-4 flex-shrink-0" />
                <span className="truncate">{s.label}</span>
              </button>
            )
          })}
        </nav>
      </aside>

      {/* Admin content */}
      <div className="flex-1 overflow-y-auto">
        <div className="max-w-3xl mx-auto p-6 space-y-6">
          {activeSection && !activeSection.type && (
            <PageHeader
              title={activeSection.label}
              description="Platform administration"
            />
          )}
          {renderContent()}
        </div>
      </div>
    </div>
  )
}
