"use client"

import { motion, AnimatePresence } from "motion/react"
import {
  User, Palette, Bell, Shield, Building, Users, CreditCard,
  AlertTriangle, Key, Box, Link2, Activity, PanelLeftClose, PanelLeftOpen, X,
  MessageSquare,
} from "lucide-react"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import type { Scope } from "./settings-context-bar"
import type { LucideIcon } from "lucide-react"

interface TabDef {
  type?: "section"
  key?: string
  label: string
  icon?: LucideIcon
  badge?: string
}

const userTabs: TabDef[] = [
  { type: "section", label: "ACCOUNT" },
  { key: "profile", label: "Profile", icon: User },
  { type: "section", label: "PREFERENCES" },
  { key: "chats", label: "Chats", icon: MessageSquare, badge: "Phase 2" },
  { key: "appearance", label: "Appearance", icon: Palette, badge: "Phase 2" },
  { key: "notifications", label: "Notifications", icon: Bell, badge: "Phase 2" },
  { type: "section", label: "DEVELOPER" },
  { key: "tokens", label: "API Tokens", icon: Key, badge: "Phase 2" },
]

const orgTabs: TabDef[] = [
  { type: "section", label: "INFRASTRUCTURE" },
  { key: "crews", label: "Crews & Containers", icon: Box },
  { key: "connections", label: "Connections", icon: Link2 },
  { key: "audit", label: "Crew Audit Log", icon: Activity },
  { type: "section", label: "WORKSPACE" },
  { key: "general", label: "General", icon: Building },
  { key: "members", label: "Members", icon: Users },
  { key: "roles", label: "Roles & Permissions", icon: Shield },
  { type: "section", label: "BILLING" },
  { key: "billing", label: "Billing & Usage", icon: CreditCard },
  { type: "section", label: "ADVANCED" },
  { key: "danger", label: "Danger Zone", icon: AlertTriangle, badge: "OWNER" },
]

interface SettingsNavProps {
  scope: Scope
  activeTab: string
  onTabChange: (tab: string) => void
  collapsed: boolean
  onCollapsedChange: (collapsed: boolean) => void
  isMobile: boolean
}

export function SettingsNav({
  scope,
  activeTab,
  onTabChange,
  collapsed,
  onCollapsedChange,
  isMobile,
}: SettingsNavProps) {
  const tabs = scope === "user" ? userTabs : orgTabs

  const navContent = (
    <nav className="flex-1 overflow-y-auto py-2 px-1.5 space-y-0.5">
      {tabs.map((t, i) => {
        if (t.type === "section") {
          if (collapsed && !isMobile) return null
          return (
            <div
              key={i}
              className="text-[10px] font-semibold text-muted-foreground/50 uppercase tracking-wider px-2.5 pt-3 pb-1"
            >
              {t.label}
            </div>
          )
        }

        const Icon = t.icon!
        const isActive = t.key === activeTab

        if (collapsed && !isMobile) {
          return (
            <TooltipProvider key={t.key} delayDuration={0}>
              <Tooltip>
                <TooltipTrigger asChild>
                  <button
                    className={cn(
                      "flex items-center justify-center w-full h-8 rounded-md transition-colors",
                      isActive
                        ? "bg-white/[0.06] text-blue-400"
                        : "text-muted-foreground hover:text-foreground/80 hover:bg-white/[0.03]",
                    )}
                    onClick={() => onTabChange(t.key!)}
                  >
                    <Icon className="h-4 w-4" />
                  </button>
                </TooltipTrigger>
                <TooltipContent side="right" className="text-xs">
                  {t.label}
                </TooltipContent>
              </Tooltip>
            </TooltipProvider>
          )
        }

        return (
          <button
            key={t.key}
            className={cn(
              "flex items-center gap-2.5 w-full px-2.5 py-2 rounded-md text-[12px] transition-colors",
              isActive
                ? "bg-white/[0.06] text-blue-400 border-l-2 border-blue-400 pl-2"
                : "text-muted-foreground hover:text-foreground/80 hover:bg-white/[0.03]",
            )}
            onClick={() => {
              onTabChange(t.key!)
              if (isMobile) onCollapsedChange(true)
            }}
          >
            <Icon className={cn("h-4 w-4 shrink-0", isActive ? "opacity-100" : "opacity-75")} />
            <span className="truncate">{t.label}</span>
            {t.badge === "Phase 2" && (
              <span className="ml-auto text-[9px] bg-white/[0.06] text-muted-foreground/60 px-1.5 py-0.5 rounded font-medium shrink-0">
                P2
              </span>
            )}
            {t.badge === "OWNER" && (
              <span className="ml-auto text-[9px] bg-amber-500/15 text-amber-400 px-1.5 py-0.5 rounded font-medium shrink-0">
                Owner
              </span>
            )}
          </button>
        )
      })}
    </nav>
  )

  // Mobile: overlay drawer
  if (isMobile) {
    return (
      <>
        {collapsed && (
          <button
            className="absolute top-2 left-2 z-20 h-8 w-8 min-h-[44px] min-w-[44px] rounded-md bg-card border border-white/[0.1] flex items-center justify-center text-muted-foreground hover:text-foreground"
            onClick={() => onCollapsedChange(false)}
          >
            <PanelLeftOpen className="h-3.5 w-3.5" />
          </button>
        )}
        <AnimatePresence>
          {!collapsed && (
            <>
              <motion.div
                className="fixed inset-0 bg-black/50 z-30"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                onClick={() => onCollapsedChange(true)}
              />
              <motion.div
                className="fixed left-0 top-0 bottom-0 w-[260px] z-40 bg-card border-r border-white/[0.1] flex flex-col"
                initial={{ x: -260 }}
                animate={{ x: 0 }}
                exit={{ x: -260 }}
                transition={{ type: "spring", damping: 25, stiffness: 300 }}
              >
                <div className="flex items-center justify-between px-3 py-2 border-b border-white/[0.1]">
                  <span className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">Settings</span>
                  <button
                    onClick={() => onCollapsedChange(true)}
                    className="h-8 w-8 min-h-[44px] min-w-[44px] flex items-center justify-center text-muted-foreground hover:text-foreground"
                  >
                    <X className="h-4 w-4" />
                  </button>
                </div>
                {navContent}
              </motion.div>
            </>
          )}
        </AnimatePresence>
      </>
    )
  }

  // Desktop: collapsible sidebar
  return (
    <motion.div
      className="bg-card border-r border-white/[0.1] flex flex-col overflow-hidden shrink-0"
      animate={{ width: collapsed ? 48 : 240 }}
      transition={{ type: "spring", damping: 25, stiffness: 300 }}
    >
      {/* Collapse toggle */}
      <div className={cn("flex items-center border-b border-white/[0.06] shrink-0", collapsed ? "justify-center py-2" : "justify-between px-3 py-2")}>
        {!collapsed && (
          <span className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">Settings</span>
        )}
        <button
          onClick={() => onCollapsedChange(!collapsed)}
          className="h-6 w-6 flex items-center justify-center text-muted-foreground hover:text-foreground/80 transition-colors rounded"
        >
          {collapsed ? <PanelLeftOpen className="h-3.5 w-3.5" /> : <PanelLeftClose className="h-3.5 w-3.5" />}
        </button>
      </div>
      {navContent}
    </motion.div>
  )
}
