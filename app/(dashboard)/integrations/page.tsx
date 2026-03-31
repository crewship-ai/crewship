"use client"

import * as React from "react"
import { Plug, Plus, Globe, Terminal, Users, ChevronRight, ChevronDown, Bot } from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import { MCPConfigEditor } from "@/components/features/mcp/mcp-config-editor"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { toast } from "sonner"
import {
  crewServerToEntry,
  entryToPayload,
  diffEntries,
  type CrewMCPServer,
} from "@/components/features/mcp/lib/integration-adapter"
import { parseConfig, serializeConfig, entryFromTemplate } from "@/components/features/mcp/lib/config-parser"
import { MCP_TEMPLATES, TEMPLATE_ICONS } from "@/components/features/mcp/templates"
import type { ServerEntry, MCPTemplate } from "@/components/features/mcp/types"

interface CrewIntegrationRow extends CrewMCPServer {
  crew_name: string
  crew_slug: string
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

export default function IntegrationsPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()
  const canManage = abilities.can("create", "Credential")

  const [crewServers, setCrewServers] = React.useState<CrewIntegrationRow[]>([])
  const [crews, setCrews] = React.useState<CrewInfo[]>([])
  const [crewAgents, setCrewAgents] = React.useState<Record<string, AgentInfo[]>>({})
  const [loading, setLoading] = React.useState(true)
  const [saving, setSaving] = React.useState(false)
  const [templatePopoverOpen, setTemplatePopoverOpen] = React.useState(false)

  // Editor state
  const snapshotRef = React.useRef<ServerEntry[]>([])
  const [editorJson, setEditorJson] = React.useState("")

  // Accordion
  const [expandedIdx, setExpandedIdx] = React.useState<number | null>(null)

  // -----------------------------------------------------------------------
  // Fetch
  // -----------------------------------------------------------------------

  const fetchAll = React.useCallback(async (wid: string) => {
    try {
      const [crewRes, crewsListRes] = await Promise.all([
        fetch(`/api/v1/integrations/crews?workspace_id=${wid}`),
        fetch(`/api/v1/crews?workspace_id=${wid}`),
      ])
      const data: CrewIntegrationRow[] = crewRes.ok ? (await crewRes.json()) ?? [] : []
      setCrewServers(data)

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
            agentMap[cid] = Array.isArray(agents) ? agents.map((a: AgentInfo) => ({ id: a.id, name: a.name, slug: a.slug })) : []
          }
        }),
      )
      setCrewAgents(agentMap)

      // Build editor JSON
      const entries = data.map((s, i) => crewServerToEntry(s, i))
      snapshotRef.current = entries
      setEditorJson(serializeConfig(entries))
    } catch {
      setCrewServers([])
      setEditorJson("")
    }
  }, [])

  React.useEffect(() => {
    if (wsLoading || !workspaceId) {
      if (!wsLoading) setLoading(false)
      return
    }
    let cancelled = false
    ;(async () => {
      setLoading(true)
      await fetchAll(workspaceId)
      if (!cancelled) setLoading(false)
    })()
    return () => { cancelled = true }
  }, [workspaceId, wsLoading, fetchAll])

  // -----------------------------------------------------------------------
  // Add from template
  // -----------------------------------------------------------------------

  function handleAddFromTemplate(template: MCPTemplate) {
    const currentEntries = parseConfig(editorJson)
    const newEntry = entryFromTemplate(template)
    const updated = [...currentEntries, newEntry]
    setEditorJson(serializeConfig(updated))
    setExpandedIdx(updated.length - 1)
    setTemplatePopoverOpen(false)
  }

  function handleAddCustom() {
    const currentEntries = parseConfig(editorJson)
    const newEntry: ServerEntry = {
      _key: Date.now(),
      name: "",
      transport: "stdio",
      command: "",
      args: "",
      url: "",
      headers: [],
      env: [],
    }
    const updated = [...currentEntries, newEntry]
    setEditorJson(serializeConfig(updated))
    setExpandedIdx(updated.length - 1)
    setTemplatePopoverOpen(false)
  }

  // -----------------------------------------------------------------------
  // Save
  // -----------------------------------------------------------------------

  async function handleSave() {
    if (!workspaceId || saving) return

    const currentEntries = parseConfig(editorJson)
    const withIds = reconcileIds(snapshotRef.current, currentEntries)
    const diff = diffEntries(snapshotRef.current, withIds)

    if (diff.create.length === 0 && diff.update.length === 0 && diff.remove.length === 0) {
      toast.info("No changes to save")
      return
    }

    setSaving(true)
    try {
      const errors: string[] = []
      let defaultCrewId = crewServers[0]?.crew_id || crews[0]?.id

      for (const entry of diff.create) {
        if (!entry.name.trim() || !defaultCrewId) continue
        const payload = entryToPayload(entry)
        const res = await fetch(
          `/api/v1/crews/${defaultCrewId}/integrations?workspace_id=${workspaceId}`,
          { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) },
        )
        if (!res.ok) {
          const d = await res.json().catch(() => null)
          errors.push(d?.error ?? `Failed to create "${entry.name}"`)
        }
      }

      for (const entry of diff.update) {
        if (!entry.id) continue
        const original = crewServers.find((s) => s.id === entry.id)
        const crewId = original?.crew_id ?? defaultCrewId
        if (!crewId) continue
        const payload = entryToPayload(entry)
        const res = await fetch(
          `/api/v1/crews/${crewId}/integrations/${entry.id}?workspace_id=${workspaceId}`,
          { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) },
        )
        if (!res.ok) {
          const d = await res.json().catch(() => null)
          errors.push(d?.error ?? `Failed to update "${entry.name}"`)
        }
      }

      for (const id of diff.remove) {
        const original = crewServers.find((s) => s.id === id)
        const crewId = original?.crew_id ?? defaultCrewId
        if (!crewId) continue
        await fetch(`/api/v1/crews/${crewId}/integrations/${id}?workspace_id=${workspaceId}`, { method: "DELETE" })
      }

      if (errors.length > 0) toast.error(errors[0])
      else toast.success("Integrations saved")

      setExpandedIdx(null)
      await fetchAll(workspaceId)
    } catch {
      toast.error("Network error")
    } finally {
      setSaving(false)
    }
  }

  // -----------------------------------------------------------------------
  // Render
  // -----------------------------------------------------------------------

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

  const entries = parseConfig(editorJson)
  const hasChanges = editorJson !== serializeConfig(snapshotRef.current)

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Integrations" description="Manage MCP server connections for your workspace">
        <div className="flex items-center gap-2">
          {canManage && hasChanges && (
            <Button onClick={handleSave} disabled={saving}>
              {saving ? "Saving..." : "Save Changes"}
            </Button>
          )}
          {canManage && (
            <Popover open={templatePopoverOpen} onOpenChange={setTemplatePopoverOpen}>
              <PopoverTrigger asChild>
                <Button>
                  <Plus className="mr-2 h-4 w-4" />
                  Add MCP Server
                </Button>
              </PopoverTrigger>
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
                          onClick={() => handleAddFromTemplate(t)}
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
                    onClick={handleAddCustom}
                  >
                    <Terminal className="h-4 w-4" />
                    Custom server
                  </button>
                </div>
              </PopoverContent>
            </Popover>
          )}
        </div>
      </PageHeader>

      {entries.length === 0 ? (
        <EmptyState
          icon={Plug}
          title="No integrations yet"
          description="Connect MCP servers to give your agents access to external tools and services."
        >
          {canManage && (
            <Popover>
              <PopoverTrigger asChild>
                <Button className="mt-4">
                  <Plus className="mr-2 h-4 w-4" />
                  Add First MCP Server
                </Button>
              </PopoverTrigger>
              <PopoverContent className="w-80 p-3">
                <div className="space-y-2">
                  <p className="text-sm font-medium">Choose a template</p>
                  <div className="grid grid-cols-2 gap-2">
                    {MCP_TEMPLATES.map((t) => {
                      const Icon = TEMPLATE_ICONS[t.icon] ?? Plug
                      return (
                        <button
                          key={t.name}
                          type="button"
                          className="flex items-center gap-2 rounded-md border px-3 py-2 text-left text-sm hover:bg-muted/60 transition-colors"
                          onClick={() => handleAddFromTemplate(t)}
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
                    onClick={handleAddCustom}
                  >
                    <Terminal className="h-4 w-4" />
                    Custom server
                  </button>
                </div>
              </PopoverContent>
            </Popover>
          )}
        </EmptyState>
      ) : (
        <div className="space-y-3">
          {/* Accordion list — each entry is a collapsible row */}
          <div className="rounded-md border divide-y">
            {entries.map((entry, idx) => {
              const isExpanded = expandedIdx === idx
              const server = crewServers.find((s) => s.name === entry.name)
              const crewName = server?.crew_name
              const agents = server ? (crewAgents[server.crew_id] ?? []) : []
              const isNew = !entry.id && !server

              return (
                <div key={entry._key}>
                  {/* Collapsed header row */}
                  <button
                    type="button"
                    className="flex w-full items-center gap-4 px-4 py-3 text-left hover:bg-muted/40 transition-colors"
                    onClick={() => setExpandedIdx(isExpanded ? null : idx)}
                  >
                    <span className="shrink-0 text-muted-foreground">
                      {isExpanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
                    </span>

                    <div className="flex items-center gap-2 min-w-0 flex-1">
                      <Plug className="h-4 w-4 shrink-0 text-muted-foreground" />
                      <div className="min-w-0">
                        <p className="font-medium text-sm truncate">{entry.name || "(new server)"}</p>
                        <p className="text-xs text-muted-foreground truncate">
                          {entry.transport === "http" ? entry.url : `${entry.command} ${entry.args}`.trim()}
                        </p>
                      </div>
                    </div>

                    {/* Crew badge */}
                    {crewName && (
                      <Badge variant="outline" className="text-xs font-normal shrink-0">
                        <Users className="mr-1 h-3 w-3" />
                        {crewName}
                      </Badge>
                    )}
                    {isNew && (
                      <Badge variant="default" className="text-xs font-normal shrink-0">
                        New
                      </Badge>
                    )}

                    {/* Transport */}
                    <span className="hidden sm:flex items-center gap-1.5 text-xs text-muted-foreground shrink-0">
                      {entry.transport === "http" ? <Globe className="h-3 w-3" /> : <Terminal className="h-3 w-3" />}
                      {entry.transport === "http" ? "HTTP" : "Stdio"}
                    </span>

                    {/* Agent count */}
                    {agents.length > 0 && (
                      <span className="hidden sm:flex items-center gap-1 text-xs text-muted-foreground shrink-0">
                        <Bot className="h-3 w-3" />
                        {agents.length}
                      </span>
                    )}
                  </button>

                  {/* Expanded panel */}
                  {isExpanded && (
                    <div className="bg-muted/20 border-t px-6 py-5 space-y-4">
                      {/* Agent & crew info */}
                      {(agents.length > 0 || crewName) && (
                        <div className="flex flex-wrap gap-4 text-sm">
                          {crewName && (
                            <div>
                              <span className="text-xs text-muted-foreground">Crew:</span>{" "}
                              <span className="font-medium">{crewName}</span>
                            </div>
                          )}
                          {agents.length > 0 && (
                            <div className="flex items-center gap-1.5">
                              <span className="text-xs text-muted-foreground">Agents:</span>
                              <div className="flex flex-wrap gap-1">
                                {agents.map((a) => (
                                  <Badge key={a.id} variant="secondary" className="text-xs font-normal">
                                    {a.name}
                                  </Badge>
                                ))}
                              </div>
                            </div>
                          )}
                        </div>
                      )}

                      {/* MCPConfigEditor for just this one entry */}
                      <MCPConfigEditor
                        value={serializeConfig([entry])}
                        onChange={(json) => {
                          const parsed = parseConfig(json)
                          if (parsed.length === 0) {
                            // Entry was deleted — if it has a DB id, call DELETE API immediately
                            if (entry.id && server && workspaceId) {
                              fetch(`/api/v1/crews/${server.crew_id}/integrations/${entry.id}?workspace_id=${workspaceId}`, { method: "DELETE" })
                                .then((res) => {
                                  if (res.ok) {
                                    toast.success(`"${entry.name}" deleted`)
                                    fetchAll(workspaceId)
                                  } else {
                                    toast.error("Failed to delete integration")
                                  }
                                })
                                .catch(() => toast.error("Network error"))
                            }
                            const updated = entries.filter((_, i) => i !== idx)
                            setEditorJson(serializeConfig(updated))
                            setExpandedIdx(null)
                          } else {
                            // Entry was updated
                            const updated = entries.map((e, i) =>
                              i === idx ? { ...parsed[0], _key: entry._key, id: entry.id } : e,
                            )
                            setEditorJson(serializeConfig(updated))
                          }
                        }}
                        readOnly={!canManage}
                        workspaceId={workspaceId ?? undefined}
                      />
                    </div>
                  )}
                </div>
              )
            })}
          </div>

          {/* Save button at bottom too if there are changes */}
          {canManage && hasChanges && (
            <div className="flex justify-end">
              <Button onClick={handleSave} disabled={saving}>
                {saving ? "Saving..." : "Save Changes"}
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function reconcileIds(snapshot: ServerEntry[], current: ServerEntry[]): ServerEntry[] {
  const idByName = new Map<string, string>()
  for (const e of snapshot) {
    if (e.id && e.name) idByName.set(e.name, e.id)
  }
  return current.map((e) => {
    if (e.id) return e
    const matchedId = idByName.get(e.name)
    return matchedId ? { ...e, id: matchedId } : e
  })
}
