import { LayoutDashboard, Users, Server, Shield, Database, History, Bell, Gauge } from "lucide-react"
import type { TabKey } from "./types"

/** The six pages nested under Data › Backups (`?tab=backups&section=`). */
export type BackupsSection = "overview" | "history" | "schedules" | "storage" | "recovery" | "keys"

export const BACKUP_SECTIONS: { key: BackupsSection; label: string }[] = [
  { key: "overview", label: "Overview" },
  { key: "history", label: "Backup history" },
  { key: "schedules", label: "Schedules" },
  { key: "storage", label: "Storage" },
  { key: "recovery", label: "Recovery" },
  { key: "keys", label: "Keys & alerts" },
]

export interface NavChild { key: BackupsSection; label: string }

export interface NavItem {
  key: TabKey | "people" | "security-page"
  label: string
  icon: React.ElementType
  /** A nested page of its own (DrillPage), opened with a chevron like
   *  Settings › Crew links; the rest are sections here. */
  href?: string
  /** Pages indented under this item in the same sidebar (Backups). */
  children?: NavChild[]
}

interface NavSection {
  label: string
  items: NavItem[]
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
      { key: "security-page", label: "Security", icon: Shield, href: "/admin/security" },
    ],
  },
  {
    label: "Data",
    items: [
      { key: "backups", label: "Backups", icon: Database, children: BACKUP_SECTIONS },
      { key: "retention", label: "Data retention", icon: History },
    ],
  },
]

export const ALL_TABS: TabKey[] = sections.flatMap((s) => s.items.filter((i) => !i.href).map((i) => i.key as TabKey))

/** Tabs that moved to a nested page, and where they went. */
const MOVED: Record<string, string> = {
  users: "/admin/people",
  workspaces: "/admin/people?view=workspaces",
  posture: "/admin/security",
  security: "/admin/security",
  reviews: "/admin/security?section=activity",
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

export function isBackupsSection(v: string | null | undefined): v is BackupsSection {
  return BACKUP_SECTIONS.some((s) => s.key === v)
}

/**
 * The Backups page from `?section=`. An old `?tab=backups` link (no section)
 * or an unknown one lands on Overview.
 */
export function initialBackupsSection(search: string): BackupsSection {
  const s = new URLSearchParams(search).get("section")
  return isBackupsSection(s) ? s : "overview"
}

/** What the sub-bar names after "Admin Console": "Backups › Schedules". */
export function adminSectionLabel(tab: TabKey, section: BackupsSection): string | undefined {
  if (tab === "backups") return `Backups › ${BACKUP_SECTIONS.find((s) => s.key === section)?.label ?? "Overview"}`
  return sections.flatMap((s) => s.items).find((i) => !i.href && i.key === tab)?.label
}

/**
 * The sidebar filtered by the search box. A child that matches keeps its
 * parent (so it stays indented under Backups); a parent that matches keeps all
 * of its children.
 */
export function filterNav(q: string): NavSection[] {
  const needle = q.trim().toLowerCase()
  if (!needle) return sections
  return sections
    .map((s) => ({
      ...s,
      items: s.items.flatMap((i) => {
        const self = i.label.toLowerCase().includes(needle)
        if (!i.children) return self ? [i] : []
        if (self) return [i]
        const kids = i.children.filter((c) => c.label.toLowerCase().includes(needle))
        return kids.length ? [{ ...i, children: kids }] : []
      }),
    }))
    .filter((s) => s.items.length > 0)
}
