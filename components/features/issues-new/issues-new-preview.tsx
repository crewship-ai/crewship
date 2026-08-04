"use client"

// /issues-new — a design preview for the issue and project detail, in the
// same spirit as /routines-new: unlisted, reachable only by typing the URL,
// and arguing the layout against the real renderer rather than a picture.
//
// It reads REAL data from the workspace — every issue and project you
// actually have — because a layout that only looks right against invented
// fixtures is a layout that has not been tested. Nothing here writes: there
// is no PATCH, no composer and no action wired up. The action slot renders
// disabled buttons so the identity card is argued at its real width.

import * as React from "react"
import { CircleDot, FolderKanban } from "lucide-react"

import { cn } from "@/lib/utils"
import { SubBar } from "@/components/layout/sub-bar"
import { Skeleton } from "@/components/ui/skeleton"
import { apiFetch } from "@/lib/api-fetch"
import { useWorkspace } from "@/hooks/use-workspace"
import { IssueCardDetail } from "./issue-card-detail"
import { ProjectCardDetail } from "./project-card-detail"
import type {
  IssueActivity,
  IssueComment,
  IssueRelation,
  Mission,
  Project,
  ProjectStats,
} from "@/lib/types/mission"

type EntityTab = "issue" | "project"

export function IssuesNewPreview() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [tab, setTab] = React.useState<EntityTab>("issue")

  const [issues, setIssues] = React.useState<Mission[]>([])
  const [projects, setProjects] = React.useState<Project[]>([])
  const [loading, setLoading] = React.useState(true)
  // Which workspace the lists above actually came from. Resetting state in
  // the effect is not enough on its own: React runs the detail effect below
  // in the same pass, and it reads `identifier` / `crewId` from the render
  // that has just happened — the OLD selection, with the NEW workspaceId
  // already in scope. That combination is a cross-workspace read. Nothing
  // downstream may fire until the lists and the workspace agree.
  const [loadedWorkspace, setLoadedWorkspace] = React.useState<string | null>(null)

  const [issueId, setIssueId] = React.useState<string | null>(null)
  const [projectId, setProjectId] = React.useState<string | null>(null)

  // Per-issue detail, fetched on selection. The list row is not enough: it
  // carries labels but not comments, activity or relations.
  const [issue, setIssue] = React.useState<Mission | null>(null)
  const [comments, setComments] = React.useState<IssueComment[]>([])
  const [activities, setActivities] = React.useState<IssueActivity[]>([])
  const [relations, setRelations] = React.useState<IssueRelation[]>([])
  const [stats, setStats] = React.useState<ProjectStats | null>(null)

  React.useEffect(() => {
    // Everything below the workspace is scoped to it. Carrying any of it
    // across a switch renders one workspace's issue under another's header,
    // and — worse — sends the old crew and identifier to the new workspace.
    // Selection goes too: `prev ?? first` keeps an id that is in no list
    // under the new workspace, and the page goes permanently blank.
    setIssues([])
    setProjects([])
    setIssue(null)
    setComments([])
    setActivities([])
    setRelations([])
    setStats(null)
    setIssueId(null)
    setProjectId(null)
    setLoadedWorkspace(null)
    setLoading(Boolean(workspaceId))

    if (!workspaceId) return
    let cancelled = false
    const qs = `workspace_id=${encodeURIComponent(workspaceId)}`
    Promise.all([
      apiFetch(`/api/v1/issues?${qs}&limit=100`).then((r) => (r.ok ? r.json() : [])),
      apiFetch(`/api/v1/projects?${qs}`).then((r) => (r.ok ? r.json() : [])),
    ])
      .then(([is, ps]: [Mission[], Project[]]) => {
        if (cancelled) return
        setIssues(Array.isArray(is) ? is : [])
        setProjects(Array.isArray(ps) ? ps : [])
        setIssueId(Array.isArray(is) && is[0] ? is[0].id : null)
        setProjectId(Array.isArray(ps) && ps[0] ? ps[0].id : null)
        setLoadedWorkspace(workspaceId)
      })
      .catch(() => {})
      .finally(() => !cancelled && setLoading(false))
    return () => {
      cancelled = true
    }
  }, [workspaceId])

  const listRow = issues.find((i) => i.id === issueId) ?? null
  const identifier = listRow?.identifier
  const crewId = listRow?.crew_id

  const ready = loadedWorkspace !== null && loadedWorkspace === workspaceId

  React.useEffect(() => {
    if (!ready || !workspaceId || !identifier || !crewId) {
      setIssue(null)
      setComments([])
      setActivities([])
      setRelations([])
      return
    }
    let cancelled = false
    const qs = `workspace_id=${encodeURIComponent(workspaceId)}`
    const base = `/api/v1/crews/${encodeURIComponent(crewId)}/issues/${encodeURIComponent(identifier)}`
    Promise.all([
      apiFetch(`/api/v1/issues/${encodeURIComponent(identifier)}?${qs}`).then((r) =>
        r.ok ? r.json() : null,
      ),
      apiFetch(`${base}/comments?${qs}`).then((r) => (r.ok ? r.json() : [])),
      apiFetch(`${base}/activity?${qs}`).then((r) => (r.ok ? r.json() : [])),
      apiFetch(`${base}/relations?${qs}`).then((r) => (r.ok ? r.json() : [])),
    ])
      .then(([full, cs, as, rs]) => {
        if (cancelled) return
        setIssue(full ?? listRow)
        setComments(Array.isArray(cs) ? cs : [])
        setActivities(Array.isArray(as) ? as : [])
        setRelations(Array.isArray(rs) ? rs : [])
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
    // listRow is the fallback only; re-running on its identity would refetch
    // every time the list is refreshed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready, workspaceId, identifier, crewId])

  React.useEffect(() => {
    if (!ready || !workspaceId || !projectId) {
      setStats(null)
      return
    }
    let cancelled = false
    apiFetch(
      `/api/v1/projects/${encodeURIComponent(projectId)}/stats?workspace_id=${encodeURIComponent(workspaceId)}`,
    )
      .then((r) => (r.ok ? r.json() : null))
      .then((s) => !cancelled && setStats(s))
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [ready, workspaceId, projectId])

  const project = projects.find((p) => p.id === projectId) ?? null
  const projectIssues = React.useMemo(
    () => (projectId ? issues.filter((i) => i.project_id === projectId) : []),
    [issues, projectId],
  )
  const issueProject = issue?.project_id
    ? (projects.find((p) => p.id === issue.project_id) ?? null)
    : null

  const busy = wsLoading || loading

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      <SubBar<EntityTab>
        icon={CircleDot}
        title="Issues"
        section="Detail proposal"
        description={
          <>
            preview · {issues.length} issues · {projects.length} projects
          </>
        }
        ariaLabel="Issue and project detail design preview"
        tabs={[
          { id: "issue", label: "Issue", icon: CircleDot },
          { id: "project", label: "Project", icon: FolderKanban },
        ]}
        activeTab={tab}
        onTabChange={setTab}
        tools={
          tab === "issue" ? (
            <Picker
              label="Issue"
              value={issueId ?? ""}
              onChange={setIssueId}
              options={issues.map((i) => ({
                value: i.id,
                label: `${i.identifier ?? i.id.slice(0, 6)} · ${i.title}`,
              }))}
            />
          ) : (
            <Picker
              label="Project"
              value={projectId ?? ""}
              onChange={setProjectId}
              options={projects.map((p) => ({ value: p.id, label: p.name }))}
            />
          )
        }
      />

      {/* One line of orientation, same as /routines-new. */}
      <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-b border-border/60 bg-card/40 px-4 py-2">
        <span className="rounded border border-primary/30 bg-primary/10 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-primary">
          Preview
        </span>
        <span className="text-[12px] text-foreground/85">
          The routine card, applied to the other two nouns: identity → figures band → two
          columns → one card at the foot.
        </span>
        <span className="text-[11px] text-muted-foreground">
          Real workspace data, read-only. Nothing on this page saves.
        </span>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {busy ? (
          <PreviewSkeleton />
        ) : tab === "issue" ? (
          issue ? (
            <IssueCardDetail
              issue={issue}
              comments={comments}
              activities={activities}
              relations={relations}
              project={issueProject}
              actions={<DisabledActions labels={["Start work", "⋯"]} />}
            />
          ) : (
            <Empty what="issue" />
          )
        ) : project ? (
          <ProjectCardDetail
            project={project}
            stats={stats}
            issues={projectIssues}
            actions={<DisabledActions labels={["New issue", "⋯"]} />}
          />
        ) : (
          <Empty what="project" />
        )}
      </div>
    </div>
  )
}

/* ------------------------------------------------------------------ */

function Picker({
  label,
  value,
  onChange,
  options,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  options: { value: string; label: string }[]
}) {
  return (
    <label className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
      <span className="hidden sm:inline">{label}</span>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-label={`Preview ${label.toLowerCase()}`}
        className="max-w-[280px] truncate rounded-md border border-border/60 bg-card px-2 py-1 text-[11px] text-foreground outline-none focus:border-primary/50"
      >
        {options.length === 0 && <option value="">none</option>}
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </label>
  )
}

/**
 * The action slot, rendered but inert. The identity card has to be argued
 * with buttons in it — their width is what pushes the title column — and a
 * preview that writes is not a preview.
 */
function DisabledActions({ labels }: { labels: string[] }) {
  return (
    <>
      {labels.map((l, i) => (
        <button
          key={l}
          type="button"
          disabled
          title="Disabled in the preview"
          className={cn(
            "inline-flex h-8 cursor-not-allowed items-center gap-1.5 rounded-lg px-3 text-[12px] font-medium opacity-60",
            i === 0
              ? "bg-primary/20 text-primary"
              : "border border-border/60 text-muted-foreground",
          )}
        >
          {l}
        </button>
      ))}
    </>
  )
}

function Empty({ what }: { what: string }) {
  return (
    <div className="grid h-full place-items-center px-6 text-center">
      <p className="max-w-sm text-[13px] text-muted-foreground">
        This workspace has no {what} to preview. Seed one and reload — the page renders real
        data, not fixtures.
      </p>
    </div>
  )
}

function PreviewSkeleton() {
  return (
    <div className="flex flex-col gap-4 p-4">
      <Skeleton className="h-[132px] w-full rounded-xl" />
      <Skeleton className="h-[64px] w-full rounded-xl" />
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3 2xl:grid-cols-4">
        <Skeleton className="h-[320px] w-full rounded-xl xl:col-span-2 2xl:col-span-3" />
        <Skeleton className="h-[320px] w-full rounded-xl" />
      </div>
    </div>
  )
}
