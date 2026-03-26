"use client"

import * as React from "react"
import { Plug, Plus, Pencil, Trash2, Globe, Terminal } from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { AddIntegrationDialog } from "@/components/features/integrations/add-integration-dialog"
import { EditIntegrationDialog } from "@/components/features/integrations/edit-integration-dialog"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { toast } from "sonner"
import type { WorkspaceMCPServer } from "@/lib/types/integration"

const TRANSPORT_CONFIG = {
  "streamable-http": { icon: Globe, label: "HTTP", variant: "default" as const },
  stdio: { icon: Terminal, label: "Stdio", variant: "secondary" as const },
} as const

export default function IntegrationsPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()
  const canManage = abilities.can("create", "Credential")
  const [servers, setServers] = React.useState<WorkspaceMCPServer[]>([])
  const [loading, setLoading] = React.useState(true)
  const [addOpen, setAddOpen] = React.useState(false)
  const [editOpen, setEditOpen] = React.useState(false)
  const [editServer, setEditServer] = React.useState<WorkspaceMCPServer | null>(null)

  const fetchServers = React.useCallback(async (wid: string) => {
    try {
      const res = await fetch(`/api/v1/integrations?workspace_id=${wid}`)
      if (!res.ok) {
        setServers([])
        return
      }
      const data = await res.json()
      setServers(Array.isArray(data) ? data : [])
    } catch {
      setServers([])
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

  function handleEdit(server: WorkspaceMCPServer) {
    setEditServer(server)
    setEditOpen(true)
  }

  async function handleDelete(server: WorkspaceMCPServer) {
    const confirmed = window.confirm(
      `Are you sure you want to delete "${server.display_name}"? This action cannot be undone.`
    )
    if (!confirmed || !workspaceId) return

    try {
      const res = await fetch(`/api/v1/integrations/${server.id}?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })
      if (res.ok) {
        toast.success(`"${server.display_name}" deleted`)
        handleRefresh()
      } else {
        toast.error("Failed to delete integration")
      }
    } catch {
      toast.error("Network error")
    }
  }

  async function handleToggleEnabled(server: WorkspaceMCPServer) {
    if (!workspaceId) return

    try {
      const res = await fetch(`/api/v1/integrations/${server.id}?workspace_id=${workspaceId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: !server.enabled }),
      })
      if (res.ok) {
        handleRefresh()
      } else {
        toast.error("Failed to update integration")
      }
    } catch {
      toast.error("Network error")
    }
  }

  function formatDate(dateStr: string): string {
    return new Intl.DateTimeFormat("en-US", {
      month: "short",
      day: "numeric",
      year: "numeric",
    }).format(new Date(dateStr))
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

      {servers.length === 0 ? (
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
        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Transport</TableHead>
                <TableHead>Enabled</TableHead>
                <TableHead>Agent Bindings</TableHead>
                <TableHead>Created</TableHead>
                {canManage && <TableHead className="text-right">Actions</TableHead>}
              </TableRow>
            </TableHeader>
            <TableBody>
              {servers.map((server) => {
                const transportConfig = TRANSPORT_CONFIG[server.transport]
                const TransportIcon = transportConfig.icon

                return (
                  <TableRow key={server.id}>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <Plug className="h-4 w-4 shrink-0 text-muted-foreground" />
                        <div className="min-w-0">
                          <p className="font-medium text-sm">{server.display_name}</p>
                          <p className="text-label text-muted-foreground font-mono">{server.name}</p>
                        </div>
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge variant={transportConfig.variant} className="text-label font-normal">
                        <TransportIcon className="mr-1 h-3 w-3" />
                        {transportConfig.label}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <Switch
                        checked={server.enabled}
                        onCheckedChange={() => handleToggleEnabled(server)}
                        disabled={!canManage}
                        aria-label={`${server.enabled ? "Disable" : "Enable"} ${server.display_name}`}
                      />
                    </TableCell>
                    <TableCell>
                      <span className="text-muted-foreground">
                        {server.agent_binding_count} {server.agent_binding_count === 1 ? "agent" : "agents"}
                      </span>
                    </TableCell>
                    <TableCell>
                      <span className="text-muted-foreground">{formatDate(server.created_at)}</span>
                    </TableCell>
                    {canManage && (
                      <TableCell className="text-right">
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => handleEdit(server)}
                            title="Edit integration"
                          >
                            <Pencil className="h-3.5 w-3.5" />
                            <span className="sr-only">Edit</span>
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => handleDelete(server)}
                            title="Delete integration"
                          >
                            <Trash2 className="h-3.5 w-3.5 text-destructive" />
                            <span className="sr-only">Delete</span>
                          </Button>
                        </div>
                      </TableCell>
                    )}
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
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
