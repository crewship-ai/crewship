"use client"

import { useEffect, useState } from "react"
import { Streamdown } from "streamdown"
import { Copy, Check, X, ShieldCheck, BadgeCheck, Lock, Dot, Sparkles, AlertTriangle } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import type { SkillCardData } from "@/components/features/skills/skill-card"

interface SkillDetail extends SkillCardData {
  content?: string | null
  spdx_license?: string | null
  homepage?: string | null
  agent_count?: number
  license?: string | null
}

const SOURCE_PILL: Record<string, { label: string; icon: React.ElementType; cls: string }> = {
  BUNDLED:    { label: "Official",  icon: ShieldCheck, cls: "bg-blue-500/10 text-blue-300 border-blue-500/30" },
  GENERATED:  { label: "Generated", icon: Sparkles,    cls: "bg-violet-500/10 text-violet-300 border-violet-500/30" },
  MARKETPLACE:{ label: "Verified",  icon: BadgeCheck,  cls: "bg-emerald-500/10 text-emerald-300 border-emerald-500/30" },
  CUSTOM:     { label: "Community", icon: Dot,         cls: "bg-white/[0.05] text-white/55 border-white/10" },
  MANAGED:    { label: "Managed",   icon: Lock,        cls: "bg-white/[0.05] text-white/55 border-white/10" },
}

// SkillsDetailPanel renders the right pane of the 3-panel layout for the
// currently selected skill. Empty state when nothing is selected; full
// markdown render via streamdown otherwise.
//
// Pulls content lazily — the list endpoint doesn't return SKILL.md
// bodies (would balloon the browse payload), so we fetch by id when
// the user clicks a card. Cached in state per render of this panel —
// switching back to a previously viewed skill re-fetches; this is fine
// for v0.1 (one detail at a time, low frequency) and avoids the
// memory footprint of a multi-skill cache.
export function SkillsDetailPanel({
  skill,
  workspaceId,
  onClose,
}: {
  skill: SkillCardData | null
  workspaceId: string | null
  onClose?: () => void
}) {
  const [detail, setDetail] = useState<SkillDetail | null>(null)
  const [loading, setLoading] = useState(false)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (!skill) {
      setDetail(null)
      return
    }
    let cancelled = false
    setLoading(true)
    fetch(`/api/v1/skills/${skill.id}` + (workspaceId ? `?workspace_id=${workspaceId}` : ""))
      .then((res) => (res.ok ? res.json() : Promise.reject()))
      .then((json) => {
        if (!cancelled) setDetail(json as SkillDetail)
      })
      .catch(() => {
        if (!cancelled) setDetail(skill as SkillDetail) // fallback to list-row data
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [skill, workspaceId])

  if (!skill) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 p-6 text-center">
        <div className="rounded-full bg-white/[0.04] p-3">
          <Sparkles className="h-5 w-5 text-white/35" />
        </div>
        <p className="text-sm text-white/55">Select a skill to see details</p>
        <p className="text-xs text-white/35">
          Click any card to view the full SKILL.md, install it on an agent, or assign to a crew.
        </p>
      </div>
    )
  }

  const sourceCfg = SOURCE_PILL[skill.source] ?? SOURCE_PILL.CUSTOM
  const SourceIcon = sourceCfg.icon
  const vendor = skill.vendor || "community"
  const installCmd = `crewship skill install ${vendor}/${skill.slug}`
  const flagged = (detail?.scan_status ?? skill.scan_status) === "FLAGGED"
  const reason = detail?.description_quality ?? skill.description_quality

  const copy = () => {
    navigator.clipboard?.writeText(installCmd).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }

  return (
    <div className="flex h-full flex-col min-h-0">
      <header className="border-b border-white/[0.08] p-4 space-y-3">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="text-xs text-white/45 truncate">{vendor}/</div>
            <h2 className="text-base font-semibold text-white/95 truncate">
              {skill.display_name ?? skill.name}
            </h2>
          </div>
          <div className="flex items-center gap-1">
            <Badge variant="outline" className={`gap-1 ${sourceCfg.cls}`}>
              <SourceIcon className="h-3 w-3" />
              {sourceCfg.label}
            </Badge>
            {onClose && (
              <Button variant="ghost" size="icon" onClick={onClose} className="h-7 w-7 xl:hidden">
                <X className="h-4 w-4" />
              </Button>
            )}
          </div>
        </div>

        {flagged && reason && (
          <div className="rounded-md border border-red-500/30 bg-red-500/[0.08] px-2 py-1.5 text-xs text-red-200 flex items-start gap-1.5">
            <AlertTriangle className="h-3.5 w-3.5 shrink-0 mt-0.5" />
            <span>{reason}</span>
          </div>
        )}

        <div className="flex items-center gap-2 rounded-md bg-black/30 border border-white/[0.08] px-2 py-1.5 text-xs font-mono text-white/85">
          <span className="flex-1 truncate">{installCmd}</span>
          <button
            type="button"
            onClick={copy}
            className="rounded hover:bg-white/[0.06] p-1 text-white/55 hover:text-white/95"
            aria-label="Copy install command"
          >
            {copied ? <Check className="h-3.5 w-3.5 text-emerald-400" /> : <Copy className="h-3.5 w-3.5" />}
          </button>
        </div>
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto p-4 prose prose-invert prose-sm max-w-none prose-headings:text-white/90 prose-p:text-white/70 prose-li:text-white/70 prose-code:text-blue-300 prose-code:bg-white/[0.06] prose-code:px-1 prose-code:rounded prose-pre:bg-black/40 prose-pre:border prose-pre:border-white/[0.08]">
        {loading && <p className="text-white/45 text-xs italic">Loading…</p>}
        {!loading && detail?.content && <Streamdown>{detail.content}</Streamdown>}
        {!loading && !detail?.content && (
          <p className="text-white/45 text-xs italic">No body content available for this skill.</p>
        )}
      </div>

      <footer className="border-t border-white/[0.08] p-3 flex items-center gap-2 text-xs">
        <Button size="sm" className="flex-1">Install to agent…</Button>
        <Button size="sm" variant="outline" className="flex-1">Assign to crew…</Button>
      </footer>
    </div>
  )
}
