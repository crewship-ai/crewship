"use client"

import * as React from "react"

import { Switch } from "@/components/ui/switch"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "@/lib/toast"
import type { CrewTool } from "./crew-tools"

// On/Off per tool of a crew MCP server (INT-13): GET and PATCH
// /crews/{crewId}/integrations/{id}/tools[/{toolName}] — `crewship integration
// tools`. The server has no tool groups, so this is a flat list. A disabled
// tool leaves the agent's prompt block and is refused by the crew gateway from
// the next start. Composio apps have no such route (#3448).

interface Binding {
  id: string
  tool_name: string
  description: string | null
  enabled: boolean
}

export function CrewToolSwitches({ workspaceId, tool, canManage }: { workspaceId: string; tool: CrewTool; canManage: boolean }) {
  const [bindings, setBindings] = React.useState<Binding[] | null>(null)
  const [error, setError] = React.useState<string | null>(null)
  const base = `/api/v1/crews/${encodeURIComponent(tool.crew_id)}/integrations/${encodeURIComponent(tool.id)}/tools`

  React.useEffect(() => {
    let cancelled = false
    apiFetch(`${base}?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then(async (res) => {
        if (!res.ok) throw new Error(`Could not load tools (HTTP ${res.status})`)
        const rows: unknown = await res.json()
        if (!cancelled) setBindings(Array.isArray(rows) ? (rows as Binding[]) : [])
      })
      .catch((e) => { if (!cancelled) setError(e instanceof Error ? e.message : "Could not load tools") })
    return () => { cancelled = true }
  }, [base, workspaceId])

  const set = (name: string, enabled: boolean) =>
    setBindings((prev) => prev?.map((b) => (b.tool_name === name ? { ...b, enabled } : b)) ?? prev)

  const toggle = async (b: Binding) => {
    const next = !b.enabled
    set(b.tool_name, next)
    try {
      const res = await apiFetch(`${base}/${encodeURIComponent(b.tool_name)}?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: next }),
      })
      if (!res.ok) throw new Error()
    } catch {
      set(b.tool_name, !next)
      toast.error(`Could not turn ${b.tool_name} ${next ? "on" : "off"}`)
    }
  }

  return (
    <div className="border-t border-border/50 px-4 py-3" data-testid="crew-tool-switches">
      {error ? (
        <p className="text-label text-destructive">{error}</p>
      ) : bindings === null ? (
        <div className="h-8 animate-pulse rounded-md bg-muted" aria-busy="true" />
      ) : bindings.length === 0 ? (
        <p className="text-label text-muted-foreground">No tools are recorded for this server yet. They appear after an agent connects to it.</p>
      ) : (
        <>
          <p className="mb-1 text-label text-muted-foreground">
            {bindings.filter((b) => b.enabled).length} of {bindings.length} on · an off tool is hidden from the crew&apos;s agents from their next start
          </p>
          <ul className="divide-y divide-border/50">
            {bindings.map((b) => (
              <li key={b.id} className="flex items-center gap-3 py-2 coarse:min-h-[44px]">
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-mono text-xs">{b.tool_name}</span>
                  {b.description && <span className="block truncate text-label text-muted-foreground">{b.description}</span>}
                </span>
                <Switch checked={b.enabled} disabled={!canManage} aria-label={b.tool_name} onCheckedChange={() => void toggle(b)} />
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  )
}
