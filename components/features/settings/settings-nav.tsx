"use client"

import {
  User, Building, Users,
  Box, Link2, Activity,
} from "lucide-react"
import { cn } from "@/lib/utils"
import type { LucideIcon } from "lucide-react"

interface NavItem {
  key: string
  label: string
  icon: LucideIcon
  badge?: string
}

interface NavSection {
  label: string
  subtitle?: string
  items: NavItem[]
}

const sections: NavSection[] = [
  {
    label: "Account",
    items: [
      { key: "profile", label: "Profile", icon: User },
    ],
  },
  {
    label: "Workspace",
    items: [
      { key: "general", label: "General", icon: Building },
      { key: "crews", label: "Crews & Containers", icon: Box },
      { key: "connections", label: "Connections", icon: Link2 },
      { key: "members", label: "Members", icon: Users },
      { key: "audit", label: "Audit Log", icon: Activity },
    ],
  },
]

interface SettingsNavProps {
  activeTab: string
  onTabChange: (tab: string) => void
  workspaceName?: string
}

export function SettingsNav({ activeTab, onTabChange, workspaceName }: SettingsNavProps) {
  return (
    <aside className="w-[220px] shrink-0 bg-sidebar border-r border-sidebar-border flex flex-col">
      <nav className="flex-1 overflow-y-auto px-2 pt-3 pb-4" aria-label="Settings sections">
        {sections.map((section) => (
          <div key={section.label} className="mb-1">
            <div className="flex items-center gap-2 px-2 pt-3 pb-1.5">
              <span className="text-micro font-medium text-sidebar-foreground/60 uppercase tracking-wider">
                {section.label}
              </span>
              {section.label === "Workspace" && workspaceName && (
                <span className="text-micro text-sidebar-foreground/40 truncate">
                  {workspaceName}
                </span>
              )}
            </div>
            {section.items.map((item) => {
              const isActive = item.key === activeTab
              return (
                <button
                  key={item.key}
                  onClick={() => onTabChange(item.key)}
                  aria-current={isActive ? "page" : undefined}
                  className={cn(
                    "flex items-center gap-2 w-full px-2 py-1.5 rounded-md text-label transition-colors",
                    isActive
                      ? "bg-sidebar-accent text-sidebar-accent-foreground font-medium"
                      : "text-sidebar-foreground/70 hover:text-sidebar-foreground hover:bg-sidebar-accent/50",
                  )}
                >
                  <item.icon className={cn("h-3.5 w-3.5 shrink-0", isActive ? "opacity-100" : "opacity-60")} />
                  <span className="truncate">{item.label}</span>
                  {item.badge === "P2" && (
                    <span className="ml-auto text-micro text-sidebar-foreground/40 shrink-0">P2</span>
                  )}
                  {item.badge === "OWNER" && (
                    <span className="ml-auto text-micro text-sidebar-foreground/60 shrink-0">Owner</span>
                  )}
                </button>
              )
            })}
          </div>
        ))}
      </nav>
    </aside>
  )
}
