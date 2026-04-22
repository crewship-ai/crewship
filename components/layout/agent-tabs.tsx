"use client"

import { useCallback, useState } from "react"
import Link from "next/link"
import { usePathname, useRouter } from "next/navigation"
import { cn } from "@/lib/utils"
import { useIsMobile } from "@/hooks/use-mobile"
import { useKeyboardShortcuts } from "@/hooks/use-keyboard-shortcuts"
import {
  LayoutGrid, LayoutDashboard, MessageSquare, FolderOpen,
  Activity, ScrollText, Wrench, Settings, X,
} from "lucide-react"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { StatusDot } from "@/components/ui/status-badge"
import { useAgentDetail } from "@/hooks/use-agent-detail"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

interface TabDef {
  label: string
  href: string
  icon: typeof LayoutDashboard
  /** Extra pathname prefixes that should still light up this tab (legacy routes during migration). */
  aliases?: string[]
}

const tabs: TabDef[] = [
  { label: "Overview", href: "", icon: LayoutDashboard, aliases: ["/history"] },
  { label: "Sessions", href: "/sessions", icon: MessageSquare, aliases: ["/chats", "/chat"] },
  { label: "Runs", href: "/runs", icon: Activity },
  { label: "Workspace", href: "/workspace", icon: FolderOpen, aliases: ["/files", "/terminal"] },
  { label: "Tools", href: "/tools", icon: Wrench, aliases: ["/skills", "/credentials", "/mcp"] },
  { label: "Logs", href: "/logs", icon: ScrollText, aliases: ["/debug"] },
  { label: "Settings", href: "/settings", icon: Settings, aliases: ["/schedule"] },
]

function isTabActive(pathname: string, basePath: string, tab: TabDef): boolean {
  if (tab.href === "") {
    if (pathname === basePath) return true
    return (tab.aliases ?? []).some((alias) => pathname.startsWith(`${basePath}${alias}`))
  }
  const tabPath = `${basePath}${tab.href}`
  if (pathname.startsWith(tabPath)) return true
  return (tab.aliases ?? []).some((alias) => pathname.startsWith(`${basePath}${alias}`))
}

// Map agent status string → canonical status identifier for StatusDot.
function mapAgentStatus(status: string | undefined): string {
  switch (status) {
    case "RUNNING": return "IN_PROGRESS"
    case "ERROR": return "FAILED"
    case "STOPPED": return "CANCELLED"
    case "IDLE":
    default: return "PENDING"
  }
}

interface AgentTabsProps {
  agentId: string
}

export function AgentTabs(_props: AgentTabsProps) {
  // Desktop tabs replaced by AgentDesktopRail, mobile by AgentMobileTabsBar
  return null
}

export function AgentDesktopRail({ agentId }: { agentId: string }) {
  const pathname = usePathname()
  const router = useRouter()
  const basePath = `/fleet/agents/${agentId}`
  const isMobile = useIsMobile()
  const [expanded, setExpanded] = useState(false)
  const { agent } = useAgentDetail()

  const go = useCallback((href: string) => {
    router.push(`${basePath}${href}`)
  }, [router, basePath])

  useKeyboardShortcuts([
    { keys: ["g", "o"], handler: () => go("") },
    { keys: ["g", "s"], handler: () => go("/sessions") },
    { keys: ["g", "r"], handler: () => go("/runs") },
    { keys: ["g", "w"], handler: () => go("/workspace") },
    { keys: ["g", "t"], handler: () => go("/tools") },
    { keys: ["g", "l"], handler: () => go("/logs") },
    { keys: ["g", ","], handler: () => go("/settings") },
  ])

  if (isMobile) return null

  const canonicalStatus = mapAgentStatus(agent?.status)
  const isLive = agent?.status === "RUNNING"

  return (
    <div
      className={cn(
        "bg-background border-r border-border flex flex-col shrink-0 h-full transition-all duration-200 overflow-hidden",
        expanded ? "w-44" : "w-12"
      )}
      onMouseEnter={() => setExpanded(true)}
      onMouseLeave={() => setExpanded(false)}
    >
      {/* Agent avatar — height matches session sidebar header */}
      <div className={cn(
        "flex items-center border-b border-border shrink-0 h-[41px]",
        expanded ? "gap-2.5 px-3" : "justify-center"
      )}>
        {agent ? (
          <img
            src={getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style || agent.crew?.avatar_style)}
            alt={agent.name}
            className="h-7 w-7 rounded-lg shrink-0"
          />
        ) : (
          <div className="h-7 w-7 rounded-lg bg-muted shrink-0" />
        )}
        {expanded && agent && (
          <div className="min-w-0 flex-1">
            <div className="text-label font-semibold truncate">{agent.name}</div>
            <div className="flex items-center gap-1 mt-0.5">
              <StatusDot status={canonicalStatus} live={isLive} className="h-1.5 w-1.5" />
              <span className="text-micro text-muted-foreground truncate">{agent.status}</span>
            </div>
          </div>
        )}
      </div>

      {/* Navigation items — icon toolbar pattern */}
      <div className="flex-1 overflow-y-auto py-1">
        {tabs.map((tab) => {
          const tabPath = tab.href ? `${basePath}${tab.href}` : basePath
          const isActive = isTabActive(pathname, basePath, tab)

          return (
            <Link
              key={tab.href}
              href={tabPath}
              title={!expanded ? tab.label : undefined}
              className={cn(
                "w-full flex items-center transition-colors",
                expanded ? "gap-2.5 px-3 py-2 text-label font-medium" : "justify-center py-2.5",
                isActive
                  ? "bg-accent text-foreground"
                  : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
              )}
            >
              <tab.icon className="h-3.5 w-3.5 shrink-0" />
              {expanded && <span className="truncate">{tab.label}</span>}
            </Link>
          )
        })}
      </div>
    </div>
  )
}

// Exported for use in chat-client and other agent pages on mobile
export const agentTabsList = tabs

export function AgentMobileTabsBar({ agentId }: { agentId: string }) {
  const pathname = usePathname()
  const basePath = `/fleet/agents/${agentId}`
  const isMobile = useIsMobile()
  const [agentMenuOpen, setAgentMenuOpen] = useState(false)
  const { agent } = useAgentDetail()

  // Chat page renders its own bar with Chat/Sessions/Files switcher
  const isChatPage = pathname.startsWith(`${basePath}/chat`)
  if (!isMobile || isChatPage) return null

  return (
    <>
      <div className="flex items-center bg-card border-t border-b border-border shrink-0">
        <button
          className="h-10 flex items-center gap-2 px-3 hover:bg-accent shrink-0 text-muted-foreground"
          onClick={() => setAgentMenuOpen(true)}
          aria-label="Agent pages"
        >
          <LayoutGrid className="h-4 w-4" />
          <span className="text-label font-medium">Pages</span>
        </button>
      </div>

      <Sheet open={agentMenuOpen} onOpenChange={setAgentMenuOpen}>
        <SheetContent side="bottom" showCloseButton={false} className="rounded-t-2xl max-h-[85vh] p-0">
          <div className="w-12 h-1.5 rounded-full bg-border mx-auto mt-3 mb-1" />
          <SheetHeader className="px-4 py-2.5 border-b border-border">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2.5">
                {agent && (
                  <img
                    src={getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style || agent.crew?.avatar_style)}
                    alt={agent.name}
                    className="h-7 w-7 rounded-lg shrink-0"
                  />
                )}
                <SheetTitle className="text-label">{agent?.name ?? "Agent"}</SheetTitle>
              </div>
              <button onClick={() => setAgentMenuOpen(false)} className="h-7 w-7 flex items-center justify-center rounded-md hover:bg-accent shrink-0">
                <X className="h-3.5 w-3.5" />
              </button>
            </div>
          </SheetHeader>
          <div className="flex-1 overflow-y-auto py-1">
            {tabs.map((tab) => {
              const tabPath = tab.href ? `${basePath}${tab.href}` : basePath
              const isActive = isTabActive(pathname, basePath, tab)
              return (
                <Link
                  key={tab.href}
                  href={tabPath}
                  onClick={() => setAgentMenuOpen(false)}
                  className={cn(
                    "w-full flex items-center gap-3 px-4 py-2.5 text-body transition-colors",
                    isActive
                      ? "bg-accent text-foreground font-medium"
                      : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
                  )}
                >
                  <tab.icon className="h-4 w-4" />
                  {tab.label}
                </Link>
              )
            })}
          </div>
        </SheetContent>
      </Sheet>
    </>
  )
}
