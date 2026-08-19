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
  /**
   * Kept on the prop surface, no longer read. It used to be interpolated into
   * the create-agent link's ?crew_id=; see CREATE_AGENT_HREF for why an id
   * cannot be forwarded to the replacement. Dropping it would be a breaking
   * signature change for a component this repair is not otherwise touching.
   */
  crewId: string
  canCreate: boolean
}

/**
 * Where "New Agent" goes.
 *
 * It was /crews/agents/new?crew_id=<id>, a route the /crews redesign deleted.
 * Creating an agent has no route now — it is a dialog on /crews, and
 * ?new=agent is the only deep link into it
 * (components/features/crews/crews-subbar.tsx:47-63).
 *
 * The crew does not survive the trip, deliberately. That handler opens the
 * dialog without pre-selecting a crew (only the toolbar button does that), and
 * the pre-selection it would take is a crew *slug* — this component holds an
 * id. Putting the id in ?crew= would hand use-crews-selection a value it
 * matches against slugs, which reads as "no such crew" and clears itself. The
 * dialog lists the crews; one extra click beats a link that lies.
 */
const CREATE_AGENT_HREF = "/crews?new=agent"

export function CrewAgents({ agents, crewId: _crewId, canCreate }: CrewAgentsProps) {
  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <h2 className="text-default font-semibold">Agents</h2>
        {canCreate && agents.length > 0 && (
          <Button size="sm" asChild>
            <Link href={CREATE_AGENT_HREF}>New Agent</Link>
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
              <Link href={CREATE_AGENT_HREF}>
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
