"use client"

import { useState } from "react"
import Link from "next/link"
import { usePathname } from "next/navigation"
import { cn } from "@/lib/utils"
import { useIsMobile } from "@/hooks/use-mobile"
import {
  LayoutGrid, LayoutDashboard, MessageSquare, FolderOpen,
  Activity, ScrollText, Zap, Key, Settings, Bug, History, X,
} from "lucide-react"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { useAgentDetail } from "@/hooks/use-agent-detail"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

const tabs = [
  { label: "Overview", href: "", icon: LayoutDashboard },
  { label: "Sessions", href: "/chat", icon: MessageSquare },
  { label: "Files", href: "/files", icon: FolderOpen },
  { label: "Runs", href: "/runs", icon: Activity },
  { label: "Logs", href: "/logs", icon: ScrollText },
  { label: "Skills", href: "/skills", icon: Zap },
  { label: "Credentials", href: "/credentials", icon: Key },
  { label: "Settings", href: "/settings", icon: Settings },
  { label: "Debug", href: "/debug", icon: Bug },
  { label: "History", href: "/history", icon: History },
]

interface AgentTabsProps {
  agentId: string
}

export function AgentTabs(_props: AgentTabsProps) {
  // Desktop tabs replaced by AgentDesktopRail, mobile by AgentMobileTabsBar
  return null
}

export function AgentDesktopRail({ agentId }: { agentId: string }) {
  const pathname = usePathname()
  const basePath = `/agents/${agentId}`
  const isMobile = useIsMobile()
  const [expanded, setExpanded] = useState(false)
  const { agent } = useAgentDetail()

  if (isMobile) return null

  const statusDot = agent?.status === "RUNNING" ? "bg-emerald-500 animate-pulse"
    : agent?.status === "ERROR" ? "bg-red-500"
    : "bg-gray-400"

  return (
    <div
      className={cn(
        "bg-background border-r flex flex-col shrink-0 transition-all duration-200 overflow-hidden",
        expanded ? "w-44" : "w-12"
      )}
      onMouseEnter={() => setExpanded(true)}
      onMouseLeave={() => setExpanded(false)}
    >
      {/* Agent avatar -- height matches session sidebar header */}
      <div className={cn(
        "flex items-center border-b shrink-0 h-[41px]",
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
            <div className="text-xs font-semibold truncate">{agent.name}</div>
            <div className="flex items-center gap-1 mt-0.5">
              <span className={cn("h-1.5 w-1.5 rounded-full", statusDot)} />
              <span className="text-micro text-muted-foreground">{agent.status}</span>
            </div>
          </div>
        )}
      </div>

      {/* Navigation items */}
      <div className="flex-1 overflow-y-auto py-1">
        {tabs.map((tab) => {
          const tabPath = tab.href ? `${basePath}${tab.href}` : basePath
          const isActive = tab.href === ""
            ? pathname === basePath
            : pathname.startsWith(tabPath)

          return (
            <Link
              key={tab.href}
              href={tabPath}
              title={!expanded ? tab.label : undefined}
              className={cn(
                "w-full flex items-center transition-colors",
                expanded ? "gap-2.5 px-3 py-2 text-xs font-medium" : "justify-center py-2.5",
                isActive
                  ? "bg-accent text-primary"
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
  const basePath = `/agents/${agentId}`
  const isMobile = useIsMobile()
  const [agentMenuOpen, setAgentMenuOpen] = useState(false)
  const { agent } = useAgentDetail()

  // Chat page renders its own bar with Chat/Sessions/Files switcher
  const isChatPage = pathname.startsWith(`${basePath}/chat`)
  if (!isMobile || isChatPage) return null

  return (
    <>
      <div className="flex items-center bg-card border-t border-b shrink-0">
        <button
          className="h-10 flex items-center gap-2 px-3 hover:bg-accent shrink-0 text-muted-foreground"
          onClick={() => setAgentMenuOpen(true)}
          aria-label="Agent pages"
        >
          <LayoutGrid className="h-4 w-4" />
          <span className="text-xs font-medium">Pages</span>
        </button>
      </div>

      <Sheet open={agentMenuOpen} onOpenChange={setAgentMenuOpen}>
        <SheetContent side="bottom" showCloseButton={false} className="rounded-t-2xl max-h-[85vh] p-0">
          <div className="w-12 h-1.5 rounded-full bg-border mx-auto mt-3 mb-1" />
          <SheetHeader className="px-4 py-2.5 border-b">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2.5">
                {agent && (
                  <img
                    src={getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style || agent.crew?.avatar_style)}
                    alt={agent.name}
                    className="h-7 w-7 rounded-lg shrink-0"
                  />
                )}
                <SheetTitle className="text-xs">{agent?.name ?? "Agent"}</SheetTitle>
              </div>
              <button onClick={() => setAgentMenuOpen(false)} className="h-7 w-7 flex items-center justify-center rounded-md hover:bg-accent shrink-0">
                <X className="h-3.5 w-3.5" />
              </button>
            </div>
          </SheetHeader>
          <div className="flex-1 overflow-y-auto py-1">
            {tabs.map((tab) => {
              const tabPath = tab.href ? `${basePath}${tab.href}` : basePath
              const isActive = tab.href === ""
                ? pathname === basePath
                : pathname.startsWith(tabPath)
              return (
                <Link
                  key={tab.href}
                  href={tabPath}
                  onClick={() => setAgentMenuOpen(false)}
                  className={cn(
                    "w-full flex items-center gap-3 px-4 py-2.5 text-sm transition-colors",
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
