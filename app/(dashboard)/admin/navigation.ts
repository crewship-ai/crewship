import { LayoutDashboard, Building, Users, Server, Shield, Database, ListTodo, Bell, Gauge } from "lucide-react"
import type { TabKey } from "./types"

interface NavSection {
  label: string
  items: { key: TabKey; label: string; icon: React.ElementType }[]
}

export const sections: NavSection[] = [
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

export const ALL_TABS: TabKey[] = sections.flatMap((s) => s.items.map((i) => i.key))

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

