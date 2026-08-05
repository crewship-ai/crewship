"use client"

// The project detail, wired. Mirrors issue-detail-surface: one owner of the
// fetches and the writes, one renderer that only draws.
//
// It replaces project-detail-inline.tsx, which was a 360px right rail being
// rendered at full width — 72px of label on the left, `justify-end` on the
// right, and a metre of nothing between them — under a breadcrumb, a header
// and a title that all said the project's name.

import * as React from "react"
import { toast } from "sonner"

import { apiFetch } from "@/lib/api-fetch"
import { Skeleton } from "@/components/ui/skeleton"
import { ProjectCardDetail } from "@/components/features/issues/project-card-detail"
import type { ProjectCardEdit } from "@/components/features/issues/project-card-editors"
import type { PickableAgent } from "@/components/features/issues/issue-card-editors"
import type { Mission, Project, ProjectStats } from "@/lib/types/mission"

interface Props {
  workspaceId: string
  project: Project
  /** Every issue the host has loaded; filtered to this project here. */
  issues: Mission[]
  editable?: boolean
  /** Rendered top-right of the identity card — New issue, and so on. */
  actions?: React.ReactNode
  /** The host's project list needs to know when a write landed. */
  onChanged?: () => void
}

export function ProjectDetailSurface({
  workspaceId,
  project,
  issues,
  editable = true,
  actions,
  onChanged,
}: Props) {
  const [stats, setStats] = React.useState<ProjectStats | null>(null)
  const [agents, setAgents] = React.useState<PickableAgent[]>([])
  const [busy, setBusy] = React.useState(false)

  const qs = `workspace_id=${encodeURIComponent(workspaceId)}`

  const fetchStats = React.useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await apiFetch(`/api/v1/projects/${encodeURIComponent(project.id)}/stats?${qs}`)
      setStats(res.ok ? await res.json() : null)
    } catch {
      /* stats are supplementary — the row's own counters still render */
    }
  }, [workspaceId, project.id, qs])

  React.useEffect(() => {
    // A different project is different figures: clearing first stops the
    // previous project's donut rendering under this one's name.
    setStats(null)
    void fetchStats()
  }, [fetchStats])

  React.useEffect(() => {
    if (!workspaceId) return
    let cancelled = false
    apiFetch(`/api/v1/agents?${qs}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((a) => {
        if (cancelled) return
        const list: PickableAgent[] = Array.isArray(a) ? a : (a?.agents ?? [])
        setAgents(list.map((x) => ({ id: x.id, name: x.name, slug: x.slug })))
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [workspaceId, qs])

  const patch = React.useCallback(
    async (body: Record<string, unknown>): Promise<boolean> => {
      setBusy(true)
      try {
        const res = await apiFetch(`/api/v1/projects/${encodeURIComponent(project.id)}?${qs}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        })
        if (!res.ok) {
          const b = await res.json().catch(() => null)
          toast.error(b?.detail ?? "Failed to update project")
          return false
        }
        await fetchStats()
        onChanged?.()
        return true
      } catch {
        toast.error("Failed to update project")
        return false
      } finally {
        setBusy(false)
      }
    },
    [project.id, qs, fetchStats, onChanged],
  )

  const edit: ProjectCardEdit | undefined = React.useMemo(
    () => (editable ? { agents, patch, busy } : undefined),
    [editable, agents, patch, busy],
  )

  const projectIssues = React.useMemo(
    () => issues.filter((i) => i.project_id === project.id),
    [issues, project.id],
  )

  return (
    <ProjectCardDetail
      project={project}
      stats={stats}
      issues={projectIssues}
      actions={actions}
      edit={edit}
    />
  )
}

export function ProjectDetailSkeleton() {
  return (
    <div className="flex flex-col gap-4 p-4">
      <Skeleton className="h-[132px] w-full rounded-xl" />
      <Skeleton className="h-[64px] w-full rounded-xl" />
    </div>
  )
}
