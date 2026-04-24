"use client"

import { useCallback, useEffect, useRef, useState } from "react"
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
  // Backend `agentResponse` returns these as nullable pointers, so mirror
  // that at the boundary — callers must handle null rather than assuming
  // a provider/model has been picked.
  llm_provider: string | null
  llm_model: string | null
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

  // Track in-flight request so a late response from workspace A can never
  // overwrite state after the user has switched to workspace B.
  const abortRef = useRef<AbortController | null>(null)

  const fetchAgents = useCallback(async (silent = false) => {
    if (!workspaceId) {
      setAgents([])
      setLoading(false)
      return
    }
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    if (!silent) {
      setLoading(true)
      setError(null)
    }
    try {
      const res = await fetch(
        `/api/v1/agents?workspace_id=${workspaceId}`,
        { signal: controller.signal },
      )
      if (controller.signal.aborted) return
      if (!res.ok) {
        if (!silent) {
          setAgents([])
          setError("Failed to load agents")
        }
        return
      }
      const data = (await res.json()) as Agent[]
      if (controller.signal.aborted) return
      setAgents(data)
    } catch (err) {
      if ((err as { name?: string })?.name === "AbortError") return
      if (!silent) {
        setAgents([])
        setError("Failed to load agents")
      }
    } finally {
      if (!silent && !controller.signal.aborted) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    if (!workspaceId) {
      setAgents([])
      if (!wsLoading) setLoading(false)
      return
    }
    fetchAgents()
    return () => {
      abortRef.current?.abort()
    }
  }, [workspaceId, wsLoading, fetchAgents])

  // Real-time: debounced refetch on agent events (prevents burst of 4 concurrent fetches)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const debouncedRefetch = useCallback(() => {
    // Skip while the initial load is still running: otherwise a silent
    // refetch here would abort the visible request and never clear the
    // `loading` flag, leaving the page stuck in skeleton mode.
    if (loading) return
    if (debounceRef.current !== null) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      debounceRef.current = null
      void fetchAgents(true)
    }, 150)
  }, [fetchAgents, loading])

  // Clear any pending timer on unmount / workspace change so a late
  // timeout cannot overwrite state with data from a previous workspace.
  useEffect(() => {
    return () => {
      if (debounceRef.current !== null) {
        clearTimeout(debounceRef.current)
        debounceRef.current = null
      }
    }
  }, [workspaceId])

  useRealtimeEvent("agent.status", debouncedRefetch)
  useRealtimeEvent("agent.created", debouncedRefetch)
  useRealtimeEvent("agent.updated", debouncedRefetch)
  useRealtimeEvent("agent.deleted", debouncedRefetch)

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
            <Link href="/crews/agents/new">
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
            abilities.can("create", "Agent") ? (
              <Button className="mt-4" asChild>
                <Link href="/crews/agents/new">
                  <Plus className="mr-2 h-4 w-4" />
                  Create Agent
                </Link>
              </Button>
            ) : null
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
