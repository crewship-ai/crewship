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
import { CREW_ICONS } from "@/lib/crew-icons"
import type { StatusTone } from "@/lib/format-status"

// The Skills page's data model and every rule that decides what a row says:
// trust, where a skill comes from, the views, the panel's facets and counts,
// sorting and grouping. Pure, so the page, the panel and the tests read one
// definition (#3033, Harbor 2 #3396).

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
  /** JSON array of strings, as the skills row stores it. */
  tags?: string | null
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

/** A workspace agent as the panel and the Used by tab list it. */
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

// ── roles ────────────────────────────────────────────────────────────────

const CREATE_ROLES = new Set(["OWNER", "ADMIN", "MANAGER"])
const MANAGE_ROLES = new Set(["OWNER", "ADMIN"])

/** Giving and taking skills, bulk import (the server's `create` tier). */
export function canGiveSkills(role: string | null | undefined): boolean {
  return CREATE_ROLES.has((role ?? "").toUpperCase())
}

/** Removing a skill and approving proposals (the server's `manage` tier). */
export function canManageSkills(role: string | null | undefined): boolean {
  return MANAGE_ROLES.has((role ?? "").toUpperCase())
}

// ── vocabulary ────────────────────────────────────────────────────────────

export interface DomainMeta {
  label: string
  icon: LucideIcon
}

const DOMAIN_META: Record<string, DomainMeta> = {
  CODING: { label: "Coding", icon: Code2 },
  DATA: { label: "Data", icon: Database },
  DEVOPS: { label: "DevOps", icon: Cloud },
  WRITING: { label: "Writing", icon: PenLine },
  RESEARCH: { label: "Research", icon: Microscope },
  PM: { label: "Planning", icon: ListChecks },
  DESIGN: { label: "Design", icon: Palette },
  SUPPORT: { label: "Support", icon: LifeBuoy },
  SECURITY: { label: "Security", icon: Shield },
  FINANCE: { label: "Finance", icon: DollarSign },
  OPS: { label: "Operations", icon: Settings },
  AUTOMATION: { label: "Automation", icon: Workflow },
  SALES: { label: "Sales", icon: HandCoins },
  CUSTOM: { label: "Other", icon: Box },
}

export function domainMeta(category: string | null | undefined): DomainMeta {
  return DOMAIN_META[category ?? ""] ?? DOMAIN_META.CUSTOM
}

/** Domains in display order, for the editor. */
export const DOMAIN_ORDER = Object.keys(DOMAIN_META)

// A skill's own icon is a lucide name from its frontmatter. The app draws
// icons from one catalog (lib/crew-icons, the crew and project pickers), so a
// few lucide names the bundled skills use are mapped onto catalog entries.
const ICON_ALIASES: Record<string, string> = {
  "code-2": "code",
  "code-xml": "code",
  "flask-conical": "flask",
  "git-pull-request": "code",
  paintbrush: "brush",
  "swatch-book": "palette",
}
const ICON_BY_NAME = new Map(CREW_ICONS.map((d) => [d.name, d]))

/** The catalog name for a skill's stored icon, or null when it has none we can draw. */
export function skillIconName(icon: string | null | undefined): string | null {
  if (!icon) return null
  const name = ICON_ALIASES[icon] ?? icon
  return ICON_BY_NAME.has(name) ? name : null
}

/** The skill's own icon when it has one, otherwise its domain's. */
export function skillIcon(s: { icon?: string | null; category?: string | null }): LucideIcon {
  const name = skillIconName(s.icon)
  return name ? ICON_BY_NAME.get(name)!.icon : domainMeta(s.category).icon
}

/** Tags decoded from the row's JSON column; never throws. */
export function skillTags(s: { tags?: string | null }): string[] {
  try {
    const v = JSON.parse(s.tags ?? "[]")
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []
  } catch {
    return []
  }
}

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

// ── where a skill comes from ──────────────────────────────────────────────

export type SkillSourceKind = "builtin" | "workspace" | "repo" | "vendor"

export interface SkillSource {
  /** Stable key for the facet and the grouping. */
  key: string
  label: string
  kind: SkillSourceKind
  /** The repository's web address, when the skill came from one. */
  url?: string
}

export const BUILTIN_SOURCE: SkillSource = { key: "builtin", label: "Crewship built-in", kind: "builtin" }
export const WORKSPACE_SOURCE: SkillSource = { key: "workspace", label: "Made in this workspace", kind: "workspace" }

const REPO_URL = /^(?:https?:\/\/)?(?:www\.)?([a-z0-9.-]+\.[a-z]{2,})\/([\w.-]+)\/([\w.-]+?)(?:\.git)?(?:[/?#].*)?$/i

/** `owner/repo` and the web address from a git URL the importer recorded. */
export function repoFromUrl(url: string | null | undefined): { name: string; url: string } | null {
  const m = REPO_URL.exec((url ?? "").trim())
  if (!m) return null
  return { name: `${m[2]}/${m[3]}`, url: `https://${m[1]}/${m[2]}/${m[3]}` }
}

/**
 * Where a skill comes from, the way people say it: the repository a bulk
 * import recorded (`homepage`), built-in, made here (generated, pasted or
 * written in the editor), or the vendor namespace for anything else.
 */
export function skillSource(s: Pick<SkillRow, "source" | "vendor" | "homepage">): SkillSource {
  if (s.source === "BUNDLED") return BUILTIN_SOURCE
  const repo = repoFromUrl(s.homepage)
  if (repo) return { key: repo.name, label: repo.name, kind: "repo", url: repo.url }
  if (s.source === "GENERATED") return WORKSPACE_SOURCE
  const vendor = (s.vendor ?? "").trim()
  if (vendor && vendor !== "community" && vendor !== "local") return { key: `vendor:${vendor}`, label: vendor, kind: "vendor" }
  if (s.source === "CUSTOM") return WORKSPACE_SOURCE
  return { key: `src:${s.source}`, label: sourceLabel(s.source), kind: "vendor" }
}

export function isMadeHere(s: Pick<SkillRow, "source" | "vendor" | "homepage">): boolean {
  return skillSource(s).kind === "workspace"
}

export function runtimeLabel(runtime: string | null | undefined): string {
  const r = (runtime ?? "INSTRUCTIONS").toUpperCase()
  if (r === "SCRIPT" || r === "HYBRID") return "Instructions and scripts"
  if (r === "MCP") return "Instructions and an MCP server"
  return "Instructions"
}

// ── trust ────────────────────────────────────────────────────────────────

export type TrustLevel = "verified" | "unreviewed" | "flagged"

export const TRUST_LEVELS: TrustLevel[] = ["verified", "unreviewed", "flagged"]

export const TRUST_LABEL: Record<TrustLevel, string> = {
  verified: "Verified",
  unreviewed: "Not reviewed",
  flagged: "Flagged",
}

const TRUST_TONE: Record<TrustLevel, StatusTone> = {
  verified: "success",
  unreviewed: "muted",
  flagged: "danger",
}

/** The import scan's own word: clean, flagged, or it never read the skill. */
export function scanState(s: Pick<SkillRow, "scan_status">): "clean" | "flagged" | "unscanned" {
  const scan = (s.scan_status ?? "").toUpperCase()
  if (scan === "FLAGGED" || scan === "BLOCKED") return "flagged"
  return scan === "CLEAN" ? "clean" : "unscanned"
}

/**
 * The one trust word a skill shows. The import scan outranks the curator: a
 * flagged skill is flagged whatever its verification says, and a skill the
 * scan never read is not reviewed even if someone marked it verified.
 */
export function skillTrust(s: Pick<SkillRow, "scan_status" | "verification">): {
  level: TrustLevel
  label: string
  tone: StatusTone
} {
  const scan = scanState(s)
  const level: TrustLevel =
    scan === "flagged" ? "flagged" : scan === "clean" && (s.verification ?? "").toUpperCase() === "VERIFIED" ? "verified" : "unreviewed"
  return { level, label: TRUST_LABEL[level], tone: TRUST_TONE[level] }
}

export function skillName(s: Pick<SkillRow, "display_name" | "name" | "slug">): string {
  return s.display_name || s.name || s.slug
}

/** Agents holding the skill without a credential it needs. */
export function agentsMissingCredentials(s: SkillRow): SkillAgentRef[] {
  return (s.installed_on ?? []).filter((a) => (a.missing_credentials?.length ?? 0) > 0)
}

export function isArchived(s: Pick<SkillRow, "lifecycle_state">): boolean {
  return (s.lifecycle_state ?? "").toLowerCase() === "archived"
}

// ── views ────────────────────────────────────────────────────────────────

export type SkillsView = "library" | "used" | "unused" | "attention" | "proposed" | "archived"

export const SKILL_VIEWS: SkillsView[] = ["library", "used", "unused", "attention", "proposed", "archived"]

export const VIEW_LABEL: Record<SkillsView, string> = {
  library: "Library",
  used: "On agents",
  unused: "Not on any agent",
  attention: "Needs attention",
  proposed: "Proposed by agents",
  archived: "Archived",
}

/** `?view=` values links and bookmarks carry from before Harbor 2. */
const LEGACY_VIEW: Record<string, SkillsView> = { all: "library", assigned: "used", unassigned: "unused" }

export function isSkillsView(v: string | null | undefined): v is SkillsView {
  return !!v && (SKILL_VIEWS as string[]).includes(v)
}

/** The view a `?view=` value means, old values included; null for anything else. */
export function viewFromParam(v: string | null | undefined): SkillsView | null {
  if (!v) return null
  if (isSkillsView(v)) return v
  return LEGACY_VIEW[v] ?? null
}

export function legacyView(v: string | null | undefined): SkillsView | null {
  return v ? (LEGACY_VIEW[v] ?? null) : null
}

/** Does a skill belong in a view's list? Attention and Proposed are not lists of rows. */
export function inView(s: SkillRow, view: SkillsView): boolean {
  const archived = isArchived(s)
  if (view === "archived") return archived
  if (archived) return false
  const held = (s.installed_on?.length ?? 0) > 0
  switch (view) {
    case "library":
      return true
    case "used":
      return held
    case "unused":
      return !held
    case "attention":
      return attentionReasons(s).length > 0
    case "proposed":
      return false
  }
}

export type AttentionKind = "flagged" | "unscanned" | "credential"

export interface AttentionReason {
  kind: AttentionKind
  title: string
  detail: string
}

/**
 * What needs a person about one skill, most severe first. A problem only
 * counts while an agent holds the skill: an unscanned skill nobody has harms
 * nobody yet. A flagged one counts either way — it should not be given.
 */
export function attentionReasons(s: SkillRow): AttentionReason[] {
  if (isArchived(s)) return []
  const out: AttentionReason[] = []
  const held = s.installed_on ?? []
  const scan = scanState(s)
  if (scan === "flagged") {
    out.push({
      kind: "flagged",
      title: "Flagged by the scan",
      detail: `${s.description_quality || "The import scan flagged its instructions"}${held.length ? ` · ${held.length} ${held.length === 1 ? "agent has" : "agents have"} it` : ""}`,
    })
  } else if (scan === "unscanned" && held.length > 0) {
    out.push({ kind: "unscanned", title: "Never scanned", detail: `${held.length} ${held.length === 1 ? "agent has" : "agents have"} it` })
  }
  const lacking = agentsMissingCredentials(s)
  if (lacking.length > 0) {
    const creds = [...new Set(lacking.flatMap((a) => a.missing_credentials ?? []))]
    out.push({
      kind: "credential",
      title: "A credential it needs is missing",
      detail: `${creds.join(", ")} · ${lacking.map((a) => a.agent_name).join(", ")} ${lacking.length === 1 ? "runs" : "run"} without it`,
    })
  }
  return out
}

// ── filters ──────────────────────────────────────────────────────────────

export type SkillStatus = "stale" | "made"

export const STATUS_LABEL: Record<SkillStatus, string> = {
  stale: "Unused 30 days",
  made: "Made here",
}

export interface SkillFilters {
  view: SkillsView
  /** Shown as a removable chip, not a facet (links from Crews carry it). */
  agentId: string | null
  /** Source facet: a SkillSource key. */
  source: string | null
  domain: string | null
  trust: TrustLevel | null
  status: SkillStatus | null
  /** Used by crew. */
  crewId: string | null
  query: string
}

export const EMPTY_SKILL_FILTERS: SkillFilters = {
  view: "library",
  agentId: null,
  source: null,
  domain: null,
  trust: null,
  status: null,
  crewId: null,
  query: "",
}

export type FacetKey = "source" | "domain" | "trust" | "status" | "crewId"

export const FACET_KEYS: FacetKey[] = ["source", "domain", "trust", "status", "crewId"]

/** How many facets (and the agent chip) narrow the list. */
export function activeFilterCount(f: SkillFilters): number {
  return FACET_KEYS.filter((k) => f[k] != null).length + (f.agentId ? 1 : 0)
}

/**
 * Does a skill pass the filters, view aside? `skip` leaves one facet out, so a
 * facet's counts say what picking each of its values would show rather than
 * dropping to zero the moment one is picked.
 */
export function matchesSkill(s: SkillRow, f: SkillFilters, skip?: FacetKey): boolean {
  const holders = s.installed_on ?? []
  if (f.agentId && !holders.some((a) => a.agent_id === f.agentId)) return false
  if (skip !== "crewId" && f.crewId && !holders.some((a) => a.crew_id === f.crewId)) return false
  if (skip !== "source" && f.source && skillSource(s).key !== f.source) return false
  if (skip !== "domain" && f.domain && s.category !== f.domain) return false
  if (skip !== "trust" && f.trust && skillTrust(s).level !== f.trust) return false
  if (skip !== "status" && f.status) {
    if (f.status === "stale" && (s.lifecycle_state ?? "").toLowerCase() !== "stale") return false
    if (f.status === "made" && !isMadeHere(s)) return false
  }
  const q = f.query.trim().toLowerCase()
  if (q) {
    const hay = [skillName(s), s.slug, s.description ?? "", skillSource(s).label, ...holders.map((a) => a.agent_name)].join(" ").toLowerCase()
    if (!q.split(/\s+/).every((term) => hay.includes(term))) return false
  }
  return true
}

/** The rows a list view shows. Attention and Proposed draw their own lists. */
export function visibleSkills(rows: SkillRow[], f: SkillFilters): SkillRow[] {
  const listView = f.view === "attention" || f.view === "proposed" ? "library" : f.view
  return rows.filter((s) => inView(s, listView) && matchesSkill(s, f))
}

export interface FacetCount {
  key: string
  label: string
  count: number
}

/** Panel counts: views over the whole library, each facet with itself left out. */
export function panelCounts(rows: SkillRow[], f: SkillFilters) {
  const views: Record<SkillsView, number> = { library: 0, used: 0, unused: 0, attention: 0, proposed: 0, archived: 0 }
  const source = new Map<string, FacetCount>()
  const domain = new Map<string, number>()
  const trust = new Map<TrustLevel, number>()
  const status = new Map<SkillStatus, number>()
  const crew = new Map<string, number>()
  const listView = f.view === "attention" || f.view === "proposed" ? "library" : f.view
  for (const s of rows) {
    for (const v of SKILL_VIEWS) if (v !== "proposed" && inView(s, v)) views[v]++
    if (!inView(s, listView)) continue
    if (matchesSkill(s, f, "source")) {
      const src = skillSource(s)
      const cur = source.get(src.key) ?? { key: src.key, label: src.label, count: 0 }
      cur.count++
      source.set(src.key, cur)
    }
    if (matchesSkill(s, f, "domain")) domain.set(s.category, (domain.get(s.category) ?? 0) + 1)
    if (matchesSkill(s, f, "trust")) {
      const t = skillTrust(s).level
      trust.set(t, (trust.get(t) ?? 0) + 1)
    }
    if (matchesSkill(s, f, "status")) {
      if ((s.lifecycle_state ?? "").toLowerCase() === "stale") status.set("stale", (status.get("stale") ?? 0) + 1)
      if (isMadeHere(s)) status.set("made", (status.get("made") ?? 0) + 1)
    }
    if (matchesSkill(s, f, "crewId")) {
      for (const c of new Set((s.installed_on ?? []).map((a) => a.crew_id).filter((x): x is string => !!x))) crew.set(c, (crew.get(c) ?? 0) + 1)
    }
  }
  const sources = [...source.values()].sort((a, b) => b.count - a.count || a.label.localeCompare(b.label))
  return { views, sources, domain, trust, status, crew }
}

/** How many distinct sources the library draws from (the page's sub-line). */
export function sourceCount(rows: SkillRow[]): number {
  return new Set(rows.filter((s) => !isArchived(s)).map((s) => skillSource(s).key)).size
}

// ── sort and group ───────────────────────────────────────────────────────

export type SkillSort = "used" | "updated" | "name"
export const SORT_LABEL: Record<SkillSort, string> = { used: "Most used", updated: "Recently updated", name: "Name" }

export function isSkillSort(v: string | null | undefined): v is SkillSort {
  return v === "used" || v === "updated" || v === "name"
}

export function sortSkills(rows: SkillRow[], sort: SkillSort): SkillRow[] {
  const byName = (a: SkillRow, b: SkillRow) => skillName(a).localeCompare(skillName(b))
  const out = [...rows]
  if (sort === "name") return out.sort(byName)
  if (sort === "updated") return out.sort((a, b) => (b.updated_at ?? "").localeCompare(a.updated_at ?? "") || byName(a, b))
  return out.sort(
    (a, b) =>
      (b.usage?.uses_7d ?? 0) - (a.usage?.uses_7d ?? 0) ||
      (b.usage?.uses_total ?? 0) - (a.usage?.uses_total ?? 0) ||
      (b.installed_on?.length ?? 0) - (a.installed_on?.length ?? 0) ||
      byName(a, b),
  )
}

export type SkillGroup = "none" | "source" | "domain"
export const GROUP_LABEL: Record<SkillGroup, string> = { none: "None", source: "Source", domain: "Domain" }

export function isSkillGroup(v: string | null | undefined): v is SkillGroup {
  return v === "none" || v === "source" || v === "domain"
}

export interface SkillGroupRows {
  key: string
  label: string
  rows: SkillRow[]
}

/** Rows (already sorted) in groups, largest first; the order inside a group is kept. */
export function groupSkills(rows: SkillRow[], group: Exclude<SkillGroup, "none">): SkillGroupRows[] {
  const m = new Map<string, SkillGroupRows>()
  for (const s of rows) {
    const key = group === "source" ? skillSource(s).key : s.category
    const label = group === "source" ? skillSource(s).label : domainMeta(s.category).label
    const g = m.get(key) ?? { key, label, rows: [] }
    g.rows.push(s)
    m.set(key, g)
  }
  return [...m.values()].sort((a, b) => b.rows.length - a.rows.length || a.label.localeCompare(b.label))
}

/** Rows a list renders before it loads more as you scroll. */
export const PAGE_SIZE = 50

// ── skill page tabs ──────────────────────────────────────────────────────

export type SkillTab = "overview" | "md" | "used" | "security" | "version"
export const SKILL_TABS: SkillTab[] = ["overview", "md", "used", "security", "version"]

/** `?tab=` values from before Harbor 2. Activity now lives under Used by. */
const LEGACY_TAB: Record<string, SkillTab> = { content: "md", agents: "used", activity: "used", versions: "version" }

export function tabFromParam(v: string | null | undefined): SkillTab {
  if (v && (SKILL_TABS as string[]).includes(v)) return v as SkillTab
  return (v && LEGACY_TAB[v]) || "overview"
}

export function isLegacyTab(v: string | null | undefined): boolean {
  return !!v && v in LEGACY_TAB
}
