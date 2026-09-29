import { LayoutDashboard, Users, Server, Shield, ShieldCheck, Database, History, ListTodo, Bell, Gauge } from "lucide-react"
import type { TabKey } from "./types"

interface NavSection {
  label: string
  /** An item with an href is a nested page of its own (DrillPage), opened
   *  with a chevron like Settings › Crew links; the rest are sections here. */
  items: { key: TabKey | "people"; label: string; icon: React.ElementType; href?: string }[]
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
      { key: "people", label: "People & workspaces", icon: Users, href: "/admin/people" },
    ],
  },
  {
    label: "Infrastructure",
    items: [
      { key: "providers", label: "Runtime", icon: Server },
      { key: "notifications", label: "Notifications", icon: Bell },
      { key: "ratelimits", label: "Limits", icon: Gauge },
    ],
  },
  {
    label: "Security",
    items: [
      { key: "posture", label: "Posture", icon: ShieldCheck },
      { key: "security", label: "Keeper", icon: Shield },
      { key: "reviews", label: "Keeper reviews", icon: ListTodo },
    ],
  },
  {
    label: "Data",
    items: [
      { key: "backups", label: "Backups", icon: Database },
      { key: "retention", label: "Retention", icon: History },
    ],
  },
]

export const ALL_TABS: TabKey[] = sections.flatMap((s) => s.items.filter((i) => !i.href).map((i) => i.key as TabKey))

/** Tabs that moved to a nested page, and where they went. */
const MOVED: Record<string, string> = {
  users: "/admin/people",
  workspaces: "/admin/people?view=workspaces",
}

/** Where an old ?tab= link now lives, or null when it is still a section here. */
export function movedAdminTabHref(search: string): string | null {
  const t = new URLSearchParams(search).get("tab")
  return t ? MOVED[t] ?? null : null
}

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

