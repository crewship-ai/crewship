"use client"

import { useCallback, useEffect, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { motion } from "motion/react"
import { toast } from "sonner"
import {
  ArrowUpRight, Bot, Brain, CheckCircle2, Clock, FolderTree, MessageSquare,
  MoreHorizontal, RotateCcw, Square, Trash2,
} from "lucide-react"
import { AnthropicIcon, GeminiIcon, OpenAIIcon } from "@/components/icons/provider-icons"
import { AvatarPickerDialog } from "@/components/features/crews/avatar-picker-dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Pill } from "@/components/ui/detail"
import { StatusPill } from "@/components/ui/status-pill"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { cn } from "@/lib/utils"
import { isGhost, effectiveStatus, ttlRemaining, latestHireReason } from "@/lib/agent-ephemeral"
import { apiFetch } from "@/lib/api-fetch"
import { entityHref } from "@/lib/entity-links"
import { getModelLabel } from "@/lib/cli-adapters"

import {
  CanvasShell,
  CanvasTabs,
  CanvasTabPanel,
  useEntityFetch,
  usePatchEntity,
  useResetTabOnSlugChange,
} from "./canvas-base"
import { OverviewTab } from "./agent-canvas-tabs/overview-tab"
import { ConfigTab } from "./agent-canvas-tabs/config-tab"
import { MemoryTab } from "./agent-canvas-tabs/memory-tab"
import type {
  AgentRecord,
  ChatRow as ChatRowType,
  InboxSummary,
  PeerMessageRow as PeerMessageRowType,
  RunRow as RunRowType,
} from "./agent-canvas-tabs/types"

export type { ChatRow, RunRow, AgentSkillRow, AgentCredRow, PeerMessageRow } from "./agent-canvas-tabs/types"

// Two entries, not six. Four of the old tabs were relations the agent merely
// points at; they hang off the overview's reach strip now, so a reader picks
// between "what is going on" and "how is it set up" instead of six nouns.
type AgentTab = "overview" | "config"

const TABS: Array<{ id: AgentTab; label: string }> = [
  { id: "overview", label: "Overview" },
  { id: "config", label: "Configuration" },
]

/** Brand mark for the model in the identity line. */
function providerMark(provider: string | null | undefined) {
  const p = (provider ?? "").toUpperCase()
  if (p === "OPENAI") return <OpenAIIcon className="h-3 w-3 shrink-0" />
  if (p === "GOOGLE") return <GeminiIcon className="h-3 w-3 shrink-0 text-[#4285F4]" />
  if (p === "ANTHROPIC") return <AnthropicIcon className="h-3 w-3 shrink-0 text-[#D97757]" />
  return <Bot className="h-3 w-3 shrink-0 text-muted-foreground" />
}

export interface AgentCanvasProps {
  workspaceId: string
  agentSlug: string
  /** Crews list passed for the Crew dropdown in Profile section. */
  crews: { id: string; name: string; slug: string }[]
  onAgentChanged: (nextSlug?: string) => void
  onSelectCrew: (slug: string | null) => void
  /** Open the bottom panel pre-targeted to the Files tab. Wired by CrewsLayout. */
  onOpenFiles?: () => void
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
  const router = useRouter()
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
  const [avatarPickerOpen, setAvatarPickerOpen] = useState(false)
  const [memoryOpen, setMemoryOpen] = useState(false)

  // Reset to Overview when switching agents.
  useResetTabOnSlugChange<AgentTab>(agentSlug, setTab, "overview")

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
    apiFetch(`/api/v1/agents/${agentId}/inbox?workspace_id=${workspaceId}`)
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
    apiFetch(`/api/v1/agents/${agentId}/runs?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((data: RunRowType[] | null) => {
        if (!cancelled && Array.isArray(data)) setRuns(data)
      })
      .catch(() => { /* tolerate */ })
    apiFetch(`/api/v1/agents/${agentId}/chats?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((data: ChatRowType[] | null) => {
        if (!cancelled && Array.isArray(data)) setChats(data)
      })
      .catch(() => { /* tolerate */ })
    return () => { cancelled = true }
  }, [agentId, workspaceId])

  const patch = usePatchEntity<AgentRecord>({
    workspaceId,
    entity: agent,
    patchUrl: (a) => `/api/v1/agents/${a.id}`,
    setEntity: setAgent,
    // Hand the new slug up. Editing the Slug field used to orphan the page:
    // the URL still said ?agent=<old>, the refetched list no longer had it,
    // and the stale-slug watcher toasted "not found" and dropped the user on
    // the empty roster — for a rename that had just succeeded.
    onChanged: (updated) => onAgentChanged(updated.slug),
  })


  const handleStop = useCallback(async () => {
    if (!agent) return
    try {
      const res = await apiFetch(`/api/v1/agents/${agent.id}/stop`, { method: "POST" })
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
      const res = await apiFetch(
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
      const res = await apiFetch(
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

  const [confirmDelete, setConfirmDelete] = useState(false)
  const handleDelete = useCallback(async () => {
    if (!agent) return
    try {
      const res = await apiFetch(`/api/v1/agents/${agent.id}?workspace_id=${workspaceId}`, { method: "DELETE" })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      toast.success(`Agent "${agent.name}" deleted`)
      onAgentChanged()
    } catch (err) {
      toast.error(`Delete failed: ${err instanceof Error ? err.message : err}`)
      throw err
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
  const isRunning = agent.status === "RUNNING" && !ghost
  const isPendingHire = agent.ephemeral === true && agent.status === "PENDING_REVIEW" && !ghost
  const ttl = agent.ephemeral && !ghost ? ttlRemaining(agent.expires_at) : ""
  const hireReason = latestHireReason(agent.hire_reason)

  return (
    <CanvasShell loading={false} error={null} notLoadedLabel="">
      {/* Header — variant C: name and actions share the top line, state and
          identity drop to the second. The name stops competing with the
          buttons, and the whole thing costs two lines instead of four. */}
      <motion.header
        layout
        initial={{ opacity: 0, y: 4 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.16, ease: "easeOut" }}
        className="border-b border-border pb-3"
      >
      {/* The avatar stands beside BOTH header lines rather than inside the
          first. It used to be a 32px square next to a 24px name, which made
          the face — now that agents wear real portraits — the smallest thing
          in its own title bar. At 48px it spans the name and the status line
          together, which is also what makes a style like Croodles legible at
          all: those are line drawings, and a line drawing at 20px is a smudge. */}
      <div className="flex items-start gap-3">
        <button
          type="button"
          onClick={() => setAvatarPickerOpen(true)}
          className="group mt-0.5 shrink-0"
          title="Change avatar"
        >
          <AgentAvatar
            seed={agent.avatar_seed || agent.name}
            style={agent.avatar_style || agent.crew?.avatar_style}
            agentId={agent.id}
            avatarUrl={agent.avatar_url}
            className={cn(
              "h-12 w-12 rounded-xl transition-transform group-hover:scale-[1.06]",
              isRunning && "ring-2 ring-success/40",
            )}
          />
        </button>

        <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="type-title leading-none">{agent.name}</h1>
          <span className="type-row font-mono text-muted-foreground">{agent.slug}</span>

          <div className="ml-auto flex items-center gap-2">
            <Button asChild variant="soft" size="sm">
              <Link href={`/chat/${encodeURIComponent(agent.slug)}`}>
                <MessageSquare />
                Chat
              </Link>
            </Button>
            {isRunning && (
              <Button
                variant="outline"
                size="sm"
                onClick={handleStop}
                className="border-destructive/30 bg-destructive/15 text-destructive hover:bg-destructive/25 hover:text-destructive"
              >
                <Square className="fill-current" />
                Stop
              </Button>
            )}
            {isPendingHire && (
              <Button
                variant="outline"
                size="sm"
                onClick={handleApproveHire}
                className="border-success/30 bg-success/20 text-success hover:bg-success/30 hover:text-success"
              >
                <CheckCircle2 />
                Approve hire
              </Button>
            )}
            {ghost && (
              <Button variant="outline" size="sm" onClick={handleRehire}>
                <RotateCcw />
                Re-hire
              </Button>
            )}
            {onOpenFiles && (
              <Button variant="outline" size="sm" onClick={onOpenFiles}>
                <FolderTree />
                Files
              </Button>
            )}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="icon-sm" title="More actions" aria-label="Agent actions">
                  <MoreHorizontal />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="min-w-[220px]">
                <DropdownMenuLabel className="type-meta text-muted-foreground">{agent.name}</DropdownMenuLabel>
                <DropdownMenuSeparator />
                {/* Memory lives here rather than on a tab. It is four markdown
                    files — AGENT.md, CREW.md, PERSONA.md, peer cards — that
                    almost nobody edits, and it spent a spell bolted to the
                    bottom of Configuration where it dwarfed every actual
                    setting. Reachable, not resident. */}
                <DropdownMenuItem onClick={() => setMemoryOpen(true)} className="flex items-center gap-2">
                  <Brain className="h-4 w-4" />
                  <span>Memory</span>
                  <span className="type-meta ml-auto text-muted-foreground-soft">
                    {agent.memory_enabled ? "on" : "off"}
                  </span>
                </DropdownMenuItem>
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
                  onClick={() => setConfirmDelete(true)}
                  className="flex items-center gap-2 text-destructive focus:bg-destructive/10 focus:text-destructive"
                >
                  <Trash2 className="h-4 w-4" />
                  <span>Delete agent</span>
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>

        <div className="mt-1.5 flex flex-wrap items-center gap-2">
          <StatusPill status={statusKey} live={isRunning} size="md" />
          {agent.agent_role === "LEAD" && <Pill tone="purple">Lead</Pill>}
          {agent.ephemeral && !ghost && (
            <Pill tone="warn">
              <Clock className="h-3 w-3" />
              ephemeral{ttl && ` · ${ttl}`}
            </Pill>
          )}
          {/* Role · crew · model is the first thing read after the name, so it
              takes the row role rather than the caption one. type-meta is for
              a timestamp in the corner of a card, not for the line that says
              what this agent IS. */}
          <span className="type-row flex flex-wrap items-center gap-x-2 gap-y-1 font-mono text-muted-foreground">
            {agent.role_title && <span>{agent.role_title}</span>}
            {agent.crew && (
              <>
                {agent.role_title && <span className="opacity-40">·</span>}
                <Link href={entityHref({ kind: "crew", slug: agent.crew.slug })} className="text-primary hover:underline">
                  {agent.crew.name}
                </Link>
              </>
            )}
            {agent.llm_model && (
              <>
                <span className="opacity-40">·</span>
                <span className="inline-flex items-center gap-1.5">
                  {providerMark(agent.llm_provider)}
                  <span title={agent.llm_model}>{getModelLabel(agent.llm_model)}</span>
                </span>
              </>
            )}
          </span>
        </div>
        </div>
      </div>

        {agent.description && (
          <p className="type-row mt-2 max-w-prose text-muted-foreground">{agent.description}</p>
        )}
        {isPendingHire && (
          <p className="type-row mt-2 inline-flex items-start gap-2 rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-warn/90">
            <Clock className="mt-px h-3.5 w-3.5 shrink-0" />
            <span>
              Requesting to join <b className="font-medium">{agent.crew?.name ?? "crew"}</b> — approve to add it.
              {hireReason && <> · {hireReason}</>}
            </span>
          </p>
        )}
        {ghost && <p className="type-row mt-2 text-muted-foreground">Expired ephemeral hire — re-hire to bring it back.</p>}
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

      {/* Menu — two entries plus the one link that leaves the screen */}
      <div className="flex items-center gap-4 border-b border-border">
        <CanvasTabs<AgentTab> tabs={TABS} active={tab} onChange={setTab} idPrefix="agent-canvas" label="Agent sections" />
        <Link
          href={`/journal?agent=${encodeURIComponent(agent.slug)}`}
          className="ml-auto inline-flex shrink-0 items-center gap-1 pb-2 text-label text-muted-foreground transition-colors hover:text-primary"
        >
          Journal
          <ArrowUpRight className="h-3 w-3" />
        </Link>
      </div>

      <CanvasTabPanel idPrefix="agent-canvas" active={tab} className="space-y-6">
      {tab === "overview" && (
        <OverviewTab
          workspaceId={workspaceId}
          agent={agent}
          crews={crews}
          inbox={inbox}
          chats={chats}
          runs={runs}
          peerMessages={peerMessages}
          patch={patch}
          onStop={isRunning ? handleStop : undefined}
          // The "Waiting on your decision" notice used to render with no
          // button at all (audit-fleet.md §6 P1.3): it stated the agent was
          // stopped and offered nothing. The inbox filtered to this agent is
          // where the decision is taken.
          onOpenInbox={() => router.push(entityHref({ kind: "inbox", agentSlug: agent.slug }))}
          onOpenConfig={() => setTab("config")}
          onAgentChanged={onAgentChanged}
        />
      )}

      {/* The old settings panel is gone. Everything it carried is a card in
          ConfigTab now, and the three controls it duplicated — timeout, tool
          profile, memory — are back to one each. Deletion stays in the ··· menu
          beside Chat, which is where a destructive action belongs. */}
      {tab === "config" && (
        <ConfigTab agent={agent} crews={crews} patch={patch} onSelectCrew={onSelectCrew} />
      )}
      </CanvasTabPanel>

      <Dialog open={memoryOpen} onOpenChange={setMemoryOpen}>
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-[820px]">
          <DialogHeader>
            <DialogTitle>Memory</DialogTitle>
            <DialogDescription>
              What {agent.name} carries between sessions. The switch that turns it on is in
              Configuration, under Model and run.
            </DialogDescription>
          </DialogHeader>
          <MemoryTab
            agentId={agent.id}
            agentSlug={agent.slug}
            crewId={agent.crew_id ?? undefined}
            workspaceId={workspaceId}
          />
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete agent ${agent.name}?`}
        description="The agent stops and leaves its crew. It cannot be undone."
        consequences={[
          { tone: "lost", text: "Its credential and connector grants are removed" },
          { tone: "kept", text: "Sessions and runs stay readable for 30 days, then are purged" },
          { tone: "kept", text: "Issues it was assigned stay open, unassigned" },
        ]}
        confirmLabel="Delete agent"
        destructive
        onConfirm={handleDelete}
      />
    </CanvasShell>
  )
}

// =============================================================================
// Layout helpers
// =============================================================================



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
