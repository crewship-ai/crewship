"use client"

import { usePathname } from "next/navigation"
import { useState, useEffect, useRef } from "react"
import Link from "next/link"
import { useAuth } from "@/hooks/use-auth"
import { Search, BookOpen, ChevronDown, User, HelpCircle, Github, LogOut } from "lucide-react"
import { BellIcon as AnimatedBell } from "@/components/ui/bell"
import { WifiIcon as AnimatedWifi, type WifiIconHandle } from "@/components/ui/wifi"
import { useRealtime } from "@/hooks/use-realtime"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Badge } from "@/components/ui/badge"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { useEngineStatus } from "@/hooks/use-engine-status"
import { useWorkspace } from "@/hooks/use-workspace"

const pageConfig: Record<string, { title: string }> = {
  "/": { title: "Dashboard" },
  "/agents": { title: "Agents" },
  "/crews": { title: "Crews" },
  "/credentials": { title: "Credentials" },
  "/skills": { title: "Skills" },
  "/audit": { title: "Audit Log" },
  "/settings": { title: "Settings" },
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

const AGENT_PATH_RE = /^\/agents\/([^/]+)/

function useAgentBreadcrumb(pathname: string, workspaceId: string | null): AgentBreadcrumb | null {
  const [data, setData] = useState<AgentBreadcrumb | null>(null)
  const match = pathname.match(AGENT_PATH_RE)
  const agentId = match?.[1]

  useEffect(() => {
    if (!agentId || agentId === "_" || !workspaceId) {
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
  const { session, signOut } = useAuth()
  const agentBreadcrumb = useAgentBreadcrumb(pathname, workspaceId)
  const { status: wsStatus } = useRealtime()
  const wifiRef = useRef<WifiIconHandle>(null)

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

  const userName = session?.user?.name ?? "User"
  const userEmail = session?.user?.email ?? ""
  const userInitials = getInitials(userName)

  const isAgentPage = AGENT_PATH_RE.test(pathname)

  function renderBreadcrumbs() {
    if (isAgentPage && agentBreadcrumb) {
      return (
        <>
          <Link href="/agents" className="text-sm text-muted-foreground hover:text-foreground transition-colors">
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
                  style={{ backgroundColor: agentBreadcrumb.crewColor ?? "#6b7280" }}
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
          <Link href="/agents" className="text-sm text-muted-foreground hover:text-foreground transition-colors">
            Agents
          </Link>
          <span className="text-muted-foreground/40 text-sm shrink-0">/</span>
          <span className="text-sm text-muted-foreground">...</span>
        </>
      )
    }

    const title = config?.title ?? "Crewship"
    return <span className="text-sm font-semibold truncate">{title}</span>
  }

  return (
    <header className="flex h-12 shrink-0 items-center justify-between bg-white dark:bg-background px-3 sm:px-4">
      {/* Left: Org switcher + breadcrumb */}
      <div className="flex items-center gap-1.5 min-w-0 overflow-hidden">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button className="flex items-center gap-1.5 rounded-md px-1.5 py-1 hover:bg-accent transition-colors shrink-0">
              <div className="flex h-5 w-5 items-center justify-center rounded bg-primary text-[8px] font-bold text-primary-foreground">U</div>
              <span className="text-sm font-medium hidden sm:inline">Unify Technology</span>
              <ChevronDown className="h-3 w-3 text-muted-foreground" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-72">
            <DropdownMenuLabel className="text-[10px] uppercase tracking-wider text-muted-foreground font-medium">Workspaces</DropdownMenuLabel>
            <DropdownMenuItem className="flex items-center gap-3 py-2 bg-primary/5">
              <div className="flex h-7 w-7 items-center justify-center rounded-lg bg-primary text-[10px] font-bold text-primary-foreground shrink-0">U</div>
              <div className="min-w-0">
                <div className="text-xs font-medium">Unify Technology</div>
                <div className="text-[10px] text-muted-foreground">3 members</div>
              </div>
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem className="text-xs">Create workspace</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        <span className="text-muted-foreground/40 text-sm shrink-0">/</span>

        {renderBreadcrumbs()}
      </div>

      {/* Right: Engine + search + help + notifications + user */}
      <div className="flex items-center gap-1 sm:gap-1.5 shrink-0">
        {engineStatus === "connected" ? (
          <div className="hidden lg:flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-emerald-50 dark:bg-emerald-950/30 border border-emerald-200 dark:border-emerald-800 mr-1">
            <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />
            <span className="text-[10px] font-medium text-emerald-700 dark:text-emerald-400">Engine</span>
          </div>
        ) : engineStatus === "checking" ? (
          <div className="hidden lg:flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-amber-50 dark:bg-amber-950/30 border border-amber-200 dark:border-amber-800 mr-1">
            <span className="h-1.5 w-1.5 rounded-full bg-amber-500 animate-pulse" />
            <span className="text-[10px] font-medium text-amber-700 dark:text-amber-400">Engine</span>
          </div>
        ) : (
          <div className="hidden lg:flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-red-50 dark:bg-red-950/30 border border-red-200 dark:border-red-800 mr-1">
            <span className="h-1.5 w-1.5 rounded-full bg-red-500" />
            <span className="text-[10px] font-medium text-red-700 dark:text-red-400">Engine</span>
          </div>
        )}

        <Button variant="outline" size="sm" className="h-8 gap-2 rounded-full border-border bg-transparent text-muted-foreground hover:text-foreground px-3" aria-label="Search">
          <Search className="h-3.5 w-3.5" />
          <span className="text-xs hidden sm:inline">Search...</span>
          <kbd className="pointer-events-none hidden h-5 select-none items-center gap-0.5 rounded border bg-muted px-1.5 font-mono text-[10px] font-medium sm:flex">
            <span className="text-xs">&#8984;</span>K
          </kbd>
        </Button>

        <Button variant="ghost" size="icon" className="h-8 w-8 hidden sm:inline-flex" aria-label="Help">
          <BookOpen className="h-4 w-4" />
        </Button>

        <Tooltip>
          <TooltipTrigger asChild>
            <div className={`hidden lg:flex items-center gap-1.5 px-2 py-1 rounded-full border mr-0.5 ${
              wsStatus === "connected"
                ? "bg-emerald-50 dark:bg-emerald-950/30 border-emerald-200 dark:border-emerald-800"
                : wsStatus === "connecting"
                  ? "bg-amber-50 dark:bg-amber-950/30 border-amber-200 dark:border-amber-800"
                  : "bg-red-50 dark:bg-red-950/30 border-red-200 dark:border-red-800"
            }`}>
              <AnimatedWifi ref={wifiRef} size={12} className={
                wsStatus === "connected"
                  ? "text-emerald-600 dark:text-emerald-400"
                  : wsStatus === "connecting"
                    ? "text-amber-600 dark:text-amber-400"
                    : "text-red-600 dark:text-red-400"
              } />
              <span className={`text-[10px] font-medium ${
                wsStatus === "connected"
                  ? "text-emerald-700 dark:text-emerald-400"
                  : wsStatus === "connecting"
                    ? "text-amber-700 dark:text-amber-400"
                    : "text-red-700 dark:text-red-400"
              }`}>WS</span>
            </div>
          </TooltipTrigger>
          <TooltipContent>
            WebSocket: {wsStatus}
          </TooltipContent>
        </Tooltip>

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" className="h-8 w-8 relative" aria-label="Notifications">
              <AnimatedBell size={16} />
              <span className="absolute -top-0.5 -right-0.5 flex h-4 w-4 items-center justify-center rounded-full bg-destructive text-[9px] font-bold text-destructive-foreground ring-2 ring-background" aria-hidden="true">
                3
              </span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-80">
            <DropdownMenuLabel className="flex items-center justify-between">
              <span>Notifications</span>
              <button className="text-[11px] text-primary font-medium">Mark all read</button>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem className="flex-col items-start gap-1 py-3">
              <div className="text-xs font-medium">No new notifications</div>
              <div className="text-[11px] text-muted-foreground">You&apos;re all caught up.</div>
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button className="flex items-center gap-2 rounded-md px-1.5 py-1 hover:bg-accent transition-colors" aria-label="User menu">
              <div className="flex h-7 w-7 items-center justify-center rounded-full bg-primary text-[10px] font-semibold text-primary-foreground">{userInitials}</div>
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
                <Badge variant="outline" className="text-[10px] px-1.5 py-0.5">Owner</Badge>
                <span className="text-[10px] text-muted-foreground">Unify Technology</span>
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
              <Github className="h-4 w-4 text-muted-foreground" />
              GitHub
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem className="gap-3 text-xs text-destructive" onClick={() => { signOut().then(() => window.location.href = "/login") }}>
              <LogOut className="h-4 w-4" />
              Log out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </header>
  )
}
