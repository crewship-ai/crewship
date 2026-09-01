"use client"

import { useMemo, useState } from "react"
import {
  AtSign, Bot, CalendarClock, Check, MessageSquare, Play, Share2, Webhook, Workflow, XCircle,
} from "lucide-react"

import { useAgentReach } from "@/hooks/use-agent-reach"
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import { AgentConnectorsCard } from "@/components/features/integrations/composio/access-editor"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { withReturnTo } from "@/lib/return-to"

import { DetailCell, type DetailCellItem, type DetailCellTone } from "../canvas/detail-cell"
import { AppearStack } from "@/components/ui/detail"
import { SkillsManager } from "../agent-canvas-managers"
import { AgentChannelsCard } from "../agent-channels-card"
import { BlockingNotice, NowRunning } from "../canvas/detail-blocks"
import { deriveTriggers, useAgentRelations } from "../canvas/use-agent-relations"
import type { AgentRecord, ChatRow, InboxSummary, PeerMessageRow, RunRow } from "./types"

// =============================================================================
// Agent overview.
//
// Ordered by the question it answers, not by the data model:
//   1. what this agent wants from you   (blocking decision)
//   2. what it is doing right now       (live run)
//   3. what it can touch                (reach strip — one row, slides out)
//   4. what work it holds               (four cells)
//   5. what it did                      (runs)
//
// It deliberately shows no counts anywhere except on the cells. The previous
// version repeated every number in a stat strip, in the menu and again in a
// card, so four surfaces disagreed the moment one of them went stale.
// =============================================================================

export interface OverviewTabProps {
  workspaceId: string
  agent: AgentRecord
  crews: { id: string; name: string; slug: string }[]
  inbox: InboxSummary
  chats: ChatRow[] | null
  runs: RunRow[] | null
  peerMessages: PeerMessageRow[]
  patch: (body: Record<string, unknown>) => Promise<void>
  onStop?: () => void
  onOpenInbox?: () => void
  /** Switches this screen to its Configuration tab — used by the trigger cell. */
  onOpenConfig?: () => void
  /** Refetch after a manager dialog changed skills / connectors / channels. */
  onAgentChanged: () => void
}

const ISSUE_TONE: Record<string, DetailCellTone> = {
  IN_PROGRESS: "primary",
  IN_REVIEW: "warn",
  DONE: "success",
  CANCELLED: "muted",
}

function issueBucket(status?: string | null): string {
  const s = (status ?? "").toUpperCase()
  if (s.includes("PROGRESS") || s.includes("REVIEW")) return "run"
  if (s.includes("DONE") || s.includes("CANCEL")) return "done"
  return "todo"
}

function runTone(status?: string | null): DetailCellTone {
  const s = (status ?? "").toUpperCase()
  if (s === "RUNNING") return "primary"
  if (s === "FAILED" || s === "ERROR") return "danger"
  if (s === "COMPLETED" || s === "SUCCESS") return "success"
  return "muted"
}

export function OverviewTab({
  workspaceId, agent, inbox, chats, runs, peerMessages, onStop, onOpenInbox, onOpenConfig, onAgentChanged,
}: OverviewTabProps) {
  // Which manager dialog is open, if any. A centred dialog rather than the old
  // right-hand drawer: the drawer had to be 420px for a list and 760px for an
  // editor, which is how one pattern ended up with two widths.
  const [manager, setManager] = useState<"skills" | "tools" | "channels" | null>(null)
  const { issues, credentials, skills, pipelines } = useAgentRelations(workspaceId, agent.id)
  const { toolkits, channels } = useAgentReach(workspaceId, agent.id)

  const agentPipelines = useMemo(
    () => pipelines.filter((p) => p.author_agent_id === agent.id),
    [pipelines, agent.id],
  )

  const triggers = useMemo(
    () => deriveTriggers(agent, peerMessages.length),
    [agent, peerMessages.length],
  )

  const runningRun = runs?.find((r) => (r.status ?? "").toUpperCase() === "RUNNING") ?? null

  const issueItems: DetailCellItem[] = issues.map((i): DetailCellItem => ({
    id: i.id,
    icon: issueBucket(i.status) === "done" ? Check : CONCEPT_ICON.issues,
    tone: ISSUE_TONE[(i.status ?? "").toUpperCase()] ?? "muted",
    title: `${i.identifier ? `${i.identifier} ` : ""}${i.title}`,
    subtitle: [i.status?.toLowerCase(), i.priority?.toLowerCase()].filter(Boolean).join(" · "),
    tag: issueBucket(i.status),
    // Canonical route (/orchestration/issues/* is a compat redirect), and it
    // carries where we came from so the issue's back arrow returns to this
    // agent instead of dumping the reader on the board.
    href: withReturnTo(
      `/issues/${encodeURIComponent(i.identifier ?? i.id)}`,
      `/crews?agent=${encodeURIComponent(agent.slug)}`,
      agent.name,
    ),
  }))

  const routineItems: DetailCellItem[] = agentPipelines.map((p): DetailCellItem => ({
    id: p.id,
    icon: CONCEPT_ICON.routines,
    tone: "purple",
    title: p.name ?? p.slug,
    subtitle: p.slug,
    tag: "all",
    href: "/routines",
  }))

  const triggerItems: DetailCellItem[] = triggers.map((t): DetailCellItem => ({
    id: t.kind,
    icon: t.kind === "schedule" ? CalendarClock
      : t.kind === "webhook" ? Webhook
      : t.kind === "delegation" ? Share2 : Play,
    tone: t.automatic ? "purple" : "success",
    title: t.title,
    subtitle: t.subtitle,
    meta: t.meta,
    tag: t.automatic ? "auto" : "man",
  }))

  // Zero credentials is not "nothing to report" — it is the failure case.
  // credentials includes both explicit grants and crew-inherited ones
  // (grant_source "crew", from a crew-scoped credential the agent picks up
  // automatically), so an agent that is fine because its crew already has a
  // key still has a non-empty array here and never hits this branch. Only a
  // genuinely empty array — no explicit grant, no crew to inherit from, or a
  // crew with none — means the agent has nothing to authenticate with.
  const credItems: DetailCellItem[] = credentials.length === 0
    ? [{
        id: "no-credential",
        icon: CONCEPT_ICON.credentials,
        tone: "warn",
        title: "No credential assigned",
        subtitle: "This agent has no workspace or crew credential — its first run will fail.",
        tag: "wait",
      }]
    : credentials.map((c): DetailCellItem => ({
        id: c.id,
        icon: CONCEPT_ICON.credentials,
        tone: c.credential_status === "ACTIVE" ? "gold" : "warn",
        title: c.credential_name,
        subtitle: `${c.credential_provider?.toLowerCase() ?? "custom"} · ${c.env_var_name}`,
        meta: c.credential_status?.toLowerCase(),
        tag: c.credential_status === "ACTIVE" ? "on" : "wait",
      }))

  const runItems: DetailCellItem[] = (runs ?? []).map((r): DetailCellItem => ({
    id: r.id,
    icon: (r.status ?? "").toUpperCase() === "FAILED" ? XCircle : Play,
    tone: runTone(r.status),
    title: r.trigger_type ? `${r.trigger_type} run` : "run",
    subtitle: r.error_message ?? r.status?.toLowerCase() ?? "",
    meta: r.started_at ? new Date(r.started_at).toLocaleTimeString() : "",
    tag: (r.status ?? "").toUpperCase() === "RUNNING" ? "run"
      : ["FAILED", "ERROR"].includes((r.status ?? "").toUpperCase()) ? "err" : "ok",
  }))

  const skillItems: DetailCellItem[] = skills.map((sk): DetailCellItem => ({
    id: sk.id, icon: CONCEPT_ICON.skills, tone: sk.enabled ? "purple" : "muted",
    title: sk.skill.display_name ?? sk.skill.name,
    subtitle: sk.skill.description ?? sk.skill.slug,
    meta: sk.enabled ? "enabled" : "disabled",
    tag: sk.enabled ? "on" : "off", dimmed: !sk.enabled,
  }))

  const toolItems: DetailCellItem[] = toolkits.map((t, idx): DetailCellItem => ({
    id: `${t.toolkit || idx}`, icon: CONCEPT_ICON.tools, tone: "notice",
    title: t.toolkit || "connector",
    subtitle: t.tools?.length ? `Composio · ${t.tools.length} tools` : `Composio · ${t.mode}`,
    tag: "all",
  }))

  const channelItems: DetailCellItem[] = channels.map((c): DetailCellItem => ({
    id: c.id, icon: CONCEPT_ICON.channels, tone: c.enabled ? "purple" : "muted",
    title: c.provider ?? c.type, subtitle: c.type,
    meta: c.enabled ? "active" : "off",
    tag: c.enabled ? "on" : "all", dimmed: !c.enabled,
  }))

  return (
    <div className="space-y-5">
      <AppearStack>
      {inbox.count > 0 && (
        <BlockingNotice
          title="Waiting on your decision."
          body={inbox.summary ?? `${inbox.count} items in this agent\u2019s inbox.`}
          detail="Until you decide, the agent is stopped on that step."
          actions={onOpenInbox ? [{ label: "Open inbox", onClick: onOpenInbox, primary: true }] : []}
        />
      )}

      {runningRun && (
        <NowRunning
          icon={Workflow}
          label={runningRun.trigger_type ? `${runningRun.trigger_type} run` : "Running run"}
          meta={runningRun.started_at ? `started ${new Date(runningRun.started_at).toLocaleTimeString()}` : undefined}
          onStop={onStop}
        />
      )}

      {/* Three rows, each answering one question, and each saying so.
          The chip row and its right-hand drawer are gone. Six of the seven
          chips carried nothing of their own: Skills, Tools and Channels were
          lists — the same card this grid uses, which is why the drawer needed
          two widths — Manage skills was Skills again, Workspace is the header's
          Files button, Activity the header's Journal link, and Memory opens
          persona/crew settings, which is Configuration. So the lists moved
          here and the rest went to where they already existed. */}
      <RowGroup title="What it holds" note="the work it is carrying">
      <div className="grid gap-3.5 @xl:grid-cols-2 @6xl:grid-cols-4">
        <DetailCell
          order={0}
          title="Issues"
          icon={CONCEPT_ICON.issues}
          count={issues.length}
          filters={[
            { id: "all", label: "All" },
            { id: "run", label: "Running" },
            { id: "todo", label: "Todo" },
            { id: "done", label: "Done" },
          ]}
          items={issueItems}
          footerLabel={`Open filtered by ${agent.slug}`}
          footerHref="/orchestration/issues"
        />
        <DetailCell
          order={1}
          title="Routines"
          icon={CONCEPT_ICON.routines}
          count={agentPipelines.length}
          filters={[{ id: "all", label: "All" }]}
          items={routineItems}
          footerLabel="Open routines"
          footerHref="/routines"
        />
        <DetailCell
          order={2}
          title="Triggers"
          icon={CONCEPT_ICON.triggers}
          count={triggers.length}
          filters={[
            { id: "all", label: "All" },
            { id: "auto", label: "Automatic" },
            { id: "man", label: "Manual" },
          ]}
          items={triggerItems}
          footerLabel="Configure triggers"
          footerOnClick={onOpenConfig}
        />
        <DetailCell
          order={3}
          title="Credentials"
          icon={CONCEPT_ICON.credentials}
          count={credentials.length}
          warn={credentials.length === 0 || credentials.some((c) => c.credential_status !== "ACTIVE")}
          filters={[
            { id: "all", label: "All" },
            { id: "on", label: "Active" },
            { id: "wait", label: "Pending" },
          ]}
          items={credItems}
          footerLabel="Open vault"
          footerHref="/credentials"
        />
      </div>
      </RowGroup>

      {/* What it can do. Each card's footer opens ONLY its own manager, in a
          centred dialog. They used to be one "Manage skills" drawer containing
          all four managers at once, which is why nothing in it was findable. */}
      <RowGroup title="What it can do" note="its abilities and where it reports">
      <div className="grid gap-3.5 @xl:grid-cols-2 @6xl:grid-cols-3">
        <DetailCell
          order={4}
          title="Skills"
          icon={CONCEPT_ICON.skills}
          count={`${skills.filter((sk) => sk.enabled).length} / ${skills.length}`}
          filters={[
            { id: "all", label: "All" },
            { id: "on", label: "Enabled" },
            { id: "off", label: "Disabled" },
          ]}
          items={skillItems}
          footerLabel="Manage skills"
          footerOnClick={() => setManager("skills")}
        />
        <DetailCell
          order={5}
          title="Tools"
          icon={CONCEPT_ICON.tools}
          count={toolkits.length}
          filters={[{ id: "all", label: "All" }]}
          items={toolItems}
          footerLabel="Manage connectors"
          footerOnClick={() => setManager("tools")}
        />
        <DetailCell
          order={6}
          title="Channels"
          icon={CONCEPT_ICON.channels}
          count={channels.length}
          filters={[{ id: "all", label: "All" }, { id: "on", label: "Active" }]}
          items={channelItems}
          footerLabel="Manage channels"
          footerOnClick={() => setManager("channels")}
        />
      </div>
      </RowGroup>

      <RowGroup title="What it has been up to" note="on its own, and with you">
      <div className="grid gap-3.5 @xl:grid-cols-2">
        <DetailCell
          order={7}
          title="Runs"
          icon={CONCEPT_ICON.runs}
          count={runs?.length ?? 0}
          filters={[
            { id: "all", label: "All" },
            { id: "err", label: "Errors only" },
            { id: "run", label: "Running" },
          ]}
          items={runItems}
          footerLabel="Open in Journal"
          footerHref={`/journal?agent=${encodeURIComponent(agent.slug)}`}
        />

        <DetailCell
          order={8}
          title="Sessions"
          icon={CONCEPT_ICON.sessions}
          count={chats?.length ?? 0}
          filters={[{ id: "all", label: "All" }]}
          items={(chats ?? []).map((c): DetailCellItem => ({
            id: c.id,
            icon: MessageSquare,
            tone: "notice",
            title: c.title ?? "Untitled",
            subtitle: `${c.message_count} message${c.message_count === 1 ? "" : "s"}`,
            meta: new Date(c.started_at).toLocaleDateString(),
            tag: "all",
            // ?session= or the row is a lie: without it the chat page falls
            // back to the freshest session, so clicking the third row down
            // opens a different conversation than the one it names.
            href: `/chat/${encodeURIComponent(agent.slug)}?session=${encodeURIComponent(c.id)}`,
          }))}
          footerLabel="Open chat"
          footerHref={`/chat/${encodeURIComponent(agent.slug)}`}
        />

        {peerMessages.length > 0 && (
          <DetailCell
            order={9}
            title="From peers"
            icon={CONCEPT_ICON.peers}
            count={peerMessages.length}
            filters={[{ id: "all", label: "All" }]}
            items={peerMessages.map((m, idx): DetailCellItem => ({
              id: m.id ?? String(idx),
              icon: AtSign,
              tone: "purple",
              title: m.from_agent_name ?? m.from_agent_slug ?? "peer",
              subtitle: m.preview ?? "",
              meta: m.created_at ? new Date(m.created_at).toLocaleDateString() : "",
              tag: "all",
            }))}
            footerLabel="Open inbox"
            footerHref={`/inbox-v2?agent=${encodeURIComponent(agent.slug)}`}
          />
        )}
      </div>
      </RowGroup>

      {issues.length === 0 && agentPipelines.length === 0 && (runs?.length ?? 0) === 0 && (
        <p className="type-row flex items-center gap-2 px-1 text-muted-foreground-soft">
          <Bot className="h-3.5 w-3.5" />
          This agent has done nothing yet. Assign it an issue or start it from chat.
        </p>
      )}

      <ManagerDialog
        open={manager !== null}
        onClose={() => setManager(null)}
        title={
          manager === "skills" ? "Skills"
            : manager === "tools" ? "Tools and connectors"
              : "Channels"
        }
        description={
          manager === "skills" ? `What ${agent.name} is able to do.`
            : manager === "tools" ? `Apps ${agent.name} may act through, and the tools it may call.`
              : `Where ${agent.name} is allowed to post.`
        }
      >
        {manager === "skills" && (
          <SkillsManager
            agentId={agent.id}
            agentSlug={agent.slug}
            workspaceId={workspaceId}
            onChange={onAgentChanged}
          />
        )}
        {manager === "tools" && (
          <AgentConnectorsCard
            agentId={agent.id}
            agentName={agent.name}
            agentCrew={agent.crew?.name ?? null}
            workspaceId={workspaceId}
          />
        )}
        {manager === "channels" && (
          <AgentChannelsCard agentId={agent.id} agentName={agent.name} workspaceId={workspaceId} />
        )}
      </ManagerDialog>
      </AppearStack>
    </div>
  )
}

/**
 * A labelled band of cards. The label is the only place this screen says out
 * loud why these cards sit together — without it the grouping is just a gap,
 * and a gap is indistinguishable from the grid running out of room.
 */
function RowGroup({ title, note, children }: { title: string; note: string; children: React.ReactNode }) {
  return (
    <section>
      <div className="mb-2 flex items-baseline gap-2">
        <h2 className="type-section text-foreground/70">{title}</h2>
        <span className="type-meta text-muted-foreground-soft">{note}</span>
      </div>
      {children}
    </section>
  )
}

/**
 * Centred, one manager at a time.
 *
 * This replaces a drawer that slid in from the right and had to be 420px wide
 * for a list and 760px for an editor — one pattern, two widths, because it was
 * carrying two different kinds of thing. A dialog is honest about being a
 * detour, and the same width suits every one of them.
 */
function ManagerDialog({
  open, onClose, title, description, children,
}: {
  open: boolean
  onClose: () => void
  title: string
  description: string
  children: React.ReactNode
}) {
  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-[720px]">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {children}
      </DialogContent>
    </Dialog>
  )
}
