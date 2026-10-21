"use client"

import Link from "next/link"
import { ChevronRight, CircleDot, Inbox, MessageSquare, Square, Users, Workflow } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { EntityChip } from "@/components/ui/detail"
import { ConceptIcon } from "@/components/ui/concept-icon"
import { usePagedList } from "@/hooks/use-paged-list"
import { entityHref } from "@/lib/entity-links"
import { formatRelativeTime } from "@/lib/time"

import { CrCard, CrKv, CrLine, CrNote } from "./cr-card"
import { formatSpend } from "./crew-spend"
import { useWorkspaceResource } from "./memory-workspace"
import { useAgentRelations } from "./canvas/use-agent-relations"
import type { AgentRecord, ChatRow, InboxSummary, PeerMessageRow, RunRow } from "./agent-canvas-tabs/types"

const CARD_LINK = "inline-flex shrink-0 items-center gap-0.5 text-control font-medium text-muted-foreground transition-colors hover:text-foreground coarse:min-h-[44px]"

/** The agent's runs over 30 days: how many, how many finished, and the run limit. */
function useRuns30d(workspaceId: string, agentId: string, revision: number) {
  const q = new URLSearchParams({ workspace_id: workspaceId, window: "30d", agent_id: agentId })
  const { data } = useWorkspaceResource<{ totals: { succeeded: number; failed: number; running: number } }>(`/api/v1/runs/insights?${q}`, revision, true)
  return data?.totals ?? null
}

function limitLabel(seconds: number | null | undefined): string | null {
  if (!seconds || seconds <= 0) return null
  if (seconds % 3600 === 0) return `${seconds / 3600} h`
  return `${Math.round(seconds / 60)} min`
}

/**
 * A-OV (Harbor 2): what the agent is doing now, what it holds, who it talks
 * with and how the last 30 days went. "Messages from teammates" follows on the
 * page itself.
 */
export function AgentOverview({ workspaceId, agent, inbox, runs, chats, error, onRetry, onStop, cost30d = null, revision }: {
  workspaceId: string; agent: AgentRecord; inbox: InboxSummary; runs: RunRow[] | null; chats: ChatRow[] | null
  /** Shown by the page's Messages from teammates card, not here. */ peerMessages?: PeerMessageRow[]
  error?: string | null; onRetry: () => void
  /** Opens the Tools and access tab; the card links of the old layout. */ onWork?: () => void
  onStop?: () => void
  /** This agent's cost over 30 days; null when unknown or not for this reader. */ cost30d?: number | null
  revision: number
}) {
  const running = runs?.find((r) => r.status === "RUNNING") ?? null
  const totals = useRuns30d(workspaceId, agent.id, revision)
  const relations = useAgentRelations(workspaceId, agent.id)
  const mine = relations.pipelines.filter((p) => p.author_agent_id === agent.id)
  const crewAgents = usePagedList<{ id: string; name: string; slug: string }>({
    url: agent.crew_id ? `/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}&crew_id=${encodeURIComponent(agent.crew_id)}` : null,
  })
  const teammates = crewAgents.items.filter((a) => a.id !== agent.id).map((a) => a.name)

  const total = totals ? totals.succeeded + totals.failed + totals.running : null
  const finished = totals && totals.succeeded + totals.failed > 0 ? `${Math.round((totals.succeeded / (totals.succeeded + totals.failed)) * 100)} %` : "—"
  const limit = limitLabel(agent.timeout_seconds)
  const lastChat = chats?.[0]

  return (
    <div className="space-y-4" data-testid="agent-overview">
      {error && <div role="alert" className="rounded-card border border-border p-4 text-body">{error} <Button variant="ghost" size="sm" onClick={onRetry}>Retry</Button></div>}
      {inbox.count > 0 && (
        <CrCard title="Needs a decision" concept="inbox" count={inbox.count}>
          <CrLine
            lead={<Inbox className="size-4 text-chip-warn-fg" aria-hidden />}
            title={inbox.summary ?? `${inbox.count} items in this agent’s inbox.`}
            right={<Button asChild size="sm"><Link href={`/inbox?agent=${encodeURIComponent(agent.slug)}`}>Review in Inbox</Link></Button>}
          />
        </CrCard>
      )}

      <div className="grid gap-4 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <CrCard
          title="Now"
          concept="activity"
          link={{ label: "All in Activity", href: `/activity?what=work&agent=${encodeURIComponent(agent.id)}` }}
        >
          <CrLine
            lead={<AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style || agent.crew?.avatar_style} avatarUrl={agent.avatar_url} className="size-7 rounded-md" />}
            title={running ? "Working" : "Not working right now"}
            sub={running ? `${running.trigger_type ? `${running.trigger_type.toLowerCase()} run` : "Run"}${running.started_at ? ` · started ${formatRelativeTime(running.started_at)}` : ""}` : runs === null ? "Loading…" : undefined}
            right={running && onStop ? <Button variant="ghost" size="sm" onClick={onStop}><Square className="size-3.5" aria-hidden /> Stop</Button> : null}
          />
        </CrCard>

        <CrCard title="Last 30 days" concept="activity">
          <CrKv rows={[
            ["Runs", <span key="r" className="tabular-nums">{total ?? "—"}</span>],
            ["Finished", <span key="f" className="tabular-nums">{finished}</span>],
            ["Cost", <span key="c" className="tabular-nums">{cost30d == null ? "—" : formatSpend(cost30d)}</span>],
            ["Runs at most", <span key="l" className="tabular-nums">{limit ?? "—"}</span>],
          ]} />
        </CrCard>
      </div>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <CrCard title="Holds" concept="crews">
          {relations.issues.length + mine.length === 0 ? (
            <CrNote>{relations.loading ? "Loading…" : "Nothing assigned to it."}</CrNote>
          ) : (
            <div className="flex flex-wrap gap-1.5 border-t border-border px-4 py-3">
              {mine.slice(0, 8).map((p) => <EntityChip key={p.id} icon={Workflow} label={p.name || p.slug} href={entityHref({ kind: "routines" })} />)}
              {relations.issues.slice(0, 8).map((i) => <EntityChip key={i.id} icon={CircleDot} label={i.identifier || i.title} href={`/issues/${encodeURIComponent(i.identifier ?? i.id)}`} />)}
            </div>
          )}
        </CrCard>

        <CrCard title="Talks with" concept="chat">
          <CrLine
            lead={<ConceptIcon icon={MessageSquare} accent="blue" variant="chip" size="md" />}
            title="Chats"
            sub={chats === null ? "Loading…" : chats.length === 0 ? "No conversations yet" : `${chats.length} ${chats.length === 1 ? "conversation" : "conversations"}${lastChat ? ` · last ${formatRelativeTime(lastChat.last_activity_at ?? lastChat.started_at)}` : ""}`}
            right={<Link href={`/chat/${encodeURIComponent(agent.slug)}`} className={CARD_LINK}>Open chat<ChevronRight className="size-3.5" aria-hidden /></Link>}
          />
          <CrLine
            lead={<ConceptIcon icon={Users} accent="blue" variant="chip" size="md" />}
            title="Teammates"
            sub={teammates.length ? teammates.join(", ") : "Works alone"}
          />
        </CrCard>
      </div>
    </div>
  )
}
