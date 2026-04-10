"use client"

import * as React from "react"
import {
  Plug,
  Plus,
  Globe,
  Terminal,
  Users,
  ChevronRight,
  Bot,
  Check,
  Trash2,
  Settings2,
  KeyRound,
  ExternalLink,
  Loader2,
  Search,
  Zap,
  AlertTriangle,
  Info,
  XCircle,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { SectionCard } from "@/components/ui/section-card"
import { StatusBadge } from "@/components/ui/status-badge"
import { Card } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { SettingsCard } from "@/components/features/settings/shared"
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
import { RegistryBrowser } from "@/components/features/mcp/components/registry-browser"
import type { RegistryAddPayload } from "@/components/features/mcp/components/registry-browser"
import { useCredentials } from "@/components/features/mcp/hooks/use-credentials"
import { cn } from "@/lib/utils"

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
      <Card className="p-4 bg-surface-subtle">
        <div className="flex items-center gap-2 text-body font-medium">
          <Check className="h-4 w-4" />
          <StatusBadge status="COMPLETED" label="OAuth connected" />
        </div>
      </Card>
    )
  }

  const isMissing = authStatus === "missing"
  const isExpired = authStatus === "expired"

  return (
    <Card
      className={cn(
        "p-4 space-y-3",
        isMissing && "border-destructive/50 bg-destructive/5",
        isExpired && "border-amber-500/50 bg-amber-500/5",
      )}
    >
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-body font-medium">
          <ExternalLink className="h-4 w-4 text-muted-foreground" />
          Authentication
        </div>
        {isMissing && (
          <StatusBadge status="FAILED" label="Credential missing" />
        )}
        {isExpired && (
          <StatusBadge status="BLOCKED" label="Expired" />
        )}
      </div>
      <p className="text-label text-muted-foreground">
        {isMissing
          ? "The credential for this integration was deleted. Reconnect to restore access."
          : isExpired
            ? "The OAuth token has expired. Reconnect to refresh."
            : "Connect with OAuth to automatically authenticate with this service."}
      </p>
      {error && (
        <p className="text-label text-destructive">{error}</p>
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
    </Card>
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
  onBrowseRegistry,
  trigger,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onSelect: (t: MCPTemplate | null) => void
  onBrowseRegistry: () => void
  trigger: React.ReactNode
}) {
  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger asChild>{trigger}</PopoverTrigger>
      <PopoverContent className="w-80 p-3" align="end">
        <div className="space-y-2">
          <p className="text-body font-medium">Add from template</p>
          <div className="grid grid-cols-2 gap-2">
            {MCP_TEMPLATES.map((t) => {
              const Icon = TEMPLATE_ICONS[t.icon] ?? Plug
              return (
                <button
                  key={t.name}
                  type="button"
                  className="flex items-center gap-2 rounded-md border border-border px-3 py-2 text-left text-body hover:bg-muted/60 transition-colors"
                  onClick={() => onSelect(t)}
                >
                  <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
                  {t.label}
                </button>
              )
            })}
          </div>
          <div className="flex gap-2">
            <button
              type="button"
              className="flex flex-1 items-center gap-2 rounded-md border border-dashed border-border px-3 py-2 text-body text-muted-foreground hover:bg-muted/60 transition-colors"
              onClick={() => onSelect(null)}
            >
              <Terminal className="h-4 w-4" />
              Custom server
            </button>
            <button
              type="button"
              className="flex flex-1 items-center gap-2 rounded-md border border-dashed border-border px-3 py-2 text-body text-muted-foreground hover:bg-muted/60 transition-colors"
              onClick={onBrowseRegistry}
            >
              <Search className="h-4 w-4" />
              Browse Registry
            </button>
          </div>
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
  const [registryOpen, setRegistryOpen] = React.useState(false)

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
        throw new Error(d?.error ?? "Failed to create server")
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
    } catch (err) {
      if (!(err instanceof Error && err.message.includes("Failed to create"))) {
        toast.error("Network error")
      }
      throw err
    }
  }

  // -------------------------------------------------------------------------
  // Add from registry
  // -------------------------------------------------------------------------

  async function handleRegistryAdd(payload: RegistryAddPayload) {
    const template: MCPTemplate = {
      name: payload.name,
      label: payload.display_name,
      icon: "",
      transport: payload.transport,
      command: payload.command ?? undefined,
      args: payload.args ?? undefined,
      url: payload.url ?? undefined,
      envHint: payload.envHint ?? undefined,
    }
    await handleAddServer(template)
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

  const headerActions = canManage ? (
    <TemplatePopover
      open={templatePopoverOpen}
      onOpenChange={setTemplatePopoverOpen}
      onSelect={handleAddServer}
      onBrowseRegistry={() => {
        setTemplatePopoverOpen(false)
        setRegistryOpen(true)
      }}
      trigger={
        <Button size="sm" className="h-7 px-2.5 text-xs">
          <Plus className="mr-1.5 h-3 w-3" />
          Add MCP Server
        </Button>
      }
    />
  ) : null

  // Computed KPIs from the already-loaded servers list — no extra API calls.
  const connectedCount = servers.filter((s) => s.auth_status === "connected").length
  const needsAttentionCount = servers.filter(
    (s) => s.auth_status === "missing" || s.auth_status === "expired",
  ).length
  const totalBindings = servers.reduce((a, s) => a + (s.agent_binding_count ?? 0), 0)
  const httpCount = servers.filter((s) => s.transport === "streamable-http").length
  const stdioCount = servers.filter((s) => s.transport !== "streamable-http").length

  // Helper: relative time ("2h ago", "3d ago")
  const formatRel = (iso: string): string => {
    const diff = Date.now() - new Date(iso).getTime()
    const min = Math.floor(diff / 60000)
    if (min < 1) return "just now"
    if (min < 60) return `${min}m ago`
    const h = Math.floor(min / 60)
    if (h < 24) return `${h}h ago`
    return `${Math.floor(h / 24)}d ago`
  }

  if (wsLoading || loading) {
    return (
      <div className="p-4 md:p-6 space-y-4 bg-background min-h-[calc(100vh-48px)]">
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-2">
            <Plug className="h-3.5 w-3.5 text-foreground/50" />
            <h1 className="text-body font-medium text-foreground/80">Integrations</h1>
          </div>
          {headerActions}
        </div>
        <div className="grid gap-4 grid-cols-2 sm:grid-cols-4">
          {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-[112px] rounded-xl" />)}
        </div>
        <Skeleton className="h-[200px] rounded-xl" />
      </div>
    )
  }

  // -------------------------------------------------------------------------
  // Render: main
  // -------------------------------------------------------------------------

  return (
    <div className="p-4 md:p-6 pb-10 space-y-4 bg-background min-h-[calc(100vh-48px)]">
      {/* ── Header ─────────────────────────────────────────────── */}
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <div className="flex items-center gap-2">
          <Plug className="h-3.5 w-3.5 text-foreground/50" />
          <h1 className="text-body font-medium text-foreground/80">Integrations</h1>
          <span className="text-[10px] font-mono text-muted-foreground/60">
            {servers.length === 0
              ? "no MCP servers"
              : `${servers.length} MCP server${servers.length === 1 ? "" : "s"}`}
          </span>
        </div>
        {headerActions}
      </div>

      {/* ── KPI strip ──────────────────────────────────────────── */}
      <div className="grid gap-4 grid-cols-2 sm:grid-cols-4">
        <KpiCard
          label="Total"
          value={servers.length}
          subtitle={
            servers.length === 0
              ? "no integrations"
              : `${httpCount} HTTP · ${stdioCount} stdio`
          }
        />
        <KpiCard
          label="Connected"
          value={connectedCount}
          valueColor={connectedCount > 0 ? "rgb(52, 211, 153)" : undefined}
          subtitle={
            servers.length === 0
              ? "—"
              : `of ${servers.length} server${servers.length === 1 ? "" : "s"}`
          }
        />
        <KpiCard
          label="Needs attention"
          value={needsAttentionCount}
          valueColor={needsAttentionCount > 0 ? "rgb(248, 113, 113)" : undefined}
          subtitle={needsAttentionCount > 0 ? "missing or expired" : "all healthy"}
        />
        <KpiCard
          label="Agent bindings"
          value={totalBindings}
          subtitle={totalBindings === 0 ? "no agents bound" : `across ${servers.length} server${servers.length === 1 ? "" : "s"}`}
        />
      </div>

      {/* ── Servers list ───────────────────────────────────────── */}
      {servers.length === 0 ? (
        <SettingsCard
          title="MCP servers"
          description="Connect MCP servers to give your agents access to external tools and services"
        >
          <div className="flex flex-col items-center justify-center py-12 text-center">
            <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center mb-3">
              <Plug className="h-4 w-4 text-muted-foreground/60" />
            </div>
            <div className="text-sm font-medium text-foreground/80">No integrations yet</div>
            <div className="text-[11px] text-muted-foreground mt-0.5 max-w-sm">
              MCP servers expose tools (GitHub, Slack, databases, browsers) that your agents can call during tasks.
            </div>
            {canManage && (
              <div className="mt-4">
                <TemplatePopover
                  open={emptyPopoverOpen}
                  onOpenChange={setEmptyPopoverOpen}
                  onSelect={handleAddServer}
                  onBrowseRegistry={() => {
                    setEmptyPopoverOpen(false)
                    setRegistryOpen(true)
                  }}
                  trigger={
                    <Button size="sm" className="h-7 px-2.5 text-xs">
                      <Plus className="mr-1.5 h-3 w-3" />
                      Add first MCP server
                    </Button>
                  }
                />
              </div>
            )}
          </div>
        </SettingsCard>
      ) : (
        <SettingsCard
          title="Connected MCP servers"
          description={
            needsAttentionCount > 0
              ? `${connectedCount} connected · ${needsAttentionCount} need attention · ${totalBindings} agent binding${totalBindings === 1 ? "" : "s"}`
              : `${connectedCount} of ${servers.length} connected · ${totalBindings} agent binding${totalBindings === 1 ? "" : "s"}`
          }
        >
          {servers.map((server, idx) => {
            const isExpanded = expandedId === server.id
            const agents = crewAgents[server.crew_id] ?? []
            const isLast = idx === servers.length - 1
            return (
              <div key={server.id} className={!isLast && !isExpanded ? "border-b border-border/40" : ""}>
                {/* Collapsed header row */}
                <button
                  type="button"
                  className={cn(
                    "flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors",
                    isExpanded ? "bg-white/[0.03]" : "hover:bg-white/[0.02]",
                  )}
                  onClick={() => setExpandedId(isExpanded ? null : server.id)}
                  aria-expanded={isExpanded}
                  aria-label={`${server.display_name || server.name} integration`}
                >
                  <ChevronRight
                    className={cn(
                      "h-3 w-3 text-muted-foreground/60 shrink-0 transition-transform",
                      isExpanded && "rotate-90 text-foreground",
                    )}
                  />

                  {/* Transport icon */}
                  {server.transport === "streamable-http" ? (
                    <Globe className="h-3 w-3 text-muted-foreground/60 shrink-0" />
                  ) : (
                    <Terminal className="h-3 w-3 text-muted-foreground/60 shrink-0" />
                  )}

                  {/* Name + subtitle */}
                  <div className="min-w-0 flex-1">
                    <div className="text-xs font-medium text-foreground/90 truncate">
                      {server.display_name || server.name}
                    </div>
                    <div className="text-[10px] text-muted-foreground/70 truncate font-mono">
                      {subtitleFor(server)}
                    </div>
                  </div>

                  {/* Crew pill */}
                  <Badge variant="outline" className="text-[10px] font-medium h-5 px-1.5 shrink-0 gap-1">
                    <Users className="h-2.5 w-2.5" />
                    {server.crew_name}
                  </Badge>

                  {/* Agent binding count */}
                  {server.agent_binding_count > 0 && (
                    <span className="hidden md:inline-flex items-center gap-1 text-[10px] text-muted-foreground shrink-0 font-mono tabular-nums">
                      <Bot className="h-2.5 w-2.5" />
                      {server.agent_binding_count}
                    </span>
                  )}

                  {/* Updated relative time */}
                  <span className="hidden lg:inline text-[10px] text-muted-foreground/60 font-mono shrink-0 w-[54px] text-right">
                    {formatRel(server.updated_at)}
                  </span>

                  {/* Auth status badge */}
                  {server.auth_status === "missing" && (
                    <StatusBadge status="FAILED" label="No credential" className="shrink-0 text-[10px]" />
                  )}
                  {server.auth_status === "expired" && (
                    <StatusBadge status="BLOCKED" label="Expired" className="shrink-0 text-[10px]" />
                  )}
                  {server.auth_status === "connected" && (
                    <StatusBadge status="COMPLETED" label="Connected" className="shrink-0 text-[10px]" />
                  )}
                  {server.auth_status === "none" && (
                    <span className="text-[10px] text-muted-foreground/50 shrink-0 uppercase tracking-wide">
                      No auth
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
        </SettingsCard>
      )}

      {/* Registry browser dialog */}
      <RegistryBrowser
        open={registryOpen}
        onOpenChange={setRegistryOpen}
        onAdd={handleRegistryAdd}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// Test Connection button
// ---------------------------------------------------------------------------

interface TestResult {
  status: "ok" | "auth_required" | "error" | "skipped"
  message?: string
}

function TestConnectionButton({
  serverId,
  crewId,
  workspaceId,
}: {
  serverId: string
  crewId: string
  workspaceId: string | null
}) {
  const [testing, setTesting] = React.useState(false)
  const [result, setResult] = React.useState<TestResult | null>(null)
  const timerRef = React.useRef<ReturnType<typeof setTimeout> | null>(null)

  React.useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current)
    }
  }, [])

  async function handleTest() {
    if (!workspaceId) return
    setTesting(true)
    setResult(null)
    if (timerRef.current) clearTimeout(timerRef.current)

    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/integrations/${serverId}/test?workspace_id=${workspaceId}`,
        { method: "POST" },
      )
      if (!res.ok) {
        const errData = await res.json().catch(() => null)
        setResult({ status: "error", message: errData?.error || `HTTP ${res.status}` })
      } else {
        const data: TestResult = await res.json()
        setResult(data)
      }

      timerRef.current = setTimeout(() => {
        setResult(null)
        timerRef.current = null
      }, 10000)
    } catch {
      setResult({ status: "error", message: "Network error" })
      timerRef.current = setTimeout(() => {
        setResult(null)
        timerRef.current = null
      }, 10000)
    } finally {
      setTesting(false)
    }
  }

  return (
    <div className="flex items-center gap-3">
      <Button
        variant="outline"
        size="sm"
        className="h-8 text-label"
        onClick={handleTest}
        disabled={testing}
      >
        {testing ? (
          <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
        ) : (
          <Zap className="mr-1.5 h-3.5 w-3.5" />
        )}
        Test Connection
      </Button>
      {result && (
        <span className="inline-flex items-center gap-1.5">
          {result.status === "ok" && (
            <StatusBadge status="COMPLETED" label={<span className="inline-flex items-center gap-1"><Check className="h-3 w-3" />Connected</span>} />
          )}
          {result.status === "auth_required" && (
            <StatusBadge status="BLOCKED" label={<span className="inline-flex items-center gap-1"><AlertTriangle className="h-3 w-3" />Authentication required</span>} />
          )}
          {result.status === "error" && (
            <StatusBadge status="FAILED" label={<span className="inline-flex items-center gap-1"><XCircle className="h-3 w-3" />{result.message || "Connection failed"}</span>} />
          )}
          {result.status === "skipped" && (
            <span className="inline-flex items-center gap-1.5 text-label text-muted-foreground">
              <Info className="h-3.5 w-3.5" />
              Tested at runtime
            </span>
          )}
        </span>
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

  // OAuth discovery state
  const [oauthDiscovered, setOauthDiscovered] = React.useState(false)
  const [discovering, setDiscovering] = React.useState(false)

  async function discoverOAuth(mcpUrl: string) {
    if (!workspaceId || transport !== "streamable-http") return
    try {
      const parsed = new URL(mcpUrl)
      if (parsed.protocol !== "https:" && parsed.protocol !== "http:") return
    } catch {
      setOauthDiscovered(false)
      return
    }
    setDiscovering(true)
    try {
      const res = await fetch(
        `/api/v1/oauth/discover?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ mcp_url: mcpUrl }),
        },
      )
      if (res.ok) {
        setOauthDiscovered(true)
      } else {
        setOauthDiscovered(false)
      }
    } catch {
      setOauthDiscovered(false)
    } finally {
      setDiscovering(false)
    }
  }

  // Auto-discover on mount if URL is already set
  React.useEffect(() => {
    if (server.transport === "streamable-http" && server.endpoint && server.auth_status === "none") {
      discoverOAuth(server.endpoint)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [server.id])

  const isConfirming = confirmDeleteId === server.id

  return (
    <div className="bg-surface-subtle border-t border-border px-6 py-5 space-y-4">
      {/* Section 1: Scope & Assignment */}
      <SectionCard surface="subtle" className="p-4 space-y-4">
        <div className="flex items-center gap-2 text-body font-medium">
          <Users className="h-4 w-4 text-muted-foreground" />
          Scope & Assignment
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div className="space-y-1.5">
            <Label htmlFor={`crew-${server.id}`} className="text-label">
              Assigned to crew
            </Label>
            <Select
              value={server.crew_id}
              onValueChange={(v) => onCrewMove(v)}
              disabled={!canManage}
            >
              <SelectTrigger id={`crew-${server.id}`} className="h-8 text-label">
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
            <Label className="text-label">Agent access</Label>
            <div className="flex flex-wrap gap-1.5">
              {agents.map((a) => {
                const bound = agentBindings[server.id]?.has(a.id) ?? false
                const hasAccess = hasAnyBindings ? bound : false
                return (
                  <button
                    key={a.id}
                    type="button"
                    aria-pressed={hasAccess}
                    className={cn(
                      "inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-label transition-colors",
                      hasAccess
                        ? "bg-primary/10 border-primary/30 text-primary"
                        : "bg-muted/30 border-border text-muted-foreground"
                    )}
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
            <p className="text-label text-muted-foreground">
              {hasAnyBindings
                ? "Only selected agents have access. Click to toggle."
                : "No agents have access yet. Click an agent to grant access."}
            </p>
          </div>
        )}
      </SectionCard>

      {/* Section 2: Server Configuration */}
      <SectionCard surface="subtle" className="p-4 space-y-4">
        <div className="flex items-center gap-2 text-body font-medium">
          <Settings2 className="h-4 w-4 text-muted-foreground" />
          Server Configuration
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div className="space-y-1.5">
            <Label htmlFor={`name-${server.id}`} className="text-label">
              Server name
            </Label>
            <Input
              id={`name-${server.id}`}
              className="h-8 text-label"
              value={name}
              onChange={(e) => setName(e.target.value)}
              onBlur={() => handleBlur("name", name)}
              readOnly={!canManage}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor={`display-${server.id}`} className="text-label">
              Display name
            </Label>
            <Input
              id={`display-${server.id}`}
              className="h-8 text-label"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              onBlur={() => handleBlur("display_name", displayName)}
              readOnly={!canManage}
            />
          </div>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor={`transport-${server.id}`} className="text-label">
            Transport
          </Label>
          <Select
            value={transport}
            onValueChange={handleTransportChange}
            disabled={!canManage}
          >
            <SelectTrigger id={`transport-${server.id}`} className="h-8 text-label w-40">
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
              <Label htmlFor={`cmd-${server.id}`} className="text-label">
                Command
              </Label>
              <Input
                id={`cmd-${server.id}`}
                className="h-8 text-label font-mono"
                placeholder="npx"
                value={command}
                onChange={(e) => setCommand(e.target.value)}
                onBlur={() => handleBlur("command", command)}
                readOnly={!canManage}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor={`args-${server.id}`} className="text-label">
                Arguments
              </Label>
              <Input
                id={`args-${server.id}`}
                className="h-8 text-label font-mono"
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
            <div className="flex items-center gap-2">
              <Label htmlFor={`url-${server.id}`} className="text-label">
                URL
              </Label>
              {discovering && (
                <span className="inline-flex items-center gap-1 text-micro text-muted-foreground">
                  <Loader2 className="h-3 w-3 animate-spin" />
                  Checking...
                </span>
              )}
              {!discovering && oauthDiscovered && (
                <StatusBadge status="IN_PROGRESS" label={
                  <span className="inline-flex items-center gap-1">
                    <ExternalLink className="h-2.5 w-2.5" />
                    OAuth detected
                  </span>
                } />
              )}
            </div>
            <Input
              id={`url-${server.id}`}
              className="h-8 text-label font-mono"
              placeholder="https://example.com/mcp"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              onBlur={() => {
                handleBlur("url", url)
                if (url && url !== (server.endpoint ?? "")) {
                  discoverOAuth(url)
                }
              }}
              readOnly={!canManage}
            />
          </div>
        )}
      </SectionCard>

      {/* Section 3: OAuth Auto-Connect (HTTP servers only) */}
      {canManage && transport === "streamable-http" && (url || server.endpoint) && (server.auth_status !== "none" || oauthDiscovered) && (
        <OAuthAutoConnect
          serverName={server.name}
          mcpURL={url || server.endpoint || ""}
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

      {/* Section 4: Environment Variables
          Hidden only for HTTP servers that actually use OAuth. Non-OAuth
          streamable-http servers still need API keys or other env-based auth. */}
      {!(transport === "streamable-http" && (server.auth_status !== "none" || oauthDiscovered)) && <SectionCard surface="subtle" className="p-4 space-y-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2 text-body font-medium">
            <KeyRound className="h-4 w-4 text-muted-foreground" />
            Environment Variables
          </div>
          {canManage && (
            <Button
              variant="outline"
              size="sm"
              className="h-7 text-label"
              onClick={addEnvVar}
            >
              <Plus className="mr-1 h-3 w-3" />
              Add Variable
            </Button>
          )}
        </div>

        {envVars.length === 0 ? (
          <p className="text-label text-muted-foreground">No environment variables configured.</p>
        ) : (
          <div className="space-y-2">
            {envVars.map((env, idx) => (
              <div key={idx} className="flex items-center gap-2">
                <Input
                  className="h-8 text-label font-mono flex-1"
                  placeholder="KEY"
                  value={env.key}
                  onChange={(e) => updateEnvVar(idx, "key", e.target.value)}
                  onBlur={handleEnvBlur}
                  readOnly={!canManage}
                  aria-label={`Environment variable key ${idx + 1}`}
                />
                <span className="text-label text-muted-foreground">=</span>
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
                    className="h-8 text-label font-mono flex-1"
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
      </SectionCard>}

      {/* Section 5: Test Connection */}
      {canManage && <TestConnectionButton
        serverId={server.id}
        crewId={server.crew_id}
        workspaceId={workspaceId}
      />}

      {/* Section 6: Actions */}
      {canManage && (
        <div className="flex justify-end">
          {isConfirming ? (
            <div className="flex items-center gap-2">
              <span className="text-body text-muted-foreground">Delete this integration?</span>
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
