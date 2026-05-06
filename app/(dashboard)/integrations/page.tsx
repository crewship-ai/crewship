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
  Wrench,
} from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { SettingsCard } from "@/components/features/settings/shared"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusBadge } from "@/components/ui/status-badge"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import type { MCPTemplate } from "@/components/features/mcp/types"
import { RegistryBrowser } from "@/components/features/mcp/components/registry-browser"
import type { RegistryAddPayload } from "@/components/features/mcp/components/registry-browser"
import { cn } from "@/lib/utils"

import { ExpandedPanel } from "@/components/features/integrations/expanded-panel"
import { TemplatePopover } from "@/components/features/integrations/template-popover"
import { MCPDetailSheet } from "@/components/features/integrations/mcp-detail-sheet"
import { AddMCPWizard } from "@/components/features/integrations/add-mcp-wizard"
import { MCPLogo } from "@/components/icons/mcp-logos"
import { RecipesEmptyState } from "@/components/features/dashboard/recipes-cards"
import { serializeArgs, subtitleFor } from "@/components/features/integrations/helpers"
import type {
  AgentBinding,
  AgentInfo,
  CrewInfo,
  CrewIntegration,
} from "@/components/features/integrations/types"

// The sub-components (OAuthAutoConnect, TemplatePopover,
// TestConnectionButton, ExpandedPanel) and the pure helpers
// (parseArgs, serializeArgs, parseEnv, serializeEnv, subtitleFor)
// were moved to components/features/integrations/ so this page stays
// focused on the list/add/delete orchestration for the default
// export.

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
  const [detailServer, setDetailServer] = React.useState<CrewIntegration | null>(null)
  const [detailOpen, setDetailOpen] = React.useState(false)
  const [wizardOpen, setWizardOpen] = React.useState(false)
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
    <Button size="sm" className="h-7 px-2.5 text-xs" onClick={() => setWizardOpen(true)}>
      <Plus className="mr-1.5 h-3 w-3" />
      Add Connector
    </Button>
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
            <h1 className="text-body font-medium text-foreground/80">Connectors</h1>
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
          <h1 className="text-body font-medium text-foreground/80">Connectors</h1>
          <span className="text-[10px] font-mono text-muted-foreground/60">
            {servers.length === 0
              ? "no connectors"
              : `${servers.length} connector${servers.length === 1 ? "" : "s"}`}
          </span>
        </div>
        {headerActions}
      </div>

      {/* Sprint 0 — tab strip removed. Marketplace lives in the
          sidebar; /integrations now renders a single connected list.
          Sprint 1 will replace AddMCPWizard with a catalog-first flow.
          See CONNECTIONS.md §5.1. */}

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
        <div className="space-y-4">
          {workspaceId && (
            <RecipesEmptyState
              workspaceId={workspaceId}
              onInstalled={() => { if (workspaceId) fetchAll(workspaceId) }}
            />
          )}
          <SettingsCard
            title="Set up manually"
            description="Add a custom MCP server"
          >
            <div className="flex flex-col items-center justify-center py-8 text-center">
              <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center mb-3">
                <Plug className="h-4 w-4 text-muted-foreground/60" />
              </div>
              <div className="text-[11px] text-muted-foreground mt-0.5 max-w-sm">
                Connectors expose tools (GitHub, Slack, databases, browsers) that your agents can call during tasks.
              </div>
              {canManage && (
                <div className="mt-4 flex gap-2">
                  <Button size="sm" className="h-7 px-2.5 text-xs" onClick={() => setWizardOpen(true)}>
                    <Plus className="mr-1.5 h-3 w-3" />
                    Add Connector
                  </Button>
                </div>
              )}
            </div>
          </SettingsCard>
        </div>
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

                  {/* Brand logo (CONNECTIONS.md §5.2) — replaces generic
                      transport icon. Falls back to Globe/Terminal when
                      no brand match. */}
                  <MCPLogo
                    name={server.icon || server.name}
                    transport={server.transport}
                    className="h-4 w-4 shrink-0 opacity-85"
                  />

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

                  {/* Tools chip — opens MCPDetailSheet on Tools tab.
                      Stop propagation so we don't toggle the inline
                      expand at the same time. */}
                  <button
                    type="button"
                    onClick={(e) => { e.stopPropagation(); setDetailServer(server); setDetailOpen(true) }}
                    className="hidden md:inline-flex items-center gap-1 text-[10px] text-muted-foreground hover:text-foreground shrink-0 font-mono tabular-nums px-1.5 h-5 rounded border border-white/10 hover:border-blue-400/40 hover:bg-blue-500/[0.05] transition-colors"
                    title="Manage tools"
                  >
                    <Wrench className="h-2.5 w-2.5" />
                    Tools
                  </button>

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

      {workspaceId && detailServer && (
        <MCPDetailSheet
          workspaceId={workspaceId}
          server={detailServer}
          open={detailOpen}
          onOpenChange={(o) => { setDetailOpen(o); if (!o) setDetailServer(null) }}
          onRefresh={() => { if (workspaceId) fetchAll(workspaceId) }}
        />
      )}

      {workspaceId && (
        <AddMCPWizard
          workspaceId={workspaceId}
          open={wizardOpen}
          onOpenChange={setWizardOpen}
          onAdded={() => { if (workspaceId) fetchAll(workspaceId) }}
        />
      )}
    </div>
  )
}
