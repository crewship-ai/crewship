/**
 * Settings › Members — the pure half: who is on the roster, what the filters
 * keep, and which role changes a caller may make. No React here, so the rules
 * the page draws its controls from are testable on their own.
 */

import { ALL_CAPABILITIES, Capability, hasCapability, type CapabilityValue } from "@/lib/capabilities"
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

/** A cell of the matrix, or a switch: on because every role has it, on
 *  because it was granted to this person, or off. */
export type CapabilityState = "role" | "granted" | "off"

export function capabilityState(granted: readonly string[] | undefined, cap: CapabilityValue): CapabilityState {
  if (cap === Capability.Chat) return "role"
  return hasCapability(granted, cap) ? "granted" : "off"
}

// ── Filters ──────────────────────────────────────────────────────────

export type MembersView = "people" | "matrix"

export interface MemberFilters {
  /** A role's wire value, or "" for everyone. */
  role: string
  /** A capability, or "" for any. */
  cap: string
  q: string
}

export const DEFAULT_MEMBER_FILTERS: MemberFilters = { role: "", cap: "", q: "" }

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
  return matchesSearch(m, f.q)
}

export function activeFilterCount(f: MemberFilters, capsReadable: boolean): number {
  return Number(Boolean(f.role)) + Number(capsReadable && Boolean(f.cap)) + Number(Boolean(f.q.trim()))
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
  view: MembersView
  member: string
  filters: MemberFilters
}

/**
 * ?view=, ?member= (a USER id) and the filters (?role= ?cap= ?q=) keep the
 * page shareable and reload-safe. Defaults stay out of the URL.
 *
 * `member` names the person, not the membership row: a link is about a
 * person, and the membership id changes if they are removed and re-added.
 * The ⌘K palette builds exactly this link.
 */
export function parseMembersUrl(search: string): MembersUrlState {
  const p = new URLSearchParams(search)
  const role = p.get("role") ?? ""
  return {
    view: p.get("view") === "matrix" ? "matrix" : "people",
    member: p.get("member") ?? "",
    filters: {
      role: (ROLES as readonly string[]).includes(role) ? role : "",
      cap: (ALL_CAPABILITIES as readonly string[]).includes(p.get("cap") ?? "") ? (p.get("cap") as string) : "",
      q: p.get("q") ?? "",
    },
  }
}

/** The query string for `state`, without the leading "?" ("" for defaults). */
export function membersUrlQuery({ view, member, filters }: MembersUrlState): string {
  const p = new URLSearchParams()
  if (view !== "people") p.set("view", view)
  if (member) p.set("member", member)
  if (filters.role) p.set("role", filters.role)
  if (filters.cap) p.set("cap", filters.cap)
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
