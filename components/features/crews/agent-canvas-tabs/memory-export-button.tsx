"use client"

import * as React from "react"
import { Download } from "lucide-react"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"

interface MemoryExportButtonProps {
  /** Crew the memory belongs to. Required — the server scopes on it. */
  crewId: string
  /** Omit for the crew-shared tier. */
  agentSlug?: string
  workspaceId?: string
}

/**
 * Downloads a memory scope as an OKF bundle.
 *
 * The archive is built by the SERVER (`?format=zip`), not here. The
 * frontmatter and the manifest live in `internal/memport`, and a copy of
 * that format in TypeScript would drift the first time either changes —
 * so the browser asks for a finished bundle and stays ignorant of the
 * layout.
 *
 * Export only. Import is deliberately not offered in the UI: in the CLI
 * the plan is the default and `--apply` is a second, deliberate step,
 * and a button makes that skippable. See #1748.
 */
export function MemoryExportButton({ crewId, agentSlug, workspaceId }: MemoryExportButtonProps) {
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)

  async function download() {
    setBusy(true)
    setError(null)
    try {
      const q = new URLSearchParams({ format: "zip", crew_id: crewId })
      if (agentSlug) q.set("agent_slug", agentSlug)
      const headers: Record<string, string> = {}
      if (workspaceId) headers["X-Workspace-Id"] = workspaceId

      const res = await apiFetch(`/api/v1/memory/export?${q.toString()}`, { headers })
      if (!res.ok) {
        // An empty scope is not a failure — it is an agent that has not
        // written anything yet, and saying so beats a red error.
        setError(res.status === 404 ? "No memory to export yet." : `Export failed (${res.status}).`)
        return
      }
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement("a")
      a.href = url
      a.download = `crewship-memory-${agentSlug || crewId}.zip`
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
    } catch {
      setError("Export failed — the server could not be reached.")
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex items-center gap-2">
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={download}
        disabled={busy}
        data-testid="memory-export-button"
        title="Download this memory as a portable OKF bundle"
      >
        <Download className="h-3.5 w-3.5" />
        {busy ? "Exporting…" : "Export"}
      </Button>
      {error && (
        <span className="text-[11px] text-muted-foreground" data-testid="memory-export-error">
          {error}
        </span>
      )}
    </div>
  )
}
