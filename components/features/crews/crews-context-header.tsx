"use client"

import Link from "next/link"
import {
  MessageSquare, ScrollText, Settings, Crown,
  Gavel, AlertTriangle, DollarSign,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusBadge } from "@/components/ui/status-badge"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { useAgentInbox } from "@/hooks/use-agent-inbox"
import { cn } from "@/lib/utils"
import type { CrewsDrawer } from "@/hooks/use-crews-selection"

interface AgentHeaderData {
  id: string
  name: string
  slug: string
  status: string
  role_title: string | null
  agent_role: string
  avatar_seed?: string | null
  avatar_style?: string | null
  crew?: { name: string; slug: string; color: string | null; avatar_style?: string | null } | null
}

interface CrewHeaderData {
  id: string
  name: string
  slug: string
  description: string | null
  icon: string | null
  color: string | null
}

export interface CrewsContextHeaderProps {
  agent?: AgentHeaderData | null
  crew?: CrewHeaderData | null
  onOpenDrawer: (drawer: CrewsDrawer) => void
}

function mapAgentStatus(status: string | undefined): string {
  switch (status) {
    case "RUNNING":
      return "IN_PROGRESS"
    case "ERROR":
      return "FAILED"
    case "STOPPED":
      return "CANCELLED"
    default:
      return "PENDING"
  }
}

/**
 * Compact identity strip above the tabs. Replaces the old hero section of
 * crews-agent-inline + the duplicate header that used to live on the "Open
 * full" agent page. Action buttons dispatch via the drawer URL param so
 * refresh and back-button restore the open panel.
 *
 * Renders nothing when no entity is selected — the layout keeps vertical
 * space tight in the workspace (all-crews) view.
 */
export function CrewsContextHeader({ agent, crew, onOpenDrawer }: CrewsContextHeaderProps) {
  if (agent) {
    return <AgentHeader agent={agent} onOpenDrawer={onOpenDrawer} />
  }
  if (crew) {
    return (
      <div className="shrink-0 flex items-center gap-3 px-4 py-2 border-b border-border bg-card/60">
        <CrewIcon icon={crew.icon ?? "users"} color={crew.color} size="sm" />
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-label font-semibold truncate">{crew.name}</span>
            <Badge variant="outline" className="text-micro text-muted-foreground">Crew</Badge>
          </div>
          {crew.description && (
            <p className="text-micro text-muted-foreground truncate mt-0.5">{crew.description}</p>
          )}
        </div>
        <div className="flex items-center gap-1.5 shrink-0">
          <Button
            variant="outline"
            size="sm"
            className="h-7 gap-1 text-label"
            onClick={() => onOpenDrawer("settings")}
            aria-label="Open crew settings drawer"
          >
            <Settings className="h-3.5 w-3.5" />
            <span className="hidden sm:inline">Settings</span>
          </Button>
        </div>
      </div>
    )
  }

  return null
}

function AgentHeader({
  agent,
  onOpenDrawer,
}: {
  agent: AgentHeaderData
  onOpenDrawer: (drawer: CrewsDrawer) => void
}) {
  const avatarUrl = getAgentAvatarUrl(
    agent.avatar_seed || agent.name,
    agent.avatar_style || agent.crew?.avatar_style,
  )
  const canonicalStatus = mapAgentStatus(agent.status)
  const { inbox } = useAgentInbox(agent.id)

  const approvals = inbox?.approvals_pending ?? 0
  const escalations = inbox?.escalations_open ?? 0
  const cost = inbox?.cost_usd_this_month ?? 0

  return (
    <div className="shrink-0 flex items-center gap-3 px-4 py-2 border-b border-border bg-card/60">
      <img
        src={avatarUrl}
        alt=""
        className="h-9 w-9 rounded-lg shrink-0 border border-border"
      />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-label font-semibold truncate">{agent.name}</span>
          <StatusBadge status={canonicalStatus} label={agent.status.toLowerCase()} />
          {agent.agent_role === "LEAD" && (
            <Badge variant="outline" className="gap-1 text-micro">
              <Crown className="h-3 w-3" /> Lead
            </Badge>
          )}
          {agent.crew && (
            <Badge variant="outline" className="text-micro text-muted-foreground">
              {agent.crew.name}
            </Badge>
          )}
        </div>
        {agent.role_title && (
          <p className="text-micro text-muted-foreground truncate mt-0.5">{agent.role_title}</p>
        )}
      </div>

      {/* Alert pills — replace the pending-work chips that used to live
          in the right-panel Inbox. Only render when > 0 so the header
          stays quiet for healthy agents. */}
      <div className="hidden md:flex items-center gap-1.5 shrink-0">
        {approvals > 0 && (
          <AlertPill
            icon={Gavel}
            tone="amber"
            href={`/approvals?agent_id=${agent.id}`}
            label={`${approvals} approval${approvals === 1 ? "" : "s"}`}
          />
        )}
        {escalations > 0 && (
          <AlertPill
            icon={AlertTriangle}
            tone="red"
            href={
              agent.crew?.slug
                ? `/crews/${agent.crew.slug}?tab=journal`
                : `/crews/agents/${agent.id}`
            }
            label={`${escalations} escalation${escalations === 1 ? "" : "s"}`}
          />
        )}
        {cost > 0 && (
          <AlertPill
            icon={DollarSign}
            tone="muted"
            href={
              agent.crew
                ? `/paymaster?crew=${agent.crew.slug}`
                : "/paymaster"
            }
            label={`$${cost.toFixed(2)} / mo`}
          />
        )}
      </div>

      <div className="flex items-center gap-1.5 shrink-0">
        <Button
          size="sm"
          className="h-7 gap-1 text-label"
          onClick={() => onOpenDrawer("chat")}
          aria-label="Open chat drawer"
        >
          <MessageSquare className="h-3.5 w-3.5" />
          <span className="hidden sm:inline">Chat</span>
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="h-7 gap-1 text-label"
          onClick={() => onOpenDrawer("logs")}
          aria-label="Open logs drawer"
        >
          <ScrollText className="h-3.5 w-3.5" />
          <span className="hidden sm:inline">Logs</span>
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="h-7 gap-1 text-label"
          onClick={() => onOpenDrawer("settings")}
          aria-label="Open settings drawer"
        >
          <Settings className="h-3.5 w-3.5" />
          <span className="hidden sm:inline">Settings</span>
        </Button>
      </div>
    </div>
  )
}

function AlertPill({
  icon: Icon,
  tone,
  href,
  label,
}: {
  icon: React.ElementType
  tone: "amber" | "red" | "muted"
  href: string
  label: string
}) {
  const toneClass = tone === "amber"
    ? "bg-amber-500/15 text-amber-400 hover:bg-amber-500/20"
    : tone === "red"
      ? "bg-red-500/15 text-red-400 hover:bg-red-500/20"
      : "bg-muted/40 text-muted-foreground hover:bg-muted/60"
  return (
    <Link
      href={href}
      className={cn(
        "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-micro font-medium transition-colors",
        toneClass,
      )}
    >
      <Icon className="h-3 w-3" aria-hidden="true" />
      <span>{label}</span>
    </Link>
  )
}
