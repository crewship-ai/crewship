"use client"

import * as React from "react"
import { Plug, Plus, Globe, Terminal, Users, ChevronRight, ChevronDown, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { AddIntegrationDialog } from "@/components/features/integrations/add-integration-dialog"
import { EditIntegrationDialog } from "@/components/features/integrations/edit-integration-dialog"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { toast } from "sonner"
import type { WorkspaceMCPServer } from "@/lib/types/integration"

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
}

type UnifiedRow = {
  id: string
  scope: "workspace" | "crew"
  name: string
  display_name: string
  transport: string
  command: string | null
  args_json: string | null
  env_json: string | null
  enabled: boolean
  created_at: string
  crew_id?: string
  crew_name?: string
  /** Original server object for workspace-level edit dialog */
  wsServer?: WorkspaceMCPServer
}

interface EditState {
  command: string
  args: string
  env: Record<string, string>
  enabled: boolean
}

const TRANSPORT_CONFIG = {
  "streamable-http": { icon: Globe, label: "HTTP" },
  stdio: { icon: Terminal, label: "Stdio" },
} as const

function parseArgsJson(argsJson: string | null | undefined): string {
  if (!argsJson) return ""
  try {
    const arr = JSON.parse(argsJson) as string[]
    return Array.isArray(arr) ? arr.join(" ") : ""
  } catch {
    return ""
  }
}

function parseEnvJson(envJson: string | null | undefined): Record<string, string> {
  if (!envJson) return {}
  try {
    const obj = JSON.parse(envJson) as Record<string, string>
    return typeof obj === "object" && obj !== null ? obj : {}
  } catch {
    return {}
  }
}

function serializeArgs(argsStr: string): string | null {
  const trimmed = argsStr.trim()
  if (!trimmed) return null
  // Split on whitespace, preserving quoted segments
  const parts = trimmed.match(/(?:[^\s"]+|"[^"]*")+/g) ?? []
  return JSON.stringify(parts.map((p) => p.replace(/^"|"$/g, "")))
}

function isCredentialRef(value: string): boolean {
  return /^\$\{[^}]+\}$/.test(value)
}

export default function IntegrationsPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()
  const canManage = abilities.can("create", "Credential")
  const [servers, setServers] = React.useState<WorkspaceMCPServer[]>([])
  const [crewServers, setCrewServers] = React.useState<CrewIntegration[]>([])
  const [loading, setLoading] = React.useState(true)
  const [addOpen, setAddOpen] = React.useState(false)
  const [editOpen, setEditOpen] = React.useState(false)
  const [editServer, setEditServer] = React.useState<WorkspaceMCPServer | null>(null)

  const [expandedId, setExpandedId] = React.useState<string | null>(null)
  const [editState, setEditState] = React.useState<EditState | null>(null)
  const [saving, setSaving] = React.useState(false)

  const fetchServers = React.useCallback(async (wid: string) => {
    try {
      const [wsRes, crewRes] = await Promise.all([
        fetch(`/api/v1/integrations?workspace_id=${wid}`),
        fetch(`/api/v1/integrations/crews?workspace_id=${wid}`),
      ])
      setServers(wsRes.ok ? (await wsRes.json()) ?? [] : [])
      setCrewServers(crewRes.ok ? (await crewRes.json()) ?? [] : [])
    } catch {
      setServers([])
      setCrewServers([])
    }
  }, [])

  React.useEffect(() => {
    if (wsLoading) return
    if (!workspaceId) {
      setServers([])
      setLoading(false)
      return
    }

    let cancelled = false

    async function load() {
      setLoading(true)
      await fetchServers(workspaceId!)
      if (!cancelled) setLoading(false)
    }

    load()
    return () => {
      cancelled = true
    }
  }, [workspaceId, wsLoading, fetchServers])

  function handleRefresh() {
    if (workspaceId) fetchServers(workspaceId)
  }

  // Build unified list
  const rows: UnifiedRow[] = React.useMemo(() => {
    const wsRows: UnifiedRow[] = servers.map((s) => ({
      id: s.id,
      scope: "workspace" as const,
      name: s.name,
      display_name: s.display_name,
      transport: s.transport,
      command: s.command ?? null,
      args_json: s.args_json ?? null,
      env_json: s.env_json ?? null,
      enabled: s.enabled,
      created_at: s.created_at,
      wsServer: s,
    }))
    const crewRows: UnifiedRow[] = crewServers.map((cs) => ({
      id: cs.id,
      scope: "crew" as const,
      name: cs.name,
      display_name: cs.display_name,
      transport: cs.transport,
      command: cs.command,
      args_json: cs.args_json,
      env_json: cs.env_json,
      enabled: cs.enabled,
      created_at: cs.created_at,
      crew_id: cs.crew_id,
      crew_name: cs.crew_name,
    }))
    return [...wsRows, ...crewRows]
  }, [servers, crewServers])

  function handleToggleRow(row: UnifiedRow) {
    if (expandedId === row.id) {
      setExpandedId(null)
      setEditState(null)
      return
    }
    setExpandedId(row.id)
    setEditState({
      command: row.command ?? "",
      args: parseArgsJson(row.args_json),
      env: parseEnvJson(row.env_json),
      enabled: row.enabled,
    })
  }

  function updateEditField<K extends keyof EditState>(field: K, value: EditState[K]) {
    setEditState((prev) => (prev ? { ...prev, [field]: value } : prev))
  }

  function updateEnvValue(key: string, value: string) {
    setEditState((prev) => {
      if (!prev) return prev
      return { ...prev, env: { ...prev.env, [key]: value } }
    })
  }

  async function handleSave(row: UnifiedRow) {
    if (!editState || !workspaceId) return

    setSaving(true)
    try {
      const body: Record<string, unknown> = {}

      if (editState.command !== (row.command ?? "")) {
        body.command = editState.command || null
      }

      const newArgsJson = serializeArgs(editState.args)
      if (newArgsJson !== row.args_json) {
        body.args_json = newArgsJson
      }

      const origEnv = parseEnvJson(row.env_json)
      const envChanged = Object.keys(editState.env).some(
        (k) => editState.env[k] !== origEnv[k]
      )
      if (envChanged) {
        body.env_json = JSON.stringify(editState.env)
      }

      if (editState.enabled !== row.enabled) {
        body.enabled = editState.enabled
      }

      if (Object.keys(body).length === 0) {
        toast.info("No changes to save")
        setSaving(false)
        return
      }

      const url =
        row.scope === "crew" && row.crew_id
          ? `/api/v1/crews/${row.crew_id}/integrations/${row.id}`
          : `/api/v1/integrations/${row.id}?workspace_id=${workspaceId}`

      const res = await fetch(url, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })

      if (res.ok) {
        toast.success(`"${row.display_name}" updated`)
        setExpandedId(null)
        setEditState(null)
        handleRefresh()
      } else {
        const data = await res.json().catch(() => null)
        toast.error(data?.detail ?? "Failed to save changes")
      }
    } catch {
      toast.error("Network error")
    } finally {
      setSaving(false)
    }
  }

  async function handleDelete(row: UnifiedRow) {
    const confirmed = window.confirm(
      `Are you sure you want to delete "${row.display_name}"? This action cannot be undone.`
    )
    if (!confirmed || !workspaceId) return

    try {
      const url =
        row.scope === "crew" && row.crew_id
          ? `/api/v1/crews/${row.crew_id}/integrations/${row.id}`
          : `/api/v1/integrations/${row.id}?workspace_id=${workspaceId}`

      const res = await fetch(url, { method: "DELETE" })
      if (res.ok) {
        toast.success(`"${row.display_name}" deleted`)
        setExpandedId(null)
        setEditState(null)
        handleRefresh()
      } else {
        toast.error("Failed to delete integration")
      }
    } catch {
      toast.error("Network error")
    }
  }

  if (wsLoading || loading) {
    return (
      <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
        <PageHeader title="Integrations" description="Manage MCP server connections" />
        <div className="space-y-3">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Integrations" description="Manage MCP server connections for your workspace">
        {canManage && (
          <Button onClick={() => setAddOpen(true)}>
            <Plus className="mr-2 h-4 w-4" />
            Add Integration
          </Button>
        )}
      </PageHeader>

      {rows.length === 0 ? (
        <EmptyState
          icon={Plug}
          title="No integrations yet"
          description="Connect MCP servers to give your agents access to external tools and services."
        >
          {canManage && (
            <Button className="mt-4" onClick={() => setAddOpen(true)}>
              <Plus className="mr-2 h-4 w-4" />
              Add First Integration
            </Button>
          )}
        </EmptyState>
      ) : (
        <div className="rounded-md border divide-y">
          {rows.map((row) => {
            const isExpanded = expandedId === row.id
            const tc =
              TRANSPORT_CONFIG[row.transport as keyof typeof TRANSPORT_CONFIG] ??
              TRANSPORT_CONFIG.stdio
            const TransportIcon = tc.icon

            return (
              <div key={row.id}>
                {/* Collapsed row */}
                <button
                  type="button"
                  className="flex w-full items-center gap-4 px-4 py-3 text-left hover:bg-muted/40 transition-colors"
                  onClick={() => handleToggleRow(row)}
                  aria-expanded={isExpanded}
                  aria-controls={`panel-${row.id}`}
                >
                  {/* Chevron */}
                  <span className="shrink-0 text-muted-foreground">
                    {isExpanded ? (
                      <ChevronDown className="h-4 w-4" />
                    ) : (
                      <ChevronRight className="h-4 w-4" />
                    )}
                  </span>

                  {/* Name */}
                  <div className="flex items-center gap-2 min-w-0 flex-1">
                    <Plug className="h-4 w-4 shrink-0 text-muted-foreground" />
                    <div className="min-w-0">
                      <p className="font-medium text-sm truncate">{row.display_name}</p>
                      <p className="text-xs text-muted-foreground font-mono truncate">
                        {row.name}
                      </p>
                    </div>
                  </div>

                  {/* Scope badge */}
                  <div className="shrink-0">
                    {row.scope === "workspace" ? (
                      <Badge variant="secondary" className="text-xs font-normal">
                        <Globe className="mr-1 h-3 w-3" />
                        Workspace
                      </Badge>
                    ) : (
                      <Badge variant="outline" className="text-xs font-normal">
                        <Users className="mr-1 h-3 w-3" />
                        {row.crew_name}
                      </Badge>
                    )}
                  </div>

                  {/* Transport */}
                  <span className="hidden sm:flex items-center gap-1.5 text-sm text-muted-foreground shrink-0">
                    <TransportIcon className="h-3.5 w-3.5" />
                    {tc.label}
                  </span>
                </button>

                {/* Expanded panel */}
                {isExpanded && editState && (
                  <div
                    id={`panel-${row.id}`}
                    className="bg-muted/30 border-t px-6 py-5 space-y-5"
                  >
                    {/* Command + Args */}
                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                      <div className="space-y-1.5">
                        <Label htmlFor={`cmd-${row.id}`}>Command</Label>
                        <Input
                          id={`cmd-${row.id}`}
                          value={editState.command}
                          onChange={(e) => updateEditField("command", e.target.value)}
                          placeholder="e.g. npx"
                          disabled={!canManage}
                        />
                      </div>
                      <div className="space-y-1.5">
                        <Label htmlFor={`args-${row.id}`}>Args</Label>
                        <Input
                          id={`args-${row.id}`}
                          value={editState.args}
                          onChange={(e) => updateEditField("args", e.target.value)}
                          placeholder="e.g. -y @modelcontextprotocol/server"
                          disabled={!canManage}
                        />
                      </div>
                    </div>

                    {/* Environment Variables */}
                    {Object.keys(editState.env).length > 0 && (
                      <div className="space-y-2">
                        <Label>Environment Variables</Label>
                        <div className="rounded-md border bg-background">
                          <div className="divide-y">
                            {Object.entries(editState.env).map(([key, value]) => (
                              <div
                                key={key}
                                className="flex items-center gap-3 px-3 py-2"
                              >
                                <code className="text-xs font-mono text-muted-foreground w-40 shrink-0 truncate">
                                  {key}
                                </code>
                                {isCredentialRef(value) ? (
                                  <Badge
                                    variant="secondary"
                                    className="text-xs font-mono"
                                  >
                                    {value}
                                  </Badge>
                                ) : (
                                  <Input
                                    value={value}
                                    onChange={(e) =>
                                      updateEnvValue(key, e.target.value)
                                    }
                                    className="h-8 text-xs font-mono"
                                    disabled={!canManage}
                                  />
                                )}
                              </div>
                            ))}
                          </div>
                        </div>
                      </div>
                    )}

                    {/* Enable/Disable toggle */}
                    <div className="flex items-center gap-3">
                      <Switch
                        id={`enabled-${row.id}`}
                        checked={editState.enabled}
                        onCheckedChange={(checked) =>
                          updateEditField("enabled", checked)
                        }
                        disabled={!canManage}
                        aria-label={`${editState.enabled ? "Disable" : "Enable"} ${row.display_name}`}
                      />
                      <Label htmlFor={`enabled-${row.id}`} className="cursor-pointer">
                        {editState.enabled ? "Enabled" : "Disabled"}
                      </Label>
                    </div>

                    {/* Actions */}
                    {canManage && (
                      <div className="flex items-center gap-2 pt-1">
                        <Button
                          onClick={() => handleSave(row)}
                          disabled={saving}
                        >
                          {saving ? "Saving..." : "Save"}
                        </Button>
                        <Button
                          variant="destructive"
                          size="sm"
                          onClick={() => handleDelete(row)}
                          disabled={saving}
                        >
                          <Trash2 className="mr-1.5 h-3.5 w-3.5" />
                          Delete
                        </Button>
                      </div>
                    )}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}

      {workspaceId && (
        <AddIntegrationDialog
          workspaceId={workspaceId}
          open={addOpen}
          onOpenChange={setAddOpen}
          onSuccess={handleRefresh}
        />
      )}

      {workspaceId && editServer && (
        <EditIntegrationDialog
          workspaceId={workspaceId}
          server={editServer}
          open={editOpen}
          onOpenChange={setEditOpen}
          onSuccess={handleRefresh}
        />
      )}
    </div>
  )
}
