"use client"

import * as React from "react"
import { Plug } from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { Skeleton } from "@/components/ui/skeleton"
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
import { parseConfig, serializeConfig } from "@/components/features/mcp/lib/config-parser"
import type { ServerEntry } from "@/components/features/mcp/types"

// The integrations/crews endpoint returns crew servers enriched with crew name.
interface CrewIntegrationRow extends CrewMCPServer {
  crew_name: string
  crew_slug: string
}

export default function IntegrationsPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()
  const canManage = abilities.can("create", "Credential")

  const [crewServers, setCrewServers] = React.useState<CrewIntegrationRow[]>([])
  const [loading, setLoading] = React.useState(true)
  const [saving, setSaving] = React.useState(false)

  // Snapshot of the entries as loaded from the API (for diffing on save).
  const snapshotRef = React.useRef<ServerEntry[]>([])

  // The editor works with a JSON string — derive it from API data.
  const [editorJson, setEditorJson] = React.useState("")

  // -----------------------------------------------------------------------
  // Fetch
  // -----------------------------------------------------------------------

  const fetchServers = React.useCallback(async (wid: string) => {
    try {
      const res = await fetch(`/api/v1/integrations/crews?workspace_id=${wid}`)
      const data: CrewIntegrationRow[] = res.ok ? (await res.json()) ?? [] : []
      setCrewServers(data)

      // Convert API data → editor entries → JSON for the editor.
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
      await fetchServers(workspaceId)
      if (!cancelled) setLoading(false)
    })()
    return () => { cancelled = true }
  }, [workspaceId, wsLoading, fetchServers])

  // -----------------------------------------------------------------------
  // Save — diff editor state against snapshot, call API for each change
  // -----------------------------------------------------------------------

  async function handleSave() {
    if (!workspaceId || saving) return

    const currentEntries = parseConfig(editorJson)
    // Re-attach IDs by matching server name to snapshot.
    const withIds = reconcileIds(snapshotRef.current, currentEntries)
    const diff = diffEntries(snapshotRef.current, withIds)

    if (diff.create.length === 0 && diff.update.length === 0 && diff.remove.length === 0) {
      toast.info("No changes to save")
      return
    }

    setSaving(true)
    try {
      const errors: string[] = []

      // Determine the crew_id to use for new entries.
      // If there are existing servers, use the first one's crew_id.
      // Otherwise we need a crew — for now, use the first crew available.
      let defaultCrewId = crewServers[0]?.crew_id
      if (!defaultCrewId) {
        // Fetch first crew in workspace
        const crewRes = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
        if (crewRes.ok) {
          const crews = await crewRes.json()
          if (Array.isArray(crews) && crews.length > 0) {
            defaultCrewId = crews[0].id
          }
        }
      }

      // CREATE new entries
      for (const entry of diff.create) {
        if (!entry.name.trim()) continue
        const crewId = defaultCrewId
        if (!crewId) {
          errors.push(`No crew available for "${entry.name}"`)
          continue
        }
        const payload = entryToPayload(entry)
        const res = await fetch(
          `/api/v1/crews/${crewId}/integrations?workspace_id=${workspaceId}`,
          { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) },
        )
        if (!res.ok) {
          const d = await res.json().catch(() => null)
          errors.push(d?.error ?? `Failed to create "${entry.name}"`)
        }
      }

      // UPDATE changed entries
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

      // DELETE removed entries
      for (const id of diff.remove) {
        const original = crewServers.find((s) => s.id === id)
        const crewId = original?.crew_id ?? defaultCrewId
        if (!crewId) continue
        const res = await fetch(
          `/api/v1/crews/${crewId}/integrations/${id}?workspace_id=${workspaceId}`,
          { method: "DELETE" },
        )
        if (!res.ok) errors.push(`Failed to delete integration`)
      }

      if (errors.length > 0) {
        toast.error(errors[0])
      } else {
        toast.success("Integrations saved")
      }

      // Refetch to sync state.
      await fetchServers(workspaceId)
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
          <Skeleton className="h-10 w-full" />
        </div>
      </div>
    )
  }

  const hasChanges = editorJson !== serializeConfig(snapshotRef.current)

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Integrations" description="Manage MCP server connections for your workspace">
        {canManage && hasChanges && (
          <Button onClick={handleSave} disabled={saving}>
            {saving ? "Saving..." : "Save Changes"}
          </Button>
        )}
      </PageHeader>

      <MCPConfigEditor
        value={editorJson}
        onChange={setEditorJson}
        readOnly={!canManage}
        workspaceId={workspaceId ?? undefined}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Re-attach database IDs to entries by matching on server name (unique per crew). */
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
