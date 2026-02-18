"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { Bot, Hourglass, Key, Activity, Plus } from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { StatCard } from "@/components/layout/stat-card"
import { FilterBar } from "@/components/layout/filter-bar"
import { Skeleton } from "@/components/ui/skeleton"
import { AgentCard } from "@/components/features/agents/agent-card"
import { useWorkspace } from "@/hooks/use-workspace"
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

interface Credential {
  id: string
}

export default function DashboardPage() {
  const router = useRouter()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [agents, setAgents] = useState<Agent[]>([])
  const [credentials, setCredentials] = useState<Credential[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [activeFilter, setActiveFilter] = useState("All")
  const [onboardingChecked, setOnboardingChecked] = useState(false)

  // Check onboarding status on mount
  useEffect(() => {
    fetch("/api/v1/onboarding/status")
      .then((res) => {
        if (!res.ok) return null
        return res.json()
      })
      .then((data) => {
        if (data && !data.completed) {
          router.push("/onboarding")
          return
        }
        setOnboardingChecked(true)
      })
      .catch(() => setOnboardingChecked(true))
  }, [router])

  useEffect(() => {
    if (!workspaceId || !onboardingChecked) return

    let cancelled = false

    async function fetchData() {
      setLoading(true)
      setError(null)
      try {
        const [agentsRes, credsRes] = await Promise.all([
          fetch(`/api/v1/agents?workspace_id=${workspaceId}`),
          fetch(`/api/v1/credentials?workspace_id=${workspaceId}`),
        ])

        if (!agentsRes.ok || !credsRes.ok) {
          setError("Failed to load dashboard data")
          return
        }

        const [agentsData, credsData] = await Promise.all([
          agentsRes.json() as Promise<Agent[]>,
          credsRes.json() as Promise<Credential[]>,
        ])

        if (!cancelled) {
          setAgents(agentsData)
          setCredentials(credsData)
        }
      } catch {
        if (!cancelled) setError("Failed to load dashboard data")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchData()
    return () => {
      cancelled = true
    }
  }, [workspaceId, onboardingChecked])

  const isLoading = wsLoading || loading

  const totalAgents = agents.length
  const runningNow = agents.filter((a) => a.status === "RUNNING").length
  const apiKeysActive = credentials.length

  const filteredAgents =
    activeFilter === "All"
      ? agents
      : agents.filter((a) => a.status === activeFilter.toUpperCase())

  if (error) {
    return (
      <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
        <PageHeader title="Dashboard" description="Overview of your AI workforce" />
        <p className="text-sm text-destructive">{error}</p>
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Dashboard" description="Overview of your AI workforce">
        <Button asChild>
          <Link href="/agents/new">
            <Plus className="mr-2 h-4 w-4" />
            New Agent
          </Link>
        </Button>
      </PageHeader>

      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 sm:gap-4">
        {isLoading ? (
          <>
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-[104px] rounded-xl" />
            ))}
          </>
        ) : (
          <>
            <StatCard
              title="Total Agents"
              value={totalAgents}
              subtitle={totalAgents === 0 ? "No agents yet" : `${totalAgents} agent${totalAgents === 1 ? "" : "s"}`}
              icon={Bot}
              iconClassName="bg-primary/10 text-primary"
            />
            <StatCard
              title="Running Now"
              value={runningNow}
              subtitle={`of ${totalAgents} agents`}
              icon={Activity}
              iconClassName="bg-emerald-500/10 text-emerald-600"
            />
            <StatCard
              title="Today's Runs"
              value={0}
              subtitle="No runs today"
              icon={Hourglass}
            />
            <StatCard
              title="API Keys Active"
              value={apiKeysActive}
              subtitle={apiKeysActive === 0 ? "Add credentials to get started" : `${apiKeysActive} key${apiKeysActive === 1 ? "" : "s"} configured`}
              icon={Key}
            />
          </>
        )}
      </div>

      <div>
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between mb-4">
          <h2 className="text-base font-semibold">All Agents</h2>
          <FilterBar
            filters={["All", "Running", "Idle", "Error"]}
            active={activeFilter}
            onFilter={setActiveFilter}
          />
        </div>

        {isLoading ? (
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3 sm:gap-4">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-[160px] rounded-xl" />
            ))}
          </div>
        ) : filteredAgents.length === 0 ? (
          <EmptyState
            icon={Bot}
            title={agents.length === 0 ? "No agents yet" : "No matching agents"}
            description={
              agents.length === 0
                ? "Create your first AI agent to start automating tasks. Agents work in crews and can chat, run tasks, and produce files."
                : "No agents match the current filter. Try changing the filter."
            }
          >
            {agents.length === 0 && (
              <Button className="mt-4" asChild>
                <Link href="/agents/new">
                  <Plus className="mr-2 h-4 w-4" />
                  Create First Agent
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
    </div>
  )
}
