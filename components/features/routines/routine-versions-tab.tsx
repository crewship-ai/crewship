"use client"

import { useEffect, useState } from "react"
import { RotateCcw, GitCommit, History } from "lucide-react"
import { Button } from "@/components/ui/button"
import { toast } from "sonner"
import { RoutineListSkeleton } from "./routine-skeletons"
import { Card, EmptyState, Pill } from "./_shared"

// RoutineVersionsTab — immutable version history with rollback.
// Restyled to match the Stripe-style dashboard: Card container,
// readable typography, semantic pill for HEAD, and an action button
// per row that calls /rollback. Each version row shows v# + HEAD +
// author + change summary + timestamp + parent.

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
    if (
      !confirm(
        `Rollback to v${target}? Current head will become a new version pointing back at v${target}'s definition.`,
      )
    )
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

  if (loading) {
    return (
      <Card title="Version history" subtitle="loading…">
        <div className="p-4">
          <RoutineListSkeleton rows={3} />
        </div>
      </Card>
    )
  }

  if (error) {
    return (
      <Card title="Version history">
        <div className="px-4 py-3 text-sm text-rose-400">Error: {error}</div>
      </Card>
    )
  }

  if (versions.length === 0) {
    return (
      <Card title="Version history">
        <EmptyState
          icon={History}
          title="No version history yet"
          description="The first save creates v1; subsequent edits append. Every save is immutable — rolling back creates a new HEAD pointing at an older definition."
        />
      </Card>
    )
  }

  const headVersion = versions[0]?.version

  return (
    <Card title="Version history" subtitle={`${versions.length} total · head v${headVersion}`}>
      <ol className="divide-y divide-white/[0.04]">
        {versions.map((v) => {
          const isHead = v.version === headVersion
          return (
            <li key={v.version} className="grid grid-cols-[auto_1fr_auto] items-start gap-3 px-4 py-3">
              <div className="flex shrink-0 items-center gap-2">
                <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-muted text-muted-foreground">
                  <GitCommit className="h-4 w-4" />
                </div>
                <div>
                  <div className="font-mono text-sm font-semibold">v{v.version}</div>
                  {isHead && (
                    <Pill tone="blue" className="mt-1">
                      <span className="h-1 w-1 rounded-full bg-current" />
                      HEAD
                    </Pill>
                  )}
                </div>
              </div>
              <div className="min-w-0 space-y-1">
                {v.change_summary ? (
                  <p className="text-sm text-foreground/90">{v.change_summary}</p>
                ) : (
                  <p className="text-sm italic text-muted-foreground/60">No change summary</p>
                )}
                <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[11px] text-muted-foreground">
                  <span>{new Date(v.created_at).toLocaleString()}</span>
                  <span className="opacity-60">·</span>
                  <span className="font-mono">{v.definition_hash.slice(0, 12)}…</span>
                  {v.parent_version != null && (
                    <>
                      <span className="opacity-60">·</span>
                      <span>
                        parent <span className="font-mono">v{v.parent_version}</span>
                      </span>
                    </>
                  )}
                  <span className="opacity-60">·</span>
                  <span className="font-mono">
                    {v.author_type}/{v.author_id.slice(0, 12)}
                  </span>
                </div>
              </div>
              {!isHead && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => rollback(v.version)}
                  disabled={rollingBack !== null}
                  className="h-8 shrink-0 gap-1.5 px-3 text-xs"
                >
                  <RotateCcw className="h-3 w-3" />
                  {rollingBack === v.version ? "Rolling back…" : "Rollback"}
                </Button>
              )}
            </li>
          )
        })}
      </ol>
    </Card>
  )
}
