"use client"

import { useMemo } from "react"
import {
  AtSign, Bell, Bot, CalendarClock, Check, CircleDot, KeyRound, MessageSquare,
  Play, Share2, Sparkles, Webhook, Workflow, Wrench, XCircle,
} from "lucide-react"

import { useAgentReach } from "@/hooks/use-agent-reach"
import { withReturnTo } from "@/lib/return-to"

import { DetailCell, type DetailCellItem, type DetailCellTone } from "../canvas/detail-cell"
import { BlockingNotice, NowRunning, ReachStrip, type ReachItem } from "../canvas/detail-blocks"
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
  /**
   * Relations whose UI already exists as a full component (memory, workspace).
   * The canvas owns their props, so it builds the chips and passes them in.
   */
  extraReach?: ReachItem[]
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
  workspaceId, agent, inbox, chats, runs, peerMessages, onStop, onOpenInbox, onOpenConfig, extraReach = [],
}: OverviewTabProps) {
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
    icon: issueBucket(i.status) === "done" ? Check : CircleDot,
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
    icon: Workflow,
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

  const credItems: DetailCellItem[] = credentials.map((c): DetailCellItem => ({
    id: c.id,
    icon: KeyRound,
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

  const reach: ReachItem[] = [
    {
      id: "skills", icon: Sparkles, label: "Skills", tone: "purple", group: "Can do",
      value: `${skills.filter((s) => s.enabled).length} / ${skills.length}`,
      cell: {
        title: "Skills",
        count: skills.length,
        filters: [{ id: "all", label: "All" }, { id: "on", label: "Enabled" }, { id: "off", label: "Disabled" }],
        items: skills.map((s): DetailCellItem => ({
          id: s.id, icon: Sparkles, tone: s.enabled ? "purple" : "muted",
          title: s.skill.display_name ?? s.skill.name,
          subtitle: s.skill.description ?? s.skill.slug,
          meta: s.enabled ? "enabled" : "disabled",
          tag: s.enabled ? "on" : "off", dimmed: !s.enabled,
        })),
        footerLabel: "Open catalog", footerHref: "/skills",
      },
    },
    {
      id: "tools", icon: Wrench, label: "Tools", tone: "notice", group: "Can do", value: String(toolkits.length),
      cell: {
        title: "Tools and connectors",
        count: toolkits.length,
        filters: [{ id: "all", label: "All" }],
        items: toolkits.map((t, idx): DetailCellItem => ({
          id: `${t.toolkit || idx}`, icon: Wrench, tone: "notice",
          title: t.toolkit || "connector",
          subtitle: t.tools?.length ? `Composio · ${t.tools.length} tools` : `Composio · ${t.mode}`,
          tag: "all",
        })),
        footerLabel: "Manage connectors", footerHref: "/integrations",
      },
    },
    {
      id: "channels", icon: Bell, label: "Channels", tone: "purple", group: "Reports to",
      value: String(channels.filter((c) => c.enabled).length),
      cell: {
        title: "Channels",
        count: channels.length,
        filters: [{ id: "all", label: "All" }, { id: "on", label: "Active" }],
        items: channels.map((c): DetailCellItem => ({
          id: c.id, icon: Bell, tone: c.enabled ? "purple" : "muted",
          title: c.provider ?? c.type, subtitle: c.type,
          meta: c.enabled ? "active" : "off",
          tag: c.enabled ? "on" : "all", dimmed: !c.enabled,
        })),
        footerLabel: "Manage channels", footerHref: "/integrations?tab=notifications",
      },
    },
  ]

  return (
    <div className="space-y-4">
      {/* Reach sits with the tabs, not under the content: it is chrome, read
          once on arrival. Chips rather than links because the grid below is
          chips-in-cards — the same shape says the two rows are one family. */}
      <ReachStrip items={[...reach, ...extraReach]} />

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

      {/* Two rows by MEANING, not by whatever the arithmetic allows.
          Row one is what the agent is holding — issues, routines, triggers,
          credentials. Row two is what it has been up to — runs on its own,
          sessions with a person. Letting all six flow into one line at wide
          widths made the grid look tidier and read worse: two unrelated
          questions in one stripe. Runs and Sessions stay a pair at every size,
          and get half the pane each, which lists deserve more than a sixth.
          Thresholds are container sizes — this pane, not the window. */}
      <div className="grid gap-3.5 @xl:grid-cols-2 @6xl:grid-cols-4">
        <DetailCell
          order={0}
          title="Issues"
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
          count={agentPipelines.length}
          filters={[{ id: "all", label: "All" }]}
          items={routineItems}
          footerLabel="Open routines"
          footerHref="/routines"
        />
        <DetailCell
          order={2}
          title="Triggers"
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
          count={credentials.length}
          warn={credentials.some((c) => c.credential_status !== "ACTIVE")}
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

      {/* Sessions was PROMOTED out of the chip row, not added: it was already
          there as a bubble with a drawer, and one concept in two places is the
          thing this screen keeps getting punished for. Both cells read runs
          and chats the canvas already fetched, so the pair costs no extra
          request — which matters on a screen already spending 11 per click. */}
      <div className="grid gap-3.5 @xl:grid-cols-2">
        <DetailCell
          order={4}
          title="Runs"
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
          order={5}
          title="Sessions"
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
            href: `/chat/${encodeURIComponent(agent.slug)}`,
          }))}
          footerLabel="Open chat"
          footerHref={`/chat/${encodeURIComponent(agent.slug)}`}
        />

        {peerMessages.length > 0 && (
          <DetailCell
            order={6}
            title="From peers"
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
            footerHref={`/inbox?agent=${encodeURIComponent(agent.slug)}`}
          />
        )}
      </div>

      {issues.length === 0 && agentPipelines.length === 0 && (runs?.length ?? 0) === 0 && (
        <p className="type-row flex items-center gap-2 px-1 text-muted-foreground-soft">
          <Bot className="h-3.5 w-3.5" />
          This agent has done nothing yet. Assign it an issue or start it from chat.
        </p>
      )}
    </div>
  )
}
