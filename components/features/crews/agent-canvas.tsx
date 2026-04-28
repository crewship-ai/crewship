"use client"

import { useCallback, useEffect, useRef, useState } from "react"
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
import { fetchWithRetry } from "@/lib/fetch-with-retry"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { cn } from "@/lib/utils"

import {
  PeersCard,
  RecentRunsCard,
  RecentSessionsCard,
  WorkspaceTab,
} from "./agent-canvas-cards"
import {
  CredentialsManager,
  SkillsManager,
} from "./agent-canvas-managers"

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

type AgentTab = "overview" | "workspace" | "skills" | "activity" | "settings"

const TABS: Array<{ id: AgentTab; label: string }> = [
  { id: "overview", label: "Overview" },
  { id: "workspace", label: "Workspace" },
  { id: "skills", label: "Skills & Tools" },
  { id: "activity", label: "Activity" },
  { id: "settings", label: "Settings" },
]

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

interface InboxSummary { count: number; summary?: string; cost?: number }

export interface ChatRow {
  id: string
  title: string | null
  message_count: number
  status: string
  started_at: string
  ended_at: string | null
  created_at: string
}

export interface RunRow {
  id: string
  status: string
  trigger_type: string
  started_at: string | null
  finished_at: string | null
  error_message: string | null
  created_at: string
}

export interface AgentSkillRow {
  id: string
  skill_id: string
  enabled: boolean
  skill: { id: string; name: string; slug: string; display_name?: string | null; description?: string | null; category?: string | null; icon?: string | null; version?: string | null }
}

export interface AgentCredRow {
  id: string
  credential_id: string
  credential_name: string
  credential_type: string
  credential_provider: string
  credential_status: string
  env_var_name: string
  priority: number
  created_at: string
}

export interface PeerMessageRow {
  id?: string
  from_agent_id?: string
  from_agent_name?: string
  from_agent_slug?: string
  preview?: string
  created_at?: string
}

export interface AgentCanvasProps {
  workspaceId: string
  agentSlug: string
  /** Crews list passed for the Crew dropdown in Profile section. */
  crews: { id: string; name: string; slug: string }[]
  onAgentChanged: () => void
  onSelectCrew: (slug: string | null) => void
  /** Open the bottom panel pre-targeted to the Files tab. Wired by CrewsLayout. */
  onOpenFiles?: () => void
}

const STATUS_BADGE: Record<string, { label: string; className: string; pulse?: boolean }> = {
  RUNNING: { label: "running", className: "bg-emerald-500/15 text-emerald-300 border-emerald-500/30", pulse: true },
  IDLE: { label: "idle", className: "bg-zinc-700/40 text-muted-foreground border-white/10" },
  ERROR: { label: "error", className: "bg-red-500/15 text-red-300 border-red-500/30" },
  STOPPED: { label: "stopped", className: "bg-amber-500/15 text-amber-300 border-amber-500/30" },
}

/**
 * Agent canvas — drives the right pane when ?agent=<slug> is selected.
 * Tabbed layout: Overview / Workspace / Skills & Tools / Activity / Settings.
 *
 * Header always visible (avatar, name, slug, role, crew, status, Chat/Stop).
 * 6-stat strip below header (Sessions / Runs / Cost-30d / Skills / Creds / Last).
 * Tabs below let users focus on one concern at a time without scrolling 600+ lines.
 */
export function AgentCanvas({
  workspaceId,
  agentSlug,
  crews,
  onAgentChanged,
  onSelectCrew,
  onOpenFiles,
}: AgentCanvasProps) {
  const [agent, setAgent] = useState<AgentRecord | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<AgentTab>("overview")
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [avatarPickerOpen, setAvatarPickerOpen] = useState(false)

  // Reset to Overview when switching agents.
  const lastAgentSlug = useRef(agentSlug)
  useEffect(() => {
    if (lastAgentSlug.current !== agentSlug) {
      setTab("overview")
      lastAgentSlug.current = agentSlug
    }
  }, [agentSlug])

  const fetchAgent = useCallback(async (signal?: AbortSignal) => {
    try {
      const listRes = await fetchWithRetry(`/api/v1/agents?workspace_id=${workspaceId}`, { signal })
      if (!listRes.ok) throw new Error(`agent list failed (${listRes.status})`)
      const list: AgentRecord[] = await listRes.json()
      const match = list.find((a) => a.slug === agentSlug)
      if (!match) throw new Error(`agent "${agentSlug}" not found in workspace`)
      const detailRes = await fetchWithRetry(`/api/v1/agents/${match.id}?workspace_id=${workspaceId}`, { signal })
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

  useRealtimeEvent("agent.status", useCallback((event) => {
    if (agent && event.payload?.agent_id === agent.id) {
      void fetchAgent()
    }
  }, [agent, fetchAgent]))

  // Inbox + cost summary (used by stats strip + InboxBanner).
  const [inbox, setInbox] = useState<InboxSummary>({ count: 0 })
  const [peerMessages, setPeerMessages] = useState<PeerMessageRow[]>([])
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
        const peers: PeerMessageRow[] = Array.isArray(data.peer_messages) ? data.peer_messages : []
        const total = escalations + assignments + approvals + peers.length
        const parts: string[] = []
        if (escalations) parts.push(`${escalations} escalation${escalations === 1 ? "" : "s"}`)
        if (assignments) parts.push(`${assignments} assignment${assignments === 1 ? "" : "s"}`)
        if (approvals) parts.push(`${approvals} approval${approvals === 1 ? "" : "s"} pending`)
        if (peers.length) parts.push(`${peers.length} peer message${peers.length === 1 ? "" : "s"}`)
        setInbox({ count: total, summary: parts.join(" · "), cost: Number(data.cost_usd_this_month ?? 0) })
        setPeerMessages(peers)
      })
      .catch(() => { /* tolerate */ })
    return () => { cancelled = true }
  }, [agent, workspaceId])

  // Runs + chats are fetched once at canvas-level and shared with the
  // overview tab's Recent cards (avoids three separate hits to the
  // same endpoints + the rate-limiter pile-up that used to follow).
  const [runs, setRuns] = useState<RunRow[] | null>(null)
  const [chats, setChats] = useState<ChatRow[] | null>(null)
  useEffect(() => {
    if (!agent) return
    let cancelled = false
    fetch(`/api/v1/agents/${agent.id}/runs?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((data: RunRow[] | null) => {
        if (!cancelled && Array.isArray(data)) setRuns(data)
      })
      .catch(() => { /* tolerate */ })
    fetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((data: ChatRow[] | null) => {
        if (!cancelled && Array.isArray(data)) setChats(data)
      })
      .catch(() => { /* tolerate */ })
    return () => { cancelled = true }
  }, [agent, workspaceId])
  const runsCount = runs?.length ?? null

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
    <div className="px-6 md:px-8 lg:px-12 py-6 space-y-6 max-w-[1180px] mx-auto w-full">
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
          <div className="flex items-center gap-2 text-sm text-muted-foreground flex-wrap mb-3">
            <code className="text-foreground/80 text-xs px-1.5 py-0.5 rounded bg-zinc-900 border border-white/8">
              {agent.slug}
            </code>
            {agent.role_title && (
              <>
                <span className="text-muted-foreground/50">·</span>
                <span>{agent.role_title}</span>
              </>
            )}
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
          {/* 6-stat strip */}
          <div className="grid grid-cols-3 sm:grid-cols-6 gap-2 max-w-[640px]">
            <StatTile label="Sessions" value={agent._count?.chats ?? 0} />
            <StatTile label="Runs" value={runsCount ?? "–"} />
            <StatTile label="Cost · 30d" value={inbox.cost !== undefined ? formatCost(inbox.cost) : "–"} />
            <StatTile label="Skills" value={agent._count?.skills ?? 0} />
            <StatTile label="Creds" value={agent._count?.credentials ?? 0} />
            <StatTile label="Last active" value={agent.last_active_at ? formatRelative(agent.last_active_at) : "—"} />
          </div>
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

      {/* Tabs */}
      <div className="flex items-center gap-5 border-b border-white/8 -mx-6 md:-mx-8 lg:-mx-12 px-6 md:px-8 lg:px-12 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        {TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            onClick={() => setTab(t.id)}
            aria-selected={tab === t.id}
            className={cn(
              "text-sm py-2 px-1 border-b-2 transition-colors shrink-0",
              tab === t.id
                ? "border-blue-400 text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground/80",
            )}
          >
            {t.label}
          </button>
        ))}
      </div>

      {/* Tab content */}
      {tab === "overview" && (
        <div className="space-y-7">
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
                  onSave={(v) => {
                    // COORDINATOR is workspace-wide and must not have a
                    // crew. Clear crew_id atomically to keep the invariant
                    // intact (otherwise the backend may either reject the
                    // patch or silently leave the agent in an inconsistent
                    // state).
                    if (v === "COORDINATOR" && agent.crew_id) {
                      return patch({ agent_role: v, crew_id: null })
                    }
                    return patch({ agent_role: v })
                  }}
                  options={[...ROLE_OPTIONS]}
                  format={(v) => ROLE_OPTIONS.find((o) => o.value === v)?.label ?? v}
                />
              </Row>
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

          {/* Recent sessions + Recent runs */}
          <section className="grid md:grid-cols-2 gap-4">
            <RecentSessionsCard agentSlug={agent.slug} chats={chats} />
            <RecentRunsCard agentId={agent.id} runs={runs} />
          </section>

          {/* Crew peers (LEAD/COORDINATOR only — uses inbox.peer_messages) */}
          {(isLead || agent.agent_role === "COORDINATOR") && peerMessages.length > 0 && (
            <PeersCard messages={peerMessages} />
          )}
        </div>
      )}

      {tab === "workspace" && (
        <WorkspaceTab agentId={agent.id} agentSlug={agent.slug} onOpenFiles={onOpenFiles} />
      )}

      {tab === "skills" && (
        <div className="space-y-7">
          <SkillsManager agentId={agent.id} agentSlug={agent.slug} workspaceId={workspaceId} onChange={onAgentChanged} />
          <CredentialsManager agentId={agent.id} agentSlug={agent.slug} workspaceId={workspaceId} onChange={onAgentChanged} />
        </div>
      )}

      {tab === "activity" && (
        <section className="space-y-3">
          <div className="flex items-baseline justify-between">
            <h2 className="text-lg font-semibold">Activity</h2>
            <Link href={`/journal?agent_id=${encodeURIComponent(agent.id)}`} className="text-xs text-blue-300 hover:underline">
              View all →
            </Link>
          </div>
          <div className="rounded-xl border border-white/8 bg-card max-h-[640px] overflow-hidden">
            <CrewActivityFeed
              workspaceId={workspaceId}
              agentId={agent.id}
            />
          </div>
        </section>
      )}

      {tab === "settings" && (
        <div className="space-y-7">
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
      )}
    </div>
  )
}

// =============================================================================
// Layout helpers
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

function StatTile({ label, value }: { label: string; value: number | string }) {
  return (
    <div className="rounded-lg border border-white/8 bg-card px-2.5 py-1.5">
      <div className="text-[10px] text-muted-foreground uppercase tracking-wide">{label}</div>
      <div className="text-sm text-foreground tabular-nums truncate">{value}</div>
    </div>
  )
}

// =============================================================================
// Recent sessions + runs cards (overview tab)
// =============================================================================


export function formatRelative(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime()
  if (ms < 0) return "just now"
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  const d = Math.floor(h / 24)
  if (d < 30) return `${d}d ago`
  return new Date(iso).toLocaleDateString()
}

export function formatDuration(startIso: string, endIso: string): string {
  const ms = new Date(endIso).getTime() - new Date(startIso).getTime()
  if (!Number.isFinite(ms) || ms < 0) return ""
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const rs = s % 60
  return `${m}m ${rs}s`
}

export function formatCost(usd: number): string {
  if (!Number.isFinite(usd)) return "–"
  if (usd === 0) return "$0.00"
  if (usd < 0.01) return "<$0.01"
  return `$${usd.toFixed(2)}`
}
