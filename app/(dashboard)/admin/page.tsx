"use client"

import { useEffect, useMemo, useState, useCallback, useRef } from "react"
import { useRouter } from "next/navigation"
import {
  LayoutDashboard, Building, Users, Server, Shield, Database, ListTodo,
  AlertTriangle, Bell, Gauge,
} from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { SubBar } from "@/components/layout/sub-bar"
import {
  SidebarToolbar, SidebarSearch, SidebarSection, SidebarRow, SIDEBAR_WIDTH,
} from "@/components/layout/sidebar-kit"

import type {
  TabKey, Stats, AdminOrg, AdminUser, KeeperStatus, KeeperLogEntry, AdminHealth,
  LicenseInfo, TelemetryInfo, VersionInfo, SecurityPosture, JournalIntegrity,
} from "./types"
import { useAdminWebSocket } from "./hooks/use-admin-websocket"
import { OverviewTab } from "./tabs/overview-tab"
import { RuntimeTab } from "./tabs/runtime-tab"
import type { RuntimeEntry } from "./tabs/runtime-tab"
import { KeeperTab } from "./tabs/keeper-tab"
import { WorkspacesTab } from "./tabs/workspaces-tab"
import { UsersTab } from "./tabs/users-tab"
import { BackupsTab } from "./tabs/backups-tab"
import { KeeperQueuePanel } from "@/components/features/admin/keeper-queue-panel"
import { NotificationsTab } from "./tabs/notifications-tab"
import { RateLimitsTab } from "./tabs/rate-limits-tab"

/**
 * Admin sidebar sections — ONLY real, wired tabs.
 *
 * The previous revision listed 12 extra placeholder sections ("System Logs",
 * "Networking", "Backups", "LLM Gateway", "Auth & SSO", "Feature Flags",
 * "Rate Limits", "Resources") that all rendered a "Coming Soon" card.
 * Those were removed on the user's explicit instruction that the UI must
 * only surface what actually works. Reintroduce them one at a time when
 * each has a real backend to talk to.
 */
interface NavSection {
  label: string
  items: { key: TabKey; label: string; icon: React.ElementType }[]
}

/**
 * One line per section, under the heading — the same shape Settings uses.
 *
 * Admin showed a bare icon and a word. Settings answers "what is this page for"
 * before the first control, which is most of what makes it readable, and the two
 * pages are the same kind of surface. A heading that only repeats the nav row you
 * just clicked is a heading that costs a line and says nothing.
 */
const SECTION_ABOUT: Partial<Record<TabKey, string>> = {
  overview: "Instance health, size and activity at a glance.",
  workspaces: "Every workspace on this instance.",
  users: "Every account, and which workspaces it belongs to.",
  providers: "Container runtime, images and the daemon's own health.",
  notifications: "Where this instance can send a message, and whether it works.",
  ratelimits: "Request budgets, tunable without a restart.",
  security: "Who may read a secret: the judge that decides, and the checks around it.",
  reviews: "What the judge has decided, and what is waiting on a human.",
  backups: "Snapshots of this instance, and restoring from one.",
}

const sections: NavSection[] = [
  {
    label: "Platform",
    items: [
      { key: "overview", label: "Overview", icon: LayoutDashboard },
    ],
  },
  {
    label: "Organizations",
    items: [
      { key: "workspaces", label: "Workspaces", icon: Building },
      { key: "users", label: "Users", icon: Users },
    ],
  },
  {
    label: "Infrastructure",
    items: [
      { key: "providers", label: "Runtime", icon: Server },
      { key: "notifications", label: "Notifications", icon: Bell },
      { key: "ratelimits", label: "Rate Limiters", icon: Gauge },
    ],
  },
  {
    label: "Security",
    items: [
      { key: "security", label: "Keeper", icon: Shield },
      { key: "reviews", label: "Keeper reviews", icon: ListTodo },
    ],
  },
  {
    label: "Data",
    items: [
      { key: "backups", label: "Backups", icon: Database },
    ],
  },
]

const ALL_TABS: TabKey[] = sections.flatMap((s) => s.items.map((i) => i.key))

/**
 * Resolve the section from `?tab=`, falling back to Overview.
 *
 * Exported so the deep-link contract is testable on its own — the same reason
 * initialSettingsTab is.
 */
export function initialAdminTab(search: string): TabKey {
  const t = new URLSearchParams(search).get("tab")
  return t && (ALL_TABS as string[]).includes(t) ? (t as TabKey) : "overview"
}

export default function AdminPage() {
  const router = useRouter()
  const { workspaceId, role, loading: wsLoading } = useWorkspace()
  // Admin console floor is ADMIN+ (#865) — kept in lockstep with the backend
  // ADMIN+ route floor so an ADMIN can open the console they can already drive.
  const isAdmin = role === "OWNER" || role === "ADMIN"
  // Deep-linkable, like /settings?tab=. Admin was the one console whose URL
  // never changed — /admin whichever section you were on — so a section could
  // not be bookmarked, linked in a ticket, or reloaded without losing your
  // place. An unknown or absent key falls back to Overview, the section every
  // admin can read.
  const [tab, _setTab] = useState<TabKey>(() =>
    typeof window === "undefined" ? "overview" : initialAdminTab(window.location.search),
  )
  const setTab = useCallback((next: TabKey) => {
    _setTab(next)
    // replaceState, not a route push: this is the same document, and a history
    // entry per sidebar click would turn Back into "undo my last five clicks".
    if (typeof window !== "undefined") {
      const url = new URL(window.location.href)
      url.searchParams.set("tab", next)
      window.history.replaceState(null, "", url.toString())
    }
  }, [])
  // Universal search doubles as a command-finder — filters the nav live.
  const [navQuery, setNavQuery] = useState("")
  const navQ = navQuery.trim().toLowerCase()
  // Hooks must run before the early returns below, so keep this memo up here.
  const filteredSections = useMemo(
    () =>
      sections
        .map((s) => ({ ...s, items: s.items.filter((i) => !navQ || i.label.toLowerCase().includes(navQ)) }))
        .filter((s) => s.items.length > 0),
    [navQ],
  )
  const firstNavMatch = filteredSections[0]?.items[0]?.key
  const [stats, setStats] = useState<Stats | null>(null)
  const [orgs, setOrgs] = useState<AdminOrg[]>([])
  const [users, setUsers] = useState<AdminUser[]>([])
  const [health, setHealth] = useState<AdminHealth | null>(null)
  const [license, setLicense] = useState<LicenseInfo | null>(null)
  const [telemetry, setTelemetry] = useState<TelemetryInfo | null>(null)
  // The instance already computes all three of these and the overview showed
  // none of them: which build is running (and whether a newer one exists),
  // what the instance thinks of its own security posture, and whether the
  // tamper-evident journal still verifies.
  const [version, setVersion] = useState<VersionInfo | null>(null)
  const [posture, setPosture] = useState<SecurityPosture | null>(null)
  const [journal, setJournal] = useState<JournalIntegrity | null>(null)
  const [loading, setLoading] = useState(true)
  // A 403/500/network failure on the primary fetches must be visible, not a
  // silently empty table (#868). Populated by fetchData; cleared on success.
  const [fetchError, setFetchError] = useState<string | null>(null)

  const [runtimeAvailable, setRuntimeAvailable] = useState<boolean | null>(null)
  const [runtimeInfo, setRuntimeInfo] = useState<{ runtime: string; version: string; socket: string } | null>(null)
  const [allRuntimes, setAllRuntimes] = useState<RuntimeEntry[]>([])
  const [runtimeInstallLinks, setRuntimeInstallLinks] = useState<Record<string, string>>({})
  const [runtimeChecking, setRuntimeChecking] = useState(false)

  const [keeperStatus, setKeeperStatus] = useState<KeeperStatus | null>(null)
  const [keeperLog, setKeeperLog] = useState<KeeperLogEntry[]>([])
  const [keeperLoading, setKeeperLoading] = useState(false)
  const [selectedKeeperEntry, setSelectedKeeperEntry] = useState<KeeperLogEntry | null>(null)

  const { keeperLiveEvents, keeperWsStatus } = useAdminWebSocket({
    enabled: isAdmin && tab === "security",
    workspaceId,
  })

  const checkRuntime = useCallback(async () => {
    setRuntimeChecking(true)
    try {
      // Pass workspace_id so the backend resolves this caller as ADMIN+ and
      // returns full host detail (versions/sockets) rather than the redacted
      // availability-only shape non-admin surfaces get (#865).
      const res = await apiFetch(`/api/v1/system/runtime?workspace_id=${workspaceId}`)
      if (!res.ok) {
        setRuntimeAvailable(false)
        return
      }
      const data = await res.json()
      setRuntimeAvailable(data.available)
      // install_links arrives on both paths since #1690 — an operator with one
      // runtime installed still needs to be told what the others are.
      setRuntimeInstallLinks(data.install_links ?? {})
      setAllRuntimes(data.runtimes ?? [])
      // The top-level summary is null when runtimes are installed but none is
      // in use (the server booted without a container provider). Keep it null
      // rather than manufacturing a {runtime: null} object — the overview
      // renders "none in use" off exactly that distinction.
      setRuntimeInfo(
        data.available && data.runtime
          ? { runtime: data.runtime, version: data.version, socket: data.socket }
          : null,
      )
    } catch {
      setRuntimeAvailable(false)
    } finally {
      setRuntimeChecking(false)
    }
  }, [workspaceId])

  useEffect(() => {
    if (wsLoading) return
    if (!isAdmin) {
      router.push("/")
      return
    }
  }, [wsLoading, role, router])

  // Lifted out of the effect so an action on a tab (creating a workspace,
  // adding a member) can ask for the same refresh the page does on mount —
  // a list that does not catch up after a create reads as a failed create.
  // A generation counter rather than a captured flag: lifting this into a
  // useCallback left a `const` that nothing could ever flip, so every
  // staleness check below was unreachable and a slow response for the previous
  // workspace could overwrite the current one's screen. A ref survives the
  // callback being recreated, which a local no longer does.
  const fetchGeneration = useRef(0)
  const fetchData = useCallback(async () => {
    if (!workspaceId || !isAdmin) return
    const generation = ++fetchGeneration.current
    const isStale = () => generation !== fetchGeneration.current
    {
      setLoading(true)
      try {
        const [
          statsRes, orgsRes, usersRes, healthRes, licenseRes, telemetryRes,
          versionRes, postureRes, journalRes,
        ] = await Promise.all([
          apiFetch(`/api/v1/admin/stats?workspace_id=${workspaceId}`),
          apiFetch(`/api/v1/admin/workspaces?workspace_id=${workspaceId}`),
          apiFetch(`/api/v1/admin/users?workspace_id=${workspaceId}`),
          apiFetch(`/api/v1/admin/health?workspace_id=${workspaceId}`),
          apiFetch(`/api/v1/system/license?workspace_id=${workspaceId}`),
          apiFetch(`/api/v1/system/telemetry`),
          apiFetch(`/api/v1/system/version`),
          apiFetch(`/api/v1/admin/security-posture?workspace_id=${workspaceId}`),
          apiFetch(`/api/v1/admin/journal/verify?workspace_id=${workspaceId}`),
        ])
        if (isStale()) return

        // Surface a failure on any of the three core tables instead of
        // rendering them empty — the whole point of the honesty pass (#868).
        const failed = [
          ["stats", statsRes],
          ["workspaces", orgsRes],
          ["users", usersRes],
        ].filter(([, res]) => !(res as Response).ok) as [string, Response][]
        if (failed.length > 0) {
          const [, first] = failed[0]
          setFetchError(
            `Failed to load ${failed.map(([n]) => n).join(", ")} (HTTP ${first.status}${first.status === 403 ? " — needs ADMIN or OWNER" : ""}).`,
          )
        } else {
          setFetchError(null)
        }

        if (statsRes.ok) setStats(await statsRes.json())
        if (orgsRes.ok) setOrgs(await orgsRes.json())
        if (usersRes.ok) setUsers(await usersRes.json())
        // Health/license/telemetry feed the overview cards; a miss there just
        // degrades those cards, it isn't a table-load failure.
        if (healthRes.ok) setHealth(await healthRes.json())
        if (licenseRes.ok) setLicense(await licenseRes.json())
        if (telemetryRes.ok) setTelemetry(await telemetryRes.json())
        if (versionRes.ok) setVersion(await versionRes.json())
        if (postureRes.ok) setPosture(await postureRes.json())
        if (journalRes.ok) setJournal(await journalRes.json())
      } catch (e) {
        if (!isStale()) setFetchError(e instanceof Error ? e.message : "Network error loading admin data.")
      } finally {
        if (!isStale()) setLoading(false)
      }
    }
  }, [workspaceId, isAdmin])

  useEffect(() => {
    void fetchData()
  }, [fetchData])

  const fetchKeeperData = useCallback(async () => {
    setKeeperLoading(true)
    try {
      const statusRes = await apiFetch(`/api/v1/system/keeper?workspace_id=${workspaceId}`)
      if (statusRes.ok) setKeeperStatus(await statusRes.json())

      if (workspaceId) {
        const logRes = await apiFetch(`/api/v1/admin/keeper/requests?workspace_id=${workspaceId}&limit=50`)
        if (logRes.ok) setKeeperLog(await logRes.json())
      }
    } catch {
      // silently fail
    } finally {
      setKeeperLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    if (isAdmin) checkRuntime()
  }, [role, checkRuntime])

  useEffect(() => {
    // Overview shows a one-line keeper verdict, so it needs the same status
    // the Keeper tab does — otherwise the line reads "unknown" until someone
    // happens to visit that tab.
    if (isAdmin && (tab === "security" || tab === "overview")) fetchKeeperData()
  }, [role, tab, fetchKeeperData])

  if (wsLoading || !isAdmin) {
    return (
      <div className="p-4 md:p-6">
        <Skeleton className="h-8 w-48 mb-3" />
        <Skeleton className="h-[300px] rounded-xl" />
      </div>
    )
  }

  function renderContent() {
    if (loading && ALL_TABS.includes(tab)) {
      return <Skeleton className="h-[200px] rounded-xl" />
    }

    if (tab === "overview") {
      return (
        <OverviewTab
          stats={stats}
          runtimeAvailable={runtimeAvailable}
          runtimeInfo={runtimeInfo}
          health={health}
          license={license}
          telemetry={telemetry}
          version={version}
          posture={posture}
          journal={journal}
          keeper={keeperStatus}
        />
      )
    }

    if (tab === "workspaces") {
      return <WorkspacesTab orgs={orgs} onRefresh={fetchData} />
    }

    if (tab === "users") {
      return <UsersTab users={users} workspaceId={workspaceId} onRefresh={fetchData} />
    }

    if (tab === "providers") {
      return (
        <RuntimeTab
          runtimeChecking={runtimeChecking}
          runtimeAvailable={runtimeAvailable}
          allRuntimes={allRuntimes}
          runtimeInstallLinks={runtimeInstallLinks}
          onCheckRuntime={checkRuntime}
          workspaceId={workspaceId}
        />
      )
    }

    if (tab === "backups") {
      return <BackupsTab workspaceId={workspaceId ?? undefined} />
    }

    if (tab === "notifications") {
      return <NotificationsTab workspaceId={workspaceId} />
    }

    if (tab === "ratelimits") {
      return <RateLimitsTab workspaceId={workspaceId} />
    }

    if (tab === "reviews") {
      return <KeeperQueuePanel workspaceId={workspaceId} />
    }


    if (tab === "security") {
      return (
        <KeeperTab
          workspaceId={workspaceId}
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

    return null
  }

  const activeItem = sections.flatMap((s) => s.items).find((i) => i.key === tab)

  return (
    <div className="flex flex-col h-[calc(100vh-48px)]">
      {/* Identity lives in the sub-bar (not repeated in the sidebar). */}
      <SubBar
        icon={Shield}
        title="Admin Console"
        section={activeItem?.label}
        ariaLabel="Admin Console"
        meta={
          <span className="text-[10px] font-mono uppercase tracking-wide text-muted-foreground/60">{role ?? ""}</span>
        }
      />

      <div className="flex flex-1 min-h-0">
        {/* ── Left nav ─────────────────────────────────────────────── */}
        <aside className={cn(SIDEBAR_WIDTH, "shrink-0 border-r border-border bg-sidebar flex flex-col overflow-hidden")}>
          <SidebarToolbar>
            <SidebarSearch
              value={navQuery}
              onValueChange={setNavQuery}
              placeholder="Search admin…"
              onKeyDown={(e) => {
                if (e.key === "Enter" && firstNavMatch) setTab(firstNavMatch)
              }}
            />
          </SidebarToolbar>
          <nav className="flex-1 overflow-y-auto pb-4" aria-label="Admin sections">
            {filteredSections.map((section) => (
              <SidebarSection key={section.label} label={section.label}>
                {section.items.map((item) => {
                  const Icon = item.icon
                  const isActive = item.key === tab
                  return (
                    <SidebarRow
                      key={item.key}
                      selected={isActive}
                      onSelect={() => setTab(item.key)}
                      aria-label={item.label}
                    >
                      <Icon className={cn("h-3.5 w-3.5 shrink-0", isActive ? "opacity-100" : "opacity-60")} />
                      <span className="truncate flex-1">{item.label}</span>
                    </SidebarRow>
                  )
                })}
              </SidebarSection>
            ))}
          </nav>
        </aside>

        {/* ── Content ─────────────────────────────────────────────── */}
        {/* tabIndex + role/label, not decoration: this pane scrolls, and the
            default section (Overview) renders nothing focusable inside it, so
            without a tab stop a keyboard-only admin cannot scroll it at all
            (axe: scrollable-region-focusable). The label names the section
            rather than saying "content", so the landmark list stays useful. */}
        <div
          className="flex-1 overflow-y-auto"
          tabIndex={0}
          role="region"
          aria-label={activeItem ? `Admin ${activeItem.label}` : "Admin content"}
        >
        <div className="p-4 md:p-6 space-y-4 max-w-5xl mx-auto">
          {activeItem && (
            <div className="space-y-1">
              <div className="flex items-center gap-2">
                <activeItem.icon className="h-3.5 w-3.5 text-foreground/50" />
                <h1 className="text-body font-medium text-foreground/80">{activeItem.label}</h1>
              </div>
              {SECTION_ABOUT[activeItem.key] && (
                <p className="text-xs text-muted-foreground leading-snug">{SECTION_ABOUT[activeItem.key]}</p>
              )}
            </div>
          )}
          {fetchError && (
            <div
              role="alert"
              className="flex items-start gap-2 rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn"
            >
              <AlertTriangle className="h-3.5 w-3.5 shrink-0 mt-0.5" />
              <span>{fetchError}</span>
            </div>
          )}
          {renderContent()}
        </div>
      </div>
      </div>
    </div>
  )
}
