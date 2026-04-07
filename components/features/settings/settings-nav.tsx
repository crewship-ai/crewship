"use client"

import {
  User, Palette, Bell, Shield, Building, Users, CreditCard,
  AlertTriangle, Key, Box, Link2, Activity,
  MessageSquare, ArrowLeft,
} from "lucide-react"
import Link from "next/link"
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
      { key: "chats", label: "Chats", icon: MessageSquare, badge: "P2" },
      { key: "appearance", label: "Appearance", icon: Palette, badge: "P2" },
      { key: "notifications", label: "Notifications", icon: Bell, badge: "P2" },
      { key: "tokens", label: "API Tokens", icon: Key, badge: "P2" },
    ],
  },
  {
    label: "Workspace",
    items: [
      { key: "crews", label: "Crews & Containers", icon: Box },
      { key: "connections", label: "Connections", icon: Link2 },
      { key: "audit", label: "Audit Log", icon: Activity },
      { key: "general", label: "General", icon: Building },
      { key: "members", label: "Members", icon: Users },
      { key: "roles", label: "Roles & Permissions", icon: Shield },
      { key: "billing", label: "Billing & Usage", icon: CreditCard },
      { key: "danger", label: "Danger Zone", icon: AlertTriangle, badge: "OWNER" },
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
    <div className="w-[220px] shrink-0 bg-card border-r border-white/[0.06] flex flex-col">
      {/* Back + title */}
      <div className="px-4 pt-4 pb-3">
        <Link
          href="/"
          className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground/50 hover:text-muted-foreground transition-colors mb-3"
        >
          <ArrowLeft className="h-3 w-3" />
          Back
        </Link>
        <h1 className="text-[15px] font-semibold text-foreground">Settings</h1>
      </div>

      {/* Nav */}
      <nav className="flex-1 overflow-y-auto px-2 pb-4">
        {sections.map((section) => (
          <div key={section.label} className="mb-1">
            <div className="flex items-center gap-2 px-2 pt-4 pb-1.5">
              <span className="text-[11px] font-medium text-muted-foreground/40">
                {section.label}
              </span>
              {section.label === "Workspace" && workspaceName && (
                <span className="text-[10px] text-muted-foreground/25 truncate">
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
                  className={cn(
                    "flex items-center gap-2 w-full px-2 py-[5px] rounded-md text-[13px] transition-colors",
                    isActive
                      ? "bg-white/[0.07] text-foreground"
                      : "text-muted-foreground/70 hover:text-foreground hover:bg-white/[0.03]",
                  )}
                >
                  <item.icon className={cn("h-[14px] w-[14px] shrink-0", isActive ? "opacity-100" : "opacity-50")} />
                  <span className="truncate">{item.label}</span>
                  {item.badge === "P2" && (
                    <span className="ml-auto text-[9px] text-muted-foreground/30 shrink-0">P2</span>
                  )}
                  {item.badge === "OWNER" && (
                    <span className="ml-auto text-[9px] text-amber-500/60 shrink-0">Owner</span>
                  )}
                </button>
              )
            })}
          </div>
        ))}
      </nav>
    </div>
  )
}
