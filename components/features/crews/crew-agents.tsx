"use client"

import { Bot, Plus } from "lucide-react"
import { Button } from "@/components/ui/button"
import { EmptyState } from "@/components/layout/empty-state"
import { AgentCard } from "@/components/features/agents/agent-card"
import Link from "next/link"

interface Agent {
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
  crew: { name: string; slug: string; color: string | null } | null
  _count: { skills: number; credentials: number; chats: number }
}

interface CrewAgentsProps {
  agents: Agent[]
  crewId: string
  canCreate: boolean
}

export function CrewAgents({ agents, crewId, canCreate }: CrewAgentsProps) {
  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <h2 className="text-default font-semibold">Agents</h2>
        {canCreate && agents.length > 0 && (
          <Button size="sm" asChild>
            <Link href={`/fleet/agents/new?crew_id=${crewId}`}>New Agent</Link>
          </Button>
        )}
      </div>
      {agents.length === 0 ? (
        <EmptyState
          icon={Bot}
          title="No agents in this crew"
          description="Add an agent to start automating tasks with this crew."
        >
          {canCreate && (
            <Button className="mt-4" size="sm" asChild>
              <Link href={`/fleet/agents/new?crew_id=${crewId}`}>
                <Plus className="mr-2 h-4 w-4" />
                Add Agent
              </Link>
            </Button>
          )}
        </EmptyState>
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          {agents.map((agent) => (
            <AgentCard key={agent.id} agent={agent} />
          ))}
        </div>
      )}
    </div>
  )
}
