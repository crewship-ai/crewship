"use client"

import { useMemo, useState } from "react"
import {
  User, Building, Users,
  Box, Link2, Activity,
  Shield, Cpu,
} from "lucide-react"
import { cn } from "@/lib/utils"
import type { LucideIcon } from "lucide-react"
import {
  SidebarToolbar,
  SidebarSearch,
  SidebarSection,
  SidebarRow,
  SIDEBAR_WIDTH,
} from "@/components/layout/sidebar-kit"

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
      { key: "privacy", label: "Privacy", icon: Shield },
    ],
  },
  {
    label: "Workspace",
    items: [
      { key: "general", label: "General", icon: Building },
      { key: "crews", label: "Crews & Containers", icon: Box },
      { key: "aux-models", label: "Auxiliary Models", icon: Cpu },
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
  // Universal search doubles as a command-finder here — type "audit" to jump
  // straight to Audit Log. Filters the nav live; Enter opens the first match.
  const [query, setQuery] = useState("")
  const q = query.trim().toLowerCase()

  const filtered = useMemo(
    () =>
      sections
        .map((s) => ({ ...s, items: s.items.filter((i) => !q || i.label.toLowerCase().includes(q)) }))
        .filter((s) => s.items.length > 0),
    [q],
  )

  const firstMatch = filtered[0]?.items[0]?.key

  return (
    <aside className={cn(SIDEBAR_WIDTH, "shrink-0 bg-sidebar border-r border-sidebar-border flex flex-col")}>
      <SidebarToolbar>
        <SidebarSearch
          value={query}
          onValueChange={setQuery}
          placeholder="Search settings…"
          onKeyDown={(e) => {
            if (e.key === "Enter" && firstMatch) onTabChange(firstMatch)
          }}
        />
      </SidebarToolbar>

      <nav className="flex-1 overflow-y-auto pb-4" aria-label="Settings sections">
        {filtered.map((section) => (
          <SidebarSection
            key={section.label}
            label={section.label}
            actions={
              section.label === "Workspace" && workspaceName ? (
                <span className="ml-1 truncate font-mono text-[10px] normal-case tracking-normal text-sidebar-foreground/35">
                  {workspaceName}
                </span>
              ) : undefined
            }
          >
            {section.items.map((item) => {
              const isActive = item.key === activeTab
              return (
                <SidebarRow
                  key={item.key}
                  selected={isActive}
                  onSelect={() => onTabChange(item.key)}
                  aria-label={item.label}
                >
                  <item.icon className={cn("h-3.5 w-3.5 shrink-0", isActive ? "opacity-100" : "opacity-60")} />
                  <span className="truncate flex-1">{item.label}</span>
                  {item.badge === "P2" && (
                    <span className="ml-auto shrink-0 font-mono text-[10px] text-sidebar-foreground/40">P2</span>
                  )}
                  {item.badge === "OWNER" && (
                    <span className="ml-auto shrink-0 font-mono text-[10px] text-sidebar-foreground/60">Owner</span>
                  )}
                </SidebarRow>
              )
            })}
          </SidebarSection>
        ))}
      </nav>
    </aside>
  )
}
