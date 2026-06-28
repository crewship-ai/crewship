"use client"

import * as React from "react"
import { motion } from "motion/react"
import {
  AlertTriangle,
  RefreshCw,
  Trash2,
  Settings as SettingsIcon,
  Wrench,
  Activity,
  Globe,
  Terminal,
} from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { toast } from "sonner"
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "@/components/ui/sheet"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Switch } from "@/components/ui/switch"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { MCPLogo } from "@/components/icons/mcp-logos"
import { TrustTierBadge, type TrustTier } from "./trust-tier-badge"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import { formatRelativeTime } from "@/lib/time"

interface ServerSummary {
  id: string
  crew_id: string
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
  trust_tier?: TrustTier
}

interface ToolBinding {
  id: string
  tool_name: string
  description: string | null
  enabled: boolean
  created_at: string
  updated_at: string
}

const TOOL_CEILING_WARNING = 40

export interface MCPDetailSheetProps {
  workspaceId: string
  server: ServerSummary | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onRefresh: () => void
}

export function MCPDetailSheet({
  workspaceId, server, open, onOpenChange, onRefresh,
}: MCPDetailSheetProps) {
  const [tab, setTab] = React.useState<"overview" | "tools" | "logs" | "settings">("overview")
  const [tools, setTools] = React.useState<ToolBinding[]>([])
  const [toolsLoading, setToolsLoading] = React.useState(false)
  const [confirmDelete, setConfirmDelete] = React.useState(false)
  const [reloading, setReloading] = React.useState(false)

  React.useEffect(() => {
    if (!open) {
      setTab("overview")
      setTools([])
    }
  }, [open])

  const loadTools = React.useCallback(async () => {
    if (!server) return
    setToolsLoading(true)
    try {
      const res = await apiFetch(`/api/v1/crews/${server.crew_id}/integrations/${server.id}/tools?workspace_id=${workspaceId}`)
      if (res.ok) setTools(await res.json())
    } catch {
      setTools([])
    } finally {
      setToolsLoading(false)
    }
  }, [server, workspaceId])

  React.useEffect(() => {
    if (open && server && tab === "tools") loadTools()
  }, [open, server, tab, loadTools])

  if (!server) return null

  const enabledCount = tools.filter((t) => t.enabled).length
  const totalCount = tools.length
  const overCeiling = enabledCount > TOOL_CEILING_WARNING

  const toggleTool = async (tool: ToolBinding) => {
    // Optimistic UI
    const next = !tool.enabled
    setTools((prev) => prev.map((t) => t.id === tool.id ? { ...t, enabled: next } : t))
    try {
      const res = await apiFetch(`/api/v1/crews/${server.crew_id}/integrations/${server.id}/tools/${encodeURIComponent(tool.tool_name)}?workspace_id=${workspaceId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: next }),
      })
      if (!res.ok) {
        setTools((prev) => prev.map((t) => t.id === tool.id ? { ...t, enabled: !next } : t))
        toast.error("Failed to update tool")
      }
    } catch {
      setTools((prev) => prev.map((t) => t.id === tool.id ? { ...t, enabled: !next } : t))
      toast.error("Network error")
    }
  }

  const refreshTools = async () => {
    if (!server) return
    setReloading(true)
    try {
      // Hot-swap reload (CONNECTIONS.md §5.5 + §7.5).
      // For MVP we just re-pull the bindings from DB; live mcp/list-tools
      // wiring lands in a sidecar follow-up.
      await loadTools()
      toast.success("Tools refreshed")
    } finally {
      setReloading(false)
    }
  }

  const handleDelete = async () => {
    const res = await apiFetch(`/api/v1/crews/${server.crew_id}/integrations/${server.id}?workspace_id=${workspaceId}`, {
      method: "DELETE",
    })
    if (res.ok) {
      toast.success("Server removed")
      onRefresh()
      onOpenChange(false)
    }
    setConfirmDelete(false)
  }

  return (
    <>
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetContent side="right" className="sm:max-w-[560px] p-0 flex flex-col">
          <SheetHeader className="px-5 pt-4 pb-3 border-b border-white/10">
            <div className="flex items-start gap-3">
              <MCPLogo name={server.icon || server.name} transport={server.transport} className="h-8 w-8 shrink-0 mt-0.5 opacity-90" />
              <div className="flex-1 min-w-0">
                <SheetTitle className="text-base">{server.display_name || server.name}</SheetTitle>
                <SheetDescription className="text-xs flex items-center gap-2 mt-1 flex-wrap">
                  <Badge variant="outline" className="text-[10px] gap-1">
                    {server.transport === "stdio" ? <Terminal className="h-2.5 w-2.5" /> : <Globe className="h-2.5 w-2.5" />}
                    {server.transport}
                  </Badge>
                  {server.trust_tier && <TrustTierBadge tier={server.trust_tier} />}
                </SheetDescription>
              </div>
            </div>
          </SheetHeader>

          <Tabs value={tab} onValueChange={(v) => setTab(v as typeof tab)} className="flex-1 flex flex-col">
            <TabsList className="px-3 mt-2 justify-start bg-transparent border-b border-white/10 rounded-none h-9">
              <TabsTrigger value="overview" className="text-xs">Overview</TabsTrigger>
              <TabsTrigger value="tools" className="text-xs gap-1.5">
                <Wrench className="h-3 w-3" />
                Tools
                {totalCount > 0 && (
                  <Badge variant="secondary" className="ml-0.5 h-4 text-[10px] px-1.5 font-mono">
                    {enabledCount}/{totalCount}
                  </Badge>
                )}
              </TabsTrigger>
              <TabsTrigger value="logs" className="text-xs"><Activity className="h-3 w-3 mr-1" />Logs</TabsTrigger>
              <TabsTrigger value="settings" className="text-xs"><SettingsIcon className="h-3 w-3 mr-1" />Settings</TabsTrigger>
            </TabsList>

            <div className="flex-1 overflow-y-auto p-4">
              <TabsContent value="overview" className="m-0 space-y-3">
                <Field label="Status">
                  {server.auth_status === "connected" ? <span className="text-emerald-400">Connected</span>
                    : server.auth_status === "missing" ? <span className="text-red-400">No credential</span>
                    : server.auth_status === "expired" ? <span className="text-amber-400">Expired</span>
                    : <span className="text-muted-foreground">No auth</span>}
                </Field>
                <Field label="Transport">{server.transport}</Field>
                {server.endpoint && <Field label="Endpoint">{server.endpoint}</Field>}
                {server.command && <Field label="Command">{server.command}</Field>}
                <Field label="Agent bindings">{server.agent_binding_count}</Field>
                <Field label="Updated">{formatRelativeTime(server.updated_at)}</Field>
              </TabsContent>

              <TabsContent value="tools" className="m-0 space-y-3">
                <div className="flex items-center justify-between gap-2">
                  <div className="text-xs text-muted-foreground">
                    {totalCount === 0 ? "No tools recorded yet" : `${enabledCount} of ${totalCount} enabled`}
                  </div>
                  <Button variant="outline" size="sm" onClick={refreshTools} disabled={reloading} className="h-7 text-xs">
                    {reloading ? <Spinner className="h-3 w-3 mr-1" /> : <RefreshCw className="h-3 w-3 mr-1" />}
                    Refresh
                  </Button>
                </div>

                {overCeiling && (
                  <div className="rounded-md border border-amber-500/30 bg-amber-500/[0.05] p-3 text-xs flex gap-2">
                    <AlertTriangle className="h-4 w-4 text-amber-400 shrink-0" />
                    <span className="text-foreground/80">
                      <strong>{enabledCount} active tools</strong> &mdash; many active tools degrade model
                      quality. Consider disabling unused tools (Cursor recommends ~40 max).
                    </span>
                  </div>
                )}

                {toolsLoading ? (
                  <div className="text-center py-8"><Spinner className="inline h-4 w-4 text-muted-foreground" /></div>
                ) : totalCount === 0 ? (
                  <div className="rounded-md border border-white/10 bg-zinc-950 p-4 text-xs text-muted-foreground">
                    No tools recorded yet. Click <strong>Refresh</strong> after a successful test
                    connection to populate this list. Until then the server exposes whatever
                    tools the upstream MCP server publishes (default: all enabled).
                  </div>
                ) : (
                  <ul className="space-y-1.5">
                    {tools.map((t, idx) => (
                      <motion.li
                        key={t.id}
                        initial={{ opacity: 0, y: 4 }}
                        animate={{ opacity: 1, y: 0 }}
                        transition={{ duration: 0.1, delay: Math.min(idx, 30) * 0.01 }}
                        className="flex items-start gap-3 rounded-md border border-white/10 bg-zinc-950 p-3"
                      >
                        <Switch
                          checked={t.enabled}
                          onCheckedChange={() => toggleTool(t)}
                          className="mt-0.5"
                        />
                        <div className="flex-1 min-w-0">
                          <div className="text-xs font-mono">{t.tool_name}</div>
                          {t.description && (
                            <div className="text-[11px] text-muted-foreground mt-0.5 line-clamp-2">
                              {t.description}
                            </div>
                          )}
                        </div>
                      </motion.li>
                    ))}
                  </ul>
                )}
              </TabsContent>

              <TabsContent value="logs" className="m-0">
                <div className="rounded-md border border-white/10 bg-zinc-950 p-4 text-xs text-muted-foreground">
                  Tool-call logs land in a follow-up ticket. The data layer (mcp_tool_calls table from v32)
                  is already populated; the UI binding ships next.
                </div>
              </TabsContent>

              <TabsContent value="settings" className="m-0 space-y-3">
                <Button variant="outline" size="sm" onClick={refreshTools} disabled={reloading} className="w-full justify-start">
                  {reloading ? <Spinner className="h-3.5 w-3.5 mr-1.5" /> : <RefreshCw className="h-3.5 w-3.5 mr-1.5" />}
                  Hot-swap reload
                </Button>

                <div className="pt-3 border-t border-white/10">
                  <Button
                    size="sm"
                    variant="outline"
                    className="w-full justify-start text-red-400 border-red-500/30 hover:bg-red-500/[0.05]"
                    onClick={() => setConfirmDelete(true)}
                  >
                    <Trash2 className="h-3.5 w-3.5 mr-1.5" />
                    Remove server
                  </Button>
                </div>
              </TabsContent>
            </div>
          </Tabs>
        </SheetContent>
      </Sheet>

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove this MCP server?</AlertDialogTitle>
            <AlertDialogDescription>
              <span className="font-mono">{server.display_name || server.name}</span> will be
              removed from this crew. Agents will lose access to its tools immediately.
              Tool bindings (per-tool enabled flags) are also deleted.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className={cn("bg-destructive text-white hover:bg-destructive/90")}
              onClick={handleDelete}
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[100px_1fr] gap-2 text-xs">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-foreground/90 font-mono break-all">{children}</span>
    </div>
  )
}
