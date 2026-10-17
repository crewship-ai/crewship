"use client"

// The project detail, wired. Mirrors issue-detail-surface: one owner of the
// fetches and the writes, one renderer that only draws.
//
// It replaces project-detail-inline.tsx, which was a 360px right rail being
// rendered at full width — 72px of label on the left, `justify-end` on the
// right, and a metre of nothing between them — under a breadcrumb, a header
// and a title that all said the project's name.

import * as React from "react"
import { ResourceShareDialog } from "@/components/features/settings/resource-share-dialog"

import { ProjectMilestonesCard } from "./project-milestones-card"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "@/lib/toast"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import type { ProjectTab } from "@/hooks/use-project-detail"
import { Skeleton } from "@/components/ui/skeleton"
import { PageSaveBar, PageSaveProvider, useInPageSave, usePageSave } from "@/components/ui/page-save-bar"
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
  /** The open tab; the host keeps it in `?tab=`. */
  tab?: ProjectTab
  onTabChange?: (tab: ProjectTab) => void
  onOpenIssue?: (issue: Mission) => void
  onNewIssue?: () => void
  /** DELETE /projects/{id} is ADMIN+; without it the Danger zone is not shown. */
  canDelete?: boolean
  /** After the server deleted the project. */
  onDeleted?: () => void
}

/**
 * The project detail edits a draft: every picker on the card records its
 * change, the page's floating Save bar counts them, and one Save sends one
 * PATCH with what changed. A status or lead picked by accident is a Discard,
 * not a write. Hosts that already give the page a save bar share it; anywhere
 * else the surface brings its own.
 */
export function ProjectDetailSurface(props: Props) {
  const inPage = useInPageSave()
  if (inPage) return <ProjectDetailDraft {...props} />
  return (
    <PageSaveProvider>
      <ProjectDetailDraft {...props} />
      <PageSaveBar className="sticky left-auto mx-auto mb-4 w-fit translate-x-0" />
    </PageSaveProvider>
  )
}

/** Keys the draft compares with the saved project; the lead is one change. */
const DRAFT_FIELDS = ["name", "status", "priority", "health", "icon", "color", "start_date", "target_date", "lead_id"] as const

function savedValue(project: Project, key: string): unknown {
  const v = (project as unknown as Record<string, unknown>)[key]
  if (key === "start_date" || key === "target_date") return typeof v === "string" ? v.split("T")[0] : ""
  if (key === "lead_id") return v ?? ""
  return v
}

function ProjectDetailDraft({
  workspaceId,
  project,
  issues,
  editable = true,
  actions,
  onChanged,
  tab = "overview",
  onTabChange,
  onOpenIssue,
  onNewIssue,
  canDelete = false,
  onDeleted,
}: Props) {
  const [confirmDelete, setConfirmDelete] = React.useState(false)
  const [stats, setStats] = React.useState<ProjectStats | null>(null)
  const [agents, setAgents] = React.useState<PickableAgent[]>([])
  const [busy, setBusy] = React.useState(false)
  // What was picked and not yet saved, keyed by the PATCH field.
  const [draft, setDraft] = React.useState<Record<string, unknown>>({})
  // What a Save just wrote, shown until the host's refreshed project lands.
  const [applied, setApplied] = React.useState<Record<string, unknown>>({})
  React.useEffect(() => { setDraft({}) }, [project.id])
  React.useEffect(() => { setApplied({}) }, [project])

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

  // A picker records its change; one equal to what is saved drops out, so
  // picking a value and picking it back leaves nothing to save.
  const stage = React.useCallback(async (body: Record<string, unknown>): Promise<boolean> => {
    setDraft((cur) => {
      const next = { ...cur, ...body }
      for (const key of DRAFT_FIELDS) {
        if (key in next && next[key] === savedValue({ ...project, ...applied } as Project, key)) {
          delete next[key]
          if (key === "lead_id") delete next.lead_type
        }
      }
      return next
    })
    return true
  }, [project, applied])

  const save = React.useCallback(async () => {
    const body = draft
    if (Object.keys(body).length === 0) return
    setBusy(true)
    try {
      const res = await apiFetch(`/api/v1/projects/${encodeURIComponent(project.id)}?${qs}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const b = await res.json().catch(() => null)
        // Thrown, not toasted here: the page bar shows it and keeps the draft.
        throw new Error(b?.detail ?? b?.error ?? `Could not update the project (HTTP ${res.status})`)
      }
      setApplied((a) => ({ ...a, ...body }))
      setDraft({})
      await fetchStats()
      onChanged?.()
    } finally {
      setBusy(false)
    }
  }, [draft, project.id, qs, fetchStats, onChanged])

  const count = DRAFT_FIELDS.filter((k) => k in draft).length
  usePageSave({ label: `Project ${project.name}`, count, saving: busy, save, discard: () => setDraft({}) })

  // The card draws the project as it will be after Save.
  const shown = React.useMemo<Project>(() => {
    const over = { ...applied, ...draft }
    const next = { ...project, ...over } as Project
    if ("lead_id" in over) {
      const id = over.lead_id as string
      const agent = agents.find((a) => a.id === id)
      next.lead_id = id || null
      next.lead_type = id ? ("agent" as Project["lead_type"]) : null
      next.lead_name = id ? (agent?.name ?? project.lead_name) : undefined
    }
    for (const key of ["start_date", "target_date"] as const) {
      if (key in over) next[key] = (over[key] as string) || null
    }
    return next
  }, [project, draft, applied, agents])

  const edit: ProjectCardEdit | undefined = React.useMemo(
    () => (editable ? { agents, patch: stage, busy } : undefined),
    [editable, agents, stage, busy],
  )

  const projectIssues = React.useMemo(
    () => issues.filter((i) => i.project_id === project.id),
    [issues, project.id],
  )

  const deleteProject = React.useCallback(async () => {
    const res = await apiFetch(`/api/v1/projects/${encodeURIComponent(project.id)}?${qs}`, { method: "DELETE" })
    if (!res.ok) {
      const b = await res.json().catch(() => null)
      const reason = b?.detail ?? b?.error ?? `HTTP ${res.status}`
      toast.error(`${project.name} was not deleted: ${reason}`)
      // Thrown so the confirmation stays open where the person was.
      throw new Error(reason)
    }
    toast.success(`Deleted ${project.name}`)
    onDeleted?.()
    onChanged?.()
  }, [project.id, project.name, qs, onDeleted, onChanged])

  return (
    <>
      <ProjectCardDetail
        project={shown}
        stats={stats}
        issues={projectIssues}
        actions={actions}
        edit={edit}
        tab={tab}
        onTabChange={onTabChange}
        workspaceId={workspaceId}
        onOpenIssue={onOpenIssue}
        onNewIssue={onNewIssue}
        canWriteFiles={editable}
        milestones={<ProjectMilestonesCard key={project.id} projectId={project.id} workspaceId={workspaceId} editable={editable} />}
        share={<ResourceShareDialog workspaceId={workspaceId} resource={{ kind: "project", id: project.id, name: project.name }} />}
        onRequestDelete={canDelete ? () => setConfirmDelete(true) : undefined}
      />
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete ${project.name}?`}
        description="Issues keep working without a project."
        consequences={[
          { tone: "kept", text: `${projectIssues.length === 1 ? "Its 1 issue stays" : `Its ${projectIssues.length} issues stay`}, without a project.` },
          { tone: "lost", text: "Milestones, project files and the project's sharing go with it." },
        ]}
        confirmLabel="Delete project"
        destructive
        onConfirm={deleteProject}
      />
    </>
  )
}

/** The hero and the tab strip, before the project list has arrived. */
export function ProjectDetailSkeleton() {
  return (
    <div className="mx-auto flex w-full max-w-[1100px] flex-col gap-4 px-4 py-5 sm:px-6" aria-busy="true" aria-label="Loading project">
      <div className="flex items-start gap-4">
        <Skeleton className="size-[52px] shrink-0 rounded-card" />
        <div className="flex flex-1 flex-col gap-2">
          <Skeleton className="h-3 w-40" />
          <Skeleton className="h-8 w-72" />
          <Skeleton className="h-4 w-96 max-w-full" />
        </div>
      </div>
      <Skeleton className="h-8 w-full" />
      <Skeleton className="h-24 w-full rounded-card" />
    </div>
  )
}
