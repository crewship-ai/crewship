/**
 * Settings › Members — the pure half: who is on the roster, what the filters
 * keep, and which role changes a caller may make. No React here, so the rules
 * the page draws its controls from are testable on their own.
 */

import { ALL_CAPABILITIES, CAPABILITY_BUNDLES, Capability, hasCapability, type CapabilityBundle, type CapabilityValue } from "@/lib/capabilities"
import { isManagerTier } from "@/lib/permissions/tiers"

/** One row of `GET /workspaces/{id}/members`. */
export interface Member {
  id: string
  role: string
  created_at: string
  user: {
    id: string
    email: string
    full_name: string | null
    avatar_url: string | null
  }
}

/** Highest first: the order the side panel lists the roles in. */
export const ROLES = ["OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"] as const
/** The roles a person can be MOVED to. OWNER is never granted through this
 *  page (ownership transfer is its own flow), so the segmented control stops
 *  at Admin and an owner's role is shown read-only. */
export const ASSIGNABLE_ROLES = ["VIEWER", "MEMBER", "MANAGER", "ADMIN"] as const

export const ROLE_LABEL: Record<string, string> = {
  OWNER: "Owner", ADMIN: "Admin", MANAGER: "Manager", MEMBER: "Member", VIEWER: "Viewer",
}

// Role rank mirrors the backend `roleRank` (internal/api/helpers.go). Used
// only to decide which members the caller may edit and which roles the
// control may offer; the server re-enforces the full ladder.
export const ROLE_RANK: Record<string, number> = {
  VIEWER: 1, MEMBER: 2, MANAGER: 3, ADMIN: 4, OWNER: 5,
}

export const ROLE_SUMMARIES: { role: string; summary: string }[] = [
  { role: "Owner", summary: "All permissions" },
  { role: "Admin", summary: "All permissions except billing transfer" },
  { role: "Manager", summary: "Crew-level access, create agents, manage credentials" },
  { role: "Member", summary: "Own resource access only" },
  { role: "Viewer", summary: "Read only" },
]

/** What a single role grants, keyed by the wire value. The legend answers
 *  "what are the roles?"; this answers "what does THIS person's role mean?"
 *  next to the control that changes it. */
export const ROLE_SUMMARY_BY_ROLE: Record<string, string> = Object.fromEntries(
  ROLE_SUMMARIES.map((r) => [r.role.toUpperCase(), r.summary]),
)

/**
 * Whether the caller may change `member`'s role, and to what.
 *
 * Role-change is `roleCreate` server-side (MANAGER+) — a plain rank
 * comparison alone would let e.g. a MEMBER "edit" a VIEWER's role, which the
 * server would 403. Gate on the tier first, then the ladder: the caller edits
 * only people they strictly outrank, never themselves, and may grant only
 * roles strictly below their own (grant-below-own, no-modify-superior).
 */
export function roleChoices(callerRole: string | null | undefined, member: Member, isSelf: boolean): string[] {
  const callerRank = callerRole ? ROLE_RANK[callerRole] ?? 0 : 0
  if (!isManagerTier(callerRole) || isSelf || callerRank <= (ROLE_RANK[member.role] ?? 0)) return []
  return ASSIGNABLE_ROLES.filter((r) => (ROLE_RANK[r] ?? 0) < callerRank)
}

/**
 * Capabilities this person holds beyond what their role brings.
 *
 * Role is not a capability (see components/admin/member-capabilities): the
 * stored set is the effective set, and the only grant every role carries is
 * the implied `chat`. So "beyond the role" is the stored set minus chat, in
 * the vocabulary's low → high stakes order.
 */
export function extraCapabilities(granted: readonly string[] | undefined): CapabilityValue[] {
  const set = new Set(granted ?? [])
  return ALL_CAPABILITIES.filter((c) => c !== Capability.Chat && set.has(c))
}

// ── Access profiles ──────────────────────────────────────────────────

/**
 * Two separate questions, two separate answers. The ROLE says what a person
 * manages (people, crews, settings); the capabilities say what they may DO.
 * Once anyone has edited a person's capabilities the two no longer move
 * together, so the page never implies that a role grants them.
 *
 * Nine switches per person is how the roster became unreadable. The server
 * already names three sets (CAPABILITY_BUNDLES, applied with `{"preset"}`), so
 * a person is described by the nearest of them plus what differs: "Power user
 * +Reveal". The "admin" bundle is called Builder here, so it never reads as
 * the Admin role.
 */
export const ACCESS_PROFILES: { key: CapabilityBundle; label: string; summary: string }[] = [
  { key: "chat", label: "Chat only", summary: "Talks to crew agents. Nothing else." },
  { key: "power", label: "Power user", summary: "Creates and runs routines, files issues, writes memory." },
  { key: "admin", label: "Builder", summary: "Power user, plus skills and every credential action — including revealing secrets." },
]
export const PROFILE_LABEL: Record<string, string> = Object.fromEntries(ACCESS_PROFILES.map((p) => [p.key, p.label]))

/** The columns of the access matrix: every capability but chat (every member
 *  has it), in three groups read left to right, low → high stakes. */
export const CAPABILITY_COLUMNS: { key: string; label: string; danger?: boolean; caps: { cap: CapabilityValue; short: string }[] }[] = [
  { key: "routines", label: "Routines", caps: [{ cap: Capability.RoutineCreate, short: "Create" }, { cap: Capability.RoutineRun, short: "Run" }] },
  {
    key: "content",
    label: "Content",
    caps: [
      { cap: Capability.IssueCreate, short: "Issues" },
      { cap: Capability.PageCreate, short: "Pages" },
      { cap: Capability.MemoryWrite, short: "Memory" },
      { cap: Capability.SkillCreate, short: "Skills" },
    ],
  },
  {
    key: "secrets",
    label: "Secrets",
    danger: true,
    caps: [
      { cap: Capability.CredentialCreate, short: "Create" },
      { cap: Capability.CredentialRotate, short: "Rotate" },
      { cap: Capability.CredentialReveal, short: "Reveal" },
    ],
  },
]

/** Short names for a profile's differences: "+Reveal", "−Run". */
export const CAPABILITY_SHORT: Record<string, string> = {
  [Capability.RoutineCreate]: "Create routines",
  [Capability.RoutineRun]: "Run",
  [Capability.IssueCreate]: "Issues",
  [Capability.PageCreate]: "Pages",
  [Capability.MemoryWrite]: "Memory",
  [Capability.SkillCreate]: "Skills",
  [Capability.CredentialCreate]: "Create credentials",
  [Capability.CredentialRotate]: "Rotate",
  [Capability.CredentialReveal]: "Reveal",
}

export interface AccessProfile {
  /** The nearest named set. */
  key: CapabilityBundle
  /** Held, though the profile does not include it. */
  added: CapabilityValue[]
  /** In the profile, though not held. */
  removed: CapabilityValue[]
}

/**
 * The profile that describes `granted` with the fewest differences. A tie
 * goes to the larger profile: "Builder −Skills" says more than "Power user
 * +Create credentials +Rotate".
 */
export function accessProfile(granted: readonly string[] | undefined): AccessProfile {
  const held = new Set(extraCapabilities(granted))
  let best: AccessProfile | null = null
  for (const p of ACCESS_PROFILES) {
    const set = new Set<string>(CAPABILITY_BUNDLES[p.key].filter((c) => c !== Capability.Chat))
    const added = ALL_CAPABILITIES.filter((c) => held.has(c) && !set.has(c))
    const removed = ALL_CAPABILITIES.filter((c) => set.has(c) && !held.has(c))
    const cand = { key: p.key, added, removed }
    if (!best || added.length + removed.length <= best.added.length + best.removed.length) best = cand
  }
  return best as AccessProfile
}

export function exceptionCount(p: AccessProfile): number {
  return p.added.length + p.removed.length
}

/** "Builder", "Power user +Reveal", "Power user −Run +Pages". */
export function profileText(p: AccessProfile): string {
  const diff = [...p.added.map((c) => `+${CAPABILITY_SHORT[c] ?? c}`), ...p.removed.map((c) => `−${CAPABILITY_SHORT[c] ?? c}`)]
  return [PROFILE_LABEL[p.key], ...diff].join(" ")
}

/** How a cell of the matrix reads: held because of the profile, held as an
 *  exception, missing as an exception, or simply not held. */
export type CellState = "profile" | "added" | "removed" | "off"

export function cellState(p: AccessProfile, granted: readonly string[] | undefined, cap: CapabilityValue): CellState {
  if (p.added.includes(cap)) return "added"
  if (p.removed.includes(cap)) return "removed"
  return hasCapability(granted, cap) ? "profile" : "off"
}

// ── Filters ──────────────────────────────────────────────────────────

export type MemberFlag = "" | "reveal" | "exceptions" | "risky-low-role"

export interface MemberFilters {
  /** A role's wire value, or "" for everyone. */
  role: string
  /** A capability, or "" for any. */
  cap: string
  /** An access profile ("chat" | "power" | "admin") held exactly, "custom"
   *  for anyone with exceptions, or "" for any. */
  profile: string
  /** One of the "needs a look" lists. */
  flag: MemberFlag
  q: string
}

export const DEFAULT_MEMBER_FILTERS: MemberFilters = { role: "", cap: "", profile: "", flag: "", q: "" }

export const MEMBER_FLAGS: { key: Exclude<MemberFlag, "">; label: string; hint: string }[] = [
  { key: "reveal", label: "Can reveal secrets", hint: "Reads stored secrets in plaintext" },
  { key: "exceptions", label: "Has exceptions", hint: "Differs from every access profile" },
  { key: "risky-low-role", label: "High-stakes below Manager", hint: "A Member or Viewer holding skills or credentials" },
]

const HIGH_STAKES: ReadonlySet<string> = new Set([
  Capability.SkillCreate, Capability.CredentialCreate, Capability.CredentialRotate, Capability.CredentialReveal,
])

function flagMatches(m: Member, flag: MemberFlag, granted: readonly string[] | undefined): boolean {
  if (!flag) return true
  if (flag === "reveal") return hasCapability(granted, Capability.CredentialReveal)
  if (flag === "exceptions") return m.role !== "OWNER" && exceptionCount(accessProfile(granted)) > 0
  return (m.role === "MEMBER" || m.role === "VIEWER") && extraCapabilities(granted).some((c) => HIGH_STAKES.has(c))
}

function profileMatches(m: Member, profile: string, granted: readonly string[] | undefined): boolean {
  if (!profile) return true
  if (m.role === "OWNER") return false
  const p = accessProfile(granted)
  if (profile === "custom") return exceptionCount(p) > 0
  return p.key === profile && exceptionCount(p) === 0
}

/** Name or email, case-insensitive. */
export function matchesSearch(m: Member, q: string): boolean {
  const needle = q.trim().toLowerCase()
  if (!needle) return true
  return `${m.user.full_name ?? ""} ${m.user.email}`.toLowerCase().includes(needle)
}

/**
 * The filters combine: role AND capability AND search. `caps` is undefined
 * when the caller cannot read capabilities (below ADMIN) — the capability
 * filter is never offered then, and a stale one from a shared URL is ignored
 * rather than emptying the list.
 */
export function memberMatches(m: Member, f: MemberFilters, caps: Record<string, string[]> | undefined): boolean {
  if (f.role && m.role !== f.role) return false
  if (f.cap && caps && !hasCapability(caps[m.user.id], f.cap as CapabilityValue)) return false
  if (caps && !profileMatches(m, f.profile, caps[m.user.id])) return false
  if (caps && !flagMatches(m, f.flag, caps[m.user.id])) return false
  return matchesSearch(m, f.q)
}

export function activeFilterCount(f: MemberFilters, capsReadable: boolean): number {
  return Number(Boolean(f.role)) + Number(Boolean(f.q.trim())) +
    (capsReadable ? Number(Boolean(f.cap)) + Number(Boolean(f.profile)) + Number(Boolean(f.flag)) : 0)
}

/** Highest role first, then by name — the order an admin scans a roster in. */
export function byRoleThenName(a: Member, b: Member): number {
  const r = (ROLE_RANK[b.role] ?? 0) - (ROLE_RANK[a.role] ?? 0)
  if (r) return r
  const an = (a.user.full_name ?? "").trim() || a.user.email
  const bn = (b.user.full_name ?? "").trim() || b.user.email
  return an.localeCompare(bn)
}

// ── URL ──────────────────────────────────────────────────────────────

export interface MembersUrlState {
  member: string
  filters: MemberFilters
}

/**
 * ?member= (a USER id) and the filters (?role= ?cap= ?profile= ?flag= ?q=) keep the
 * page shareable and reload-safe. Defaults stay out of the URL.
 *
 * `member` names the person, not the membership row: a link is about a
 * person, and the membership id changes if they are removed and re-added.
 * The ⌘K palette builds exactly this link.
 */
export function parseMembersUrl(search: string): MembersUrlState {
  const p = new URLSearchParams(search)
  const role = p.get("role") ?? ""
  const profile = p.get("profile") ?? ""
  const flag = p.get("flag") ?? ""
  return {
    member: p.get("member") ?? "",
    filters: {
      role: (ROLES as readonly string[]).includes(role) ? role : "",
      cap: (ALL_CAPABILITIES as readonly string[]).includes(p.get("cap") ?? "") ? (p.get("cap") as string) : "",
      profile: ["chat", "power", "admin", "custom"].includes(profile) ? profile : "",
      flag: MEMBER_FLAGS.some((f) => f.key === flag) ? (flag as MemberFlag) : "",
      q: p.get("q") ?? "",
    },
  }
}

/** The query string for `state`, without the leading "?" ("" for defaults). */
export function membersUrlQuery({ member, filters }: MembersUrlState): string {
  const p = new URLSearchParams()
  if (member) p.set("member", member)
  if (filters.role) p.set("role", filters.role)
  if (filters.cap) p.set("cap", filters.cap)
  if (filters.profile) p.set("profile", filters.profile)
  if (filters.flag) p.set("flag", filters.flag)
  if (filters.q) p.set("q", filters.q)
  return p.toString()
}

export function relativeTime(dateStr: string, now = Date.now()): string {
  const diffSec = Math.floor((now - new Date(dateStr).getTime()) / 1000)
  if (diffSec < 60) return "just now"
  const diffMin = Math.floor(diffSec / 60)
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHr = Math.floor(diffMin / 60)
  if (diffHr < 24) return `${diffHr}h ago`
  const diffDay = Math.floor(diffHr / 24)
  if (diffDay < 30) return `${diffDay}d ago`
  const diffMon = Math.floor(diffDay / 30)
  if (diffMon < 12) return `${diffMon}mo ago`
  return `${Math.floor(diffMon / 12)}y ago`
}
