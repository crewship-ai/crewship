"use client"

import {
  Loader2,
  Users,
  Terminal,
  Shield,
  Cpu,
  Bot,
} from "lucide-react"
import { CLI_ADAPTERS, getModelLabel, getProviderLabel } from "@/lib/cli-adapters"
import { useAgentFetch } from "@/hooks/use-agent-fetch"

interface AgentContextInfo {
  name: string
  slug: string
  agent_role: string
  system_prompt: string | null
  tool_profile: string | null
  cli_adapter: string | null
  llm_provider: string | null
  llm_model: string | null
  crew_id: string | null
  description: string | null
}

interface CrewInfo {
  name: string
  description: string | null
  network_mode: string | null
  allowed_domains: string | null
}

interface ContextPayload {
  agent: AgentContextInfo
  crew: CrewInfo | null
}

export interface SharedContextTabProps {
  agentId: string
  workspaceId: string | null
}

export function SharedContextTab({ agentId, workspaceId }: SharedContextTabProps) {
  const { data, loading } = useAgentFetch<ContextPayload>(
    async (signal) => {
      const r = await fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`, { signal })
      if (!r.ok) throw new Error(`agent fetch HTTP ${r.status}`)
      const agent: AgentContextInfo = await r.json()
      // Chain the crew fetch so the combined loading state only clears
      // once BOTH have resolved — otherwise the crew section "pops in"
      // after the spinner clears.
      let crew: CrewInfo | null = null
      if (agent.crew_id) {
        const cr = await fetch(`/api/v1/crews/${agent.crew_id}?workspace_id=${workspaceId}`, { signal })
        if (!cr.ok) throw new Error(`crew fetch HTTP ${cr.status}`)
        crew = await cr.json()
      }
      return { agent, crew }
    },
    [agentId, workspaceId],
    { enabled: workspaceId !== null, logLabel: "SharedContextTab" },
  )

  if (loading) return <div className="flex items-center justify-center h-full"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div>
  if (!workspaceId) return <div className="p-4 text-label text-muted-foreground">Select a workspace to view context.</div>
  if (!data) return <div className="p-4 text-label text-muted-foreground">Unable to load agent</div>

  const { agent, crew } = data

  return (
    <div className="p-3 space-y-4 text-sm">
      {/* Agent Info */}
      <div className="space-y-2">
        <div className="flex items-center gap-1.5 text-label font-medium text-muted-foreground uppercase tracking-wider">
          <Bot className="h-3 w-3" />
          Agent
        </div>
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <span className="text-label font-medium">{agent.name}</span>
            <span className="text-micro bg-accent px-1.5 py-0.5 rounded">{agent.agent_role}</span>
          </div>
          {agent.description && <p className="text-label text-muted-foreground line-clamp-2">{agent.description}</p>}
          <div className="flex flex-wrap gap-2 text-micro text-muted-foreground">
            {(agent.llm_provider || agent.llm_model) && (
              <span className="flex items-center gap-1">
                <Cpu className="h-3 w-3" />
                {agent.llm_provider ? getProviderLabel(agent.llm_provider) : "—"} · {agent.llm_model ? getModelLabel(agent.llm_model) : "default"}
              </span>
            )}
            {agent.cli_adapter && <span className="flex items-center gap-1"><Terminal className="h-3 w-3" />{CLI_ADAPTERS[agent.cli_adapter]?.label ?? agent.cli_adapter}</span>}
            {agent.tool_profile && <span className="flex items-center gap-1"><Shield className="h-3 w-3" />{agent.tool_profile}</span>}
          </div>
        </div>
      </div>

      {/* System Prompt */}
      {agent.system_prompt && (
        <div className="space-y-2">
          <div className="text-label font-medium text-muted-foreground uppercase tracking-wider">System Prompt</div>
          <pre className="text-label text-muted-foreground bg-accent p-2 rounded whitespace-pre-wrap break-words max-h-48 overflow-y-auto font-mono leading-relaxed">
            {agent.system_prompt}
          </pre>
        </div>
      )}

      {/* Crew Context */}
      {crew && (
        <div className="space-y-2">
          <div className="flex items-center gap-1.5 text-label font-medium text-muted-foreground uppercase tracking-wider">
            <Users className="h-3 w-3" />
            Crew
          </div>
          <div className="space-y-1">
            <span className="text-label font-medium">{crew.name}</span>
            {crew.description && <p className="text-label text-muted-foreground line-clamp-2">{crew.description}</p>}
            {crew.network_mode && (
              <p className="text-micro text-muted-foreground">
                Network: {crew.network_mode}
                {crew.allowed_domains && ` (${crew.allowed_domains})`}
              </p>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
