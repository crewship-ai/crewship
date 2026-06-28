"use client"

import { useCallback, useEffect, useState } from "react"
import Link from "next/link"
import { motion } from "motion/react"
import { toast } from "sonner"
import {
  MessageSquare, MoreHorizontal, Square,
  Trash2, RotateCcw, CheckCircle2, Clock,
} from "lucide-react"
import { EditableField } from "@/components/shared/editable-field"
import { AvatarPickerDialog } from "@/components/features/crews/avatar-picker-dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { cn } from "@/lib/utils"
import { isGhost, effectiveStatus, ttlRemaining, latestHireReason } from "@/lib/agent-ephemeral"

import {
  CanvasShell,
  CanvasTabs,
  useEntityFetch,
  usePatchEntity,
  useResetTabOnSlugChange,
} from "./canvas-base"
import { ActivityTab } from "./agent-canvas-tabs/activity-tab"
import { OverviewTab } from "./agent-canvas-tabs/overview-tab"
import { SettingsTab } from "./agent-canvas-tabs/settings-tab"
import { SkillsTab } from "./agent-canvas-tabs/skills-tab"
import { MemoryTab } from "./agent-canvas-tabs/memory-tab"
import { WorkspaceTab } from "./agent-canvas-tabs/workspace-tab"
import type {
  AgentRecord,
  ChatRow as ChatRowType,
  InboxSummary,
  PeerMessageRow as PeerMessageRowType,
  RunRow as RunRowType,
} from "./agent-canvas-tabs/types"

export type { ChatRow, RunRow, AgentSkillRow, AgentCredRow, PeerMessageRow } from "./agent-canvas-tabs/types"

type AgentTab = "overview" | "workspace" | "skills" | "memory" | "activity" | "settings"

const TABS: Array<{ id: AgentTab; label: string }> = [
  { id: "overview", label: "Overview" },
  { id: "workspace", label: "Workspace" },
  { id: "skills", label: "Skills & Tools" },
  { id: "memory", label: "Memory" },
  { id: "activity", label: "Activity" },
  { id: "settings", label: "Settings" },
]

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
  PENDING_REVIEW: { label: "pending review", className: "bg-amber-500/15 text-amber-300 border-amber-500/30" },
  EXPIRED: { label: "expired", className: "bg-slate-500/15 text-slate-400 border-slate-500/30" },
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
  const {
    entity: agent,
    setEntity: setAgent,
    loading,
    error,
    refetch: fetchAgent,
  } = useEntityFetch<AgentRecord>({
    workspaceId,
    slug: agentSlug,
    listUrl: "/api/v1/agents",
    detailUrl: (id) => `/api/v1/agents/${id}`,
    matchSlug: (a) => a.slug,
    notFoundMessage: `agent "${agentSlug}" not found in workspace`,
    listErrorMessage: "agent list failed",
    detailErrorMessage: "agent detail failed",
  })

  const [tab, setTab] = useState<AgentTab>("overview")
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [customModelOpen, setCustomModelOpen] = useState(false)
  const [customModelDraft, setCustomModelDraft] = useState("")
  const [avatarPickerOpen, setAvatarPickerOpen] = useState(false)

  // Reset to Overview when switching agents.
  const resetAdvanced = useCallback(() => setShowAdvanced(false), [])
  useResetTabOnSlugChange<AgentTab>(agentSlug, setTab, "overview", resetAdvanced)

  useRealtimeEvent("agent.status", useCallback((event) => {
    if (agent && event.payload?.agent_id === agent.id) {
      void fetchAgent()
    }
  }, [agent, fetchAgent]))

  // Inbox + cost summary (used by stats strip + InboxBanner).
  const [inbox, setInbox] = useState<InboxSummary>({ count: 0 })
  const [peerMessages, setPeerMessages] = useState<PeerMessageRowType[]>([])
  const agentId = agent?.id
  useEffect(() => {
    if (!agentId) return
    let cancelled = false
    // Clear previous agent's data immediately so a stale inbox / peer list
    // never leaks into the next selection while the request is in flight.
    setInbox({ count: 0 })
    setPeerMessages([])
    fetch(`/api/v1/agents/${agentId}/inbox?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((data) => {
        if (cancelled || !data) return
        const escalations = Number(data.escalations_open ?? 0)
        const assignments = Number(data.assignments_open ?? 0)
        const approvals = Number(data.approvals_pending ?? 0)
        const peers: PeerMessageRowType[] = Array.isArray(data.peer_messages) ? data.peer_messages : []
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
  }, [agentId, workspaceId])

  // Runs + chats are fetched once at canvas-level and shared with the
  // overview tab's Recent cards (avoids three separate hits to the
  // same endpoints + the rate-limiter pile-up that used to follow).
  const [runs, setRuns] = useState<RunRowType[] | null>(null)
  const [chats, setChats] = useState<ChatRowType[] | null>(null)
  useEffect(() => {
    if (!agentId) return
    let cancelled = false
    // Reset before fetch so the previous agent's runs/chats don't leak into
    // this canvas while the new request is pending.
    setRuns(null)
    setChats(null)
    fetch(`/api/v1/agents/${agentId}/runs?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((data: RunRowType[] | null) => {
        if (!cancelled && Array.isArray(data)) setRuns(data)
      })
      .catch(() => { /* tolerate */ })
    fetch(`/api/v1/agents/${agentId}/chats?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((data: ChatRowType[] | null) => {
        if (!cancelled && Array.isArray(data)) setChats(data)
      })
      .catch(() => { /* tolerate */ })
    return () => { cancelled = true }
  }, [agentId, workspaceId])
  const runsCount = runs?.length ?? null

  const patch = usePatchEntity<AgentRecord>({
    workspaceId,
    entity: agent,
    patchUrl: (a) => `/api/v1/agents/${a.id}`,
    setEntity: setAgent,
    onChanged: onAgentChanged,
  })

  // Fire-and-forget wrapper for click/key handlers that can't await.
  // Without this the rejected promise becomes an unhandled rejection
  // and the UI shows nothing on save failure.
  const safePatch = useCallback((body: Record<string, unknown>) => {
    patch(body).catch((err) => {
      toast.error(`Save failed: ${err instanceof Error ? err.message : err}`)
    })
  }, [patch])

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

  // Approve a pending ephemeral hire straight from the agent page (same
  // endpoint the inbox uses). workspace_id rides in the query string —
  // the route's RequireWorkspace middleware reads it from there, never
  // the body. The server resolves the blocking inbox waitpoint too.
  const handleApproveHire = useCallback(async () => {
    if (!agent) return
    try {
      const res = await fetch(
        `/api/v1/agents/${agent.id}/approve-hire?workspace_id=${encodeURIComponent(agent.workspace_id)}`,
        { method: "POST", headers: { "Content-Type": "application/json" } },
      )
      if (!res.ok) {
        const body = (await res.json().catch(() => null)) as { error?: string } | null
        throw new Error(body?.error ?? `HTTP ${res.status}`)
      }
      toast.success("Hire approved — agent is live")
      void fetchAgent()
    } catch (err) {
      toast.error(`Approve failed: ${err instanceof Error ? err.message : err}`)
    }
  }, [agent, fetchAgent])

  // Re-hire a ghost (expired) ephemeral agent with a fresh TTL.
  const handleRehire = useCallback(async () => {
    if (!agent) return
    try {
      const res = await fetch(
        `/api/v1/agents/${agent.id}/rehire?workspace_id=${encodeURIComponent(agent.workspace_id)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ ttl_minutes: 60, reason: "rehire from agent page" }),
        },
      )
      if (!res.ok) {
        const body = (await res.json().catch(() => null)) as { error?: string } | null
        throw new Error(body?.error ?? `HTTP ${res.status}`)
      }
      toast.success("Re-hired — agent is live again")
      void fetchAgent()
    } catch (err) {
      toast.error(`Re-hire failed: ${err instanceof Error ? err.message : err}`)
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

  if (loading || error || !agent) {
    return (
      <CanvasShell
        loading={loading}
        error={loading ? null : (error ?? "agent not found")}
        notLoadedLabel="Could not load agent"
      >
        {null}
      </CanvasShell>
    )
  }

  const ghost = isGhost(agent)
  const statusKey = effectiveStatus(agent)
  const status = STATUS_BADGE[statusKey] || STATUS_BADGE.IDLE
  const isRunning = agent.status === "RUNNING" && !ghost
  const isPendingHire = agent.ephemeral === true && agent.status === "PENDING_REVIEW" && !ghost
  const ttl = agent.ephemeral && !ghost ? ttlRemaining(agent.expires_at) : ""
  const hireReason = latestHireReason(agent.hire_reason)

  return (
    <CanvasShell loading={false} error={null} notLoadedLabel="">
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
          <AgentAvatar
            seed={agent.avatar_seed || agent.name}
            style={agent.avatar_style || agent.crew?.avatar_style}
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
              <EditableField value={agent.name} onSave={(v) => patch({ name: v })} ariaLabel="Agent name" placeholder="Name…" />
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
                <span className="text-muted-foreground-soft">·</span>
                <span>{agent.role_title}</span>
              </>
            )}
            {agent.crew && (
              <>
                <span className="text-muted-foreground-soft">·</span>
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
          {/* Ephemeral hire context — what's being approved, TTL, reason */}
          {agent.ephemeral && (
            <div className="mb-3 text-xs">
              {isPendingHire ? (
                <div className="inline-flex items-start gap-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-amber-200/90">
                  <Clock className="h-3.5 w-3.5 mt-0.5 shrink-0" />
                  <span>
                    Requesting to join <span className="font-medium">{agent.crew?.name ?? "this crew"}</span> — approve to add it.
                    {ttl && <> · TTL {ttl}</>}
                    {hireReason && <> · {hireReason}</>}
                  </span>
                </div>
              ) : ghost ? (
                <span className="text-muted-foreground">Expired ephemeral hire — re-hire to bring it back.</span>
              ) : (
                <span className="inline-flex items-center gap-1.5 text-cyan-300/80">
                  <Clock className="h-3 w-3" /> Ephemeral hire{ttl && <span className="text-muted-foreground"> · TTL {ttl}</span>}
                </span>
              )}
            </div>
          )}

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
          {isPendingHire && (
            <button
              type="button"
              onClick={handleApproveHire}
              className="px-3.5 py-2 rounded-lg bg-emerald-500/20 hover:bg-emerald-500/30 text-emerald-300 border border-emerald-500/30 text-sm font-medium flex items-center gap-1.5"
              title="Approve this ephemeral hire — the agent joins the crew and any waiting work resumes"
            >
              <CheckCircle2 className="h-3.5 w-3.5" />
              Approve hire
            </button>
          )}
          {ghost && (
            <button
              type="button"
              onClick={handleRehire}
              className="px-3 py-2 rounded-lg bg-white/5 hover:bg-white/10 text-foreground/80 border border-white/10 text-sm font-medium flex items-center gap-1.5"
              title="Re-hire this expired agent with a fresh TTL"
            >
              <RotateCcw className="h-3 w-3" />
              Re-hire
            </button>
          )}
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
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                type="button"
                className="p-2 rounded-lg border border-white/10 hover:bg-white/5 text-muted-foreground"
                title="More actions"
                aria-label="Agent actions"
              >
                <MoreHorizontal className="h-3.5 w-3.5" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-[220px]">
              <DropdownMenuLabel className="text-xs text-muted-foreground">
                {agent.name}
              </DropdownMenuLabel>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                onClick={() => toast.info("Container restart will land in a follow-up")}
                className="flex items-center gap-2"
              >
                <RotateCcw className="h-4 w-4" />
                <span>Restart container</span>
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                onClick={handleDelete}
                className="flex items-center gap-2 text-destructive focus:text-destructive focus:bg-destructive/10"
              >
                <Trash2 className="h-4 w-4" />
                <span>Delete agent</span>
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
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
      <CanvasTabs<AgentTab> tabs={TABS} active={tab} onChange={setTab} />

      {/* Tab content */}
      {tab === "overview" && (
        <OverviewTab
          agent={agent}
          crews={crews}
          inbox={inbox}
          chats={chats}
          runs={runs}
          peerMessages={peerMessages}
          patch={patch}
        />
      )}

      {tab === "workspace" && (
        <WorkspaceTab agentId={agent.id} agentSlug={agent.slug} onOpenFiles={onOpenFiles} />
      )}

      {tab === "skills" && (
        <SkillsTab
          agentId={agent.id}
          agentSlug={agent.slug}
          agentName={agent.name}
          agentCrew={agent.crew?.name ?? null}
          workspaceId={workspaceId}
          onAgentChanged={onAgentChanged}
        />
      )}

      {tab === "memory" && (
        <MemoryTab
          agentId={agent.id}
          agentSlug={agent.slug}
          crewId={agent.crew_id ?? undefined}
          workspaceId={workspaceId}
        />
      )}

      {tab === "activity" && (
        <ActivityTab workspaceId={workspaceId} agentId={agent.id} />
      )}

      {tab === "settings" && (
        <SettingsTab
          agent={agent}
          patch={patch}
          safePatch={safePatch}
          showAdvanced={showAdvanced}
          setShowAdvanced={setShowAdvanced}
          customModelOpen={customModelOpen}
          setCustomModelOpen={setCustomModelOpen}
          customModelDraft={customModelDraft}
          setCustomModelDraft={setCustomModelDraft}
        />
      )}
    </CanvasShell>
  )
}

// =============================================================================
// Layout helpers
// =============================================================================


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

// `formatDuration(startIso, endIso)` moved to lib/time.ts as
// `formatDurationSpan` (canonical home for time formatters).

export function formatCost(usd: number): string {
  if (!Number.isFinite(usd)) return "–"
  if (usd === 0) return "$0.00"
  if (usd < 0.01) return "<$0.01"
  return `$${usd.toFixed(2)}`
}
