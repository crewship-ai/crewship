"use client"

import { useMemo } from "react"
import {
  Bot, Users, Target, Clock, Cpu, Key, FolderOpen, Settings2,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent } from "@/components/ui/card"
import { CrewIcon } from "@/components/ui/crew-icon"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

import { timeAgo } from "@/lib/time"
import Link from "next/link"

interface CrewData {
  id: string
  name: string
  slug: string
  description: string | null
  color: string | null
  icon: string | null
  created_at: string
  _count: { agents: number; members: number }
}

interface AgentData {
  id: string
  name: string
  slug: string
  status: string
  role_title: string | null
  agent_role: string
  llm_provider: string
  llm_model: string
  avatar_seed?: string | null
  avatar_style?: string | null
  crew?: { name: string; slug: string; color: string | null; avatar_style?: string | null } | null
  _count?: { skills: number; credentials: number; chats: number }
  last_active_at?: string | null
}

interface MissionData {
  id: string
  title: string
  status: string
  crew_id: string
  tasks?: { id: string; status: string }[]
  created_at: string
}

const STATUS_CONFIG: Record<string, { label: string; className: string; pulse?: boolean }> = {
  IDLE: { label: "Idle", className: "bg-muted text-muted-foreground" },
  RUNNING: { label: "Running", className: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400", pulse: true },
  ERROR: { label: "Error", className: "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-400" },
  STOPPED: { label: "Stopped", className: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-400" },
}

const MISSION_STATUS: Record<string, { dot: string; text: string }> = {
  IN_PROGRESS: { dot: "bg-blue-500 animate-pulse", text: "text-blue-400" },
  PLANNING: { dot: "bg-purple-500", text: "text-purple-400" },
  COMPLETED: { dot: "bg-green-500", text: "text-green-400" },
  FAILED: { dot: "bg-red-500", text: "text-red-400" },
  CANCELLED: { dot: "bg-gray-500", text: "text-gray-400" },
  REVIEW: { dot: "bg-amber-500", text: "text-amber-400" },
}

export interface CrewsCrewDetailProps {
  crew: CrewData
  agents: AgentData[]
  missions: MissionData[]
  onAgentClick: (agentId: string) => void
}

export function CrewsCrewDetail({ crew, agents, missions, onAgentClick }: CrewsCrewDetailProps) {
  const allCrewMissions = useMemo(
    () => missions.filter((m) => m.crew_id === crew.id),
    [missions, crew.id],
  )

  const crewMissions = useMemo(() => allCrewMissions.slice(0, 5), [allCrewMissions])
  const missionCount = allCrewMissions.length

  return (
    <div className="p-4 sm:p-6 h-full overflow-y-auto space-y-5">
      {/* Crew header */}
      <div className="flex items-start gap-4">
        <CrewIcon icon={crew.icon || "briefcase"} color={crew.color} size="lg" />
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <h2 className="text-lg font-semibold">{crew.name}</h2>
            <span className="text-[11px] font-mono text-muted-foreground">{crew.slug}</span>
          </div>
          {crew.description && (
            <p className="text-[13px] text-muted-foreground mt-0.5">{crew.description}</p>
          )}
          <p className="text-[11px] text-muted-foreground/50 mt-1">
            Created {new Date(crew.created_at).toLocaleDateString()}
          </p>
        </div>
      </div>

      {/* Action buttons */}
      <div className="flex items-center gap-2">
        <Button variant="outline" size="sm" asChild>
          <Link href={`/crews/${crew.id}`}>
            <Settings2 className="mr-1.5 h-3.5 w-3.5" />
            Edit
          </Link>
        </Button>
        <Button variant="outline" size="sm" asChild>
          <Link href={`/crews/${crew.id}/files`}>
            <FolderOpen className="mr-1.5 h-3.5 w-3.5" />
            Files
          </Link>
        </Button>
      </div>

      {/* Stats grid */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
        {[
          { label: "Agents", value: crew._count.agents, icon: Bot },
          { label: "Members", value: crew._count.members, icon: Users },
          { label: "Missions", value: missionCount, icon: Target },
          { label: "Running", value: agents.filter((a) => a.status === "RUNNING").length, icon: Clock },
        ].map(({ label, value, icon: Icon }) => (
          <Card key={label}>
            <CardContent className="p-3">
              <div className="flex items-center gap-1.5 text-muted-foreground">
                <Icon className="h-3.5 w-3.5" />
                <span className="text-[11px]">{label}</span>
              </div>
              <p className="text-xl font-bold mt-1 tabular-nums">{value}</p>
            </CardContent>
          </Card>
        ))}
      </div>

      {/* Agents grid */}
      <div>
        <div className="flex items-center justify-between mb-3">
          <h3 className="text-[13px] font-semibold">Agents</h3>
          <Button variant="outline" size="sm" className="h-7 text-[11px]" asChild>
            <Link href={`/crews/agents/new?crew_id=${crew.id}`}>+ Add</Link>
          </Button>
        </div>

        {agents.length === 0 ? (
          <div className="text-center py-8 text-muted-foreground/50">
            <Bot className="h-8 w-8 mx-auto mb-2 opacity-50" />
            <p className="text-[12px]">No agents in this crew</p>
          </div>
        ) : (
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            {agents.map((agent) => {
              const status = STATUS_CONFIG[agent.status] || STATUS_CONFIG.IDLE
              return (
                <Card
                  key={agent.id}
                  tabIndex={0}
                  role="button"
                  className="hover:border-primary/50 hover:bg-accent/30 hover:shadow-md transition-all duration-150 cursor-pointer focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2 outline-none"
                  onClick={() => onAgentClick(agent.id)}
                  onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onAgentClick(agent.id) } }}
                >
                  <CardContent className="p-4">
                    <div className="flex items-start gap-3">
                      <img
                        src={getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style || agent.crew?.avatar_style)}
                        alt=""
                        className="h-9 w-9 rounded-lg shrink-0"
                      />
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center justify-between gap-2">
                          <h4 className="text-[13px] font-semibold truncate">{agent.name}</h4>
                          <Badge variant="secondary" className={cn("text-[10px] shrink-0 gap-1", status.className)}>
                            {status.pulse && (
                              <span className="relative flex h-1.5 w-1.5">
                                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
                                <span className="relative inline-flex rounded-full h-1.5 w-1.5 bg-emerald-500" />
                              </span>
                            )}
                            {status.label}
                          </Badge>
                        </div>
                        <p className="text-[11px] text-muted-foreground mt-0.5">
                          {agent.role_title || agent.agent_role}
                        </p>
                      </div>
                    </div>

                    <div className="mt-2">
                      <Badge variant="outline" className="text-[10px] gap-1 text-muted-foreground">
                        {agent.llm_provider} / {agent.llm_model}
                      </Badge>
                    </div>

                    <div className="mt-2 pt-2 border-t flex items-center gap-3 text-[11px] text-muted-foreground">
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
              )
            })}
          </div>
        )}
      </div>

      {/* Recent missions */}
      {crewMissions.length > 0 && (
        <div>
          <h3 className="text-[13px] font-semibold mb-3">Recent Missions</h3>
          <div className="space-y-1.5">
            {crewMissions.map((mission) => {
              const ms = MISSION_STATUS[mission.status] || MISSION_STATUS.CANCELLED
              const total = mission.tasks?.length || 0
              const done = mission.tasks?.filter((t) => t.status === "COMPLETED").length || 0
              return (
                <Link
                  key={mission.id}
                  href="/orchestration"
                  className="flex items-center gap-3 px-3 py-2 rounded-md hover:bg-white/[0.04] transition-colors"
                >
                  <div className={cn("w-2 h-2 rounded-full shrink-0", ms.dot)} />
                  <span className="text-[12px] font-medium flex-1 truncate">{mission.title}</span>
                  {total > 0 && (
                    <span className="text-[10px] font-mono text-muted-foreground tabular-nums">
                      {done}/{total}
                    </span>
                  )}
                  <span className="text-[10px] text-muted-foreground/50">
                    {timeAgo(mission.created_at)}
                  </span>
                </Link>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}
