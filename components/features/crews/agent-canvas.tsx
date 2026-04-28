"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { motion } from "motion/react"
import { toast } from "sonner"
import {
  ChevronDown, MessageSquare, MoreHorizontal, Square,
} from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { EditableField } from "@/components/shared/editable-field"
import { SystemPromptEditor } from "@/components/features/crews/system-prompt-editor"
import { ScheduleEditor } from "@/components/features/crews/schedule-editor"
import { InboxBanner } from "@/components/features/crews/inbox-banner"
import { AvatarPickerDialog } from "@/components/features/crews/avatar-picker-dialog"
import { CrewActivityFeed } from "@/components/features/crews/crew-activity-feed"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { cn } from "@/lib/utils"

const ROLE_OPTIONS = [
  { value: "AGENT", label: "Agent" },
  { value: "LEAD", label: "Lead" },
  { value: "COORDINATOR", label: "Coordinator" },
] as const

const ADAPTER_OPTIONS = [
  { value: "CLAUDE_CODE", label: "Claude Code" },
  { value: "OPENCODE", label: "OpenCode" },
  { value: "CODEX", label: "Codex CLI" },
  { value: "GEMINI", label: "Gemini CLI" },
] as const

const TOOL_PROFILE_OPTIONS = [
  { value: "CODING", label: "Coding (full)" },
  { value: "SANDBOX", label: "Sandbox (restricted)" },
  { value: "READONLY", label: "Read-only" },
] as const

interface AgentRecord {
  id: string
  workspace_id: string
  crew_id: string | null
  name: string
  slug: string
  description: string | null
  role_title: string | null
  agent_role: string
  lead_mode: string | null
  status: string
  cli_adapter: string
  llm_provider: string | null
  llm_model: string | null
  system_prompt: string | null
  temperature?: number | null
  max_tokens?: number | null
  timeout_seconds: number
  tool_profile: string
  memory_enabled: boolean
  cli_tools?: string[] | null
  schedule_cron?: string | null
  schedule_prompt?: string | null
  schedule_enabled?: boolean | null
  schedule_last_run?: string | null
  schedule_next_run?: string | null
  webhook_secret?: string | null
  avatar_seed: string | null
  avatar_style: string | null
  updated_at: string
  crew: { id?: string; name: string; slug: string; color: string | null; avatar_style: string | null } | null
  _count?: { skills: number; credentials: number; chats: number }
  last_active_at?: string | null
}

interface InboxSummary { count: number; summary?: string }

export interface AgentCanvasProps {
  workspaceId: string
  agentSlug: string
  /** Crews list passed for the Crew dropdown in Profile section. */
  crews: { id: string; name: string; slug: string }[]
  onAgentChanged: () => void
  onSelectCrew: (slug: string | null) => void
}

const STATUS_BADGE: Record<string, { label: string; className: string; pulse?: boolean }> = {
  RUNNING: { label: "running", className: "bg-emerald-500/15 text-emerald-300 border-emerald-500/30", pulse: true },
  IDLE: { label: "idle", className: "bg-zinc-700/40 text-muted-foreground border-white/10" },
  ERROR: { label: "error", className: "bg-red-500/15 text-red-300 border-red-500/30" },
  STOPPED: { label: "stopped", className: "bg-amber-500/15 text-amber-300 border-amber-500/30" },
}

/**
 * Agent canvas — drives the right pane when ?agent=<slug> is selected.
 * Single-column layout, all sections inline (no drawer).
 *
 * Sections (top→bottom): Header → meta line → Inbox banner → Profile →
 * System prompt → Runtime + Advanced → Schedule → Skills + Credentials →
 * Activity → Danger.
 *
 * Each editable field PATCHes /api/v1/agents/{id}; the response
 * replaces local state so the next render reflects the persisted truth.
 */
export function AgentCanvas({
  workspaceId,
  agentSlug,
  crews,
  onAgentChanged,
  onSelectCrew,
}: AgentCanvasProps) {
  const [agent, setAgent] = useState<AgentRecord | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [avatarPickerOpen, setAvatarPickerOpen] = useState(false)

  const fetchAgent = useCallback(async (signal?: AbortSignal) => {
    // Backend has no /agents/by-slug endpoint, so list + filter + detail.
    try {
      const listRes = await fetch(`/api/v1/agents?workspace_id=${workspaceId}`, { signal })
      if (!listRes.ok) throw new Error(`agent list failed (${listRes.status})`)
      const list: AgentRecord[] = await listRes.json()
      const match = list.find((a) => a.slug === agentSlug)
      if (!match) throw new Error(`agent "${agentSlug}" not found in workspace`)
      const detailRes = await fetch(`/api/v1/agents/${match.id}?workspace_id=${workspaceId}`, { signal })
      if (!detailRes.ok) throw new Error(`agent detail failed (${detailRes.status})`)
      const detail: AgentRecord = await detailRes.json()
      if (!signal?.aborted) {
        setAgent(detail)
        setError(null)
      }
    } catch (err) {
      if ((err as { name?: string })?.name === "AbortError") return
      setError(err instanceof Error ? err.message : "Failed to load agent")
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [agentSlug, workspaceId])

  useEffect(() => {
    setLoading(true)
    setShowAdvanced(false)
    const controller = new AbortController()
    void fetchAgent(controller.signal)
    return () => controller.abort()
  }, [agentSlug, fetchAgent])

  // Live status updates for the visible agent only.
  useRealtimeEvent("agent.status", useCallback((event) => {
    if (agent && event.payload?.agent_id === agent.id) {
      void fetchAgent()
    }
  }, [agent, fetchAgent]))

  // Inbox count surfaced in the yellow banner. Real API shape (verified
  // 2026-04-28 in internal/api/agent_inbox.go): {
  //   approvals_pending: int, assignments_open: int, escalations_open: int,
  //   peer_messages: PeerMessage[], cost_usd_this_month, ...
  // } — note the *_open keys are COUNTS, not arrays. The first iteration
  // of this code expected escalations: array and rendered nothing.
  const [inbox, setInbox] = useState<InboxSummary>({ count: 0 })
  useEffect(() => {
    if (!agent) return
    let cancelled = false
    fetch(`/api/v1/agents/${agent.id}/inbox?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((data) => {
        if (cancelled || !data) return
        const escalations = Number(data.escalations_open ?? 0)
        const assignments = Number(data.assignments_open ?? 0)
        const approvals = Number(data.approvals_pending ?? 0)
        const peerMessages = Array.isArray(data.peer_messages) ? data.peer_messages.length : 0
        const total = escalations + assignments + approvals + peerMessages
        const parts: string[] = []
        if (escalations) parts.push(`${escalations} escalation${escalations === 1 ? "" : "s"}`)
        if (assignments) parts.push(`${assignments} assignment${assignments === 1 ? "" : "s"}`)
        if (approvals) parts.push(`${approvals} approval${approvals === 1 ? "" : "s"} pending`)
        if (peerMessages) parts.push(`${peerMessages} peer message${peerMessages === 1 ? "" : "s"}`)
        setInbox({ count: total, summary: parts.join(" · ") })
      })
      .catch(() => { /* tolerate */ })
    return () => { cancelled = true }
  }, [agent, workspaceId])

  const patch = useCallback(async (body: Record<string, unknown>) => {
    if (!agent) return
    const res = await fetch(`/api/v1/agents/${agent.id}?workspace_id=${workspaceId}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    })
    if (!res.ok) {
      const text = await res.text()
      throw new Error(text || `HTTP ${res.status}`)
    }
    const updated: AgentRecord = await res.json()
    setAgent(updated)
    onAgentChanged()
  }, [agent, workspaceId, onAgentChanged])

  const handleStop = useCallback(async () => {
    if (!agent) return
    try {
      const res = await fetch(`/api/v1/agents/${agent.id}/stop`, { method: "POST" })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      toast.success("Stop requested")
      void fetchAgent()
    } catch (err) {
      toast.error(`Could not stop: ${err instanceof Error ? err.message : err}`)
    }
  }, [agent, fetchAgent])

  const handleAvatarSave = useCallback(async (next: { avatar_seed: string; avatar_style: string | null }) => {
    if (!agent) return
    try {
      await patch(next)
      toast.success("Avatar updated")
    } catch (err) {
      toast.error(`Could not save avatar: ${err instanceof Error ? err.message : err}`)
    }
  }, [agent, patch])

  const handleDelete = useCallback(async () => {
    if (!agent) return
    if (!confirm(`Delete agent "${agent.name}"? Sessions and runs are kept for 30 days, then purged.`)) return
    try {
      const res = await fetch(`/api/v1/agents/${agent.id}?workspace_id=${workspaceId}`, { method: "DELETE" })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      toast.success(`Agent "${agent.name}" deleted`)
      onAgentChanged()
    } catch (err) {
      toast.error(`Delete failed: ${err instanceof Error ? err.message : err}`)
    }
  }, [agent, onAgentChanged, workspaceId])

  const meta = useMemo(() => {
    if (!agent) return null
    const parts: string[] = []
    if (agent._count?.chats !== undefined) parts.push(`${agent._count.chats} sessions`)
    if (agent._count?.skills !== undefined) parts.push(`${agent._count.skills} skill${agent._count.skills === 1 ? "" : "s"}`)
    if (agent._count?.credentials !== undefined) parts.push(`${agent._count.credentials} credential${agent._count.credentials === 1 ? "" : "s"}`)
    return parts.join(" · ")
  }, [agent])

  if (loading) {
    return <div className="px-6 md:px-8 lg:px-12 py-6 max-w-[1180px] mx-auto w-full"><Skeleton className="h-[600px] w-full rounded-xl" /></div>
  }
  if (error || !agent) {
    return (
      <div className="px-6 md:px-8 lg:px-12 py-12 max-w-[1180px] mx-auto w-full text-center">
        <p className="text-sm text-red-300 mb-2">Could not load agent</p>
        <p className="text-xs text-muted-foreground">{error}</p>
      </div>
    )
  }

  const status = STATUS_BADGE[agent.status] || STATUS_BADGE.IDLE
  const isRunning = agent.status === "RUNNING"
  const isLead = agent.agent_role === "LEAD"
  const crewOptions = [
    { value: "", label: "(no crew)" },
    ...crews.map((c) => ({ value: c.id, label: c.name })),
  ]

  return (
    <div className="px-6 md:px-8 lg:px-12 py-6 space-y-7 max-w-[1180px] mx-auto w-full">
      {/* Header */}
      <motion.header
        layout
        initial={{ opacity: 0, y: 4 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.16, ease: "easeOut" }}
        className="flex items-start gap-5 pb-5 border-b border-white/8"
      >
        <button
          type="button"
          onClick={() => setAvatarPickerOpen(true)}
          className="relative shrink-0 group"
          title="Customize avatar"
        >
          <img
            src={getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style || agent.crew?.avatar_style)}
            alt=""
            className={cn(
              "w-20 h-20 rounded-2xl transition-transform group-hover:scale-[1.03]",
              isRunning && "ring-2 ring-emerald-500/40",
            )}
          />
          <span className="absolute inset-0 rounded-2xl ring-2 ring-blue-400/0 group-hover:ring-blue-400/40 transition-all pointer-events-none" />
        </button>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1">
            <h1 className="text-2xl font-semibold">
              <EditableField value={agent.name} onSave={(v) => patch({ name: v })} placeholder="Name…" />
            </h1>
            <span className={cn("text-[11px] flex items-center gap-1.5 px-2 py-0.5 rounded-full border shrink-0", status.className)}>
              <span className={cn("w-1.5 h-1.5 rounded-full", isRunning ? "bg-emerald-400 animate-pulse" : "bg-current")} />
              {status.label}
            </span>
          </div>
          <div className="flex items-center gap-2 text-sm text-muted-foreground flex-wrap">
            <code className="text-foreground/80 text-xs px-1.5 py-0.5 rounded bg-zinc-900 border border-white/8">
              {agent.slug}
            </code>
            <span className="text-muted-foreground/50">·</span>
            {agent.role_title && <span>{agent.role_title}</span>}
            {agent.crew && (
              <>
                <span className="text-muted-foreground/50">·</span>
                <button
                  type="button"
                  onClick={() => onSelectCrew(agent.crew!.slug)}
                  className="text-fuchsia-300 hover:underline text-xs"
                >
                  {agent.crew.name}
                </button>
              </>
            )}
          </div>
          {meta && (
            <div className="text-xs text-muted-foreground mt-1.5">{meta}</div>
          )}
        </div>
        <div className="flex items-center gap-2 shrink-0">
          {isRunning && (
            <button
              type="button"
              onClick={handleStop}
              className="px-3 py-2 rounded-lg bg-red-500/15 hover:bg-red-500/25 text-red-300 border border-red-500/30 text-sm font-medium flex items-center gap-1.5"
              title="Stop running agent"
            >
              <Square className="h-3 w-3 fill-current" />
              Stop
            </button>
          )}
          <Link
            href={`/chat/${encodeURIComponent(agent.slug)}`}
            className="px-3.5 py-2 rounded-lg bg-blue-500 hover:bg-blue-400 text-white text-sm font-medium flex items-center gap-2"
          >
            <MessageSquare className="h-3.5 w-3.5" />
            Chat
          </Link>
          <button
            type="button"
            className="p-2 rounded-lg border border-white/10 hover:bg-white/5 text-muted-foreground"
            title="More actions"
          >
            <MoreHorizontal className="h-3.5 w-3.5" />
          </button>
        </div>
      </motion.header>

      <AvatarPickerDialog
        open={avatarPickerOpen}
        onOpenChange={setAvatarPickerOpen}
        agentName={agent.name}
        seed={agent.avatar_seed}
        style={agent.avatar_style}
        crewStyle={agent.crew?.avatar_style ?? null}
        onSave={handleAvatarSave}
      />

      <InboxBanner agentId={agent.id} count={inbox.count} summary={inbox.summary} />

      {/* Profile */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-lg font-semibold">Profile</h2>
          <span className="text-[10px] text-muted-foreground">
            updated {new Date(agent.updated_at).toLocaleDateString()}
          </span>
        </div>
        <div className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
          <Row label="Name">
            <EditableField value={agent.name} onSave={(v) => patch({ name: v })} />
          </Row>
          <Row label="Slug">
            <EditableField value={agent.slug} onSave={(v) => patch({ slug: v })} mono />
          </Row>
          <Row label="Role title">
            <EditableField value={agent.role_title} onSave={(v) => patch({ role_title: v })} />
          </Row>
          <Row label="Description" align="start">
            <EditableField value={agent.description} onSave={(v) => patch({ description: v })} />
          </Row>
          <Row label="Crew">
            <EditableField
              value={agent.crew_id ?? ""}
              onSave={(v) => patch({ crew_id: v || null })}
              options={crewOptions}
              format={(_v) => agent.crew?.name ?? "(no crew)"}
            />
          </Row>
          <Row label="Agent role">
            <EditableField
              value={agent.agent_role}
              onSave={(v) => patch({ agent_role: v })}
              options={[...ROLE_OPTIONS]}
              format={(v) => ROLE_OPTIONS.find((o) => o.value === v)?.label ?? v}
            />
          </Row>
          {/* Lead mode — only for LEAD agents. Frontend marker; backend
              orchestration logic is a separate PR. */}
          {isLead && (
            <Row label="Lead mode" align="center">
              <EditableField
                value={agent.lead_mode || "active"}
                onSave={(v) => patch({ lead_mode: v })}
                options={[
                  { value: "active", label: "Active (orchestrates crew)" },
                  { value: "passive", label: "Passive (frontend only)" },
                ]}
                format={(v) => (v === "active" ? "Active" : "Passive")}
              />
            </Row>
          )}
        </div>
      </section>

      <SystemPromptEditor
        value={agent.system_prompt}
        onSave={(v) => patch({ system_prompt: v })}
        updatedHint={`updated ${new Date(agent.updated_at).toLocaleDateString()}`}
      />

      {/* Runtime + Advanced */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Runtime</h2>
        <div className="rounded-xl border border-white/8 bg-card">
          <div className="divide-y divide-white/5">
            <Row label="Adapter">
              <EditableField
                value={agent.cli_adapter}
                onSave={(v) => patch({ cli_adapter: v })}
                options={[...ADAPTER_OPTIONS]}
                format={(v) => ADAPTER_OPTIONS.find((o) => o.value === v)?.label ?? v}
              />
            </Row>
            <Row label="Model">
              <EditableField
                value={agent.llm_model ?? ""}
                onSave={(v) => patch({ llm_model: v })}
                placeholder="claude-haiku-4-5"
              />
            </Row>
            <Row label="Provider" align="center">
              <span className="text-sm text-foreground/80">{agent.llm_provider || "—"}</span>
            </Row>
          </div>
          <button
            type="button"
            onClick={() => setShowAdvanced((v) => !v)}
            className="w-full px-4 py-2.5 flex items-center gap-2 text-xs text-muted-foreground hover:bg-white/[0.03] hover:text-foreground border-t border-white/5 transition-colors"
          >
            <ChevronDown
              className={cn("h-3 w-3 transition-transform duration-200", !showAdvanced && "-rotate-90")}
            />
            Advanced (LLM tuning, tools, memory, webhook, hooks)
          </button>
          {showAdvanced && (
            <motion.div
              initial={{ opacity: 0, height: 0 }}
              animate={{ opacity: 1, height: "auto" }}
              exit={{ opacity: 0, height: 0 }}
              transition={{ duration: 0.18, ease: "easeOut" }}
              className="divide-y divide-white/5 border-t border-white/5 overflow-hidden">
              <Row label="Timeout (s)">
                <EditableField
                  value={String(agent.timeout_seconds)}
                  onSave={(v) => patch({ timeout_seconds: Number(v) })}
                />
              </Row>
              <Row label="Tool profile">
                <EditableField
                  value={agent.tool_profile}
                  onSave={(v) => patch({ tool_profile: v })}
                  options={[...TOOL_PROFILE_OPTIONS]}
                  format={(v) => TOOL_PROFILE_OPTIONS.find((o) => o.value === v)?.label ?? v}
                />
              </Row>
              <Row label="Tools enabled" align="start">
                <div className="flex flex-wrap items-center gap-1">
                  {(agent.cli_tools && agent.cli_tools.length > 0) ? (
                    agent.cli_tools.slice(0, 6).map((t) => (
                      <span key={t} className="text-[10px] px-1.5 py-0.5 rounded bg-zinc-800 border border-white/10 text-foreground/80">
                        {t}
                      </span>
                    ))
                  ) : (
                    <em className="text-sm text-muted-foreground italic">(default for tool profile)</em>
                  )}
                  {agent.cli_tools && agent.cli_tools.length > 6 && (
                    <span className="text-[10px] text-muted-foreground">+ {agent.cli_tools.length - 6} more</span>
                  )}
                </div>
              </Row>
              <Row label="Memory">
                <button
                  type="button"
                  onClick={() => patch({ memory_enabled: !agent.memory_enabled })}
                  className={cn(
                    "relative inline-flex items-center w-9 h-5 rounded-full transition-colors",
                    agent.memory_enabled ? "bg-emerald-600/70" : "bg-zinc-700",
                  )}
                  aria-pressed={agent.memory_enabled}
                >
                  <span
                    className={cn(
                      "absolute w-4 h-4 rounded-full bg-white transition-transform",
                      agent.memory_enabled ? "translate-x-[18px]" : "translate-x-0.5",
                    )}
                  />
                </button>
                <span className="text-sm text-muted-foreground ml-2">
                  {agent.memory_enabled ? "enabled" : "disabled"}
                </span>
              </Row>
              <Row label="Hooks" align="center">
                <span className="text-sm text-muted-foreground">
                  Manage via CLI:{" "}
                  <code className="text-foreground/80">crewship hooks list</code>
                  {" / "}
                  <code className="text-foreground/80">enable</code>
                  {" / "}
                  <code className="text-foreground/80">disable</code>
                </span>
              </Row>
              <Row label="Webhook" align="center">
                <span className="text-sm text-muted-foreground">
                  Manage via CLI: <code className="text-foreground/80">crewship agent webhook {agent.slug}</code>
                </span>
              </Row>
            </motion.div>
          )}
        </div>
      </section>

      <ScheduleEditor
        cron={agent.schedule_cron}
        prompt={agent.schedule_prompt}
        enabled={Boolean(agent.schedule_enabled)}
        lastRun={agent.schedule_last_run ?? null}
        nextRun={agent.schedule_next_run ?? null}
        onSave={(s) => patch({
          schedule_cron: s.cron || null,
          schedule_prompt: s.prompt || null,
          schedule_enabled: s.enabled,
        })}
      />

      {/* Skills + Credentials side-by-side */}
      <section className="grid grid-cols-1 md:grid-cols-2 gap-3">
        <CountCard
          label="Skills"
          count={agent._count?.skills ?? 0}
          hint={<>CLI: <code className="text-foreground/80">crewship skill assign &lt;skill&gt; {agent.slug}</code></>}
        />
        <CountCard
          label="Credentials"
          count={agent._count?.credentials ?? 0}
          hint={<>CLI: <code className="text-foreground/80">crewship credential assign &lt;name&gt; {agent.slug}</code></>}
        />
      </section>

      {/* Activity */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-lg font-semibold">Activity</h2>
          <Link href={`/journal?agent_id=${encodeURIComponent(agent.id)}`} className="text-xs text-blue-300 hover:underline">
            View all →
          </Link>
        </div>
        <div className="rounded-xl border border-white/8 bg-card max-h-[400px] overflow-hidden">
          <CrewActivityFeed
            workspaceId={workspaceId}
            agentId={agent.id}
          />
        </div>
      </section>

      {/* Danger */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold text-red-400">Danger zone</h2>
        <div className="rounded-xl border border-red-500/30 bg-red-500/5 p-4 flex items-center justify-between">
          <div>
            <div className="text-sm font-medium">Delete this agent</div>
            <div className="text-xs text-muted-foreground">
              Sessions, runs, and journal entries are kept for 30 days then purged.
            </div>
          </div>
          <button
            type="button"
            onClick={handleDelete}
            className="text-xs px-3 py-1.5 rounded bg-red-500/20 text-red-300 border border-red-500/40 hover:bg-red-500/30"
          >
            Delete {agent.name}
          </button>
        </div>
      </section>
    </div>
  )
}

// =============================================================================
// Internal helpers
// =============================================================================

function Row({
  label,
  align = "center",
  children,
}: {
  label: string
  align?: "center" | "start"
  children: React.ReactNode
}) {
  return (
    <div className={cn(
      "grid grid-cols-[180px_1fr] gap-4 px-4 py-2.5",
      align === "center" ? "items-center" : "items-start",
    )}>
      <span className="text-xs text-muted-foreground">{label}</span>
      <div className="flex items-center gap-2 min-w-0">{children}</div>
    </div>
  )
}

function CountCard({ label, count, hint }: { label: string; count: number; hint: React.ReactNode }) {
  return (
    <div className="rounded-xl border border-white/8 bg-card p-4">
      <div className="flex items-baseline justify-between">
        <h3 className="text-sm font-semibold">{label}</h3>
        <span className="text-xl font-semibold">{count}</span>
      </div>
      <div className="text-[11px] text-muted-foreground mt-2">{hint}</div>
    </div>
  )
}
