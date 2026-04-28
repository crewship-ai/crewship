"use client"

import { usePathname } from "next/navigation"
import { useState, useEffect, useRef } from "react"
import Link from "next/link"
import { useAuth } from "@/hooks/use-auth"
import {
  Search, BookOpen, ChevronDown, User, HelpCircle, GitBranch, LogOut, Menu, X,
  LayoutDashboard, Network, Zap, Key, Activity, Shield, Settings, Store, ShieldCheck,
} from "lucide-react"

import { WifiIcon as AnimatedWifi, type WifiIconHandle } from "@/components/ui/wifi"
import { useRealtime } from "@/hooks/use-realtime"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,

  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Badge } from "@/components/ui/badge"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { useEngineStatus } from "@/hooks/use-engine-status"
import { useCrewsStatus } from "@/hooks/use-crews-status"
import { usePendingEscalations } from "@/hooks/use-pending-escalations"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsMobile } from "@/hooks/use-mobile"
import { useAbilities } from "@/hooks/use-abilities"
import { getCrewDotColor } from "@/lib/crew-icon"
import { CommandPalette } from "@/components/command-palette"
import { NotificationBell } from "@/components/features/notifications/notification-bell"
import { useAppStore } from "@/lib/store"

const mobileNavSections = [
  {
    label: "Work",
    items: [
      { title: "Dashboard", href: "/", icon: LayoutDashboard },
      { title: "Crews & Agents", href: "/crews", icon: Network },
    ],
  },
  {
    label: "Configure",
    items: [
      { title: "Skills", href: "/skills", icon: Zap },
      { title: "Marketplace", href: "/marketplace", icon: Store, disabled: true },
      { title: "Credentials", href: "/credentials", icon: Key },
    ],
  },
  {
    label: "Monitor",
    items: [
      { title: "Runs", href: "/runs", icon: Activity },
      { title: "Audit Log", href: "/audit", icon: Shield },
    ],
  },
  {
    label: "System",
    items: [
      { title: "Settings", href: "/settings", icon: Settings },
      { title: "Admin", href: "/admin", icon: ShieldCheck, ownerOnly: true },
    ],
  },
]

const pageConfig: Record<string, { title: string }> = {
  "/": { title: "Dashboard" },
  "/crews/agents": { title: "Agents" },
  "/crews": { title: "Crews" },
  "/credentials": { title: "Credentials" },
  "/skills": { title: "Skills" },
  "/audit": { title: "Audit Log" },
  "/settings": { title: "Settings" },
}

const settingsTabTitles: Record<string, string> = {
  profile: "Profile",
  general: "General",
  crews: "Crews & Containers",
  connections: "Connections",
  members: "Members",
  audit: "Audit Log",
}

function getInitials(name: string): string {
  if (!name.trim()) return "?"
  return name
    .split(" ")
    .map((n) => n[0])
    .join("")
    .slice(0, 2)
    .toUpperCase()
}

interface AgentBreadcrumb {
  agentName: string
  crewName: string | null
  crewId: string | null
  crewColor: string | null
}

const AGENT_PATH_RE = /^\/crews\/agents\/([^/]+)/

function useAgentBreadcrumb(pathname: string, workspaceId: string | null): AgentBreadcrumb | null {
  const [data, setData] = useState<AgentBreadcrumb | null>(null)
  const match = pathname.match(AGENT_PATH_RE)
  const agentId = match?.[1]

  useEffect(() => {
    if (!agentId || agentId === "_" || agentId === "new" || !workspaceId) {
      setData(null)
      return
    }

    let cancelled = false
    setData(null)

    async function fetchBreadcrumb() {
      try {
        const res = await fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`)
        if (!res.ok) {
          if (!cancelled) setData(null)
          return
        }
        const agent = await res.json()
        if (!cancelled) {
          setData({
            agentName: agent.name,
            crewName: agent.crew?.name ?? null,
            crewId: agent.crew_id,
            crewColor: agent.crew?.color ?? null,
          })
        }
      } catch {
        if (!cancelled) setData(null)
      }
    }

    fetchBreadcrumb()
    return () => { cancelled = true }
  }, [agentId, workspaceId])

  return agentId ? data : null
}

export function AppToolbar() {
  const pathname = usePathname()
  const config = pageConfig[pathname] ?? null
  const { workspaceId } = useWorkspace()
  const { status: engineStatus } = useEngineStatus(workspaceId)
  const crewsStatus = useCrewsStatus(workspaceId)
  const pendingEscalations = usePendingEscalations(workspaceId)
  const { session, signOut } = useAuth()
  const agentBreadcrumb = useAgentBreadcrumb(pathname, workspaceId)
  const { status: wsStatus } = useRealtime()
  const wifiRef = useRef<WifiIconHandle>(null)
  const isMobile = useIsMobile()
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const [cmdkOpen, setCmdkOpen] = useState(false)
  const { role } = useAbilities()
  const settingsTab = useAppStore((s) => s.settingsTab)
  const breadcrumbs = useAppStore((s) => s.breadcrumbs)

  useEffect(() => {
    if (wsStatus === "connected") {
      const handle = wifiRef.current
      handle?.startAnimation()
      const t = setTimeout(() => handle?.stopAnimation(), 1000)
      return () => {
        clearTimeout(t)
        handle?.stopAnimation()
      }
    }
  }, [wsStatus])

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault()
        setCmdkOpen((prev) => !prev)
      }
    }
    document.addEventListener("keydown", onKeyDown)
    return () => document.removeEventListener("keydown", onKeyDown)
  }, [])

  const userName = session?.user?.name ?? "User"
  const userEmail = session?.user?.email ?? ""
  const userInitials = getInitials(userName)

  const isAgentPage = AGENT_PATH_RE.test(pathname)
  const isCrewsPage = pathname === "/crews"
  const chatMatch = pathname.match(/^\/chat\/([^/]+)/)
  const isChatPage = Boolean(chatMatch)
  const chatAgentSlug = chatMatch?.[1] ? decodeURIComponent(chatMatch[1]) : null

  function renderBreadcrumbs() {
    if (isAgentPage && agentBreadcrumb) {
      return (
        <>
          <Link href="/crews/agents" className="text-sm text-muted-foreground hover:text-foreground transition-colors">
            Agents
          </Link>
          {agentBreadcrumb.crewName && agentBreadcrumb.crewId && (
            <>
              <span className="text-muted-foreground/40 text-sm shrink-0">/</span>
              <Link
                href={`/crews/${agentBreadcrumb.crewId}`}
                className="text-sm text-muted-foreground hover:text-foreground transition-colors flex items-center gap-1.5"
              >
                <span
                  className="h-2 w-2 rounded-full shrink-0"
                  style={{ backgroundColor: getCrewDotColor(agentBreadcrumb.crewColor) }}
                />
                {agentBreadcrumb.crewName}
              </Link>
            </>
          )}
          <span className="text-muted-foreground/40 text-sm shrink-0">/</span>
          <span className="text-sm font-semibold truncate">{agentBreadcrumb.agentName}</span>
        </>
      )
    }

    if (isAgentPage) {
      return (
        <>
          <Link href="/crews/agents" className="text-sm text-muted-foreground hover:text-foreground transition-colors">
            Agents
          </Link>
          <span className="text-muted-foreground/40 text-sm shrink-0">/</span>
          <span className="text-sm text-muted-foreground">...</span>
        </>
      )
    }

    // Crews page: just the section title (subbar already shows
    // breadcrumb + stats — no point duplicating "Crews / Crews & Agents").
    if (isCrewsPage) {
      return (
        <span className="text-sm font-semibold">Crews &amp; Agents</span>
      )
    }

    // Chat page: link back to /crews?agent=<slug> so the toolbar back-action
    // restores agent selection in the canvas (instead of dumping the user
    // on an empty roster).
    if (isChatPage && chatAgentSlug) {
      return (
        <>
          <Link
            href={`/crews?agent=${encodeURIComponent(chatAgentSlug)}`}
            className="text-sm text-muted-foreground hover:text-foreground transition-colors"
          >
            Crews
          </Link>
          <span className="text-muted-foreground/40 text-sm shrink-0">/</span>
          <Link
            href={`/crews?agent=${encodeURIComponent(chatAgentSlug)}`}
            className="text-sm text-muted-foreground hover:text-foreground transition-colors truncate"
          >
            {chatAgentSlug}
          </Link>
          <span className="text-muted-foreground/40 text-sm shrink-0">/</span>
          <span className="text-sm font-semibold">Chat</span>
        </>
      )
    }

    // Settings breadcrumb: Settings / Profile
    if (pathname === "/settings" && settingsTab) {
      const tabTitle = settingsTabTitles[settingsTab]
      return (
        <>
          <span className="text-sm text-muted-foreground">Settings</span>
          {tabTitle && (
            <>
              <span className="text-muted-foreground/40 text-sm shrink-0">/</span>
              <span className="text-sm font-semibold truncate">{tabTitle}</span>
            </>
          )}
        </>
      )
    }

    const title = config?.title ?? "Crewship"
    return (
      <div className="flex items-center gap-1.5 min-w-0">
        <span className="text-sm font-semibold truncate">{title}</span>
        {breadcrumbs.length > 0 && breadcrumbs.map((item, i) => (
          <div key={i} className="flex items-center gap-1.5 min-w-0">
            <span className="text-muted-foreground/30 text-xs">/</span>
            {item.onClick ? (
              <button
                type="button"
                onClick={item.onClick}
                className="text-xs text-muted-foreground/70 hover:text-foreground/90 transition-colors truncate max-w-[160px]"
              >
                {item.label}
              </button>
            ) : (
              <span className="text-xs text-foreground/80 truncate max-w-[160px]">{item.label}</span>
            )}
          </div>
        ))}
      </div>
    )
  }

  return (
    <header className="flex h-12 shrink-0 items-center justify-between bg-card px-3 sm:px-4 border-b border-white/[0.1]">
      {/* Left: breadcrumb only */}
      <div className="flex items-center gap-1.5 min-w-0 overflow-hidden">
        {renderBreadcrumbs()}
      </div>

      {/* Right: Status indicators + search + notifications */}
      <div className="flex items-center gap-1 sm:gap-1.5 shrink-0">
        {/* Status indicators: System + Crews + Escalations */}
        {(() => {
          const systemOnline = engineStatus === "connected" && wsStatus === "connected"
          const systemChecking = engineStatus === "checking" || wsStatus === "connecting"

          let crewsLabel = ""
          let crewsColor: "emerald" | "amber" | "red" | "muted" = "muted"
          if (!crewsStatus) {
            crewsLabel = "Loading..."
            crewsColor = "muted"
          } else if (crewsStatus.total === 0) {
            crewsLabel = "No agents"
            crewsColor = "muted"
          } else if (crewsStatus.error > 0 && crewsStatus.running > 0) {
            crewsLabel = `${crewsStatus.running > 99 ? "99+" : crewsStatus.running} active \u00b7 ${crewsStatus.error} error${crewsStatus.error > 1 ? "s" : ""}`
            crewsColor = "amber"
          } else if (crewsStatus.error > 0) {
            crewsLabel = `${crewsStatus.error} error${crewsStatus.error > 1 ? "s" : ""}`
            crewsColor = "red"
          } else if (crewsStatus.running > 0) {
            crewsLabel = `${crewsStatus.running > 99 ? "99+" : crewsStatus.running} active`
            crewsColor = "emerald"
          } else {
            crewsLabel = "Crews idle"
            crewsColor = "muted"
          }

          const colorMap = {
            emerald: { bg: "bg-emerald-50 dark:bg-emerald-950/30 border-emerald-200 dark:border-emerald-800", dot: "bg-emerald-500", text: "text-emerald-700 dark:text-emerald-400", icon: "text-emerald-600" },
            amber: { bg: "bg-amber-50 dark:bg-amber-950/30 border-amber-200 dark:border-amber-800", dot: "bg-amber-500", text: "text-amber-700 dark:text-amber-400", icon: "text-amber-600" },
            red: { bg: "bg-red-50 dark:bg-red-950/30 border-red-200 dark:border-red-800", dot: "bg-red-500", text: "text-red-700 dark:text-red-400", icon: "text-red-600" },
            muted: { bg: "bg-muted/50 border-border", dot: "bg-muted-foreground/40", text: "text-muted-foreground", icon: "text-muted-foreground" },
          }

          const sysColors = systemOnline ? colorMap.emerald : systemChecking ? colorMap.amber : colorMap.red
          const crewsColors = colorMap[crewsColor]

          return (
            <div className="hidden lg:flex items-center gap-1.5 mr-1">
              <Tooltip>
                <TooltipTrigger asChild>
                  <div tabIndex={0} role="status" aria-label={`System ${systemOnline ? "online" : systemChecking ? "connecting" : "offline"}`} className={`flex items-center gap-1.5 px-2.5 py-1 rounded-full border ${sysColors.bg}`}>
                    <AnimatedWifi ref={wifiRef} size={12} className={sysColors.icon} />
                    <span className={`text-micro font-medium ${sysColors.text}`}>
                      {systemOnline ? "Online" : systemChecking ? "Connecting" : "Offline"}
                    </span>
                  </div>
                </TooltipTrigger>
                <TooltipContent>
                  Engine: {engineStatus === "connected" ? "Online" : engineStatus === "checking" ? "Connecting..." : "Offline"} / Real-time: {wsStatus === "connected" ? "Connected" : wsStatus === "connecting" ? "Connecting..." : "Disconnected"}
                </TooltipContent>
              </Tooltip>

              <Tooltip>
                <TooltipTrigger asChild>
                  <div tabIndex={0} role="status" aria-label={`Crews: ${crewsLabel}`} className={`flex items-center gap-1.5 px-2.5 py-1 rounded-full border ${crewsColors.bg}`}>
                    <span className={`h-1.5 w-1.5 rounded-full ${crewsColors.dot} ${crewsStatus?.running ? "animate-pulse" : ""}`} />
                    <span className={`text-micro font-medium ${crewsColors.text}`}>{crewsLabel}</span>
                  </div>
                </TooltipTrigger>
                <TooltipContent>
                  {crewsStatus ? `${crewsStatus.total} agents: ${crewsStatus.running} running, ${crewsStatus.idle} idle, ${crewsStatus.error} errors` : "Loading crews status..."}
                </TooltipContent>
              </Tooltip>

              {pendingEscalations > 0 && (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Link href="/crews" className={`flex items-center gap-1.5 px-2.5 py-1 rounded-full border ${colorMap.amber.bg} hover:brightness-95 transition-all`}>
                      <span className="h-1.5 w-1.5 rounded-full bg-amber-500 animate-pulse" />
                      <span className={`text-micro font-medium ${colorMap.amber.text}`}>
                        {pendingEscalations > 99 ? "99+" : pendingEscalations} escalation{pendingEscalations !== 1 ? "s" : ""}
                      </span>
                    </Link>
                  </TooltipTrigger>
                  <TooltipContent>
                    {pendingEscalations} pending escalation{pendingEscalations !== 1 ? "s" : ""} need your attention
                  </TooltipContent>
                </Tooltip>
              )}
            </div>
          )
        })()}

        {/* Desktop: search button */}
        <Button variant="outline" size="sm" className="hidden md:flex h-8 gap-2 rounded-full border-border bg-transparent text-muted-foreground hover:text-foreground px-3" aria-label="Search" onClick={() => setCmdkOpen(true)}>
          <Search className="h-3.5 w-3.5" />
          <span className="text-xs hidden sm:inline">Search...</span>
          <kbd className="pointer-events-none hidden h-5 select-none items-center gap-0.5 rounded border bg-muted px-1.5 font-mono text-[10px] font-medium sm:flex">
            <span className="text-xs">&#8984;</span>K
          </kbd>
        </Button>

        {/* Mobile: search icon only */}
        <Button variant="ghost" size="icon" className="h-8 w-8 md:hidden" aria-label="Search" onClick={() => setCmdkOpen(true)}>
          <Search className="h-4 w-4" />
        </Button>

        {/* Desktop: notifications */}
        <div className="hidden md:flex">
          <NotificationBell />
        </div>

        <Button variant="ghost" size="icon" className="h-8 w-8 hidden sm:inline-flex" aria-label="Help">
          <BookOpen className="h-4 w-4" />
        </Button>

        {/* Desktop: user menu */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button className="hidden md:flex items-center gap-2 rounded-md px-1.5 py-1 hover:bg-accent transition-colors" aria-label="User menu">
              <div className="flex h-7 w-7 items-center justify-center rounded-full bg-primary text-micro font-semibold text-primary-foreground">{userInitials}</div>
              <span className="text-xs font-medium hidden sm:inline">{userName.split(" ")[0]}</span>
              <ChevronDown className="h-3 w-3 text-muted-foreground hidden sm:block" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-64">
            <div className="px-2 py-3">
              <div className="flex items-center gap-3">
                <div className="flex h-10 w-10 items-center justify-center rounded-full bg-primary text-sm font-semibold text-primary-foreground">{userInitials}</div>
                <div>
                  <div className="text-sm font-medium">{userName}</div>
                  <div className="text-xs text-muted-foreground">{userEmail}</div>
                </div>
              </div>
              <div className="flex items-center gap-1.5 mt-2">
                <Badge variant="outline" className="text-micro px-1.5 py-0.5">Owner</Badge>
                <span className="text-micro text-muted-foreground">Unify Technology</span>
              </div>
            </div>
            <DropdownMenuSeparator />
            <DropdownMenuItem className="gap-3 text-xs">
              <User className="h-4 w-4 text-muted-foreground" />
              Profile & Settings
            </DropdownMenuItem>
            <DropdownMenuItem className="gap-3 text-xs">
              <HelpCircle className="h-4 w-4 text-muted-foreground" />
              Help & Support
            </DropdownMenuItem>
            <DropdownMenuItem className="gap-3 text-xs">
              <BookOpen className="h-4 w-4 text-muted-foreground" />
              Documentation
            </DropdownMenuItem>
            <DropdownMenuItem className="gap-3 text-xs">
              <GitBranch className="h-4 w-4 text-muted-foreground" />
              GitHub
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem className="gap-3 text-xs text-destructive" onClick={() => { signOut().then(() => window.location.href = "/login") }}>
              <LogOut className="h-4 w-4" />
              Log out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        {/* Mobile: hamburger for main navigation */}
        <Button variant="ghost" size="icon" className="h-8 w-8 md:hidden" aria-label="Navigation" onClick={() => setMobileNavOpen(true)}>
          <Menu className="h-4 w-4" />
        </Button>
      </div>

      {/* Mobile: main navigation bottom sheet */}
      {isMobile && (
        <Sheet open={mobileNavOpen} onOpenChange={setMobileNavOpen}>
          <SheetContent side="bottom" showCloseButton={false} className="rounded-t-2xl max-h-[85vh] p-0">
            <div className="w-12 h-1.5 rounded-full bg-border mx-auto mt-3 mb-1" />
            <SheetHeader className="px-4 py-2 border-b">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <div className="flex h-6 w-6 items-center justify-center rounded bg-primary text-[8px] font-bold text-primary-foreground">U</div>
                  <SheetTitle className="text-sm">Unify Technology</SheetTitle>
                </div>
                <button onClick={() => setMobileNavOpen(false)} className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent">
                  <X className="h-4 w-4" />
                </button>
              </div>
            </SheetHeader>
            <div className="flex-1 overflow-y-auto py-2">
              {mobileNavSections.map((section) => (
                <div key={section.label}>
                  <div className="px-3 py-1 text-micro uppercase tracking-wider font-semibold text-muted-foreground">{section.label}</div>
                  {section.items
                    .filter((item) => !("ownerOnly" in item && item.ownerOnly && role !== "OWNER"))
                    .map((item) => {
                      const isActive = pathname === item.href || (item.href !== "/" && pathname.startsWith(item.href))
                      const disabled = "disabled" in item && item.disabled
                      return (
                        <Link
                          key={item.href}
                          href={disabled ? "#" : item.href}
                          onClick={() => !disabled && setMobileNavOpen(false)}
                          className={`w-full flex items-center gap-3 px-4 py-2.5 text-sm transition-colors ${
                            disabled
                              ? "text-muted-foreground/50 pointer-events-none"
                              : isActive
                                ? "bg-accent text-foreground font-medium"
                                : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
                          }`}
                        >
                          <item.icon className="h-4 w-4" />
                          {item.title}
                          {disabled && <span className="text-micro bg-muted px-1.5 rounded ml-auto">FUTURE</span>}
                        </Link>
                      )
                    })}
                </div>
              ))}
            </div>
            <div className="border-t p-4">
              <div className="flex items-center gap-3">
                <div className="h-8 w-8 rounded-full bg-primary text-micro font-bold text-primary-foreground flex items-center justify-center">{userInitials}</div>
                <div className="flex-1 min-w-0">
                  <div className="text-xs font-medium">{userName}</div>
                  <div className="text-micro text-muted-foreground">{userEmail}</div>
                </div>
                <button
                  onClick={() => { signOut().then(() => window.location.href = "/login") }}
                  className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent text-muted-foreground"
                  aria-label="Log out"
                >
                  <LogOut className="h-4 w-4" />
                </button>
              </div>
            </div>
          </SheetContent>
        </Sheet>
      )}

      <CommandPalette open={cmdkOpen} onOpenChange={setCmdkOpen} />
    </header>
  )
}
