"use client"

import { useEffect, useState, useCallback } from "react"
import { useParams } from "next/navigation"
import { Plug, Loader2, AlertCircle, Info } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { SectionCard } from "@/components/ui/section-card"
import { toast } from "sonner"
import { useWorkspace } from "@/hooks/use-workspace"
import { MCPConfigEditor } from "@/components/features/mcp/mcp-config-editor"

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface AgentData {
  id: string
  name: string
  crew_id: string | null
  mcp_config_json: string | null
}

interface CrewData {
  id: string
  name: string
  mcp_config_json: string | null
}

interface MCPConfig {
  mcpServers: Record<string, unknown>
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function mergeConfigs(crewJson: string | null, agentJson: string | null): string {
  const crewServers = parseServers(crewJson)
  const agentServers = parseServers(agentJson)

  const merged = { ...crewServers, ...agentServers }
  if (Object.keys(merged).length === 0) return ""
  return JSON.stringify({ mcpServers: merged }, null, 2)
}

function parseServers(raw: string | null | undefined): Record<string, unknown> {
  if (!raw || raw.trim() === "") return {}
  try {
    const parsed: MCPConfig = JSON.parse(raw)
    return parsed.mcpServers ?? {}
  } catch {
    return {}
  }
}

// ---------------------------------------------------------------------------
// Client Component
// ---------------------------------------------------------------------------

export function MCPPageClient() {
  const { agentId } = useParams<{ agentId: string }>()
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [agent, setAgent] = useState<AgentData | null>(null)
  const [crew, setCrew] = useState<CrewData | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const [configJson, setConfigJson] = useState("")
  const [savedJson, setSavedJson] = useState("")

  // -- Fetch agent (and crew if applicable) ---------------------------------

  useEffect(() => {
    if (!workspaceId) return
    let cancelled = false

    async function fetchData() {
      try {
        const agentRes = await fetch(
          `/api/v1/agents/${agentId}?workspace_id=${workspaceId}`,
        )
        if (!agentRes.ok) {
          if (!cancelled) setError("Failed to load agent")
          return
        }

        const agentData: AgentData = await agentRes.json()
        if (cancelled) return

        setAgent(agentData)
        const json = agentData.mcp_config_json ?? ""
        setConfigJson(json)
        setSavedJson(json)

        // Fetch crew config for merged preview
        if (agentData.crew_id) {
          try {
            const crewRes = await fetch(
              `/api/v1/crews/${agentData.crew_id}?workspace_id=${workspaceId}`,
            )
            if (crewRes.ok) {
              const crewData: CrewData = await crewRes.json()
              if (!cancelled) setCrew(crewData)
            }
          } catch {
            // Non-critical: merged preview just won't include crew config
          }
        }
      } catch {
        if (!cancelled) setError("Network error. Please try again.")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchData()
    return () => {
      cancelled = true
    }
  }, [agentId, workspaceId])

  // -- Save handler ---------------------------------------------------------

  const handleSave = useCallback(async () => {
    if (!workspaceId) return
    setSaving(true)

    try {
      const res = await fetch(
        `/api/v1/agents/${agentId}?workspace_id=${workspaceId}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ mcp_config_json: configJson || null }),
        },
      )

      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: "Failed to save" }))
        toast.error(
          typeof data.error === "string" ? data.error : "Failed to save MCP configuration",
        )
        return
      }

      setSavedJson(configJson)
      toast.success("MCP configuration saved")
    } catch {
      toast.error("Network error saving MCP configuration")
    } finally {
      setSaving(false)
    }
  }, [agentId, workspaceId, configJson])

  // -- Loading / error states -----------------------------------------------

  if (wsLoading || loading) {
    return <MCPPageSkeleton />
  }

  if (error) {
    return (
      <div className="p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-body">{error}</p>
        </div>
      </div>
    )
  }

  const hasChanges = configJson !== savedJson
  const mergedJson = mergeConfigs(crew?.mcp_config_json ?? null, configJson)
  const hasCrew = Boolean(agent?.crew_id && crew)

  return (
    <div className="p-6 space-y-6 max-w-3xl">
      <div>
        <h2 className="text-title font-semibold">MCP Servers</h2>
        <p className="text-body text-muted-foreground mt-1">
          Model Context Protocol servers available to this agent.
        </p>
      </div>

      {/* Info banner */}
      <div className="flex items-start gap-2 rounded-lg border border-border bg-surface-subtle p-3">
        <Info className="h-4 w-4 text-muted-foreground mt-0.5 shrink-0" />
        <p className="text-label text-muted-foreground">
          Use <code className="font-mono text-foreground">{"${VAR_NAME}"}</code> to reference credentials assigned to this agent.
          Claude Code expands environment variables automatically.
        </p>
      </div>

      {/* Agent MCP servers */}
      <SectionCard
        title={
          <span className="flex items-center gap-2">
            <Plug className="h-4 w-4 text-muted-foreground" />
            Agent MCP Servers
          </span>
        }
        description="MCP servers specific to this agent. These are merged with crew-level servers at runtime."
      >
        <div className="space-y-4">
          <MCPConfigEditor value={configJson} onChange={setConfigJson} workspaceId={workspaceId ?? undefined} />

          {hasChanges && (
            <Button size="sm" onClick={handleSave} disabled={saving} className="gap-1.5">
              {saving && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
              {saving ? "Saving..." : "Save MCP Configuration"}
            </Button>
          )}
        </div>
      </SectionCard>

      {/* Effective (merged) configuration */}
      {hasCrew && (
        <SectionCard
          surface="subtle"
          title={
            <span className="flex items-center gap-2">
              <Plug className="h-4 w-4 text-muted-foreground" />
              Effective Configuration
            </span>
          }
          description={`Merged view of crew-level (${crew?.name}) and agent-level MCP servers. Agent servers override crew servers with the same name.`}
        >
          {mergedJson ? (
            <MCPConfigEditor value={mergedJson} onChange={() => {}} readOnly />
          ) : (
            <p className="text-body text-muted-foreground">
              No MCP servers configured at either level.
            </p>
          )}
        </SectionCard>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Skeleton
// ---------------------------------------------------------------------------

function MCPPageSkeleton() {
  return (
    <div className="p-6 space-y-6 max-w-3xl">
      <div className="space-y-2">
        <Skeleton className="h-7 w-40" />
        <Skeleton className="h-4 w-72" />
      </div>
      <Skeleton className="h-12 w-full rounded-lg" />
      <SectionCard
        title={<Skeleton className="h-5 w-40" />}
        description={<Skeleton className="h-4 w-72" />}
      >
        <div className="space-y-4">
          <Skeleton className="h-32 w-full" />
          <Skeleton className="h-32 w-full" />
        </div>
      </SectionCard>
    </div>
  )
}
