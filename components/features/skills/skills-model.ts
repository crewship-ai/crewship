import {
  Box,
  Cloud,
  Code2,
  Database,
  DollarSign,
  HandCoins,
  LifeBuoy,
  ListChecks,
  Microscope,
  Palette,
  PenLine,
  Settings,
  Shield,
  Workflow,
  type LucideIcon,
} from "lucide-react"
import type { StatusTone } from "@/lib/format-status"

// The Skills page's data model and every rule that decides what a row says:
// trust, the View buckets, the explorer's counts, the filter and the sort.
// Pure so the page, its cards and the tests read one definition (#3033).

/** An agent of the caller's workspace that holds the skill (#3032). */
export interface SkillAgentRef {
  agent_id: string
  agent_slug: string
  agent_name: string
  avatar_seed: string | null
  avatar_style: string | null
  avatar_url?: string | null
  crew_id: string | null
  crew_slug: string | null
  crew_name: string | null
  crew_color: string | null
  crew_icon: string | null
  crew_avatar_style: string | null
  /** The skill's needs_credentials this agent is not delivered at run start. */
  missing_credentials?: string[]
}

/** This workspace's invocations of the skill. */
export interface SkillUsage {
  uses_7d: number
  errors_7d: number
  uses_total: number
  last_used_at: string | null
}

export interface SkillRow {
  id: string
  name: string
  slug: string
  display_name: string | null
  description: string | null
  version: string | null
  author: string | null
  category: string
  source: string
  icon: string | null
  vendor?: string | null
  homepage?: string | null
  spdx_license?: string | null
  maturity?: string | null
  runtime?: string | null
  verification?: string | null
  scan_status?: string | null
  description_quality?: string | null
  lifecycle_state?: string | null
  needs_credentials?: string[]
  usage?: SkillUsage
  installed_on?: SkillAgentRef[]
  created_at?: string
  updated_at?: string
}

export interface SkillDetail extends SkillRow {
  content: string | null
  license: string | null
  agent_count: number
}

/** A workspace agent as the explorer and the Agents tab list it. */
export interface SkillsAgent {
  id: string
  slug: string
  name: string
  crew_id: string | null
  role_title?: string | null
  agent_role?: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
}

export interface SkillsCrew {
  id: string
  slug: string
  name: string
  color: string | null
  icon: string | null
}

/** A skill an agent or the memory consolidator proposed, staged per crew. */
export interface ProposedSkill {
  crew_id: string
  file_name: string
  name: string
  description: string
  description_quality: string
  category: string
}

// ── vocabulary ────────────────────────────────────────────────────────────

export interface DomainMeta {
  label: string
  icon: LucideIcon
  /** Identity colour of the domain: names it, never carries status. */
  color: string
}

const DOMAIN_META: Record<string, DomainMeta> = {
  CODING: { label: "Coding", icon: Code2, color: "#5b8def" },
  DATA: { label: "Data", icon: Database, color: "#22d3ee" },
  DEVOPS: { label: "DevOps", icon: Cloud, color: "#34d399" },
  WRITING: { label: "Writing", icon: PenLine, color: "#f59e0b" },
  RESEARCH: { label: "Research", icon: Microscope, color: "#22d3ee" },
  PM: { label: "Project management", icon: ListChecks, color: "#84cc16" },
  DESIGN: { label: "Design", icon: Palette, color: "#d946ef" },
  SUPPORT: { label: "Support", icon: LifeBuoy, color: "#f43f5e" },
  SECURITY: { label: "Security", icon: Shield, color: "#f43f5e" },
  FINANCE: { label: "Finance", icon: DollarSign, color: "#34d399" },
  OPS: { label: "Operations", icon: Settings, color: "#8b5cf6" },
  AUTOMATION: { label: "Automation", icon: Workflow, color: "#8b5cf6" },
  SALES: { label: "Sales", icon: HandCoins, color: "#f59e0b" },
  CUSTOM: { label: "Custom", icon: Box, color: "#84cc16" },
}

export function domainMeta(category: string | null | undefined): DomainMeta {
  return DOMAIN_META[category ?? ""] ?? DOMAIN_META.CUSTOM
}

/** Domains in display order, for the explorer. */
export const DOMAIN_ORDER = Object.keys(DOMAIN_META)

const SOURCE_LABEL: Record<string, string> = {
  BUNDLED: "Built-in",
  CUSTOM: "Imported",
  GENERATED: "Generated",
  MARKETPLACE: "Marketplace",
  MANAGED: "Managed",
}

export function sourceLabel(source: string | null | undefined): string {
  return SOURCE_LABEL[source ?? ""] ?? "Imported"
}

export const MATURITY_LABEL: Record<string, string> = {
  OFFICIAL: "Official",
  CURATED: "Curated",
  COMMUNITY: "Community",
  EXPERIMENTAL: "Experimental",
}

export type TrustLevel = "flagged" | "unscanned" | "verified" | "unverified"

export const TRUST_LABEL: Record<TrustLevel, string> = {
  flagged: "Flagged",
  unscanned: "Not scanned",
  verified: "Verified",
  unverified: "Unverified",
}

const TRUST_TONE: Record<TrustLevel, StatusTone> = {
  flagged: "danger",
  unscanned: "warn",
  verified: "success",
  unverified: "muted",
}

/**
 * The one trust word a skill shows. The import scan outranks the curator:
 * a flagged skill is flagged whatever its verification says, and a skill the
 * scan never read cannot claim to be clean.
 */
export function skillTrust(s: Pick<SkillRow, "scan_status" | "verification">): {
  level: TrustLevel
  label: string
  tone: StatusTone
} {
  const scan = (s.scan_status ?? "").toUpperCase()
  let level: TrustLevel
  if (scan === "FLAGGED" || scan === "BLOCKED") level = "flagged"
  else if (scan !== "CLEAN") level = "unscanned"
  else if ((s.verification ?? "").toUpperCase() === "VERIFIED") level = "verified"
  else level = "unverified"
  return { level, label: TRUST_LABEL[level], tone: TRUST_TONE[level] }
}

export function skillName(s: Pick<SkillRow, "display_name" | "name" | "slug">): string {
  return s.display_name || s.name || s.slug
}

/** Agents holding the skill without a credential it needs. */
export function agentsMissingCredentials(s: SkillRow): SkillAgentRef[] {
  return (s.installed_on ?? []).filter((a) => (a.missing_credentials?.length ?? 0) > 0)
}

// ── views, filters and counts ─────────────────────────────────────────────

export type SkillsView = "all" | "assigned" | "unassigned" | "attention" | "proposed"

export const SKILL_VIEWS: SkillsView[] = ["all", "assigned", "unassigned", "attention", "proposed"]

export function isSkillsView(v: string | null | undefined): v is SkillsView {
  return !!v && (SKILL_VIEWS as string[]).includes(v)
}

/** Does a skill belong in a View bucket? `proposed` holds no catalog rows. */
export function inView(s: SkillRow, view: SkillsView): boolean {
  const held = (s.installed_on?.length ?? 0) > 0
  switch (view) {
    case "all":
      return true
    case "assigned":
      return held
    case "unassigned":
      return !held
    case "attention":
      // On an agent and something is wrong with it there: the scan did not
      // pass, or an agent lacks a credential it needs. An unscanned skill
      // nobody holds harms nobody yet.
      return held && (skillTrust(s).level === "flagged" || skillTrust(s).level === "unscanned" || agentsMissingCredentials(s).length > 0)
    case "proposed":
      return false
  }
}

export interface SkillFilters {
  view: SkillsView
  agentId: string | null
  crewId: string | null
  domain: string | null
  query: string
  sources: string[]
  trust: TrustLevel[]
  maturities: string[]
  needsCredential: boolean
}

export const EMPTY_SKILL_FILTERS: SkillFilters = {
  view: "all",
  agentId: null,
  crewId: null,
  domain: null,
  query: "",
  sources: [],
  trust: [],
  maturities: [],
  needsCredential: false,
}

/** Facets behind the Filter button (the explorer's sections are not counted). */
export function popoverFilterCount(f: SkillFilters): number {
  return f.sources.length + f.trust.length + f.maturities.length + (f.needsCredential ? 1 : 0)
}

export type FilterAxis = "view" | "holder" | "domain"

/**
 * Does a skill pass the filters? `ignore` leaves one explorer axis out, so a
 * section's counts say what picking each of its rows would show rather than
 * dropping to zero the moment one row is picked.
 */
export function matchesSkill(s: SkillRow, f: SkillFilters, ignore?: FilterAxis): boolean {
  if (ignore !== "view" && !inView(s, f.view)) return false
  if (ignore !== "holder") {
    const holders = s.installed_on ?? []
    if (f.agentId && !holders.some((a) => a.agent_id === f.agentId)) return false
    if (!f.agentId && f.crewId && !holders.some((a) => a.crew_id === f.crewId)) return false
  }
  if (ignore !== "domain" && f.domain && s.category !== f.domain) return false
  if (f.sources.length && !f.sources.includes(s.source)) return false
  if (f.trust.length && !f.trust.includes(skillTrust(s).level)) return false
  if (f.maturities.length && !f.maturities.includes((s.maturity ?? "COMMUNITY").toUpperCase())) return false
  if (f.needsCredential && !(s.needs_credentials?.length ?? 0)) return false
  const q = f.query.trim().toLowerCase()
  if (q) {
    const hay = [
      skillName(s),
      s.slug,
      s.description ?? "",
      s.vendor ?? "",
      ...(s.installed_on ?? []).map((a) => a.agent_name),
    ]
      .join(" ")
      .toLowerCase()
    if (!q.split(/\s+/).every((term) => hay.includes(term))) return false
  }
  return true
}

export type SkillSort = "used" | "agents" | "name"

export function isSkillSort(v: string | null | undefined): v is SkillSort {
  return v === "used" || v === "agents" || v === "name"
}

export function sortSkills(rows: SkillRow[], sort: SkillSort): SkillRow[] {
  const byName = (a: SkillRow, b: SkillRow) => skillName(a).localeCompare(skillName(b))
  const agents = (s: SkillRow) => s.installed_on?.length ?? 0
  const out = [...rows]
  if (sort === "name") return out.sort(byName)
  if (sort === "agents") return out.sort((a, b) => agents(b) - agents(a) || byName(a, b))
  return out.sort(
    (a, b) =>
      (b.usage?.uses_7d ?? 0) - (a.usage?.uses_7d ?? 0) ||
      (b.usage?.uses_total ?? 0) - (a.usage?.uses_total ?? 0) ||
      agents(b) - agents(a) ||
      byName(a, b),
  )
}

/** Explorer counts, each computed with its own axis left out. */
export function explorerCounts(rows: SkillRow[], f: SkillFilters) {
  const views: Record<SkillsView, number> = { all: 0, assigned: 0, unassigned: 0, attention: 0, proposed: 0 }
  const byAgent = new Map<string, number>()
  const byCrew = new Map<string, number>()
  const byDomain = new Map<string, number>()
  for (const s of rows) {
    if (matchesSkill(s, f, "view")) {
      for (const v of SKILL_VIEWS) if (inView(s, v)) views[v]++
    }
    if (matchesSkill(s, f, "holder")) {
      const crews = new Set<string>()
      for (const a of s.installed_on ?? []) {
        byAgent.set(a.agent_id, (byAgent.get(a.agent_id) ?? 0) + 1)
        if (a.crew_id) crews.add(a.crew_id)
      }
      for (const c of crews) byCrew.set(c, (byCrew.get(c) ?? 0) + 1)
    }
    if (matchesSkill(s, f, "domain")) byDomain.set(s.category, (byDomain.get(s.category) ?? 0) + 1)
  }
  return { views, byAgent, byCrew, byDomain }
}

/** Workspace totals for the usage tiles. */
export function usageTotals(rows: SkillRow[]) {
  let held = 0
  let uses = 0
  let errors = 0
  for (const s of rows) {
    if ((s.installed_on?.length ?? 0) > 0) held++
    uses += s.usage?.uses_7d ?? 0
    errors += s.usage?.errors_7d ?? 0
  }
  return { total: rows.length, held, idle: rows.length - held, uses, errors }
}
