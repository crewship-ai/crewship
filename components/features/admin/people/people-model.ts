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

/** Guest (restricted) in any workspace. Such an account is never made ADMIN or
 *  OWNER anywhere — SetMembership refuses it with 409
 *  (administratorAccountEligible) — so the role controls leave those out. */
export function isGuest(p: Pick<Person, "memberships"> | null | undefined): boolean {
  return !!p?.memberships?.some((m) => m.access_mode === "restricted")
}

/** Roles an administrator may give: everything, or below Admin for a Guest. */
export function rolesFor(guest: boolean | undefined): readonly Role[] {
  return guest ? ROLES.filter((r) => r !== "OWNER" && r !== "ADMIN") : ROLES
}

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

/**
 * The panel's views. "people" is Everyone; "accounts" is the personal
 * accounts. Before this redesign a view was one of people / accounts /
 * workspaces / access and the people groups were a `facet`; both old URLs
 * still open (see normalizeUrl in people-url.ts).
 */
export type View = "people" | "hand" | "admins" | "suspended" | "accounts" | "workspaces" | "access"

/** What narrows a list of people: a status, or having no workspace. */
export type Facet = "all" | "active" | "setup" | "locked" | "suspended" | "noaccess"

export const STATUS_FACETS: { key: Exclude<Facet, "all">; label: string }[] = [
  { key: "active", label: "Active" },
  { key: "setup", label: "Setup pending" },
  { key: "locked", label: "Locked" },
  { key: "suspended", label: "Suspended" },
  { key: "noaccess", label: "No workspace" },
]

// ── Workspaces and personal accounts ──────────────────────────────────────

/**
 * A personal workspace is the empty "<Name>'s Workspace" a sign-up without an
 * invitation gets (the server's `kind`). An older server sends no kind: then
 * the pattern decides — one member, named "…'s Workspace".
 */
export function isPersonal(w: Pick<Workspace, "kind" | "name" | "_count_members">): boolean {
  if (w.kind) return w.kind === "personal"
  return w._count_members <= 1 && /'s Workspace$/.test(w.name)
}

/** Nothing in it yet: no crews, no agents, no runs this week. */
export function isEmptyWorkspace(w: Workspace): boolean {
  return (w._count_crews ?? 0) === 0 && (w._count_agents ?? 0) === 0 && (w.runs_7d ?? 0) === 0
}

/** Personal workspaces are named after their owner; the person is the label. */
export function personalOwnerName(w: Workspace, people: Person[]): string {
  const owner = people.find((p) => p.id === w.personal_owner_id)
    ?? people.find((p) => p.memberships.some((m) => m.workspace_id === w.id && m.role === "OWNER"))
  return owner ? displayName(owner) : w.name.replace(/'s Workspace$/, "")
}

export function facetMatches(p: Person, facet: Facet, now = Date.now()): boolean {
  switch (facet) {
    case "all":
      return true
    case "noaccess":
      return p.memberships.length === 0
    default:
      return personStatus(p, now) === facet
  }
}

export function personMatches(p: Person, q: string): boolean {
  const needle = q.trim().toLowerCase()
  if (!needle) return true
  return `${p.full_name ?? ""} ${p.email} ${p.memberships.map((m) => m.name).join(" ")}`.toLowerCase().includes(needle)
}

export function workspaceMatches(w: Workspace, q: string, people: Person[] = []): boolean {
  const needle = q.trim().toLowerCase()
  if (!needle) return true
  // A personal workspace is found by its owner too: "ivana" finds Ivana's.
  const owners = people.filter((p) => p.memberships.some((m) => m.workspace_id === w.id && m.role === "OWNER"))
  return `${w.name} ${w.slug} ${owners.map((p) => `${displayName(p)} ${p.email}`).join(" ")}`.toLowerCase().includes(needle)
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

// ── Needs a hand ──────────────────────────────────────────────────────────

/**
 * Who cannot get in, and who gets in to nothing, most pressing first. The
 * groups do not overlap (a locked person with no workspace is "locked"), so
 * their counts add up to the number the banner and the panel show.
 */
export interface HandGroups {
  locked: Person[]
  setup: Person[]
  nowhere: Person[]
  /** Personal accounts with no crews, agents or runs. */
  emptyAccounts: Workspace[]
}

export function handGroups(people: Person[], workspaces: Workspace[], now = Date.now()): HandGroups {
  const locked: Person[] = []
  const setup: Person[] = []
  const nowhere: Person[] = []
  for (const p of people) {
    const s = personStatus(p, now)
    if (s === "locked") locked.push(p)
    else if (s === "setup") setup.push(p)
    else if (s === "active" && p.memberships.length === 0) nowhere.push(p)
  }
  return { locked, setup, nowhere, emptyAccounts: workspaces.filter((w) => isPersonal(w) && isEmptyWorkspace(w)) }
}

/** The people who need a hand (not the accounts): the banner's number. */
export const handPeopleCount = (g: HandGroups) => g.locked.length + g.setup.length + g.nowhere.length

export type PeopleOrder = "hand" | "az" | "recent"

const needRank = (p: Person, now: number) => {
  const s = personStatus(p, now)
  return s === "locked" ? 0 : s === "setup" ? 1 : s === "active" && p.memberships.length === 0 ? 2 : 3
}

const timeOf = (iso: string | null | undefined) => {
  const t = parse(iso)
  return Number.isNaN(t) ? 0 : t
}

/** A new array: the order the toolbar asks for. */
export function sortPeople(list: readonly Person[], order: PeopleOrder, now = Date.now()): Person[] {
  const byName = (a: Person, b: Person) => displayName(a).localeCompare(displayName(b))
  const out = [...list]
  if (order === "az") return out.sort(byName)
  if (order === "recent") return out.sort((a, b) => timeOf(b.last_active_at) - timeOf(a.last_active_at) || byName(a, b))
  return out.sort((a, b) => needRank(a, now) - needRank(b, now) || byName(a, b))
}

export type WorkspaceOrder = "busy" | "az" | "new"

export function sortWorkspaces(list: readonly Workspace[], order: WorkspaceOrder): Workspace[] {
  const byName = (a: Workspace, b: Workspace) => a.name.localeCompare(b.name)
  const out = [...list]
  if (order === "az") return out.sort(byName)
  if (order === "new") return out.sort((a, b) => timeOf(b.created_at) - timeOf(a.created_at) || byName(a, b))
  return out.sort((a, b) => (b.runs_7d ?? 0) - (a.runs_7d ?? 0) || byName(a, b))
}

/** The panel's "Busiest this week": team workspaces by runs in 7 days. */
export function busiestWorkspaces(workspaces: readonly Workspace[], n = 6): Workspace[] {
  return sortWorkspaces(workspaces.filter((w) => !isPersonal(w)), "busy").slice(0, n)
}

// ── Export ────────────────────────────────────────────────────────────────

const csvCell = (v: unknown) => {
  const s = v == null ? "" : String(v)
  // A cell that starts like a formula is neutralised for spreadsheet apps.
  const safe = /^[=+\-@]/.test(s) ? `'${s}` : s
  return /[",\n]/.test(safe) ? `"${safe.replace(/"/g, '""')}"` : safe
}
const csv = (rows: unknown[][]) => rows.map((r) => r.map(csvCell).join(",")).join("\n") + "\n"

/** The rows shown, one line each. */
export function peopleCsv(people: readonly Person[], now = Date.now()): string {
  return csv([
    ["Name", "Email", "Workspaces", "Last active", "Status", "Instance admin", "Sessions"],
    ...people.map((p) => [
      p.full_name ?? "", p.email, p.memberships.map((m) => `${m.name} (${m.role.toLowerCase()})`).join("; "),
      p.last_active_at ?? "", STATUS_LABEL[personStatus(p, now)], p.instance_admin ? "yes" : "no", p.active_sessions ?? 0,
    ]),
  ])
}

export function workspacesCsv(workspaces: readonly Workspace[], people: readonly Person[]): string {
  return csv([
    ["Name", "Address", "Owner", "People", "Crews", "Agents", "Runs (7 days)", "Last activity", "Created"],
    ...workspaces.map((w) => {
      const owner = people.find((p) => roleIn(p, w.id) === "OWNER")
      return [w.name, w.slug, owner ? owner.email : "", w._count_members, w._count_crews, w._count_agents, w.runs_7d ?? 0, w.last_activity_at ?? "", w.created_at]
    }),
  ])
}
