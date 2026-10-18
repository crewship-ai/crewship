"use client"

// The project page (Harbor 2): a hero that says what the project is, then
// tabs — Overview, Tree, Issues, Files, Settings.
//
// Hidden until the backend has them (each would be a control that does
// nothing): Updates and Write update, Budget and Spend, a second lead next
// to the first (a project has one lead, a person or an agent), Archive, and
// the Outputs and Artifacts sources under Files (#3464).

import * as React from "react"
import { ChevronRight, Download, FileText, Folder, FolderKanban, MoreHorizontal, Plus, Trash2, Upload, Users } from "lucide-react"

import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { formatShortDate, relTime } from "@/lib/time"
import { DetailCard } from "@/components/ui/detail"
import { StatusPill } from "@/components/ui/status-pill"
import { TabBar } from "@/components/ui/tab-bar"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { StatusIcon } from "@/components/features/issues/status-icon"
import { ProjectStatusIcon } from "@/components/features/issues/project-status-icon"
import { IssueRow } from "@/components/features/issues/issue-row"
import { STATUS_TEXT } from "@/components/features/issues/issues-view-model"
import {
  ProjectDatesPicker,
  ProjectHealthPicker,
  ProjectIconEditor,
  LeadAvatar,
  ProjectLeadPicker,
  ProjectNameEditor,
  ProjectPriorityPicker,
  ProjectStatusPicker,
  type ProjectCardEdit,
} from "@/components/features/issues/project-card-editors"
import { getCrewIconDef, iconColorProps } from "@/lib/entities"
import { ISSUE_STATUS_COLORS, CREW_COLOR_DEFAULT } from "@/lib/colors"
import { projectHealthLabel, projectHealthTone, projectStatusLabel } from "@/lib/issue-facts"
import type { ProjectTab } from "@/hooks/use-project-detail"
import type { Milestone, Mission, Project, ProjectStats } from "@/lib/types/mission"

interface Props {
  project: Project
  stats: ProjectStats | null
  /** The project's issues, already filtered by the caller. */
  issues: Mission[]
  /** Rendered after the hero's pills — host actions that belong to the project. */
  actions?: React.ReactNode
  /** Makes the page writable. Absent, every property renders as text. */
  edit?: ProjectCardEdit
  tab?: ProjectTab
  onTabChange?: (tab: ProjectTab) => void
  /** Needed by Files and Tree, which read their own endpoints. */
  workspaceId?: string
  /** Files: Upload and Retire (POST / DELETE …/projects/{id}/files). */
  canWriteFiles?: boolean
  /** A row or tree node was picked. Absent, rows link to /issues/<id>. */
  onOpenIssue?: (issue: Mission) => void
  onNewIssue?: () => void
  /** The milestones card (editable), owned by the host. */
  milestones?: React.ReactNode
  /** Settings › Access: the share control. */
  share?: React.ReactNode
  /** Settings › Danger zone. Absent, the zone is not shown. */
  onRequestDelete?: () => void
}

export function ProjectCardDetail({
  project,
  stats,
  issues,
  actions,
  edit,
  tab = "overview",
  onTabChange,
  workspaceId,
  canWriteFiles = false,
  onOpenIssue,
  onNewIssue,
  milestones,
  share,
  onRequestDelete,
}: Props) {
  const open = React.useCallback(
    (i: Mission) => {
      if (onOpenIssue) onOpenIssue(i)
      else if (typeof window !== "undefined") window.location.assign(`/issues/${encodeURIComponent(i.identifier ?? i.id)}`)
    },
    [onOpenIssue],
  )

  return (
    <div className="mx-auto flex w-full max-w-[1100px] flex-col gap-4 px-4 py-5 sm:px-6">
      <ProjectHero project={project} stats={stats} edit={edit} actions={actions} />

      <TabBar
        value={tab}
        onValueChange={(v) => onTabChange?.(v as ProjectTab)}
        ariaLabel="Project"
        layoutId={`project-tabs-${project.id}`}
        className="overflow-x-auto [scrollbar-width:none]"
      >
        <TabBar.Item value="overview">Overview</TabBar.Item>
        <TabBar.Item value="tree">Tree</TabBar.Item>
        <TabBar.Item value="issues" count={issues.length}>Issues</TabBar.Item>
        <TabBar.Item value="files">Files</TabBar.Item>
        <TabBar.Item value="settings">Settings</TabBar.Item>
      </TabBar>

      <div role="tabpanel" aria-label={tabLabel(tab)}>
        {tab === "overview" && (
          <ProjectOverview project={project} stats={stats} issues={issues} milestones={milestones} />
        )}
        {tab === "tree" && workspaceId && <ProjectTree workspaceId={workspaceId} project={project} issues={issues} onOpen={open} />}
        {tab === "issues" && <ProjectIssues issues={issues} onOpen={open} onNewIssue={onNewIssue} />}
        {tab === "files" && workspaceId && <ProjectFiles workspaceId={workspaceId} projectId={project.id} canWrite={canWriteFiles} />}
        {tab === "settings" && <ProjectSettings project={project} edit={edit} share={share} onRequestDelete={onRequestDelete} />}
      </div>
    </div>
  )
}

function tabLabel(tab: ProjectTab): string {
  return { overview: "Overview", tree: "Tree", issues: "Issues", files: "Files", settings: "Settings" }[tab]
}

/* ------------------------------------------------------------------ *
 *  Hero                                                               *
 * ------------------------------------------------------------------ */

function ProjectHero({
  project,
  stats,
  edit,
  actions,
}: {
  project: Project
  stats: ProjectStats | null
  edit?: ProjectCardEdit
  actions?: React.ReactNode
}) {
  const Icon = getCrewIconDef(project.icon || "folder").icon
  const crews = stats?.crews ?? []
  return (
    <header className="flex items-start gap-4" data-slot="project-hero">
      {edit ? (
        <div className="shrink-0">
          <ProjectIconEditor project={project} edit={edit} />
        </div>
      ) : (
        <span className="grid size-[52px] shrink-0 place-items-center rounded-card border border-border bg-surface-raised">
          <Icon className={cn("size-6", iconColorProps(project.color).className)} style={iconColorProps(project.color).style} />
        </span>
      )}
      <div className="min-w-0 flex-1">
        <div className="text-label font-semibold text-muted-foreground">Project · {dateRange(project)}</div>
        {edit ? (
          <ProjectNameEditor name={project.name} onSave={(next) => void edit.patch({ name: next })} />
        ) : (
          <h1 className="mt-0.5 truncate text-display font-bold tracking-tight">{project.name}</h1>
        )}
        {project.description && <p className="mb-2.5 mt-1 max-w-[64ch] text-body text-muted-foreground">{project.description}</p>}
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <StatusPill
            size="md"
            status={project.status}
            icon={<ProjectStatusIcon status={project.status} className="size-3" />}
            label={projectStatusLabel(project.status)}
          />
          <StatusPill size="md" tone={projectHealthTone(project.health)} label={projectHealthLabel(project.health)} />
          {project.lead_name && (
            <span data-lead-kind={project.lead_type === "user" ? "user" : "agent"} className="inline-flex items-center gap-1.5 text-label text-muted-foreground">
              <LeadAvatar type={project.lead_type} id={project.lead_id} name={project.lead_name} className="size-5" />
              <span className="font-medium text-foreground">{project.lead_name}</span>
              <span className="text-muted-foreground-soft">lead</span>
            </span>
          )}
          {crews.length > 0 && (
            <span className="inline-flex items-center gap-1.5 text-label text-muted-foreground">
              <Users className="size-3.5" aria-hidden />
              {crews.join(", ")}
            </span>
          )}
          {actions}
        </div>
      </div>
    </header>
  )
}

/** "1 Oct → 31 Oct", or "not scheduled". */
function dateRange(project: Project): string {
  if (!project.start_date && !project.target_date) return "not scheduled"
  const from = project.start_date ? formatShortDate(project.start_date) : "?"
  const to = project.target_date ? formatShortDate(project.target_date) : "?"
  return `${from} → ${to}`
}

/* ------------------------------------------------------------------ *
 *  Overview                                                           *
 * ------------------------------------------------------------------ */

const SEGMENT_ORDER = ["DONE", "REVIEW", "IN_PROGRESS", "TODO", "BACKLOG", "FAILED"] as const

function statusCounts(stats: ProjectStats | null, issues: Mission[]): Record<string, number> {
  if (stats?.by_status && Object.keys(stats.by_status).length > 0) {
    const out: Record<string, number> = {}
    for (const [k, v] of Object.entries(stats.by_status)) {
      const key = k === "COMPLETED" ? "DONE" : k === "PLANNING" ? "TODO" : k
      out[key] = (out[key] ?? 0) + v
    }
    return out
  }
  const out: Record<string, number> = {}
  for (const i of issues) out[i.status] = (out[i.status] ?? 0) + 1
  return out
}

function ProjectOverview({
  project,
  stats,
  issues,
  milestones,
}: {
  project: Project
  stats: ProjectStats | null
  issues: Mission[]
  milestones?: React.ReactNode
}) {
  const total = stats?.total_issues ?? project.issue_count
  const done = stats?.completed_issues ?? project.done_count
  const pct = total > 0 ? Math.round((done / total) * 100) : 0
  const counts = statusCounts(stats, issues)
  const segments = SEGMENT_ORDER.filter((s) => (counts[s] ?? 0) > 0)

  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-[auto_minmax(0,1fr)]" data-slot="project-kpis">
        <div className="flex items-center gap-3 rounded-card border border-border bg-card px-4 py-3.5">
          <DoneRing pct={pct} />
          <div>
            <small className="block text-label text-muted-foreground">Done</small>
            <b className="block text-heading font-semibold">{`${done} of ${total}`}</b>
          </div>
        </div>
        <div className="flex min-w-0 flex-col justify-center gap-2 rounded-card border border-border bg-card px-4 py-3.5">
          <small className="text-label text-muted-foreground">Issues by status</small>
          {segments.length === 0 ? (
            <p className="text-label text-muted-foreground-soft">No issues to measure yet.</p>
          ) : (
            <>
              <div className="flex h-2.5 gap-0.5 overflow-hidden rounded-full bg-muted" aria-hidden>
                {segments.map((s) => (
                  <i key={s} className="block h-full" style={{ flex: counts[s], background: ISSUE_STATUS_COLORS[s] || CREW_COLOR_DEFAULT }} />
                ))}
              </div>
              <ul className="flex flex-wrap gap-x-3 gap-y-1 text-label text-muted-foreground">
                {segments.map((s) => (
                  <li key={s} className="inline-flex items-center gap-1">
                    <StatusIcon status={s} className="size-3" />
                    {counts[s]} {(STATUS_TEXT[s] ?? s).toLowerCase()}
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <div className="flex min-w-0 flex-col gap-4">{milestones}</div>
        <div className="flex min-w-0 flex-col gap-4">
          <DetailCard title="Team" icon={Users}>
            <div className="flex flex-col gap-3">
              <div className="flex items-center gap-2.5">
                {project.lead_name ? (
                  <>
                    <LeadAvatar type={project.lead_type} id={project.lead_id} name={project.lead_name} className="size-7" />
                    <div>
                      <b className="block text-body font-semibold">{project.lead_name}</b>
                      <small className="text-label text-muted-foreground">Lead</small>
                    </div>
                  </>
                ) : (
                  <p className="text-label text-muted-foreground">No lead yet. Pick one in Settings.</p>
                )}
              </div>
              {(stats?.by_assignee?.length ?? 0) > 0 && (
                <ul className="flex flex-col gap-2 border-t border-border pt-3">
                  {stats!.by_assignee.map((a) => (
                    <li key={a.agent_id || a.agent_name} className="flex items-center gap-2.5">
                      <AgentAvatar seed={a.agent_id || a.agent_name} className="size-5 rounded-full" alt="" />
                      <span className="min-w-0 flex-1 truncate text-body">{a.agent_name}</span>
                      <span className="shrink-0 font-mono text-label tabular-nums text-muted-foreground">
                        {a.completed}/{a.total}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
              {(stats?.crews?.length ?? 0) === 0 && (
                <p className="text-label text-muted-foreground-soft">No crew has picked up work here yet.</p>
              )}
            </div>
          </DetailCard>
        </div>
      </div>
    </div>
  )
}

function DoneRing({ pct }: { pct: number }) {
  const c = 100.5
  return (
    <svg viewBox="0 0 40 40" className="size-14 shrink-0" role="img" aria-label={`${pct}% done`}>
      <circle cx="20" cy="20" r="16" fill="none" stroke="var(--border)" strokeWidth="4" />
      <circle
        cx="20"
        cy="20"
        r="16"
        fill="none"
        stroke="var(--success)"
        strokeWidth="4"
        strokeLinecap="round"
        strokeDasharray={`${((pct / 100) * c).toFixed(1)} ${c}`}
        transform="rotate(-90 20 20)"
      />
      <text x="20" y="24" textAnchor="middle" className="fill-foreground text-micro font-semibold">
        {pct}%
      </text>
    </svg>
  )
}

/* ------------------------------------------------------------------ *
 *  Issues                                                             *
 * ------------------------------------------------------------------ */

function ProjectIssues({ issues, onOpen, onNewIssue }: { issues: Mission[]; onOpen: (i: Mission) => void; onNewIssue?: () => void }) {
  if (issues.length === 0) {
    return (
      <div className="flex flex-col items-center gap-3 rounded-card border border-dashed border-border px-6 py-10 text-center">
        <FolderKanban className="size-6 text-muted-foreground-soft" aria-hidden />
        <b className="text-body font-semibold">No issues in this project yet</b>
        <p className="max-w-[44ch] text-label text-muted-foreground">File work under this project to see it here, on the Tree and in the progress figures.</p>
        {onNewIssue && (
          <Button size="sm" onClick={onNewIssue}>
            <Plus className="size-3.5" aria-hidden />
            New issue
          </Button>
        )}
      </div>
    )
  }
  return (
    <div className="overflow-hidden rounded-card border border-border bg-card" data-slot="project-issues">
      {issues.map((i) => (
        <IssueRow key={i.id} issue={i} onOpen={onOpen} />
      ))}
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  Tree (Outline): milestone → issue → sub-issue                      *
 * ------------------------------------------------------------------ */

function ProjectTree({
  workspaceId,
  project,
  issues,
  onOpen,
}: {
  workspaceId: string
  project: Project
  issues: Mission[]
  onOpen: (i: Mission) => void
}) {
  const [milestones, setMilestones] = React.useState<Milestone[] | null>(null)
  React.useEffect(() => {
    let cancelled = false
    setMilestones(null)
    apiFetch(`/api/v1/projects/${encodeURIComponent(project.id)}/milestones?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((b) => {
        if (!cancelled) setMilestones(Array.isArray(b) ? b : [])
      })
      .catch(() => {
        if (!cancelled) setMilestones([])
      })
    return () => {
      cancelled = true
    }
  }, [workspaceId, project.id])

  const groups = React.useMemo(() => {
    const ids = new Set(issues.map((i) => i.id))
    // Sub-issues hang off their parent when the parent is here too.
    const children = new Map<string, Mission[]>()
    const top: Mission[] = []
    for (const i of issues) {
      if (i.parent_issue_id && ids.has(i.parent_issue_id)) {
        const list = children.get(i.parent_issue_id) ?? []
        list.push(i)
        children.set(i.parent_issue_id, list)
      } else top.push(i)
    }
    const known = new Set((milestones ?? []).map((m) => m.id))
    const out: { key: string; name: string; meta?: string; issues: Mission[] }[] = (milestones ?? []).map((m) => ({
      key: m.id,
      name: m.name,
      meta: m.target_date ? formatShortDate(m.target_date) : undefined,
      issues: top.filter((i) => i.milestone_id === m.id),
    }))
    const rest = top.filter((i) => !i.milestone_id || !known.has(i.milestone_id))
    if (rest.length > 0) out.push({ key: "none", name: "No milestone", issues: rest })
    return { out, children }
  }, [issues, milestones])

  const doneOf = (list: Mission[]) => list.filter((i) => i.status === "DONE" || i.status === "COMPLETED").length

  return (
    <div className="overflow-hidden rounded-card border border-border bg-card py-1.5">
      <div className="flex items-center gap-2 px-3 py-1.5 text-body">
        <Folder className="size-3.5 text-muted-foreground" aria-hidden />
        <b className="font-semibold">{project.name}</b>
        <span className="flex-1" />
        <span className="font-mono text-label text-muted-foreground">
          {doneOf(issues)}/{issues.length}
        </span>
      </div>
      <ul role="tree" aria-label={`${project.name} outline`}>
        {milestones === null && <li className="px-8 py-2 text-label text-muted-foreground">Loading milestones…</li>}
        {groups.out.map((g) => (
          <li key={g.key} role="treeitem" aria-expanded aria-label={`${g.name}, ${doneOf(g.issues)} of ${g.issues.length} done`}>
            <div className="flex items-center gap-2 py-1.5 pl-6 pr-3 text-body">
              <ChevronRight className="size-3 rotate-90 text-muted-foreground-soft" aria-hidden />
              <b className="font-semibold">{g.name}</b>
              {g.meta && <span className="text-label text-muted-foreground">{g.meta}</span>}
              <span className="flex-1" />
              <span className="font-mono text-label text-muted-foreground">
                {doneOf(g.issues)}/{g.issues.length}
              </span>
            </div>
            <ul role="group">
              {g.issues.length === 0 && <li className="py-1 pl-14 text-label text-muted-foreground-soft">No issues in this milestone.</li>}
              {g.issues.map((i) => (
                <TreeIssue key={i.id} issue={i} depth={2} subs={groups.children.get(i.id) ?? []} onOpen={onOpen} />
              ))}
            </ul>
          </li>
        ))}
      </ul>
    </div>
  )
}

function TreeIssue({ issue, depth, subs, onOpen }: { issue: Mission; depth: 2 | 3; subs: Mission[]; onOpen: (i: Mission) => void }) {
  return (
    <li role="treeitem" aria-label={`${issue.identifier ?? ""} ${issue.title}`.trim()} aria-expanded={subs.length > 0 ? true : undefined}>
      <button
        type="button"
        onClick={() => onOpen(issue)}
        className={cn("row-hover flex w-full items-center gap-2 py-1.5 pr-3 text-left text-body", depth === 2 ? "pl-11" : "pl-16")}
      >
        <StatusIcon status={issue.status} className="size-3.5 shrink-0" />
        {issue.identifier && <span className="shrink-0 font-mono text-label text-muted-foreground-soft">{issue.identifier}</span>}
        <span className="min-w-0 truncate">{issue.title}</span>
        <span className="flex-1" />
        {issue.delegate?.name || issue.assignee_name ? (
          <span className="shrink-0 truncate text-label text-muted-foreground">{issue.delegate?.name ?? issue.assignee_name}</span>
        ) : (
          <span className="shrink-0 text-label text-muted-foreground-soft">no delegate</span>
        )}
      </button>
      {subs.length > 0 && (
        <ul role="group">
          {subs.map((s) => (
            <TreeIssue key={s.id} issue={s} depth={3} subs={[]} onOpen={onOpen} />
          ))}
        </ul>
      )}
    </li>
  )
}

/* ------------------------------------------------------------------ *
 *  Files: project files (server, versioned)                           *
 * ------------------------------------------------------------------ */

interface ProjectFileVersion {
  version_id: string
  file_id: string
  name: string
  revision: number
  size_bytes: number
  created_at: string
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

/** The server's limit per project file (internal/access MaxProjectFileBytes). */
const MAX_PROJECT_FILE_BYTES = 1024 * 1024

function readBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => {
      const url = String(r.result ?? "")
      resolve(url.slice(url.indexOf(",") + 1))
    }
    r.onerror = () => reject(r.error ?? new Error("The file could not be read"))
    r.readAsDataURL(file)
  })
}

function ProjectFiles({ workspaceId, projectId, canWrite = false }: { workspaceId: string; projectId: string; canWrite?: boolean }) {
  const [state, setState] = React.useState<{ files: ProjectFileVersion[] | null; error: string | null }>({ files: null, error: null })
  const [reload, setReload] = React.useState(0)
  const [writeError, setWriteError] = React.useState<string | null>(null)
  const [uploading, setUploading] = React.useState(false)
  const [retiring, setRetiring] = React.useState<ProjectFileVersion | null>(null)
  const fileInput = React.useRef<HTMLInputElement>(null)
  const inputId = React.useId()
  const base = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/projects/${encodeURIComponent(projectId)}/files`

  // A name already in the list is that file's next revision; anything else
  // is a new file at revision 0. The server refuses a stale revision (409).
  const upload = async (file: File) => {
    setWriteError(null)
    if (file.size > MAX_PROJECT_FILE_BYTES) {
      setWriteError(`${file.name} is larger than 1 MB, the limit for a project file. Bigger outputs belong in Artifacts.`)
      return
    }
    if (file.name.startsWith(".")) {
      setWriteError("A project file name can't start with a dot.")
      return
    }
    const existing = state.files?.find((f) => f.name === file.name)
    setUploading(true)
    try {
      const content = await readBase64(file)
      const body = existing
        ? { file_id: existing.file_id, name: file.name, expected_revision: existing.revision, content_base64: content }
        : { name: file.name, expected_revision: 0, content_base64: content }
      const res = await apiFetch(base, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
      if (!res.ok) {
        setWriteError(
          res.status === 409
            ? `${file.name} was not saved: it changed since you loaded it, or the project is full (256 files). Reload and try again.`
            : `${file.name} was not saved (HTTP ${res.status}).`,
        )
        return
      }
      setReload((n) => n + 1)
    } catch (e) {
      setWriteError(e instanceof Error ? e.message : "Could not reach the server")
    } finally {
      setUploading(false)
      if (fileInput.current) fileInput.current.value = ""
    }
  }

  const retire = async (f: ProjectFileVersion) => {
    setWriteError(null)
    const res = await apiFetch(`${base}/${encodeURIComponent(f.file_id)}`, {
      method: "DELETE",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ expected_revision: f.revision }),
    })
    if (!res.ok) {
      setWriteError(
        res.status === 409 ? `${f.name} changed since you loaded it. Reload and try again.` : `${f.name} was not retired (HTTP ${res.status}).`,
      )
      return
    }
    setReload((n) => n + 1)
  }

  React.useEffect(() => {
    let cancelled = false
    setState((s) => (s.files ? s : { files: null, error: null }))
    apiFetch(base)
      .then(async (r) => {
        if (!r.ok) throw new Error(`Project files could not load (HTTP ${r.status})`)
        const b = (await r.json()) as { files?: ProjectFileVersion[] }
        if (!cancelled) setState({ files: b.files ?? [], error: null })
      })
      .catch((e: unknown) => {
        if (!cancelled) setState({ files: null, error: e instanceof Error ? e.message : "Project files could not load" })
      })
    return () => {
      cancelled = true
    }
  }, [base, reload])

  if (state.error) {
    return (
      <div role="alert" className="flex items-center gap-3 rounded-card border border-destructive/30 bg-destructive/10 px-3 py-2 text-label text-destructive">
        <span className="flex-1">{state.error}</span>
        <Button size="xs" variant="ghost" onClick={() => setReload((n) => n + 1)}>Retry</Button>
      </div>
    )
  }
  if (state.files === null) return <p className="px-1 py-3 text-label text-muted-foreground">Loading project files…</p>
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <p className="min-w-0 flex-1 text-label text-muted-foreground">
          Project files live on the server and are versioned; every run reads the exact revision it started with. Max 1 MB per file.
        </p>
        {canWrite && (
          <>
            <label htmlFor={inputId} className="sr-only">Choose a file to upload</label>
            <input
              id={inputId}
              ref={fileInput}
              type="file"
              className="sr-only"
              tabIndex={-1}
              onChange={(e) => {
                const f = e.target.files?.[0]
                if (f) void upload(f)
              }}
            />
            <Button size="sm" variant="outline" disabled={uploading} onClick={() => fileInput.current?.click()} className="gap-1.5">
              <Upload className="size-3.5" aria-hidden />
              {uploading ? "Uploading…" : "Upload file"}
            </Button>
          </>
        )}
      </div>
      {writeError && (
        <div role="alert" className="flex items-center gap-3 rounded-card border border-destructive/30 bg-destructive/10 px-3 py-2 text-label text-destructive">
          <span className="flex-1">{writeError}</span>
          <Button size="xs" variant="ghost" onClick={() => { setWriteError(null); setReload((n) => n + 1) }}>Reload</Button>
        </div>
      )}
      {state.files.length === 0 ? (
        <div className="rounded-card border border-dashed border-border px-6 py-8 text-center text-label text-muted-foreground">
          No project files yet.
        </div>
      ) : (
        <div className="overflow-hidden rounded-card border border-border bg-card">
          <table className="w-full text-body">
            <thead className="border-b border-border text-left text-label text-muted-foreground">
              <tr>
                <th className="px-4 py-2 font-medium">File</th>
                <th className="px-3 py-2 font-medium">Revision</th>
                <th className="px-3 py-2 font-medium max-sm:hidden">Size</th>
                <th className="px-3 py-2 font-medium max-sm:hidden">Saved</th>
                <th className="w-10 px-2 py-2"><span className="sr-only">Download</span></th>
              </tr>
            </thead>
            <tbody>
              {state.files.map((f) => (
                <tr key={f.version_id} className="border-b border-border last:border-0">
                  <td className="px-4 py-2">
                    <span className="inline-flex min-w-0 items-center gap-2">
                      <FileText className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                      <b className="truncate font-medium">{f.name}</b>
                    </span>
                  </td>
                  <td className="px-3 py-2 font-mono text-label">{`r${f.revision}`}</td>
                  <td className="px-3 py-2 font-mono text-label text-muted-foreground max-sm:hidden">{formatBytes(f.size_bytes)}</td>
                  <td className="px-3 py-2 text-label text-muted-foreground max-sm:hidden">{relTime(f.created_at)}</td>
                  <td className="px-2 py-1">
                    <div className="flex items-center justify-end gap-0.5">
                      <Button asChild variant="ghost" size="icon-xs">
                        <a href={`${base}/${encodeURIComponent(f.version_id)}/download`} aria-label={`Download ${f.name}`} download>
                          <Download className="size-3.5" aria-hidden />
                        </a>
                      </Button>
                      {canWrite && (
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon-xs" aria-label={`${f.name} options`}>
                              <MoreHorizontal className="size-3.5" aria-hidden />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end" className="w-44">
                            <DropdownMenuItem variant="destructive" onSelect={() => setRetiring(f)}>
                              <Trash2 className="size-3.5" aria-hidden /> Retire file…
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {retiring && (
        <ConfirmDialog
          open
          onOpenChange={(o) => { if (!o) setRetiring(null) }}
          title={`Retire ${retiring.name}?`}
          description="New runs no longer see it. Runs that already started keep the revision they read."
          destructive
          confirmLabel="Retire file"
          onConfirm={async () => {
            const f = retiring
            setRetiring(null)
            await retire(f)
          }}
        />
      )}
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  Settings                                                           *
 * ------------------------------------------------------------------ */

function SettingsRow({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5 border-t border-border px-4 py-3 first:border-t-0">
      <div className="min-w-[10rem] flex-1">
        <b className="block text-body font-medium">{label}</b>
        {hint && <small className="text-label text-muted-foreground">{hint}</small>}
      </div>
      <div className="flex min-w-0 items-center text-body">{children}</div>
    </div>
  )
}

function ProjectSettings({
  project,
  edit,
  share,
  onRequestDelete,
}: {
  project: Project
  edit?: ProjectCardEdit
  share?: React.ReactNode
  onRequestDelete?: () => void
}) {
  return (
    <div className="flex max-w-[760px] flex-col gap-4">
      <section className="overflow-hidden rounded-card border border-border bg-card" aria-label="Project">
        <header className="flex items-center gap-3 border-b border-border px-4 py-3">
          <FolderKanban className="size-4 text-muted-foreground" aria-hidden />
          <div>
            <b className="block text-body font-semibold">Project</b>
            <small className="text-label text-muted-foreground">Name, look and plan</small>
          </div>
        </header>
        <SettingsRow label="Status">
          {edit ? <ProjectStatusPicker project={project} edit={edit} /> : projectStatusLabel(project.status)}
        </SettingsRow>
        {edit && (
          <SettingsRow label="Priority">
            <ProjectPriorityPicker project={project} edit={edit} />
          </SettingsRow>
        )}
        <SettingsRow label="Health">
          {edit ? <ProjectHealthPicker project={project} edit={edit} /> : projectHealthLabel(project.health)}
        </SettingsRow>
        <SettingsRow label="Lead" hint="One person or agent accountable for the project">
          {edit ? <ProjectLeadPicker project={project} edit={edit} /> : (project.lead_name ?? "No lead")}
        </SettingsRow>
        <SettingsRow label="Start and target">
          {edit ? (
            <ProjectDatesPicker project={project} edit={edit}>
              <span className="truncate text-body">{dateRange(project)}</span>
            </ProjectDatesPicker>
          ) : (
            dateRange(project)
          )}
        </SettingsRow>
      </section>

      {share && (
        <section className="overflow-hidden rounded-card border border-border bg-card" aria-label="Access">
          <header className="flex items-center gap-3 px-4 py-3">
            <Users className="size-4 text-muted-foreground" aria-hidden />
            <div className="flex-1">
              <b className="block text-body font-semibold">Access</b>
              <small className="text-label text-muted-foreground">Who outside the crews working on it can see this project</small>
            </div>
            {share}
          </header>
        </section>
      )}

      {onRequestDelete && (
        <section className="overflow-hidden rounded-card border border-destructive/30 bg-card" aria-label="Danger zone">
          <header className="flex items-center gap-3 border-b border-border px-4 py-3">
            <Trash2 className="size-4 text-destructive" aria-hidden />
            <div>
              <b className="block text-body font-semibold">Danger zone</b>
              <small className="text-label text-muted-foreground">Admins and owners</small>
            </div>
          </header>
          <SettingsRow label="Delete project" hint="Issues keep working without a project.">
            <Button size="sm" variant="destructive" onClick={onRequestDelete}>
              Delete…
            </Button>
          </SettingsRow>
        </section>
      )}
    </div>
  )
}
