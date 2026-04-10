"use client"

import { useCallback, useEffect, useState } from "react"
import { Bot, Plus, AlertCircle } from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageShell } from "@/components/layout/page-shell"
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

  // Real-time: refetch agents when status changes or CRUD operations occur
  useRealtimeEvent("agent.status", useCallback(() => { fetchAgents(true) }, [fetchAgents]))
  useRealtimeEvent("agent.created", useCallback(() => { fetchAgents(true) }, [fetchAgents]))
  useRealtimeEvent("agent.updated", useCallback(() => { fetchAgents(true) }, [fetchAgents]))
  useRealtimeEvent("agent.deleted", useCallback(() => { fetchAgents(true) }, [fetchAgents]))

  const isLoading = wsLoading || loading

  const filteredAgents =
    activeFilter === "All"
      ? agents
      : agents.filter((a) => a.status === activeFilter.toUpperCase())

  return (
    <PageShell
      title="Agents"
      description="Manage your AI virtual employees"
      actions={
        abilities.can("create", "Agent") && (
          <Button asChild>
            <Link href="/agents/new">
              <Plus className="mr-2 h-4 w-4" />
              New Agent
            </Link>
          </Button>
        )
      }
    >
      <FilterBar
        filters={["All", "Running", "Idle", "Error", "Stopped"]}
        active={activeFilter}
        onFilter={setActiveFilter}
      />

      {error && (
        <div className="flex items-center gap-3">
          <AlertCircle className="h-5 w-5 text-destructive shrink-0" />
          <p className="text-body text-destructive flex-1">{error}</p>
        </div>
      )}

      {isLoading ? (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-[160px] rounded-[var(--radius)]" />
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
          {agents.length === 0 ? (
            <Button className="mt-4" asChild>
              <Link href="/agents/new">
                <Plus className="mr-2 h-4 w-4" />
                Create Agent
              </Link>
            </Button>
          ) : (
            <Button className="mt-4" variant="outline" onClick={() => setActiveFilter("All")}>
              Clear filter
            </Button>
          )}
        </EmptyState>
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {filteredAgents.map((agent) => (
            <AgentCard key={agent.id} agent={agent} />
          ))}
        </div>
      )}
    </PageShell>
  )
}
