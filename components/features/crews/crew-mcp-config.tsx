"use client"

import { useEffect, useState, useCallback, useRef } from "react"
import { Plug, Loader2, ChevronDown } from "lucide-react"
import {
  Card, CardContent, CardHeader,
} from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import { MCPConfigEditor } from "@/components/features/mcp/mcp-config-editor"
import { parseConfig, serializeConfig } from "@/components/features/mcp/lib/config-parser"
import type { ServerEntry } from "@/components/features/mcp/types"
import {
  crewServerToEntry,
  entryToPayload,
  diffEntries,
} from "@/components/features/mcp/lib/integration-adapter"
import type { CrewMCPServer } from "@/components/features/mcp/lib/integration-adapter"

interface CrewMCPConfigProps {
  crewId: string
  workspaceId: string
}

export function CrewMCPConfig({ crewId, workspaceId }: CrewMCPConfigProps) {
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [open, setOpen] = useState(false)

  // The serialized JSON string that MCPConfigEditor uses as its value.
  // We keep this as the "current" editing state.
  const [configJson, setConfigJson] = useState("")

  // Snapshot of entries as they were loaded from the API — used for diffing.
  const originalEntriesRef = useRef<ServerEntry[]>([])

  // The serialized JSON of the last-saved state (for dirty detection).
  const [savedJson, setSavedJson] = useState("")

  const fetchIntegrations = useCallback(async () => {
    setLoading(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/integrations?workspace_id=${workspaceId}`,
        { credentials: "include" },
      )
      if (!res.ok) {
        toast.error("Failed to load crew MCP configuration")
        setOpen(false)
        return
      }
      const servers: CrewMCPServer[] = await res.json()
      const entries = servers.map((s, i) => crewServerToEntry(s, i + 1))
      originalEntriesRef.current = entries

      const json = serializeConfig(entries)
      setConfigJson(json)
      setSavedJson(json)
      setOpen(entries.length > 0)
    } catch {
      toast.error("Network error loading MCP configuration")
      setOpen(false)
    } finally {
      setLoading(false)
    }
  }, [crewId, workspaceId])

  useEffect(() => {
    fetchIntegrations()
  }, [fetchIntegrations])

  const handleSave = useCallback(async () => {
    setSaving(true)
    try {
      // Parse current editor state into entries, preserving ids from originals.
      const currentEntries = reconcileIds(
        originalEntriesRef.current,
        parseConfig(configJson),
      )
      const { create, update, remove } = diffEntries(
        originalEntriesRef.current,
        currentEntries,
      )

      const baseUrl = `/api/v1/crews/${crewId}/integrations`
      const wsParam = `workspace_id=${workspaceId}`
      const errors: string[] = []

      // Run all mutations in parallel
      const promises: Promise<void>[] = []

      for (const entry of create) {
        const payload = entryToPayload(entry)
        promises.push(
          fetch(`${baseUrl}?${wsParam}`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            credentials: "include",
            body: JSON.stringify(payload),
          }).then(async (res) => {
            if (!res.ok) {
              const data = await res.json().catch(() => ({ error: "Failed" }))
              errors.push(`Create "${entry.name}": ${data.error ?? "Failed"}`)
            }
          }),
        )
      }

      for (const entry of update) {
        const payload = entryToPayload(entry)
        promises.push(
          fetch(`${baseUrl}/${entry.id}?${wsParam}`, {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            credentials: "include",
            body: JSON.stringify(payload),
          }).then(async (res) => {
            if (!res.ok) {
              const data = await res.json().catch(() => ({ error: "Failed" }))
              errors.push(`Update "${entry.name}": ${data.error ?? "Failed"}`)
            }
          }),
        )
      }

      for (const id of remove) {
        promises.push(
          fetch(`${baseUrl}/${id}?${wsParam}`, {
            method: "DELETE",
            credentials: "include",
          }).then(async (res) => {
            if (!res.ok) {
              const data = await res.json().catch(() => ({ error: "Failed" }))
              errors.push(`Delete: ${data.error ?? "Failed"}`)
            }
          }),
        )
      }

      await Promise.all(promises)

      if (errors.length > 0) {
        toast.error(errors.join("; "))
      } else {
        toast.success("MCP configuration saved")
      }

      // Refetch to get authoritative state with IDs
      await fetchIntegrations()
    } catch {
      toast.error("Network error saving MCP configuration")
    } finally {
      setSaving(false)
    }
  }, [crewId, workspaceId, configJson, fetchIntegrations])

  const hasChanges = configJson !== savedJson

  if (loading) {
    return (
      <Card>
        <CardHeader>
          <Skeleton className="h-5 w-32" />
          <Skeleton className="h-4 w-64" />
        </CardHeader>
        <CardContent className="space-y-4">
          <Skeleton className="h-32 w-full" />
        </CardContent>
      </Card>
    )
  }

  return (
    <Card>
      <Collapsible open={open} onOpenChange={setOpen}>
        <CardHeader className="pb-3">
          <CollapsibleTrigger asChild>
            <button
              type="button"
              className="flex items-center justify-between w-full text-left group"
            >
              <div className="flex items-center gap-2">
                <Plug className="h-4 w-4 text-muted-foreground" />
                <span className="text-sm font-semibold">MCP Servers</span>
                {originalEntriesRef.current.length > 0 && (
                  <span className="text-xs text-muted-foreground">
                    ({originalEntriesRef.current.length} active)
                  </span>
                )}
              </div>
              <ChevronDown
                className={cn(
                  "h-4 w-4 text-muted-foreground transition-transform",
                  open && "rotate-180",
                )}
              />
            </button>
          </CollapsibleTrigger>
        </CardHeader>

        <CollapsibleContent>
          <CardContent className="space-y-4 pt-0">
            <p className="text-xs text-muted-foreground">
              Configure Model Context Protocol servers shared by all agents in this crew.
              Agent-level configurations are merged on top of crew settings.
            </p>

            <MCPConfigEditor value={configJson} onChange={setConfigJson} workspaceId={workspaceId} />

            {hasChanges && (
              <Button size="sm" onClick={handleSave} disabled={saving} className="gap-1.5">
                {saving && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                {saving ? "Saving..." : "Save MCP Configuration"}
              </Button>
            )}
          </CardContent>
        </CollapsibleContent>
      </Collapsible>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * After the MCPConfigEditor round-trips through serialize/parse, entries lose
 * their `id` field. This function re-attaches ids by matching entries from the
 * original list by name (since names are unique per crew).
 */
function reconcileIds(
  originals: ServerEntry[],
  current: ServerEntry[],
): ServerEntry[] {
  const idByName = new Map<string, string>()
  for (const entry of originals) {
    if (entry.id) {
      idByName.set(entry.name, entry.id)
    }
  }

  return current.map((entry) => {
    const existingId = idByName.get(entry.name)
    if (existingId) {
      // Remove from map so duplicate names don't reuse the same id
      idByName.delete(entry.name)
      return { ...entry, id: existingId }
    }
    return entry
  })
}
