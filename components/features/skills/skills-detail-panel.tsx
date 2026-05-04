"use client"

import { useEffect, useMemo, useState } from "react"
import { Streamdown } from "streamdown"
import {
  Copy, Check, X, ShieldCheck, BadgeCheck, Lock, Dot, Sparkles,
  AlertTriangle, Loader2,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
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

interface AgentRow {
  id: string
  name: string
  slug: string
  crew_id: string | null
  crew?: { id: string; name: string; slug: string } | null
}

interface CrewRow {
  id: string
  name: string
  slug: string
  _count?: { agents: number }
}

// SkillsDetailPanel renders the right pane of the 3-panel browser. Pulls
// the SKILL.md body lazily because the list endpoint omits it (would
// balloon the browse payload). Owns the install / assign dialogs so the
// browser doesn't have to thread their state through.
export function SkillsDetailPanel({
  skill,
  workspaceId,
  onClose,
  onChanged,
}: {
  skill: SkillCardData | null
  workspaceId: string | null
  onClose?: () => void
  // Fired after a successful install/assign so the parent can refetch
  // the list and refresh install_count / agent_count counters.
  onChanged?: () => void
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
        if (!cancelled) setDetail(skill as SkillDetail)
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
          Click any card to view the full SKILL.md, install on an agent, or assign to a crew.
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
      <header className="border-b border-white/[0.08] p-3 space-y-2 shrink-0">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="text-[11px] text-white/45 truncate">{vendor}/</div>
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
              <Button variant="ghost" size="icon" onClick={onClose} className="h-7 w-7 xl:hidden" aria-label="Close skill detail">
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
            className="rounded hover:bg-white/[0.06] p-1 text-white/55 hover:text-white/95 transition-colors"
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

      <footer className="border-t border-white/[0.08] p-3 flex items-center gap-2 text-xs shrink-0">
        <InstallToAgentDialog skill={skill} workspaceId={workspaceId} onInstalled={onChanged} />
        <AssignToCrewDialog skill={skill} workspaceId={workspaceId} onAssigned={onChanged} />
      </footer>
    </div>
  )
}

// InstallToAgentDialog opens an agent picker; on submit it issues
// POST /api/v1/agents/{id}/skills for each selected agent. The list of
// agents is loaded lazily on dialog open to avoid eager fetch on every
// detail-panel render.
function InstallToAgentDialog({
  skill,
  workspaceId,
  onInstalled,
}: {
  skill: SkillCardData
  workspaceId: string | null
  onInstalled?: () => void
}) {
  const [open, setOpen] = useState(false)
  const [agents, setAgents] = useState<AgentRow[]>([])
  const [filter, setFilter] = useState("")
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open || !workspaceId) return
    let cancelled = false
    setLoading(true)
    setError(null)
    fetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((res) => (res.ok ? res.json() : Promise.reject()))
      .then((json) => {
        if (cancelled) return
        setAgents((json as AgentRow[]) ?? [])
      })
      .catch(() => !cancelled && setError("Failed to load agents"))
      .finally(() => !cancelled && setLoading(false))
    return () => {
      cancelled = true
    }
  }, [open, workspaceId])

  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase()
    if (!q) return agents
    return agents.filter(
      (a) =>
        a.name.toLowerCase().includes(q) ||
        a.slug.toLowerCase().includes(q) ||
        a.crew?.name?.toLowerCase().includes(q),
    )
  }, [agents, filter])

  async function handleInstall() {
    if (picked.size === 0) return
    if (!workspaceId) {
      setError("workspace not loaded — refresh the page and retry")
      return
    }
    setSubmitting(true)
    setError(null)
    const errors: string[] = []
    // The agents/{id}/skills endpoint runs through wsCtx middleware
    // which requires ?workspace_id=… in the query string. Without it
    // the server returns 400 'workspace_id is required' (caught
    // when the user clicked Install on dev1).
    const wsParam = `?workspace_id=${encodeURIComponent(workspaceId)}`
    for (const agentId of picked) {
      try {
        const res = await fetch(`/api/v1/agents/${agentId}/skills${wsParam}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ skill_id: skill.id }),
        })
        if (!res.ok && res.status !== 409) {
          // 409 = already installed; treat as success so bulk runs are idempotent.
          const body = await res.text().catch(() => res.statusText)
          let detail = body
          try {
            const parsed = JSON.parse(body) as { detail?: string; error?: string }
            detail = parsed.detail ?? parsed.error ?? body
          } catch {
            // not JSON; use raw body
          }
          errors.push(`${agentId}: ${detail || res.statusText}`)
        }
      } catch {
        errors.push(`${agentId}: network error`)
      }
    }
    setSubmitting(false)
    if (errors.length > 0) {
      setError(`Failed for ${errors.length} of ${picked.size}: ${errors[0]}`)
      return
    }
    setOpen(false)
    setPicked(new Set())
    onInstalled?.()
  }

  return (
    <Dialog open={open} onOpenChange={(v) => { setOpen(v); if (!v) { setPicked(new Set()); setError(null) } }}>
      <DialogTrigger asChild>
        <Button size="sm" className="flex-1">Install to agent…</Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Install {skill.display_name ?? skill.name}</DialogTitle>
          <DialogDescription>
            Pick one or more agents. The skill body is materialised into each agent&apos;s container under
            <code className="text-blue-300 mx-1">.claude/skills/</code>,
            <code className="text-blue-300 mx-1">.agents/skills/</code>,
            and the other CLI discovery paths.
          </DialogDescription>
        </DialogHeader>

        <Input
          placeholder="Filter by name, slug, or crew…"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          className="mb-2"
        />

        <div className="max-h-72 overflow-y-auto rounded border border-white/[0.08] divide-y divide-white/[0.04]">
          {loading ? (
            <div className="p-4 text-center text-xs text-white/45">
              <Loader2 className="h-3 w-3 inline mr-1 animate-spin" />
              Loading agents…
            </div>
          ) : filtered.length === 0 ? (
            <div className="p-4 text-center text-xs text-white/45">No agents found.</div>
          ) : (
            filtered.map((a) => {
              const selected = picked.has(a.id)
              return (
                <button
                  key={a.id}
                  type="button"
                  onClick={() =>
                    setPicked((prev) => {
                      const next = new Set(prev)
                      if (next.has(a.id)) next.delete(a.id)
                      else next.add(a.id)
                      return next
                    })
                  }
                  className={`flex w-full items-center gap-2 px-3 py-2 text-left text-xs transition-colors ${
                    selected ? "bg-blue-500/[0.12]" : "hover:bg-white/[0.04]"
                  }`}
                >
                  <span
                    className={`inline-block h-3 w-3 rounded border ${
                      selected ? "border-blue-400 bg-blue-500" : "border-white/20"
                    }`}
                  />
                  <span className="font-medium text-white/90 flex-1 truncate">{a.name}</span>
                  {a.crew && (
                    <span className="text-[10px] text-white/45 truncate">{a.crew.name}</span>
                  )}
                </button>
              )
            })
          )}
        </div>

        {error && (
          <div className="text-xs text-red-300 flex items-start gap-1">
            <AlertTriangle className="h-3 w-3 mt-0.5 shrink-0" />
            {error}
          </div>
        )}

        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)}>Cancel</Button>
          <Button onClick={handleInstall} disabled={picked.size === 0 || submitting}>
            {submitting ? <Loader2 className="h-3 w-3 mr-1 animate-spin" /> : null}
            Install ({picked.size})
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// AssignToCrewDialog applies the skill to every agent in the picked
// crew. There's no CrewSkill table yet (deferred to v0.2 per project
// memory: "skills only assign to individual agents; crew-level skill
// inheritance unimplemented"), so this is bulk-apply over the crew's
// current agent roster. New agents added later won't auto-receive the
// skill — documented in the dialog body so the user knows the
// limitation.
function AssignToCrewDialog({
  skill,
  workspaceId,
  onAssigned,
}: {
  skill: SkillCardData
  workspaceId: string | null
  onAssigned?: () => void
}) {
  const [open, setOpen] = useState(false)
  const [crews, setCrews] = useState<CrewRow[]>([])
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pickedCrew, setPickedCrew] = useState<string | null>(null)

  useEffect(() => {
    if (!open || !workspaceId) return
    let cancelled = false
    setLoading(true)
    setError(null)
    fetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((res) => (res.ok ? res.json() : Promise.reject()))
      .then((json) => {
        if (cancelled) return
        setCrews((json as CrewRow[]) ?? [])
      })
      .catch(() => !cancelled && setError("Failed to load crews"))
      .finally(() => !cancelled && setLoading(false))
    return () => {
      cancelled = true
    }
  }, [open, workspaceId])

  async function handleAssign() {
    if (!pickedCrew || !workspaceId) return
    setSubmitting(true)
    setError(null)
    const wsParam = `workspace_id=${encodeURIComponent(workspaceId)}`
    try {
      const agentsRes = await fetch(`/api/v1/agents?${wsParam}&crew_id=${encodeURIComponent(pickedCrew)}`)
      if (!agentsRes.ok) {
        setError("Failed to load crew agents")
        setSubmitting(false)
        return
      }
      const agents = (await agentsRes.json()) as AgentRow[]
      const targets = agents.filter((a) => a.crew_id === pickedCrew)
      if (targets.length === 0) {
        setError("Crew has no agents to install on.")
        setSubmitting(false)
        return
      }
      const errors: string[] = []
      // Each per-agent POST also needs ?workspace_id — same wsCtx
      // middleware requirement as the single-install path. Surface
      // the JSON detail when the body is RFC7807 so the user sees
      // 'agent not in workspace X' instead of 'Bad Request'.
      for (const a of targets) {
        const res = await fetch(`/api/v1/agents/${encodeURIComponent(a.id)}/skills?${wsParam}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ skill_id: skill.id }),
        })
        if (!res.ok && res.status !== 409) {
          const body = await res.text().catch(() => res.statusText)
          let detail = body
          try {
            const parsed = JSON.parse(body) as { detail?: string; error?: string }
            detail = parsed.detail ?? parsed.error ?? body
          } catch {
            // raw body; use as-is
          }
          errors.push(`${a.slug}: ${detail || res.statusText}`)
        }
      }
      if (errors.length > 0) {
        setError(`Failed for ${errors.length} of ${targets.length}: ${errors[0]}`)
        setSubmitting(false)
        return
      }
      setOpen(false)
      setPickedCrew(null)
      onAssigned?.()
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => { setOpen(v); if (!v) { setPickedCrew(null); setError(null) } }}>
      <DialogTrigger asChild>
        <Button size="sm" variant="outline" className="flex-1">Assign to crew…</Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Assign {skill.display_name ?? skill.name} to crew</DialogTitle>
          <DialogDescription>
            Bulk-installs the skill on every agent currently in the crew. Future agents added later
            won&apos;t auto-receive the skill (per-crew skill inheritance is on the v0.2 roadmap).
          </DialogDescription>
        </DialogHeader>

        <div className="max-h-72 overflow-y-auto rounded border border-white/[0.08] divide-y divide-white/[0.04]">
          {loading ? (
            <div className="p-4 text-center text-xs text-white/45">
              <Loader2 className="h-3 w-3 inline mr-1 animate-spin" />
              Loading crews…
            </div>
          ) : crews.length === 0 ? (
            <div className="p-4 text-center text-xs text-white/45">No crews in this workspace.</div>
          ) : (
            crews.map((c) => {
              const selected = pickedCrew === c.id
              return (
                <button
                  key={c.id}
                  type="button"
                  onClick={() => setPickedCrew(c.id)}
                  className={`flex w-full items-center gap-2 px-3 py-2 text-left text-xs transition-colors ${
                    selected ? "bg-blue-500/[0.12]" : "hover:bg-white/[0.04]"
                  }`}
                >
                  <span
                    className={`inline-block h-3 w-3 rounded-full border ${
                      selected ? "border-blue-400 bg-blue-500" : "border-white/20"
                    }`}
                  />
                  <span className="font-medium text-white/90 flex-1 truncate">{c.name}</span>
                  {c._count?.agents != null && (
                    <span className="text-[10px] text-white/45 tabular-nums">{c._count.agents} agents</span>
                  )}
                </button>
              )
            })
          )}
        </div>

        {error && (
          <div className="text-xs text-red-300 flex items-start gap-1">
            <AlertTriangle className="h-3 w-3 mt-0.5 shrink-0" />
            {error}
          </div>
        )}

        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)}>Cancel</Button>
          <Button onClick={handleAssign} disabled={!pickedCrew || submitting}>
            {submitting ? <Loader2 className="h-3 w-3 mr-1 animate-spin" /> : null}
            Apply to crew
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
