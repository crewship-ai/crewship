"use client"

import {
  Blocks, Code, Search, Hammer, Server, MessageCircle, Settings,
  Palette, ShieldCheck, BadgeCheck, Lock, Dot, Download, Clock,
  AlertTriangle, FileText, Sparkles, Plug,
} from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"

// SkillCardData mirrors the skillResponse JSON the backend emits after
// the Sprint 1 v65 schema changes. New fields (vendor, maturity, runtime,
// scan_status) are optional in the type so the card still renders for
// rows imported before the migration ran.
export interface SkillCardData {
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
  maturity?: string | null
  runtime?: string | null
  scan_status?: string | null
  description_quality?: string | null
  downloads: number | null
  featured: boolean
  updated_at?: string
}

// Source badge — Composio's auth-method badge proved that a single
// trust glyph reads faster than a publisher avatar. Map the 5 source
// enum values onto 4 visual treatments (MARKETPLACE / VERIFIED share).
const SOURCE_BADGE: Record<string, { label: string; icon: React.ElementType; className: string }> = {
  BUNDLED:   { label: "Official",  icon: ShieldCheck, className: "bg-blue-500/10 text-blue-300 border-blue-500/30" },
  GENERATED: { label: "Generated", icon: Sparkles,    className: "bg-violet-500/10 text-violet-300 border-violet-500/30" },
  MARKETPLACE: { label: "Verified", icon: BadgeCheck, className: "bg-emerald-500/10 text-emerald-300 border-emerald-500/30" },
  CUSTOM:    { label: "Community", icon: Dot,         className: "bg-white/[0.05] text-white/55 border-white/10" },
  MANAGED:   { label: "Managed",   icon: Lock,        className: "bg-white/[0.05] text-white/55 border-white/10" },
}

// Domain colour — single accent chip per card per the mockup
// (.claude/mockups/skills-page.html). 12% bg keeps dense grid readable.
const DOMAIN_COLORS: Record<string, string> = {
  CODING:     "bg-blue-500/12 text-blue-300",
  AUTOMATION: "bg-cyan-500/12 text-cyan-300",
  DATA:       "bg-violet-500/12 text-violet-300",
  DEVOPS:     "bg-orange-500/12 text-orange-300",
  WRITING:    "bg-teal-500/12 text-teal-300",
  RESEARCH:   "bg-amber-500/12 text-amber-300",
  PM:         "bg-pink-500/12 text-pink-300",
  DESIGN:     "bg-fuchsia-500/12 text-fuchsia-300",
  SECURITY:   "bg-red-500/12 text-red-300",
  SUPPORT:    "bg-cyan-500/12 text-cyan-300",
  FINANCE:    "bg-emerald-500/12 text-emerald-300",
  OPS:        "bg-indigo-500/12 text-indigo-300",
  CUSTOM:     "bg-white/[0.06] text-white/65",
}

const DOMAIN_ICONS: Record<string, React.ElementType> = {
  CODING:     Code,
  AUTOMATION: Plug,
  DATA:       Search,
  DEVOPS:     Server,
  WRITING:    FileText,
  RESEARCH:   Search,
  PM:         Hammer,
  DESIGN:     Palette,
  SECURITY:   ShieldCheck,
  SUPPORT:    MessageCircle,
  FINANCE:    Settings,
  OPS:        Server,
  CUSTOM:     Settings,
}

// Maturity badge — rendered only when NOT OFFICIAL (which already
// shows a source shield). Stable=COMMUNITY without a maturity badge
// would be the silent default once we promote skills via review.
const MATURITY_BADGE: Record<string, { label: string; className: string }> = {
  EXPERIMENTAL: { label: "Experimental", className: "bg-violet-500/15 text-violet-300 border-violet-500/30" },
  COMMUNITY:    { label: "Beta",         className: "bg-yellow-500/15 text-yellow-300 border-yellow-500/30" },
  CURATED:      { label: "Curated",      className: "bg-cyan-500/15 text-cyan-300 border-cyan-500/30" },
}

function formatRelative(iso?: string): string {
  if (!iso) return ""
  const ts = new Date(iso).getTime()
  if (Number.isNaN(ts)) return ""
  const days = Math.floor((Date.now() - ts) / 86_400_000)
  if (days < 1) return "Updated today"
  if (days === 1) return "Updated 1d ago"
  if (days < 30) return `Updated ${days}d ago`
  const months = Math.floor(days / 30)
  if (months < 12) return `Updated ${months}mo ago`
  return `Updated ${Math.floor(months / 12)}y ago`
}

function formatCount(n: number | null | undefined): string {
  if (n == null || n === 0) return "0 installs"
  if (n < 1000) return `${n} installs`
  if (n < 10_000) return `${(n / 1000).toFixed(1)}k installs`
  return `${Math.round(n / 1000)}k installs`
}

interface SkillCardProps {
  skill: SkillCardData
  selected?: boolean
  onSelect?: (skill: SkillCardData) => void
}

// SkillCard renders the 7-field layout from .claude/mockups/skills-page.html:
// namespace+name, one-line description, domain chip, install count,
// updated relative, source badge, maturity badge (only when non-OFFICIAL).
// Plus a flag chip when scan_status=FLAGGED.
export function SkillCard({ skill, selected, onSelect }: SkillCardProps) {
  const sourceCfg = SOURCE_BADGE[skill.source] ?? SOURCE_BADGE.CUSTOM
  const SourceIcon = sourceCfg.icon
  const DomainIcon = DOMAIN_ICONS[skill.category] ?? Blocks
  const domainCls = DOMAIN_COLORS[skill.category] ?? DOMAIN_COLORS.CUSTOM
  const matCfg = skill.maturity && skill.maturity !== "OFFICIAL" ? MATURITY_BADGE[skill.maturity] : undefined
  const flagged = skill.scan_status === "FLAGGED"
  const vendor = skill.vendor || "community"
  const displayName = skill.display_name ?? skill.name

  return (
    <button
      type="button"
      onClick={() => onSelect?.(skill)}
      aria-label={`${vendor}/${skill.slug}: ${skill.description ?? "no description"}`}
      aria-pressed={selected}
      className={`group w-full text-left rounded-lg border transition-all duration-150 outline-none focus-visible:ring-2 focus-visible:ring-blue-400/40 ${
        selected
          ? "border-blue-400/60 bg-blue-500/[0.08]"
          : "border-white/[0.08] bg-white/[0.03] hover:border-white/[0.16] hover:bg-white/[0.06]"
      }`}
    >
      <Card className="border-0 bg-transparent shadow-none">
        <CardContent className="p-4">
          <div className="flex items-start justify-between gap-2">
            <div className="min-w-0 flex-1">
              <div className="flex items-baseline gap-1 truncate">
                <span className="text-xs text-white/45 truncate">{vendor}/</span>
                <span className="text-sm font-semibold text-white/95 truncate">{displayName}</span>
              </div>
            </div>
            <Badge
              variant="outline"
              className={`shrink-0 gap-1 px-1.5 py-0 h-5 text-[10px] font-medium ${sourceCfg.className}`}
            >
              <SourceIcon className="h-3 w-3" />
              {sourceCfg.label}
            </Badge>
          </div>

          <p className="mt-2 line-clamp-2 min-h-[2.4em] text-xs text-white/60 leading-relaxed">
            {skill.description ?? <span className="italic text-white/35">No description</span>}
          </p>

          <div className="mt-3 flex flex-wrap items-center gap-1.5">
            <span className={`inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[10px] font-medium ${domainCls}`}>
              <DomainIcon className="h-3 w-3" />
              {skill.category.charAt(0) + skill.category.slice(1).toLowerCase()}
            </span>
            {matCfg && (
              <span className={`inline-flex items-center rounded-md border px-1.5 py-0.5 text-[10px] font-medium ${matCfg.className}`}>
                {matCfg.label}
              </span>
            )}
            {flagged && (
              <span className="inline-flex items-center gap-1 rounded-md border border-red-500/30 bg-red-500/10 px-1.5 py-0.5 text-[10px] font-medium text-red-300">
                <AlertTriangle className="h-3 w-3" />
                Flagged
              </span>
            )}
          </div>

          <div className="mt-3 flex items-center gap-3 text-[11px] text-white/45 tabular-nums">
            <span className="flex items-center gap-1">
              <Download className="h-3 w-3" />
              {formatCount(skill.downloads)}
            </span>
            <span className="flex items-center gap-1">
              <Clock className="h-3 w-3" />
              {formatRelative(skill.updated_at)}
            </span>
          </div>
        </CardContent>
      </Card>
    </button>
  )
}
