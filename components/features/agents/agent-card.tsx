"use client"

import Link from "next/link"
import { Cpu, Key, MessageSquare } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

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
  llm_provider: string
  llm_model: string
  avatar_seed?: string | null
  avatar_style?: string | null
  crew: AgentCrew | null
  _count: AgentCount
}

const statusConfig: Record<string, { label: string; className: string }> = {
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
  },
  STOPPED: {
    label: "Stopped",
    className: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-400",
  },
}

export function AgentCard({ agent }: { agent: AgentData }) {
  const status = statusConfig[agent.status] ?? statusConfig.IDLE

  return (
    <Link href={`/agents/${agent.id}`}>
      <Card className="hover:border-primary/50 transition-colors cursor-pointer h-full">
        <CardContent className="p-4 sm:p-5">
          <div className="flex items-start gap-3">
            <img
              src={getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style || agent.crew?.avatar_style)}
              alt={agent.name}
              className="h-10 w-10 rounded-lg shrink-0"
            />
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-2 flex-wrap">
                <h3 className="text-sm font-semibold truncate">{agent.name}</h3>
                <Badge variant="secondary" className={`text-[10px] shrink-0 gap-1.5 ${status.className}`}>
                  {agent.status === "RUNNING" && (
                    <span className="relative flex h-1.5 w-1.5">
                      <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
                      <span className="relative inline-flex rounded-full h-1.5 w-1.5 bg-emerald-500" />
                    </span>
                  )}
                  {status.label}
                </Badge>
              </div>
              <p className="text-xs text-muted-foreground mt-0.5">
                {agent.role_title ?? agent.agent_role}
              </p>
            </div>
          </div>

          <div className="mt-3 flex items-center gap-2 flex-wrap">
            {agent.crew && (
              <Badge variant="outline" className="text-[10px] gap-1">
                <span
                  className="h-2 w-2 rounded-full shrink-0"
                  style={{ backgroundColor: agent.crew.color ?? "#6b7280" }}
                />
                {agent.crew.name}
              </Badge>
            )}
            <span className="text-[10px] text-muted-foreground">
              {agent.llm_provider} / {agent.llm_model}
            </span>
          </div>

          <div className="mt-3 pt-3 border-t flex items-center gap-4 text-xs text-muted-foreground">
            <span className="flex items-center gap-1">
              <Cpu className="h-3 w-3" />
              {agent._count?.skills ?? 0} skills
            </span>
            <span className="flex items-center gap-1">
              <Key className="h-3 w-3" />
              {agent._count?.credentials ?? 0} keys
            </span>
            <span className="flex items-center gap-1">
              <MessageSquare className="h-3 w-3" />
              {agent._count?.chats ?? 0} sessions
            </span>
          </div>
        </CardContent>
      </Card>
    </Link>
  )
}
