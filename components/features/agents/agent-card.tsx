"use client"

import { memo } from "react"
import Link from "next/link"
import { Cpu, Key, Clock, AlertCircle, Pause } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { getCrewDotColor } from "@/lib/entities"
import { timeAgo } from "@/lib/time"
import { CLI_ADAPTERS, getModelLabel, getProviderLabel } from "@/lib/cli-adapters"
import { PROVIDER_ICONS } from "@/components/icons/provider-icons"
import { PROVIDER_ICON_COLOR } from "@/lib/colors"
import { cn } from "@/lib/utils"

interface AgentCrew {
  name: string
  slug: string
  color: string | null
  avatar_style?: string | null
}

interface AgentCount {
  skills: number
  credentials: number
  chats: number
}

interface AgentData {
  id: string
  name: string
  slug: string
  description: string | null
  role_title: string | null
  agent_role: string
  status: string
  cli_adapter: string
  llm_provider: string | null
  llm_model: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  crew: AgentCrew | null
  _count: AgentCount
  last_active_at?: string | null
}

const statusConfig: Record<string, { label: string; className: string; icon?: React.ElementType }> = {
  IDLE: {
    label: "Idle",
    className: "bg-muted text-muted-foreground",
  },
  RUNNING: {
    label: "Running",
    className: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400",
  },
  ERROR: {
    label: "Error",
    className: "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-400",
    icon: AlertCircle,
  },
  STOPPED: {
    label: "Stopped",
    className: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-400",
    icon: Pause,
  },
}

export const AgentCard = memo(function AgentCard({ agent }: { agent: AgentData }) {
  const status = statusConfig[agent.status] ?? statusConfig.IDLE
  const StatusIcon = status.icon

  return (
    <Link
      href={`/crews/agents/${agent.id}`}
      className="rounded-[var(--radius)] focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2 outline-none"
    >
      <Card className="hover:border-primary/50 hover:bg-accent/30 hover:shadow-md transition-all duration-150 cursor-pointer h-full border-border/80 shadow-md">
        <CardContent className="p-4 sm:p-5">
          <div className="flex items-start gap-3">
            <img
              src={getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style || agent.crew?.avatar_style)}
              alt=""
              className="h-10 w-10 rounded-lg shrink-0"
            />
            <div className="flex-1 min-w-0">
              <div className="flex items-center justify-between gap-2">
                <h3 className="text-body font-semibold truncate">{agent.name}</h3>
                <Badge variant="secondary" className={`text-micro shrink-0 gap-1.5 ${status.className}`}>
                  {agent.status === "RUNNING" && (
                    <span className="relative flex h-1.5 w-1.5">
                      <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
                      <span className="relative inline-flex rounded-full h-1.5 w-1.5 bg-emerald-500" />
                    </span>
                  )}
                  {StatusIcon && <StatusIcon className="h-3 w-3" />}
                  {status.label}
                </Badge>
              </div>
              <p className="text-label text-muted-foreground mt-0.5">
                {agent.role_title ?? agent.agent_role}
              </p>
            </div>
          </div>

          <div className="mt-3 flex items-center gap-2 flex-wrap">
            {agent.crew && (
              <Badge variant="outline" className="text-micro gap-1">
                <span
                  className="h-2 w-2 rounded-full shrink-0"
                  style={{ backgroundColor: getCrewDotColor(agent.crew.color) }}
                />
                {agent.crew.name}
              </Badge>
            )}
            {/* CLI adapter badge with the adapter's brand icon. Pre-fix this
                surface only showed llm_provider/llm_model as raw API IDs;
                the CLI adapter (CLAUDE_CODE / CODEX_CLI / CURSOR_CLI / ...)
                was invisible despite being a primary axis of agent identity. */}
            {agent.cli_adapter && CLI_ADAPTERS[agent.cli_adapter] && (() => {
              const adapter = CLI_ADAPTERS[agent.cli_adapter]
              const Icon = adapter.icon
              return (
                <Badge variant="outline" className="text-micro gap-1">
                  <Icon className={cn("h-3 w-3", PROVIDER_ICON_COLOR[adapter.provider])} />
                  {adapter.label}
                </Badge>
              )
            })()}
            {(agent.llm_provider || agent.llm_model) && (() => {
              const ProviderIcon = agent.llm_provider ? PROVIDER_ICONS[agent.llm_provider] : null
              const tint = agent.llm_provider ? PROVIDER_ICON_COLOR[agent.llm_provider] : ""
              const modelLabel = agent.llm_model ? getModelLabel(agent.llm_model) : "—"
              const providerLabel = agent.llm_provider ? getProviderLabel(agent.llm_provider) : "—"
              return (
                <Badge variant="outline" className="text-micro gap-1 text-muted-foreground">
                  {ProviderIcon && <ProviderIcon className={cn("h-3 w-3", tint)} />}
                  <span>{providerLabel}</span>
                  <span className="opacity-60">·</span>
                  <span>{modelLabel}</span>
                </Badge>
              )
            })()}
          </div>

          <div className="mt-3 pt-3 border-t flex items-center gap-4 text-label text-muted-foreground">
            <span className="flex items-center gap-1">
              <Cpu className="h-3 w-3" />
              {agent._count?.skills ?? 0} skills
            </span>
            <span className="flex items-center gap-1">
              <Key className="h-3 w-3" />
              {agent._count?.credentials ?? 0} keys
            </span>
            <span className="flex items-center gap-1">
              <Clock className="h-3 w-3" />
              {agent.last_active_at ? timeAgo(agent.last_active_at) : "no activity"}
            </span>
          </div>
        </CardContent>
      </Card>
    </Link>
  )
})
