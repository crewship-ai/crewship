"use client"

import { useCallback, useEffect, useState } from "react"
import { Bot, Plus } from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { FilterBar } from "@/components/layout/filter-bar"
import { Skeleton } from "@/components/ui/skeleton"
import { AgentCard } from "@/components/features/agents/agent-card"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import Link from "next/link"

interface AgentCrew {
  name: string
  slug: string
  color: string | null
}

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
  crew: AgentCrew | null
  _count: { skills: number; credentials: number; chats: number }
}

export default function AgentsPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()
  const [agents, setAgents] = useState<Agent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [activeFilter, setActiveFilter] = useState("All")

  const fetchAgents = useCallback(async (silent = false) => {
    if (!workspaceId) return
    if (!silent) {
      setLoading(true)
      setError(null)
    }
    try {
      const res = await fetch(`/api/v1/agents?workspace_id=${workspaceId}`)
      if (!res.ok) {
        if (!silent) setError("Failed to load agents")
        return
      }
      const data = (await res.json()) as Agent[]
      setAgents(data)
    } catch {
      if (!silent) setError("Failed to load agents")
    } finally {
      if (!silent) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    if (!workspaceId) {
      if (!wsLoading) setLoading(false)
      return
    }
    fetchAgents()
  }, [workspaceId, wsLoading, fetchAgents])

  // Real-time: refetch agents when status changes
  useRealtimeEvent("agent.status", useCallback(() => { fetchAgents(true) }, [fetchAgents]))

  const isLoading = wsLoading || loading

  const filteredAgents =
    activeFilter === "All"
      ? agents
      : agents.filter((a) => a.status === activeFilter.toUpperCase())

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Agents" description="Manage your AI virtual employees">
        {abilities.can("create", "Agent") && (
          <Button asChild>
            <Link href="/agents/new">
              <Plus className="mr-2 h-4 w-4" />
              New Agent
            </Link>
          </Button>
        )}
      </PageHeader>

      <FilterBar
        filters={["All", "Running", "Idle", "Error", "Stopped"]}
        active={activeFilter}
        onFilter={setActiveFilter}
      />

      {error && <p className="text-sm text-destructive">{error}</p>}

      {isLoading ? (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3 sm:gap-4">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-[160px] rounded-xl" />
          ))}
        </div>
      ) : filteredAgents.length === 0 ? (
        <EmptyState
          icon={Bot}
          title={agents.length === 0 ? "No agents yet" : "No matching agents"}
          description={
            agents.length === 0
              ? "Create your first AI agent to start automating tasks."
              : "No agents match the current filter."
          }
        >
          {agents.length === 0 && (
            <Button className="mt-4" asChild>
              <Link href="/agents/new">
                <Plus className="mr-2 h-4 w-4" />
                Create Agent
              </Link>
            </Button>
          )}
        </EmptyState>
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3 sm:gap-4">
          {filteredAgents.map((agent) => (
            <AgentCard key={agent.id} agent={agent} />
          ))}
        </div>
      )}
    </div>
  )
}
