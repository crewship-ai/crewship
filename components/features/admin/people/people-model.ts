import type { AdminMembership, AdminOrg, AdminUser } from "@/app/(dashboard)/admin/types"

/**
 * Admin › People & workspaces: the model behind the page. Pure functions over
 * the two admin lists, so the rules the page shows (who needs attention, who
 * is the last owner, what a facet counts) are tested once, here.
 */

export type Person = AdminUser & {
  memberships: AdminMembership[]
}
export type Workspace = AdminOrg

export const ROLES = ["OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"] as const
export type Role = (typeof ROLES)[number]

/** One state per person, the most pressing first. */
export type PersonStatus = "suspended" | "locked" | "setup" | "active"

const parse = (iso: string | null | undefined) =>
  iso ? Date.parse(iso.includes("T") ? iso : iso.replace(" ", "T") + "Z") : NaN

export function personStatus(p: AdminUser, now = Date.now()): PersonStatus {
  if (p.suspended_at) return "suspended"
  if (p.locked_until && parse(p.locked_until) > now) return "locked"
  if (p.setup_link_expires_at && parse(p.setup_link_expires_at) > now) return "setup"
  return "active"
}

export const STATUS_LABEL: Record<PersonStatus, string> = {
  suspended: "Suspended",
  locked: "Locked",
  setup: "Setup pending",
  active: "Active",
}

export type Facet = "all" | "attention" | "setup" | "locked" | "suspended" | "noaccess" | "admins"

export const FACETS: { key: Facet; label: string; dot?: string }[] = [
  { key: "all", label: "All people" },
  { key: "attention", label: "Needs attention", dot: "bg-warn" },
  { key: "setup", label: "Setup pending", dot: "bg-primary" },
  { key: "locked", label: "Locked", dot: "bg-destructive" },
  { key: "suspended", label: "Suspended", dot: "bg-muted-foreground" },
  { key: "noaccess", label: "No workspace", dot: "bg-border" },
  { key: "admins", label: "Instance admins", dot: "bg-[var(--purple)]" },
]

export function facetMatches(p: Person, facet: Facet, now = Date.now()): boolean {
  const s = personStatus(p, now)
  switch (facet) {
    case "attention":
      return s !== "active"
    case "setup":
    case "locked":
    case "suspended":
      return s === facet
    case "noaccess":
      return p.memberships.length === 0
    case "admins":
      return !!p.instance_admin
    default:
      return true
  }
}

export function personMatches(p: Person, q: string): boolean {
  const needle = q.trim().toLowerCase()
  if (!needle) return true
  return `${p.full_name ?? ""} ${p.email} ${p.memberships.map((m) => m.name).join(" ")}`.toLowerCase().includes(needle)
}

export function workspaceMatches(w: Workspace, q: string): boolean {
  const needle = q.trim().toLowerCase()
  return !needle || `${w.name} ${w.slug}`.toLowerCase().includes(needle)
}

export const displayName = (p: Pick<AdminUser, "full_name" | "email">) => p.full_name?.trim() || p.email

export function roleIn(p: Person, workspaceId: string): Role | null {
  return (p.memberships.find((m) => m.workspace_id === workspaceId)?.role as Role | undefined) ?? null
}

export function membersOf(people: Person[], workspaceId: string): Person[] {
  return people.filter((p) => roleIn(p, workspaceId) !== null)
}

export function ownersOf(people: Person[], workspaceId: string): Person[] {
  return people.filter((p) => roleIn(p, workspaceId) === "OWNER")
}

/** The one owner a workspace has left: they cannot be moved down or out. */
export function isLastOwner(people: Person[], workspaceId: string, userId: string): boolean {
  const owners = ownersOf(people, workspaceId)
  return owners.length === 1 && owners[0].id === userId
}

/** Days until an ISO time, rounded up; 0 when past. */
export function daysUntil(iso: string | null | undefined, now = Date.now()): number {
  const t = parse(iso)
  return Number.isNaN(t) ? 0 : Math.max(0, Math.ceil((t - now) / 86_400_000))
}

/** How a person became an instance admin, in words. */
export function adminSourceLabel(source: string | null | undefined): string {
  switch (source) {
    case "env":
      return "Instance owner (CREWSHIP_OWNER_EMAIL)"
    case "role":
      return "Named instance admin"
    default:
      return ""
  }
}

/** Sort people: those needing attention first, then by name. */
export function byAttentionThenName(a: Person, b: Person, now = Date.now()): number {
  const rank = (p: Person) => (personStatus(p, now) === "active" ? 1 : 0)
  return rank(a) - rank(b) || displayName(a).localeCompare(displayName(b))
}
