"use client"

import { useEffect, useState } from "react"
import { RotateCcw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { toast } from "sonner"
import { RoutineListSkeleton } from "./routine-skeletons"

// RoutineVersionsTab — immutable version history with rollback.
// Mirrors the Versions tab in the existing pipeline-detail-sheet but
// embedded in the routines page detail panel, so users can navigate
// version history while keeping the rest of the page state.

interface PipelineVersion {
  version: number
  parent_version: number | null
  definition_hash: string
  author_type: string
  author_id: string
  change_summary: string
  created_at: string
}

interface Props {
  workspaceId: string
  slug: string
  onRolledBack: () => void
}

export function RoutineVersionsTab({ workspaceId, slug, onRolledBack }: Props) {
  const [versions, setVersions] = useState<PipelineVersion[]>([])
  const [loading, setLoading] = useState(true)
  const [rollingBack, setRollingBack] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)

  const fetchVersions = async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await fetch(`/api/v1/workspaces/${workspaceId}/pipelines/${slug}/versions`)
      if (!res.ok) throw new Error(`fetch versions: ${res.status}`)
      const data: PipelineVersion[] = await res.json()
      setVersions(Array.isArray(data) ? data : [])
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    fetchVersions()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId, slug])

  const rollback = async (target: number) => {
    if (!confirm(`Rollback to v${target}? Current head will become a new version pointing back at v${target}'s definition.`))
      return
    setRollingBack(target)
    try {
      const res = await fetch(`/api/v1/workspaces/${workspaceId}/pipelines/${slug}/rollback`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ target_version: target }),
      })
      if (!res.ok) {
        const t = await res.text().catch(() => "")
        throw new Error(`${res.status}: ${t || res.statusText}`)
      }
      toast.success(`Rolled back to v${target}`)
      onRolledBack()
      fetchVersions()
    } catch (e) {
      toast.error("Rollback failed", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setRollingBack(null)
    }
  }

  if (loading) return <RoutineListSkeleton rows={3} />
  if (error) return <div className="py-4 text-xs text-red-400">Error: {error}</div>
  if (versions.length === 0)
    return (
      <div className="rounded-md border border-dashed border-border/60 p-6 text-center text-xs text-muted-foreground">
        No version history yet. The first save creates v1; subsequent edits append.
      </div>
    )

  const headVersion = versions[0]?.version

  return (
    <ol className="space-y-2">
      {versions.map((v) => (
        <li
          key={v.version}
          className="rounded-md border border-white/[0.06] bg-card/40 p-2.5 text-[11px]"
        >
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <Badge variant="outline" className="font-mono text-[10px]">
                v{v.version}
              </Badge>
              {v.version === headVersion && (
                <Badge className="bg-blue-500/15 text-blue-300 text-[9px]">HEAD</Badge>
              )}
              <span className="font-mono text-[10px] text-muted-foreground">
                {v.author_type}/{v.author_id.slice(0, 12)}
              </span>
            </div>
            {v.version !== headVersion && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => rollback(v.version)}
                disabled={rollingBack !== null}
                className="h-6 gap-1 px-2 text-[10px]"
              >
                <RotateCcw className="h-2.5 w-2.5" />
                {rollingBack === v.version ? "Rolling back…" : "Rollback"}
              </Button>
            )}
          </div>
          {v.change_summary && (
            <p className="mt-1.5">{v.change_summary}</p>
          )}
          <div className="mt-1 flex items-center gap-1.5 text-[9px] text-muted-foreground">
            <span>{new Date(v.created_at).toLocaleString()}</span>
            <span>·</span>
            <span className="font-mono">{v.definition_hash.slice(0, 12)}…</span>
            {v.parent_version && (
              <>
                <span>·</span>
                <span>parent v{v.parent_version}</span>
              </>
            )}
          </div>
        </li>
      ))}
    </ol>
  )
}
