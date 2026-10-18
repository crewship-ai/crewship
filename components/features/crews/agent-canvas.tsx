"use client"

import { useCallback, useEffect, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { toast } from "@/lib/toast"
import { Brain, CalendarClock, ChevronLeft, Clock, Lock, RotateCcw, Trash2 } from "lucide-react"
import { AvatarPickerDialog } from "@/components/features/crews/avatar-picker-dialog"
import { AgentHeadTile } from "./agent-ring-avatar"
import { getModelLabel } from "@/lib/cli-adapters"
import { adapterLabel } from "@/lib/credentials/provider-logins"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { StatusPill } from "@/components/ui/status-pill"
import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"
import { PageError, PageNotFound } from "@/components/layout/page-state"
import { useAbilities } from "@/hooks/use-abilities"
import { useSessionSafe } from "@/hooks/use-auth"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { isGhost, effectiveStatus, ttlRemaining, latestHireReason } from "@/lib/agent-ephemeral"
import { apiFetch } from "@/lib/api-fetch"
import { toApiError } from "@/lib/api-error"
import { storeAgentAvatar } from "@/lib/agent-avatar-persist"
import { stopAgent, stopSuccessMessage } from "@/lib/agent-stop"
import { entityHref } from "@/lib/entity-links"

import {
  CanvasShell,
  CanvasTabs,
  CanvasTabPanel,
  useEntityFetch,
  usePatchEntity,
} from "./canvas-base"
import { OverviewTab } from "./agent-canvas-tabs/overview-tab"
import { CreateAgentDialog } from "./create-agent/create-agent-dialog"
import { AgentOverview } from "./workspace-overview"
import { AgentMemoryCards } from "./agent-memory"
import { TeammateMessages } from "./agent-teammate-messages"
import { AgentInstructionsExtras } from "./agent-instructions-extras"
import { AgentPageBar } from "./crews-bar"
import { CrCard, CrLine, HeadChip, LeadMark } from "./cr-card"
import { useCrewSpend } from "./crew-spend"
import { TOOL_ACCESS_LABEL } from "./crew-team"
import { AGENT_TABS, type AgentTab } from "./crews-tabs"
import type {
  AgentRecord,
  ChatRow as ChatRowType,
  InboxSummary,
  PeerMessageRow as PeerMessageRowType,
  RunRow as RunRowType,
} from "./agent-canvas-tabs/types"

export type { ChatRow, RunRow, AgentSkillRow, AgentCredRow, PeerMessageRow } from "./agent-canvas-tabs/types"

export interface AgentCanvasProps {
  workspaceId: string
  agentSlug: string
  /** Crews list passed for the Crew dropdown in Profile section. */
  crews: { id: string; name: string; slug: string }[]
  onAgentChanged: (nextSlug?: string) => void
  onLoaded?: (agent: AgentRecord) => void
  onSelectCrew: (slug: string | null) => void
  /** The open tab (?tab=), already mapped from legacy values. */
  tab: AgentTab
  onTabChange: (tab: AgentTab) => void
  /** Opens the dock on the Terminal tab for this agent. */
  onOpenTerminal: () => void
}

/**
 * The agent page (Harbor 2): back to its crew, one head, and the tabs
 * Overview · Instructions · Model and limits · Tools and access · Memory ·
 * Settings. Editing happens in the tabs through the page's one save bar.
 */
export function AgentCanvas({
  workspaceId,
  agentSlug,
  crews,
  onAgentChanged,
  onLoaded,
  onSelectCrew,
  tab,
  onTabChange,
  onOpenTerminal,
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

  useEffect(() => { if (agent) onLoaded?.(agent) }, [agent, onLoaded])

  const [avatarPickerOpen, setAvatarPickerOpen] = useState(false)
  const { abilities, role } = useAbilities()
  const { data: session } = useSessionSafe()
  const canDelete = abilities.can("delete", "Agent")
  const canUpdate = abilities.can("update", "Agent")
  const isAdmin = role === "OWNER" || role === "ADMIN"
  // Persona PUT/DELETE are roleCreate (MANAGER+), like creating an agent.
  const canEditPersona = abilities.can("create", "Agent")
  const [personaRev, setPersonaRev] = useState(0)
  // Activity › Spend is for owners and admins (#3476); nothing is asked for anyone else.
  const spend = useCrewSpend(workspaceId, agent?.crew_id ?? undefined, isAdmin)
  const [revision, setRevision] = useState(0)
  const [inboxError, setInboxError] = useState<string | null>(null)
  const [activityError, setActivityError] = useState<string | null>(null)


  useRealtimeEvent("agent.status", useCallback((event) => {
    if (agent && event.payload?.agent_id === agent.id) {
      void fetchAgent()
      setRevision((n) => n + 1)
    }
  }, [agent, fetchAgent]))

  // Inbox + cost summary (used by stats strip + InboxBanner).
  const [inbox, setInbox] = useState<InboxSummary>({ count: 0 })
  const [peerMessages, setPeerMessages] = useState<PeerMessageRowType[]>([])
  const [peersUnavailable, setPeersUnavailable] = useState(false)
  const agentId = agent?.id
  useEffect(() => {
    if (!agentId) return
    let cancelled = false
    // Clear previous agent's data immediately so a stale inbox / peer list
    // never leaks into the next selection while the request is in flight.
    setInbox({ count: 0 })
    setInboxError(null)
    setPeerMessages([])
    setPeersUnavailable(false)
    apiFetch(`/api/v1/agents/${agentId}/inbox?workspace_id=${workspaceId}`)
      .then((r) => { if (!r.ok) throw new Error(`Activity could not be loaded (${r.status}).`); return r.json() })
      .then((data) => {
        if (cancelled || !data) return
        const unavailable: string[] = data.unavailable ?? []
        if (unavailable.length) setInboxError(`Some summary data is unavailable: ${unavailable.map(value => ({ cost: "costs", peers: "agent collaboration", approvals: "approvals", assignments: "assignments", escalations: "escalations" }[value] ?? "activity")).join(", ")}.`)
        const approvals = unavailable.includes("approvals") ? 0 : Number(data.approvals_pending ?? 0)
        const peers: PeerMessageRowType[] = Array.isArray(data.peer_messages) ? data.peer_messages : []
        const total = approvals
        const parts: string[] = []
        if (approvals) parts.push(`${approvals} approval${approvals === 1 ? "" : "s"} pending`)
        setInbox({ count: total, summary: parts.join(" · "), cost: !unavailable.includes("cost") && typeof data.cost_usd_this_month === "number" ? data.cost_usd_this_month : undefined })
        setPeerMessages(peers)
        setPeersUnavailable(unavailable.includes("peers"))
      })
      .catch(() => { if (!cancelled) { setInboxError("Approvals and activity summary could not be loaded."); setPeersUnavailable(true) } })
    return () => { cancelled = true }
  }, [agentId, workspaceId, revision])

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
    setActivityError(null)
    setRuns(null)
    setChats(null)
    apiFetch(`/api/v1/agents/${agentId}/runs?workspace_id=${workspaceId}`)
      .then((r) => { if (!r.ok) throw new Error(`Activity could not be loaded (${r.status}).`); return r.json() })
      .then((data: RunRowType[] | null) => {
        if (!Array.isArray(data)) throw new Error("Invalid runs response")
        if (!cancelled) setRuns(data)
      })
      .catch(() => { if (!cancelled) setActivityError("Some activity could not be loaded.") })
    apiFetch(`/api/v1/agents/${agentId}/chats?kind=direct&workspace_id=${workspaceId}`)
      .then((r) => { if (!r.ok) throw new Error(`Activity could not be loaded (${r.status}).`); return r.json() })
      .then((data: ChatRowType[] | null) => {
        if (!Array.isArray(data)) throw new Error("Invalid chats response")
        if (!cancelled) setChats(data)
      })
      .catch(() => { if (!cancelled) setActivityError("Some activity could not be loaded.") })
    return () => { cancelled = true }
  }, [agentId, workspaceId, revision])

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
    // The route needs the workspace (RequireWorkspace); the old call sent
    // none. stopAgent never throws and words the 502s (#2864).
    const result = await stopAgent(agent.id, agent.workspace_id)
    if (result.ok) {
      toast.success(stopSuccessMessage(result))
    } else {
      toast.error(`Could not stop ${agent.name}`, { description: result.message })
    }
    void fetchAgent()
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
        throw await toApiError(res, `HTTP ${res.status}`)
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
        throw await toApiError(res, `HTTP ${res.status}`)
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
      // A changed seed or style drops the stored render server-side; store
      // the new one now, while the person who chose it is here (#2876). If
      // the save changed nothing the old render is still there and the
      // server answers 409, which is fine.
      await storeAgentAvatar({ id: agent.id, slug: agent.slug, crew: agent.crew, ...next }, workspaceId)
      toast.success("Avatar updated")
    } catch (err) {
      toast.error(`Could not save avatar: ${err instanceof Error ? err.message : err}`)
    }
  }, [agent, patch, workspaceId])

  const [confirmDelete, setConfirmDelete] = useState(false)
  const handleDelete = useCallback(async () => {
    if (!agent) return
    try {
      const res = await apiFetch(`/api/v1/agents/${agent.id}?workspace_id=${workspaceId}`, { method: "DELETE" })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      toast.success(`Agent "${agent.name}" deleted`)
      onAgentChanged()
      onSelectCrew(agent.crew?.slug ?? null)
    } catch (err) {
      toast.error(`Delete failed: ${err instanceof Error ? err.message : err}`)
      throw err
    }
  }, [agent, onAgentChanged, onSelectCrew, workspaceId])

  if (loading) return <CanvasShell loading error={null} notLoadedLabel="">{null}</CanvasShell>
  if (error && /not found/.test(error)) {
    return <PageNotFound what="agent" description={<>No agent is called “{agentSlug}” in this workspace. It may have been deleted, or the link is wrong.</>} back={{ label: "Back to crews", onClick: () => onSelectCrew(null) }} />
  }
  if (error || !agent) {
    return <PageError title="The agent could not be loaded" description={error ?? undefined} onRetry={() => void fetchAgent()} />
  }

  const ghost = isGhost(agent)
  const statusKey = effectiveStatus(agent)
  const isRunning = agent.status === "RUNNING" && !ghost
  const isPendingHire = agent.ephemeral === true && agent.status === "PENDING_REVIEW" && !ghost
  const ttl = agent.ephemeral && !ghost ? ttlRemaining(agent.expires_at) : ""
  const hireReason = latestHireReason(agent.hire_reason)
  const crewName = agent.crew?.name ?? null
  const tool = TOOL_ACCESS_LABEL[agent.tool_profile]
  const editorKey = `${agent.id}:${agent.updated_at}`
  const editorProps = {
    workspaceId,
    open: true,
    onOpenChange: () => {},
    defaultCrewSlug: agent.crew?.slug ?? null,
    crews,
    agent,
    onCreated: (slug: string) => { onAgentChanged(slug); void fetchAgent(); setRevision((n) => n + 1) },
  }

  return (
    <PageSaveProvider>
      <AgentPageBar
        agent={agent}
        crewName={crewName}
        running={isRunning}
        onTerminal={agent.crew_id ? onOpenTerminal : undefined}
        onStop={handleStop}
      />
      <CanvasShell loading={false} error={null} notLoadedLabel="">
        {agent.crew && (
          <button type="button" onClick={() => onSelectCrew(agent.crew!.slug)} className="-mb-3 inline-flex items-center gap-1 text-label text-muted-foreground hover:text-foreground coarse:min-h-[44px]">
            <ChevronLeft className="size-3.5" aria-hidden /> {agent.crew.name}
          </button>
        )}
        <header className="flex items-start gap-4" data-testid="agent-header">
          <button type="button" onClick={() => setAvatarPickerOpen(true)} className="group shrink-0" title="Change avatar" aria-label="Change avatar">
            <AgentHeadTile
              seed={agent.avatar_seed || agent.name}
              style={agent.avatar_style || agent.crew?.avatar_style}
              avatarUrl={agent.avatar_url}
              crewColor={agent.crew?.color}
              status={agent.status}
              lead={agent.agent_role === "LEAD"}
            />
          </button>
          <div className="min-w-0 flex-1">
            <h1 className="type-title flex items-center gap-2">{agent.name}{agent.agent_role === "LEAD" && <LeadMark />}</h1>
            <p className="mt-1 text-body text-muted-foreground">
              {[agent.role_title, crewName ? `in ${crewName}` : null].filter(Boolean).join(" ")}
              {agent.cli_adapter && <> · {adapterLabel(agent.cli_adapter)}</>}
              {agent.llm_model && <> · {getModelLabel(agent.llm_model)}</>}
            </p>
            <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
              <StatusPill className="font-sans" status={statusKey} live={isRunning} size="md" />
              {agent.ephemeral && (
                <HeadChip icon={<Clock className="size-3.5" aria-hidden />} tone={ghost ? undefined : "warn"}>
                  {ghost ? "Temporary · expired" : isPendingHire ? "Temporary · waiting for approval" : `Temporary${ttl ? ` · ${ttl}` : ""}`}
                </HeadChip>
              )}
              {tool && <HeadChip icon={<Lock className="size-3.5" aria-hidden />}><span title={tool.hint}>{tool.label}</span></HeadChip>}
              <HeadChip icon={<Brain className="size-3.5" aria-hidden />}>Memory {agent.memory_enabled ? "on" : "off"}</HeadChip>
            </div>
          </div>
        </header>

        <AvatarPickerDialog
          open={avatarPickerOpen}
          onOpenChange={setAvatarPickerOpen}
          agentName={agent.name}
          seed={agent.avatar_seed}
          style={agent.avatar_style}
          crewStyle={agent.crew?.avatar_style ?? null}
          onSave={handleAvatarSave}
        />

        <CanvasTabs<AgentTab> tabs={AGENT_TABS} active={tab} onChange={onTabChange} idPrefix="agent-canvas" label="Agent sections" />

        <CanvasTabPanel idPrefix="agent-canvas" active={tab} className="space-y-4 pb-24">
          {tab === "overview" && (
            <>
              {isPendingHire && (
                <CrCard title={`${agent.name} waits for your approval`} concept="inbox" data-testid="agent-hire-decision">
                  <CrLine
                    title={hireReason ? `“${hireReason}”` : "A temporary hire"}
                    sub="It starts only after you approve."
                    right={
                      <>
                        <Button asChild variant="ghost" size="sm"><Link href={entityHref({ kind: "inbox", agentSlug: agent.slug })}>Review in Inbox</Link></Button>
                        <Button size="sm" onClick={() => void handleApproveHire()}>Approve hire</Button>
                      </>
                    }
                  />
                </CrCard>
              )}
              {ghost && (
                <CrCard title="This temporary hire has expired" concept="crews">
                  <CrLine title="Its runs and notes stay." sub="Rehire it to bring it back with its notes." right={<Button variant="outline" size="sm" onClick={() => void handleRehire()}><RotateCcw className="size-3.5" aria-hidden /> Rehire</Button>} />
                </CrCard>
              )}
              <AgentOverview workspaceId={workspaceId} agent={agent} inbox={inbox} runs={runs} chats={chats} peerMessages={peerMessages} error={[activityError, inboxError].filter(Boolean).join(" ") || null} onRetry={() => setRevision((n) => n + 1)} onStop={isRunning ? handleStop : undefined} cost30d={spend?.byAgent.get(agent.id) ?? null} revision={revision} />
              <TeammateMessages messages={peerMessages} unavailable={peersUnavailable} />
            </>
          )}
          {tab === "persona" && (
            <>
              <CreateAgentDialog key={`persona:${editorKey}:${personaRev}`} {...editorProps} inlineSections={["instructions", "chat"]} inlineLabel="Instructions" />
              <AgentInstructionsExtras
                workspaceId={workspaceId}
                agentId={agent.id}
                agentName={agent.name}
                crew={agent.crew_id && agent.crew ? { id: agent.crew_id, name: agent.crew.name } : null}
                canEdit={canEditPersona}
                memoryEnabled={agent.memory_enabled !== false}
                onReset={() => setPersonaRev((n) => n + 1)}
              />
            </>
          )}
          {tab === "model" && (
            <>
              <CreateAgentDialog key={`model:${editorKey}`} {...editorProps} inlineSections={["model", "access"]} inlineLabel="Model and limits" />
              <div className="flex items-start gap-2.5 rounded-card border border-border bg-card px-4 py-3 text-body text-muted-foreground">
                <CalendarClock className="mt-0.5 size-4 shrink-0" aria-hidden />
                <span className="flex-1">Schedules and webhooks moved to Routines.</span>
                <Button asChild variant="ghost" size="sm"><Link href="/routines">Routines</Link></Button>
              </div>
            </>
          )}
          {tab === "tools" && (
            <OverviewTab
              accessOnly
              workspaceId={workspaceId}
              agent={agent}
              crews={crews}
              inbox={inbox}
              chats={chats}
              runs={runs}
              peerMessages={peerMessages}
              patch={patch}
              onStop={isRunning ? handleStop : undefined}
              onOpenInbox={() => router.push(entityHref({ kind: "inbox", agentSlug: agent.slug }))}
              onOpenConfig={() => onTabChange("model")}
              onAgentChanged={onAgentChanged}
            />
          )}
          {tab === "memory" && (
            <AgentMemoryCards
              key={agent.id}
              workspaceId={workspaceId}
              agentId={agent.id}
              agentSlug={agent.slug}
              agentName={agent.name}
              crewId={agent.crew_id ?? undefined}
              userId={session?.user.id}
              userName={session?.user.name || undefined}
              memoryEnabled={agent.memory_enabled}
              canEdit={canUpdate}
              canExport={isAdmin}
              onMemoryEnabled={async (next) => {
                await patch({ memory_enabled: next })
                toast.success(next ? `${agent.name} remembers between runs` : `${agent.name} no longer adds saved knowledge to its runs`)
              }}
            />
          )}
          {tab === "settings" && (
            <>
              <CreateAgentDialog key={`identity:${editorKey}`} {...editorProps} inlineSections={["identity"]} inlineLabel="Identity" />
              <CrCard title="Command line" concept="settings">
                {[`crewship agent get ${agent.slug}`, `crewship agent update ${agent.slug}`, `crewship agent logs ${agent.slug}`].map((cmd) => (
                  <CrLine key={cmd} title={<code className="font-mono text-label">{cmd}</code>} />
                ))}
              </CrCard>
              {canDelete && (
                <CrCard title="Danger zone" concept="settings">
                  <CrLine
                    title="Delete agent"
                    sub="The agent stops and leaves its crew. It cannot be undone."
                    right={<Button variant="destructive" size="sm" onClick={() => setConfirmDelete(true)}><Trash2 className="size-3.5" aria-hidden /> Delete agent…</Button>}
                  />
                </CrCard>
              )}
            </>
          )}
        </CanvasTabPanel>
      </CanvasShell>
      <PageSaveBar />

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
    </PageSaveProvider>
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
