"use client"

import * as React from "react"
import {
  Plug,
  Plus,
  Globe,
  Terminal,
  Users,
  ChevronRight,
  ChevronDown,
  Bot,
  Check,
  Trash2,
  Settings2,
  KeyRound,
  ExternalLink,
  Loader2,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { Badge } from "@/components/ui/badge"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { toast } from "sonner"
import { MCP_TEMPLATES, TEMPLATE_ICONS } from "@/components/features/mcp/templates"
import type { MCPTemplate } from "@/components/features/mcp/types"
import { CredentialPicker } from "@/components/features/mcp/components/credential-picker"
import { useCredentials } from "@/components/features/mcp/hooks/use-credentials"

// ---------------------------------------------------------------------------
// OAuth Auto-Connect component
// ---------------------------------------------------------------------------

function OAuthAutoConnect({
  serverName,
  mcpURL,
  workspaceId,
  authStatus,
  onCredentialCreated,
}: {
  serverName: string
  mcpURL: string
  workspaceId: string | null
  authStatus: "connected" | "missing" | "expired" | "none"
  onCredentialCreated: (credId: string) => Promise<void>
}) {
  const [status, setStatus] = React.useState<"idle" | "discovering" | "authorizing" | "polling" | "done" | "error">(
    authStatus === "connected" ? "done" : "idle",
  )
  const [error, setError] = React.useState("")
  const pollRef = React.useRef<ReturnType<typeof setInterval> | null>(null)

  React.useEffect(() => {
    return () => {
      if (pollRef.current) clearInterval(pollRef.current)
    }
  }, [])

  async function handleConnect() {
    if (!workspaceId) return
    setStatus("discovering")
    setError("")

    try {
      const res = await fetch(`/api/v1/oauth/auto-connect?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mcp_url: mcpURL, server_name: serverName }),
      })
      const data = await res.json()

      if (data.status === "authorize") {
        setStatus("authorizing")
        // Open browser for OAuth consent
        window.open(data.auth_url, "_blank", "width=600,height=700")

        // Poll credential status until ACTIVE
        const credId = data.credential_id
        pollRef.current = setInterval(async () => {
          try {
            const credRes = await fetch(`/api/v1/credentials/${credId}?workspace_id=${workspaceId}`)
            if (credRes.ok) {
              const cred = await credRes.json()
              if (cred.status === "ACTIVE") {
                if (pollRef.current) clearInterval(pollRef.current)
                pollRef.current = null
                setStatus("done")
                await onCredentialCreated(credId)
              }
            }
          } catch { /* keep polling */ }
        }, 2000)

        // Stop polling after 2 minutes
        setTimeout(() => {
          if (pollRef.current) {
            clearInterval(pollRef.current)
            pollRef.current = null
            setStatus("error")
            setError("Authorization timed out. Please try again.")
          }
        }, 120000)
      } else if (data.status === "needs_client_id") {
        setStatus("error")
        setError(data.message || "Please provide Client ID manually via OAuth form in credential picker.")
      } else {
        setStatus("error")
        setError(data.error || "Unknown error")
      }
    } catch {
      setStatus("error")
      setError("Network error")
    }
  }

  if (status === "done" && authStatus !== "missing" && authStatus !== "expired") {
    return (
      <div className="rounded-md border border-green-500/30 bg-green-500/5 p-4">
        <div className="flex items-center gap-2 text-sm text-green-600 dark:text-green-400">
          <Check className="h-4 w-4" />
          OAuth connected
        </div>
      </div>
    )
  }

  const isMissing = authStatus === "missing"
  const isExpired = authStatus === "expired"

  return (
    <div className={`rounded-md border p-4 space-y-3 ${
      isMissing ? "border-destructive/50 bg-destructive/5" :
      isExpired ? "border-yellow-500/50 bg-yellow-500/5" :
      "bg-background"
    }`}>
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-sm font-medium">
          <ExternalLink className="h-4 w-4 text-muted-foreground" />
          Authentication
        </div>
        {isMissing && (
          <Badge variant="destructive" className="text-xs">Credential missing</Badge>
        )}
        {isExpired && (
          <Badge variant="outline" className="text-xs border-yellow-500 text-yellow-600">Expired</Badge>
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        {isMissing
          ? "The credential for this integration was deleted. Reconnect to restore access."
          : isExpired
            ? "The OAuth token has expired. Reconnect to refresh."
            : "Connect with OAuth to automatically authenticate with this service."}
      </p>
      {error && (
        <p className="text-xs text-destructive">{error}</p>
      )}
      <Button
        size="sm"
        variant={isMissing || isExpired ? "destructive" : "default"}
        onClick={handleConnect}
        disabled={status === "discovering" || status === "authorizing" || status === "polling"}
      >
        {(status === "discovering" || status === "authorizing") && (
          <Loader2 className="mr-2 h-3 w-3 animate-spin" />
        )}
        {status === "authorizing" ? "Waiting for authorization..."
          : isMissing || isExpired ? "Reconnect with OAuth"
          : "Connect with OAuth"}
      </Button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface CrewIntegration {
  id: string
  crew_id: string
  crew_name: string
  crew_slug: string
  name: string
  display_name: string
  transport: string
  endpoint: string | null
  command: string | null
  args_json: string | null
  env_json: string | null
  icon: string | null
  enabled: boolean
  created_at: string
  updated_at: string
  agent_binding_count: number
  auth_status: "connected" | "missing" | "expired" | "none"
}

interface AgentInfo {
  id: string
  name: string
  slug: string
}

interface CrewInfo {
  id: string
  name: string
  slug: string
}

interface AgentBinding {
  id: string
  mcp_server_id: string
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function parseArgs(argsJson: string | null): string {
  if (!argsJson) return ""
  try {
    const arr = JSON.parse(argsJson) as string[]
    if (!Array.isArray(arr)) return ""
    // Round-trip safe: JSON-encode each arg and join, so spaces inside args are preserved
    return JSON.stringify(arr)
  } catch {
    return ""
  }
}

function serializeArgs(argsStr: string): string {
  const trimmed = argsStr.trim()
  if (!trimmed) return "[]"
  // If it's already valid JSON array, use as-is
  try {
    const parsed = JSON.parse(trimmed)
    if (Array.isArray(parsed)) return JSON.stringify(parsed)
  } catch {
    // Not JSON — fall back to space-splitting (user typed plain text)
  }
  const parts = trimmed.split(/\s+/).filter(Boolean)
  return JSON.stringify(parts)
}

function parseEnv(envJson: string | null): { key: string; value: string }[] {
  if (!envJson) return []
  try {
    const obj = JSON.parse(envJson) as Record<string, string>
    return Object.entries(obj).map(([key, value]) => ({ key, value }))
  } catch {
    return []
  }
}

function serializeEnv(entries: { key: string; value: string }[]): string {
  return JSON.stringify(
    Object.fromEntries(entries.filter((e) => e.key.trim()).map((e) => [e.key, e.value])),
  )
}

function subtitleFor(server: CrewIntegration): string {
  if (server.transport === "streamable-http") return server.endpoint ?? ""
  const cmd = server.command ?? ""
  const args = parseArgs(server.args_json)
  return `${cmd} ${args}`.trim()
}

// ---------------------------------------------------------------------------
// Template popover (shared between header and empty state)
// ---------------------------------------------------------------------------

function TemplatePopover({
  open,
  onOpenChange,
  onSelect,
  trigger,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onSelect: (t: MCPTemplate | null) => void
  trigger: React.ReactNode
}) {
  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger asChild>{trigger}</PopoverTrigger>
      <PopoverContent className="w-80 p-3" align="end">
        <div className="space-y-2">
          <p className="text-sm font-medium">Add from template</p>
          <div className="grid grid-cols-2 gap-2">
            {MCP_TEMPLATES.map((t) => {
              const Icon = TEMPLATE_ICONS[t.icon] ?? Plug
              return (
                <button
                  key={t.name}
                  type="button"
                  className="flex items-center gap-2 rounded-md border px-3 py-2 text-left text-sm hover:bg-muted/60 transition-colors"
                  onClick={() => onSelect(t)}
                >
                  <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
                  {t.label}
                </button>
              )
            })}
          </div>
          <button
            type="button"
            className="flex w-full items-center gap-2 rounded-md border border-dashed px-3 py-2 text-sm text-muted-foreground hover:bg-muted/60 transition-colors"
            onClick={() => onSelect(null)}
          >
            <Terminal className="h-4 w-4" />
            Custom server
          </button>
        </div>
      </PopoverContent>
    </Popover>
  )
}

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

export default function IntegrationsPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()
  const canManage = abilities.can("create", "Credential")

  // Data
  const [servers, setServers] = React.useState<CrewIntegration[]>([])
  const [crews, setCrews] = React.useState<CrewInfo[]>([])
  const [crewAgents, setCrewAgents] = React.useState<Record<string, AgentInfo[]>>({})
  const [agentBindings, setAgentBindings] = React.useState<Record<string, Set<string>>>({})
  const [bindingIds, setBindingIds] = React.useState<Record<string, Record<string, string>>>({})

  // UI state
  const [loading, setLoading] = React.useState(true)
  const [expandedId, setExpandedId] = React.useState<string | null>(null)
  const [templatePopoverOpen, setTemplatePopoverOpen] = React.useState(false)
  const [emptyPopoverOpen, setEmptyPopoverOpen] = React.useState(false)
  const [confirmDeleteId, setConfirmDeleteId] = React.useState<string | null>(null)

  // -------------------------------------------------------------------------
  // Fetch all data
  // -------------------------------------------------------------------------

  const fetchAll = React.useCallback(async (wid: string) => {
    try {
      const [crewRes, crewsListRes] = await Promise.all([
        fetch(`/api/v1/integrations/crews?workspace_id=${wid}`),
        fetch(`/api/v1/crews?workspace_id=${wid}`),
      ])

      const data: CrewIntegration[] = crewRes.ok ? (await crewRes.json()) ?? [] : []
      setServers(data)

      const crewsList: CrewInfo[] = crewsListRes.ok ? (await crewsListRes.json()) ?? [] : []
      setCrews(crewsList)

      // Fetch agents for each crew that has integrations
      const crewIds = [...new Set(data.map((s) => s.crew_id))]
      const agentMap: Record<string, AgentInfo[]> = {}
      await Promise.all(
        crewIds.map(async (cid) => {
          const r = await fetch(`/api/v1/agents?workspace_id=${wid}&crew_id=${cid}`)
          if (r.ok) {
            const agents = await r.json()
            agentMap[cid] = Array.isArray(agents)
              ? agents.map((a: AgentInfo) => ({ id: a.id, name: a.name, slug: a.slug }))
              : []
          }
        }),
      )
      setCrewAgents(agentMap)

      // Fetch agent bindings
      const allAgents = Object.values(agentMap).flat()
      const bMap: Record<string, Set<string>> = {}
      const idMap: Record<string, Record<string, string>> = {}
      await Promise.all(
        allAgents.map(async (agent) => {
          const r = await fetch(`/api/v1/agents/${agent.id}/integrations?workspace_id=${wid}`)
          if (r.ok) {
            const bindings: AgentBinding[] = (await r.json()) ?? []
            if (Array.isArray(bindings)) {
              for (const b of bindings) {
                if (!bMap[b.mcp_server_id]) bMap[b.mcp_server_id] = new Set()
                bMap[b.mcp_server_id].add(agent.id)
                if (!idMap[b.mcp_server_id]) idMap[b.mcp_server_id] = {}
                idMap[b.mcp_server_id][agent.id] = b.id
              }
            }
          }
        }),
      )
      setAgentBindings(bMap)
      setBindingIds(idMap)
    } catch {
      setServers([])
    }
  }, [])

  const activeWsRef = React.useRef<string | null>(null)
  React.useEffect(() => {
    if (wsLoading || !workspaceId) {
      if (!wsLoading) setLoading(false)
      return
    }
    activeWsRef.current = workspaceId
    ;(async () => {
      setLoading(true)
      await fetchAll(workspaceId)
      // Only commit loading state if this is still the active workspace
      if (activeWsRef.current === workspaceId) setLoading(false)
    })()
    return () => {
      activeWsRef.current = null
    }
  }, [workspaceId, wsLoading, fetchAll])

  // -------------------------------------------------------------------------
  // Add from template / custom
  // -------------------------------------------------------------------------

  async function handleAddServer(template: MCPTemplate | null) {
    if (!workspaceId) return
    setTemplatePopoverOpen(false)
    setEmptyPopoverOpen(false)

    const defaultCrewId = servers[0]?.crew_id ?? crews[0]?.id
    if (!defaultCrewId) {
      toast.error("Create a crew first before adding integrations")
      return
    }

    const payload: Record<string, unknown> = template
      ? {
          name: template.name,
          display_name: template.label,
          transport: template.transport,
          command: template.command ?? null,
          args_json: template.args ? serializeArgs(template.args) : null,
          endpoint: template.url ?? null,
          env_json: template.envHint
            ? JSON.stringify(
                Object.fromEntries(template.envHint.split(",").map((h) => [h.trim(), ""])),
              )
            : null,
          enabled: true,
        }
      : {
          name: "custom-server",
          display_name: "Custom Server",
          transport: "stdio",
          command: "",
          args_json: "[]",
          endpoint: null,
          env_json: null,
          enabled: true,
        }

    try {
      let res = await fetch(
        `/api/v1/crews/${defaultCrewId}/integrations?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
        },
      )
      // If name conflict, retry with a numbered suffix
      if (res.status === 409 && payload.name) {
        const suffixed = { ...payload, name: `${payload.name}-${Date.now().toString(36).slice(-4)}` }
        res = await fetch(
          `/api/v1/crews/${defaultCrewId}/integrations?workspace_id=${workspaceId}`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(suffixed),
          },
        )
      }
      if (!res.ok) {
        const d = await res.json().catch(() => null)
        toast.error(d?.error ?? "Failed to create server")
        return
      }
      const created = await res.json().catch(() => null)
      toast.success(`"${payload.display_name ?? payload.name}" added`)
      await fetchAll(workspaceId)
      // Auto-expand the new row
      if (created?.id) {
        setExpandedId(created.id)
      } else {
        // Fallback: expand by name match
        setExpandedId(null)
      }
    } catch {
      toast.error("Network error")
    }
  }

  // -------------------------------------------------------------------------
  // Inline field update (PATCH)
  // -------------------------------------------------------------------------

  async function patchServer(
    server: CrewIntegration,
    fields: Record<string, unknown>,
  ) {
    if (!workspaceId) return
    try {
      const res = await fetch(
        `/api/v1/crews/${server.crew_id}/integrations/${server.id}?workspace_id=${workspaceId}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(fields),
        },
      )
      if (!res.ok) {
        const d = await res.json().catch(() => null)
        toast.error(d?.error ?? "Failed to update")
        return
      }
      await fetchAll(workspaceId)
    } catch {
      toast.error("Network error")
    }
  }

  // -------------------------------------------------------------------------
  // Crew move
  // -------------------------------------------------------------------------

  async function handleCrewMove(server: CrewIntegration, newCrewId: string) {
    if (!workspaceId || newCrewId === server.crew_id) return
    const serverName = server.name

    try {
      // Create on new crew FIRST (safe: if this fails, nothing was deleted)
      const payload = {
        name: server.name,
        display_name: server.display_name,
        transport: server.transport,
        command: server.command,
        args_json: server.args_json,
        endpoint: server.endpoint,
        env_json: server.env_json,
        icon: server.icon,
        enabled: server.enabled,
      }
      const createRes = await fetch(
        `/api/v1/crews/${newCrewId}/integrations?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
        },
      )
      if (!createRes.ok) {
        toast.error("Failed to create on new crew")
        return
      }

      // Delete from old crew only after successful create
      const delRes = await fetch(
        `/api/v1/crews/${server.crew_id}/integrations/${server.id}?workspace_id=${workspaceId}`,
        { method: "DELETE" },
      )
      if (!delRes.ok) {
        toast.error("Moved but failed to remove from old crew")
      }

      toast.success("Integration moved")
      await fetchAll(workspaceId)

      // Find the new server by name to keep panel open
      const refetchRes = await fetch(
        `/api/v1/integrations/crews?workspace_id=${workspaceId}`,
      )
      if (refetchRes.ok) {
        const all: CrewIntegration[] = (await refetchRes.json()) ?? []
        const moved = all.find((s) => s.name === serverName && s.crew_id === newCrewId)
        if (moved) setExpandedId(moved.id)
      }
    } catch {
      toast.error("Network error")
    }
  }

  // -------------------------------------------------------------------------
  // Agent binding toggle
  // -------------------------------------------------------------------------

  async function handleAgentToggle(
    server: CrewIntegration,
    agent: AgentInfo,
    hasAccess: boolean,
    hasAnyBindings: boolean,
  ) {
    if (!workspaceId || !canManage) return

    try {
      if (hasAccess && hasAnyBindings) {
        // Remove binding
        const bId = bindingIds[server.id]?.[agent.id]
        if (bId) {
          await fetch(
            `/api/v1/agents/${agent.id}/integrations/${bId}?workspace_id=${workspaceId}`,
            { method: "DELETE" },
          )
        }
      } else {
        // Create binding
        await fetch(`/api/v1/agents/${agent.id}/integrations?workspace_id=${workspaceId}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            mcp_server_id: server.id,
            mcp_server_scope: "crew",
            enabled: true,
          }),
        })
      }
      await fetchAll(workspaceId)
    } catch {
      toast.error("Network error")
    }
  }

  // -------------------------------------------------------------------------
  // Delete server
  // -------------------------------------------------------------------------

  async function handleDelete(server: CrewIntegration) {
    if (!workspaceId) return
    try {
      const res = await fetch(
        `/api/v1/crews/${server.crew_id}/integrations/${server.id}?workspace_id=${workspaceId}`,
        { method: "DELETE" },
      )
      if (res.ok) {
        toast.success(`"${server.display_name || server.name}" deleted`)
        setExpandedId(null)
        setConfirmDeleteId(null)
        await fetchAll(workspaceId)
      } else {
        toast.error("Failed to delete integration")
      }
    } catch {
      toast.error("Network error")
    }
  }

  // -------------------------------------------------------------------------
  // Render: loading
  // -------------------------------------------------------------------------

  if (wsLoading || loading) {
    return (
      <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
        <PageHeader title="Integrations" description="Manage MCP server connections" />
        <div className="space-y-3">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      </div>
    )
  }

  // -------------------------------------------------------------------------
  // Render: main
  // -------------------------------------------------------------------------

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Integrations" description="Manage MCP server connections for your workspace">
        {canManage && (
          <TemplatePopover
            open={templatePopoverOpen}
            onOpenChange={setTemplatePopoverOpen}
            onSelect={handleAddServer}
            trigger={
              <Button>
                <Plus className="mr-2 h-4 w-4" />
                Add MCP Server
              </Button>
            }
          />
        )}
      </PageHeader>

      {servers.length === 0 ? (
        <EmptyState
          icon={Plug}
          title="No integrations yet"
          description="Connect MCP servers to give your agents access to external tools and services."
        >
          {canManage && (
            <TemplatePopover
              open={emptyPopoverOpen}
              onOpenChange={setEmptyPopoverOpen}
              onSelect={handleAddServer}
              trigger={
                <Button className="mt-4">
                  <Plus className="mr-2 h-4 w-4" />
                  Add First MCP Server
                </Button>
              }
            />
          )}
        </EmptyState>
      ) : (
        <div className="rounded-md border divide-y">
          {servers.map((server) => {
            const isExpanded = expandedId === server.id
            const agents = crewAgents[server.crew_id] ?? []

            return (
              <div key={server.id}>
                {/* Collapsed header row */}
                <button
                  type="button"
                  className="flex w-full items-center gap-4 px-4 py-3 text-left hover:bg-muted/40 transition-colors"
                  onClick={() => setExpandedId(isExpanded ? null : server.id)}
                  aria-expanded={isExpanded}
                  aria-label={`${server.display_name || server.name} integration`}
                >
                  <span className="shrink-0 text-muted-foreground">
                    {isExpanded ? (
                      <ChevronDown className="h-4 w-4" />
                    ) : (
                      <ChevronRight className="h-4 w-4" />
                    )}
                  </span>

                  <div className="flex items-center gap-2 min-w-0 flex-1">
                    <Plug className="h-4 w-4 shrink-0 text-muted-foreground" />
                    <div className="min-w-0">
                      <p className="font-medium text-sm truncate">
                        {server.display_name || server.name}
                      </p>
                      <p className="text-xs text-muted-foreground truncate">
                        {subtitleFor(server)}
                      </p>
                    </div>
                  </div>

                  {/* Crew badge */}
                  <Badge variant="outline" className="text-xs font-normal shrink-0">
                    <Users className="mr-1 h-3 w-3" />
                    {server.crew_name}
                  </Badge>

                  {/* Transport */}
                  <span className="hidden sm:flex items-center gap-1.5 text-xs text-muted-foreground shrink-0">
                    {server.transport === "streamable-http" ? (
                      <Globe className="h-3 w-3" />
                    ) : (
                      <Terminal className="h-3 w-3" />
                    )}
                    {server.transport === "streamable-http" ? "HTTP" : "Stdio"}
                  </span>

                  {/* Auth status indicator */}
                  {server.auth_status === "missing" && (
                    <Badge variant="destructive" className="text-[10px] px-1.5 py-0 shrink-0">No credential</Badge>
                  )}
                  {server.auth_status === "expired" && (
                    <Badge variant="outline" className="text-[10px] px-1.5 py-0 shrink-0 border-yellow-500 text-yellow-600">Expired</Badge>
                  )}
                  {server.auth_status === "connected" && (
                    <Badge variant="outline" className="text-[10px] px-1.5 py-0 shrink-0 border-green-500 text-green-600">Connected</Badge>
                  )}

                  {/* Agent count */}
                  {server.agent_binding_count > 0 && (
                    <span className="hidden sm:flex items-center gap-1 text-xs text-muted-foreground shrink-0">
                      <Bot className="h-3 w-3" />
                      {server.agent_binding_count}
                    </span>
                  )}
                </button>

                {/* Expanded panel */}
                {isExpanded && (
                  <ExpandedPanel
                    server={server}
                    crews={crews}
                    agents={agents}
                    agentBindings={agentBindings}
                    bindingIds={bindingIds}
                    confirmDeleteId={confirmDeleteId}
                    canManage={canManage}
                    workspaceId={workspaceId}
                    onPatch={(fields) => patchServer(server, fields)}
                    onCrewMove={(newCrewId) => handleCrewMove(server, newCrewId)}
                    onAgentToggle={(agent, hasAccess, hasAny) =>
                      handleAgentToggle(server, agent, hasAccess, hasAny)
                    }
                    onDelete={() => handleDelete(server)}
                    onConfirmDeleteChange={(v) => setConfirmDeleteId(v ? server.id : null)}
                    onRefresh={() => { if (workspaceId) fetchAll(workspaceId) }}
                  />
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Expanded panel component
// ---------------------------------------------------------------------------

function ExpandedPanel({
  server,
  crews,
  agents,
  agentBindings,
  bindingIds,
  confirmDeleteId,
  onRefresh,
  canManage,
  workspaceId,
  onPatch,
  onCrewMove,
  onAgentToggle,
  onDelete,
  onConfirmDeleteChange,
}: {
  server: CrewIntegration
  crews: CrewInfo[]
  agents: AgentInfo[]
  agentBindings: Record<string, Set<string>>
  bindingIds: Record<string, Record<string, string>>
  confirmDeleteId: string | null
  canManage: boolean
  workspaceId: string | null
  onPatch: (fields: Record<string, unknown>) => Promise<void>
  onCrewMove: (newCrewId: string) => Promise<void>
  onAgentToggle: (agent: AgentInfo, hasAccess: boolean, hasAny: boolean) => Promise<void>
  onDelete: () => Promise<void>
  onConfirmDeleteChange: (v: boolean) => void
  onRefresh: () => void
}) {
  const { credentials, loading: credLoading, fetchCredentials, addCredential } = useCredentials(
    canManage ? (workspaceId ?? undefined) : undefined,
  )

  // Local state for inputs (save on blur)
  const [name, setName] = React.useState(server.name)
  const [displayName, setDisplayName] = React.useState(server.display_name || "")
  const [command, setCommand] = React.useState(server.command ?? "")
  const [args, setArgs] = React.useState(parseArgs(server.args_json))
  const [url, setUrl] = React.useState(server.endpoint ?? "")
  const [transport, setTransport] = React.useState(server.transport)
  const [envVars, setEnvVars] = React.useState(parseEnv(server.env_json))

  // Sync local state if server data changes (after refetch)
  React.useEffect(() => {
    setName(server.name)
    setDisplayName(server.display_name || "")
    setCommand(server.command ?? "")
    setArgs(parseArgs(server.args_json))
    setUrl(server.endpoint ?? "")
    setTransport(server.transport)
    setEnvVars(parseEnv(server.env_json))
  }, [server])

  const hasAnyBindings = (agentBindings[server.id]?.size ?? 0) > 0

  function handleBlur(field: string, value: string) {
    switch (field) {
      case "name":
        if (value !== server.name) onPatch({ name: value })
        break
      case "display_name":
        if (value !== (server.display_name || "")) onPatch({ display_name: value })
        break
      case "command":
        if (value !== (server.command ?? "")) onPatch({ command: value })
        break
      case "args":
        if (value !== parseArgs(server.args_json)) onPatch({ args_json: serializeArgs(value) })
        break
      case "url":
        if (value !== (server.endpoint ?? "")) onPatch({ endpoint: value })
        break
    }
  }

  function handleTransportChange(newTransport: string) {
    setTransport(newTransport)
    if (newTransport !== server.transport) {
      onPatch({ transport: newTransport })
    }
  }

  function handleEnvBlur() {
    const newJson = serializeEnv(envVars)
    const oldJson = server.env_json ?? "{}"
    if (newJson !== oldJson) {
      onPatch({ env_json: newJson })
    }
  }

  function addEnvVar() {
    setEnvVars((prev) => [...prev, { key: "", value: "" }])
  }

  function removeEnvVar(idx: number) {
    const updated = envVars.filter((_, i) => i !== idx)
    setEnvVars(updated)
    // Save immediately on remove
    const newJson = serializeEnv(updated)
    const oldJson = server.env_json ?? "{}"
    if (newJson !== oldJson) {
      onPatch({ env_json: newJson })
    }
  }

  function updateEnvVar(idx: number, field: "key" | "value", val: string) {
    setEnvVars((prev) => prev.map((e, i) => (i === idx ? { ...e, [field]: val } : e)))
  }

  const isConfirming = confirmDeleteId === server.id

  return (
    <div className="bg-muted/20 border-t px-6 py-5 space-y-4">
      {/* Section 1: Scope & Assignment */}
      <div className="rounded-md border bg-background p-4 space-y-4">
        <div className="flex items-center gap-2 text-sm font-medium">
          <Users className="h-4 w-4 text-muted-foreground" />
          Scope & Assignment
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div className="space-y-1.5">
            <Label htmlFor={`crew-${server.id}`} className="text-xs">
              Assigned to crew
            </Label>
            <Select
              value={server.crew_id}
              onValueChange={(v) => onCrewMove(v)}
              disabled={!canManage}
            >
              <SelectTrigger id={`crew-${server.id}`} className="h-8 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {crews.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>

        {/* Agent assignment */}
        {agents.length > 0 && (
          <div className="space-y-2">
            <Label className="text-xs">Agent access</Label>
            <div className="flex flex-wrap gap-1.5">
              {agents.map((a) => {
                const bound = agentBindings[server.id]?.has(a.id) ?? false
                const hasAccess = hasAnyBindings ? bound : false
                return (
                  <button
                    key={a.id}
                    type="button"
                    className={`inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs transition-colors ${
                      hasAccess
                        ? "bg-primary/10 border-primary/30 text-primary"
                        : "bg-muted/30 border-border text-muted-foreground"
                    }`}
                    onClick={() => onAgentToggle(a, hasAccess, hasAnyBindings)}
                    disabled={!canManage}
                    title={hasAccess ? `Remove ${a.name} access` : `Grant ${a.name} access`}
                  >
                    {hasAccess && <Check className="h-3 w-3" />}
                    <Bot className="h-3 w-3" />
                    {a.name}
                  </button>
                )
              })}
            </div>
            <p className="text-xs text-muted-foreground">
              {hasAnyBindings
                ? "Only selected agents have access. Click to toggle."
                : "No agents have access yet. Click an agent to grant access."}
            </p>
          </div>
        )}
      </div>

      {/* Section 2: Server Configuration */}
      <div className="rounded-md border bg-background p-4 space-y-4">
        <div className="flex items-center gap-2 text-sm font-medium">
          <Settings2 className="h-4 w-4 text-muted-foreground" />
          Server Configuration
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div className="space-y-1.5">
            <Label htmlFor={`name-${server.id}`} className="text-xs">
              Server name
            </Label>
            <Input
              id={`name-${server.id}`}
              className="h-8 text-xs"
              value={name}
              onChange={(e) => setName(e.target.value)}
              onBlur={() => handleBlur("name", name)}
              readOnly={!canManage}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor={`display-${server.id}`} className="text-xs">
              Display name
            </Label>
            <Input
              id={`display-${server.id}`}
              className="h-8 text-xs"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              onBlur={() => handleBlur("display_name", displayName)}
              readOnly={!canManage}
            />
          </div>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor={`transport-${server.id}`} className="text-xs">
            Transport
          </Label>
          <Select
            value={transport}
            onValueChange={handleTransportChange}
            disabled={!canManage}
          >
            <SelectTrigger id={`transport-${server.id}`} className="h-8 text-xs w-40">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="stdio">
                <span className="flex items-center gap-1.5">
                  <Terminal className="h-3 w-3" /> Stdio
                </span>
              </SelectItem>
              <SelectItem value="streamable-http">
                <span className="flex items-center gap-1.5">
                  <Globe className="h-3 w-3" /> HTTP
                </span>
              </SelectItem>
            </SelectContent>
          </Select>
        </div>

        {transport === "stdio" ? (
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div className="space-y-1.5">
              <Label htmlFor={`cmd-${server.id}`} className="text-xs">
                Command
              </Label>
              <Input
                id={`cmd-${server.id}`}
                className="h-8 text-xs font-mono"
                placeholder="npx"
                value={command}
                onChange={(e) => setCommand(e.target.value)}
                onBlur={() => handleBlur("command", command)}
                readOnly={!canManage}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor={`args-${server.id}`} className="text-xs">
                Arguments
              </Label>
              <Input
                id={`args-${server.id}`}
                className="h-8 text-xs font-mono"
                placeholder="-y @modelcontextprotocol/server-github"
                value={args}
                onChange={(e) => setArgs(e.target.value)}
                onBlur={() => handleBlur("args", args)}
                readOnly={!canManage}
              />
            </div>
          </div>
        ) : (
          <div className="space-y-1.5">
            <Label htmlFor={`url-${server.id}`} className="text-xs">
              URL
            </Label>
            <Input
              id={`url-${server.id}`}
              className="h-8 text-xs font-mono"
              placeholder="https://example.com/mcp"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              onBlur={() => handleBlur("url", url)}
              readOnly={!canManage}
            />
          </div>
        )}
      </div>

      {/* Section 3: OAuth Auto-Connect (HTTP servers only) */}
      {canManage && server.transport === "streamable-http" && server.endpoint && (
        <OAuthAutoConnect
          serverName={server.name}
          mcpURL={server.endpoint}
          workspaceId={workspaceId}
          authStatus={server.auth_status}
          onCredentialCreated={async (credId: string) => {
            if (!workspaceId) return
            // Update existing bindings with credential
            const bindingsForServer = agentBindings[server.id]
            if (bindingsForServer && bindingsForServer.size > 0) {
              for (const agentId of Array.from(bindingsForServer)) {
                const bId = bindingIds[server.id]?.[agentId]
                if (bId) {
                  await fetch(`/api/v1/agents/${agentId}/integrations/${bId}?workspace_id=${workspaceId}`, {
                    method: "PATCH",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ credential_id: credId, cred_type: "bearer" }),
                  })
                }
              }
            } else {
              // No bindings yet — auto-grant access to ALL agents in the crew with credential
              for (const agent of agents) {
                await fetch(`/api/v1/agents/${agent.id}/integrations?workspace_id=${workspaceId}`, {
                  method: "POST",
                  headers: { "Content-Type": "application/json" },
                  body: JSON.stringify({
                    mcp_server_id: server.id,
                    mcp_server_scope: "crew",
                    credential_id: credId,
                    cred_type: "bearer",
                    enabled: true,
                  }),
                })
              }
            }
            onRefresh()
            toast.success("OAuth connected! All agents have access.")
          }}
        />
      )}

      {/* Section 4: Environment Variables (hidden for HTTP servers that use OAuth) */}
      {!(server.transport === "streamable-http" && server.endpoint) && <div className="rounded-md border bg-background p-4 space-y-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2 text-sm font-medium">
            <KeyRound className="h-4 w-4 text-muted-foreground" />
            Environment Variables
          </div>
          {canManage && (
            <Button
              variant="outline"
              size="sm"
              className="h-7 text-xs"
              onClick={addEnvVar}
            >
              <Plus className="mr-1 h-3 w-3" />
              Add Variable
            </Button>
          )}
        </div>

        {envVars.length === 0 ? (
          <p className="text-xs text-muted-foreground">No environment variables configured.</p>
        ) : (
          <div className="space-y-2">
            {envVars.map((env, idx) => (
              <div key={idx} className="flex items-center gap-2">
                <Input
                  className="h-8 text-xs font-mono flex-1"
                  placeholder="KEY"
                  value={env.key}
                  onChange={(e) => updateEnvVar(idx, "key", e.target.value)}
                  onBlur={handleEnvBlur}
                  readOnly={!canManage}
                  aria-label={`Environment variable key ${idx + 1}`}
                />
                <span className="text-xs text-muted-foreground">=</span>
                {canManage && workspaceId ? (
                  <div className="flex-1">
                    <CredentialPicker
                      envKey={env.key}
                      envValue={env.value}
                      credentials={credentials}
                      credLoading={credLoading}
                      workspaceId={workspaceId}
                      onFetchCredentials={fetchCredentials}
                      onAddCredential={addCredential}
                      onChangeValue={(val) => {
                        updateEnvVar(idx, "value", val)
                        // Save immediately after credential selection
                        const updated = envVars.map((e, i) => (i === idx ? { ...e, value: val } : e))
                        onPatch({ env_json: serializeEnv(updated) })
                      }}
                    />
                  </div>
                ) : (
                  <Input
                    className="h-8 text-xs font-mono flex-1"
                    placeholder="value"
                    value={env.value ? "••••••••" : ""}
                    readOnly
                    tabIndex={-1}
                    aria-label={`Environment variable value ${idx + 1} (redacted)`}
                  />
                )}
                {canManage && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
                    onClick={() => removeEnvVar(idx)}
                    aria-label={`Remove environment variable ${env.key || idx + 1}`}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                )}
              </div>
            ))}
          </div>
        )}
      </div>}

      {/* Section 5: Actions */}
      {canManage && (
        <div className="flex justify-end">
          {isConfirming ? (
            <div className="flex items-center gap-2">
              <span className="text-sm text-muted-foreground">Delete this integration?</span>
              <Button
                variant="destructive"
                size="sm"
                onClick={onDelete}
              >
                Confirm Delete
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => onConfirmDeleteChange(false)}
              >
                Cancel
              </Button>
            </div>
          ) : (
            <Button
              variant="outline"
              size="sm"
              className="text-destructive hover:text-destructive hover:bg-destructive/10"
              onClick={() => onConfirmDeleteChange(true)}
            >
              <Trash2 className="mr-1.5 h-3.5 w-3.5" />
              Delete Integration
            </Button>
          )}
        </div>
      )}
    </div>
  )
}
