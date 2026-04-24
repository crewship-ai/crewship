"use client"

import { MessageSquare, ScrollText, Settings, Crown } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusBadge } from "@/components/ui/status-badge"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
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
    const avatarUrl = getAgentAvatarUrl(
      agent.avatar_seed || agent.name,
      agent.avatar_style || agent.crew?.avatar_style,
    )
    const canonicalStatus = mapAgentStatus(agent.status)

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
