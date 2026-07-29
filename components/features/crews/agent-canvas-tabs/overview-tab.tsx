"use client"

import { useMemo } from "react"
import {
  AtSign, Bell, Bot, CalendarClock, Check, CircleDot, KeyRound, MessageSquare,
  Play, Share2, Sparkles, Webhook, Workflow, Wrench, XCircle,
} from "lucide-react"

import { usePipelines } from "@/hooks/use-pipelines"
import { useAgentReach } from "@/hooks/use-agent-reach"

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
  workspaceId, agent, inbox, chats, runs, peerMessages, onStop, onOpenInbox,
}: OverviewTabProps) {
  const { issues, credentials, skills } = useAgentRelations(workspaceId, agent.id)
  const { pipelines } = usePipelines(workspaceId)
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
    href: `/issues?issue=${encodeURIComponent(i.identifier ?? i.id)}`,
  }))

  const routineItems: DetailCellItem[] = agentPipelines.map((p): DetailCellItem => ({
    id: p.id,
    icon: Workflow,
    tone: "purple",
    title: p.name ?? p.slug,
    subtitle: p.slug,
    tag: "all",
    href: `/routines?routine=${encodeURIComponent(p.slug)}`,
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
      id: "skills", icon: Sparkles, label: "Skilly", tone: "purple",
      value: `${skills.filter((s) => s.enabled).length} / ${skills.length}`,
      cell: {
        title: "Skilly",
        count: skills.length,
        filters: [{ id: "all", label: "Vše" }, { id: "on", label: "Zapnuté" }, { id: "off", label: "Vypnuté" }],
        items: skills.map((s): DetailCellItem => ({
          id: s.id, icon: Sparkles, tone: s.enabled ? "purple" : "muted",
          title: s.skill.display_name ?? s.skill.name,
          subtitle: s.skill.description ?? s.skill.slug,
          meta: s.enabled ? "zapnuto" : "vypnuto",
          tag: s.enabled ? "on" : "off", dimmed: !s.enabled,
        })),
        footerLabel: "Otevřít katalog", footerHref: "/skills",
      },
    },
    {
      id: "tools", icon: Wrench, label: "Nástroje", tone: "notice", value: String(toolkits.length),
      cell: {
        title: "Nástroje a konektory",
        count: toolkits.length,
        filters: [{ id: "all", label: "Vše" }],
        items: toolkits.map((t, idx): DetailCellItem => ({
          id: `${t.toolkit || idx}`, icon: Wrench, tone: "notice",
          title: t.toolkit || "konektor",
          subtitle: t.tools?.length ? `Composio · ${t.tools.length} nástrojů` : `Composio · ${t.mode}`,
          tag: "all",
        })),
        footerLabel: "Spravovat konektory", footerHref: "/integrations",
      },
    },
    {
      id: "channels", icon: Bell, label: "Kam hlásí", tone: "purple",
      value: String(channels.filter((c) => c.enabled).length),
      cell: {
        title: "Kam hlásí",
        count: channels.length,
        filters: [{ id: "all", label: "Vše" }, { id: "on", label: "Aktivní" }],
        items: channels.map((c): DetailCellItem => ({
          id: c.id, icon: Bell, tone: c.enabled ? "purple" : "muted",
          title: c.provider ?? c.type, subtitle: c.type,
          meta: c.enabled ? "aktivní" : "vypnuto",
          tag: c.enabled ? "on" : "all", dimmed: !c.enabled,
        })),
        footerLabel: "Spravovat kanály", footerHref: "/integrations?tab=notifications",
      },
    },
    {
      id: "sessions", icon: MessageSquare, label: "Sezení", tone: "notice", value: String(chats?.length ?? 0),
      cell: {
        title: "Sezení",
        count: chats?.length ?? 0,
        filters: [{ id: "all", label: "Vše" }],
        items: (chats ?? []).map((c): DetailCellItem => ({
          id: c.id, icon: MessageSquare, tone: "notice",
          title: c.title ?? "Bez názvu",
          subtitle: `${c.message_count} zpráv`,
          meta: new Date(c.started_at).toLocaleDateString(),
          tag: "all",
          href: `/chat?agent=${encodeURIComponent(agent.slug)}`,
        })),
        footerLabel: "Otevřít chat", footerHref: `/chat?agent=${encodeURIComponent(agent.slug)}`,
      },
    },
  ]

  return (
    <div className="space-y-4">
      {inbox.count > 0 && (
        <BlockingNotice
          title="Čeká na tvoje rozhodnutí."
          body={inbox.summary ?? `${inbox.count} položek v inboxu agenta.`}
          detail="Dokud nerozhodneš, agent na tom kroku stojí."
          actions={onOpenInbox ? [{ label: "Otevřít inbox", onClick: onOpenInbox, primary: true }] : []}
        />
      )}

      {runningRun && (
        <NowRunning
          icon={Workflow}
          label={runningRun.trigger_type ? `${runningRun.trigger_type} run` : "Běžící run"}
          meta={runningRun.started_at ? `spuštěno ${new Date(runningRun.started_at).toLocaleTimeString()}` : undefined}
          onStop={onStop}
        />
      )}

      <ReachStrip items={reach} />

      <div className="grid gap-3.5 md:grid-cols-2 xl:grid-cols-4">
        <DetailCell
          title="Issues"
          count={issues.length}
          filters={[
            { id: "all", label: "Vše" },
            { id: "run", label: "Běží" },
            { id: "todo", label: "Todo" },
            { id: "done", label: "Hotové" },
          ]}
          items={issueItems}
          footerLabel={`Otevřít s filtrem ${agent.slug}`}
          footerHref={`/issues?assignee=${encodeURIComponent(agent.slug)}`}
        />
        <DetailCell
          title="Rutiny"
          count={agentPipelines.length}
          filters={[{ id: "all", label: "Vše" }]}
          items={routineItems}
          footerLabel="Otevřít rutiny"
          footerHref="/routines"
        />
        <DetailCell
          title="Čím se spouští"
          count={triggers.length}
          filters={[
            { id: "all", label: "Vše" },
            { id: "auto", label: "Automaticky" },
            { id: "man", label: "Ručně" },
          ]}
          items={triggerItems}
          footerLabel="Nastavit spouštění"
          footerHref={`/crews?agent=${encodeURIComponent(agent.slug)}&tab=settings`}
        />
        <DetailCell
          title="Credentials"
          count={credentials.length}
          warn={credentials.some((c) => c.credential_status !== "ACTIVE")}
          filters={[
            { id: "all", label: "Vše" },
            { id: "on", label: "Aktivní" },
            { id: "wait", label: "Čeká" },
          ]}
          items={credItems}
          footerLabel="Otevřít vault"
          footerHref="/credentials"
        />
      </div>

      <DetailCell
        title="Runy"
        count={runs?.length ?? 0}
        filters={[
          { id: "all", label: "Vše" },
          { id: "err", label: "Jen chyby" },
          { id: "run", label: "Běžící" },
        ]}
        items={runItems}
        tall
        footerLabel="Otevřít v Journalu"
        footerHref={`/journal?agent=${encodeURIComponent(agent.slug)}`}
      />

      {peerMessages.length > 0 && (
        <DetailCell
          title="Od kolegů"
          count={peerMessages.length}
          filters={[{ id: "all", label: "Vše" }]}
          items={peerMessages.map((m, idx): DetailCellItem => ({
            id: m.id ?? String(idx),
            icon: AtSign,
            tone: "purple",
            title: m.from_agent_name ?? m.from_agent_slug ?? "kolega",
            subtitle: m.preview ?? "",
            meta: m.created_at ? new Date(m.created_at).toLocaleDateString() : "",
            tag: "all",
          }))}
          footerLabel="Otevřít inbox"
          footerHref={`/inbox?agent=${encodeURIComponent(agent.slug)}`}
        />
      )}

      {issues.length === 0 && agentPipelines.length === 0 && (runs?.length ?? 0) === 0 && (
        <p className="flex items-center gap-2 px-1 text-label text-muted-foreground-soft">
          <Bot className="h-3.5 w-3.5" />
          Tenhle agent zatím nic nedělal. Zadej mu issue nebo ho spusť z chatu.
        </p>
      )}
    </div>
  )
}
