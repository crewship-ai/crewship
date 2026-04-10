"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useParams, useRouter } from "next/navigation"
import {
  ArrowLeft,
  Calendar,
  ChevronRight,
  Clock,
  FolderKanban,
  Link2,
  MoreHorizontal,
  Pencil,
  Plus,
  Star,
} from "lucide-react"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSession } from "@/hooks/use-auth"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { ProjectStatusIcon } from "@/components/features/issues/project-status-icon"
import { PROJECT_STATUSES, PRIORITY_OPTIONS } from "@/components/features/issues/issue-constants"
import { ProjectSidebar } from "@/components/features/orchestration/project-sidebar"
import { CrewIconPopover } from "@/components/crew-icon-popover"
import { Button } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"
import { ISSUE_STATUS_COLORS, CREW_COLOR_DEFAULT } from "@/lib/colors"
import { formatShortDate } from "@/lib/time"
import { toast } from "sonner"
import type {
  Milestone,
  Mission,
  Project,
  ProjectStats,
} from "@/lib/types/mission"

// ---------------------------------------------------------------------------
// Constants & Types — shared from @/components/features/issues
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

// ProjectStatusIcon, SectionHeader, PropertyRow — imported from shared modules

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export function ProjectDetailClient() {
  const params = useParams()
  const router = useRouter()
  const projectId = params.projectId as string
  const { workspaceId, loading: wsLoading } = useWorkspace()
  useSession()

  // Core data
  const [project, setProject] = useState<Project | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Stats
  const [stats, setStats] = useState<ProjectStats | null>(null)

  // Issues
  const [issues, setIssues] = useState<Mission[]>([])
  const [loadingIssues, setLoadingIssues] = useState(false)

  // Agents
  const [agents, setAgents] = useState<{ id: string; name: string; slug?: string }[]>([])

  // Active tab
  const [activeTab, setActiveTab] = useState<"overview" | "issues" | "activity">("overview")

  // Editing states
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState("")
  const [editingDesc, setEditingDesc] = useState(false)
  const [descDraft, setDescDraft] = useState("")

  // Sidebar section state
  const [propertiesOpen, setPropertiesOpen] = useState(true)
  const [milestonesOpen, setMilestonesOpen] = useState(false)
  const [progressOpen, setProgressOpen] = useState(true)
  const [activityOpen, setActivityOpen] = useState(true)

  // Sidebar popover state
  const [statusOpen, setStatusOpen] = useState(false)
  const [priorityOpen, setPriorityOpen] = useState(false)
  const [leadOpen, setLeadOpen] = useState(false)

  // Progress tab
  const [progressTab, setProgressTab] = useState<"assignees" | "labels">("assignees")

  // Star state (local only)
  const [starred, setStarred] = useState(false)

  // Milestones
  const [milestones, setMilestones] = useState<Milestone[]>([])
  const [addingMilestone, setAddingMilestone] = useState(false)
  const [newMilestoneName, setNewMilestoneName] = useState("")
  const [newMilestoneDate, setNewMilestoneDate] = useState("")
  const [editingMilestoneId, setEditingMilestoneId] = useState<string | null>(null)
  const [editMilestoneName, setEditMilestoneName] = useState("")

  // -----------------------------------------------------------------------
  // Fetchers
  // -----------------------------------------------------------------------

  const fetchProject = useCallback(async () => {
    if (!workspaceId) {
      setError("No workspace selected")
      setLoading(false)
      return
    }
    try {
      const res = await fetch(
        `/api/v1/projects/${encodeURIComponent(projectId)}?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (!res.ok) {
        setError(res.status === 404 ? "Project not found" : "Failed to load project")
        return
      }
      const data: Project = await res.json()
      setProject(data)
      setError(null)
    } catch {
      setError("Failed to load project")
    } finally {
      setLoading(false)
    }
  }, [workspaceId, projectId])

  const fetchStats = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(
        `/api/v1/projects/${encodeURIComponent(projectId)}/stats?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (res.ok) {
        setStats(await res.json())
      }
    } catch {
      // ignore
    }
  }, [workspaceId, projectId])

  const fetchIssues = useCallback(async () => {
    if (!workspaceId) return
    setLoadingIssues(true)
    try {
      const res = await fetch(
        `/api/v1/issues?workspace_id=${encodeURIComponent(workspaceId)}&project_id=${encodeURIComponent(projectId)}`,
      )
      if (res.ok) {
        const data = await res.json()
        setIssues(Array.isArray(data) ? data : data.issues ?? [])
      }
    } catch {
      // ignore
    } finally {
      setLoadingIssues(false)
    }
  }, [workspaceId, projectId])

  const fetchAgents = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (res.ok) {
        const data = await res.json()
        const list = Array.isArray(data) ? data : data.agents ?? []
        setAgents(
          list.map((a: { id: string; name: string; slug?: string }) => ({
            id: a.id,
            name: a.name,
            slug: a.slug,
          })),
        )
      }
    } catch {
      // ignore
    }
  }, [workspaceId])

  const fetchMilestones = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(
        `/api/v1/projects/${encodeURIComponent(projectId)}/milestones?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (res.ok) {
        const data = await res.json()
        setMilestones(Array.isArray(data) ? data : data.milestones ?? [])
      }
    } catch {
      // ignore
    }
  }, [workspaceId, projectId])

  // Initial load
  useEffect(() => {
    if (wsLoading) return
    fetchProject()
    fetchStats()
    fetchIssues()
    fetchAgents()
    fetchMilestones()
  }, [wsLoading, fetchProject, fetchStats, fetchIssues, fetchAgents, fetchMilestones])

  // Realtime refresh (debounced to avoid rapid-fire refetches)
  const realtimeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const handleRealtime = useCallback(() => {
    if (realtimeTimerRef.current) clearTimeout(realtimeTimerRef.current)
    realtimeTimerRef.current = setTimeout(() => {
      fetchProject()
      fetchStats()
      fetchIssues()
      realtimeTimerRef.current = null
    }, 500)
  }, [fetchProject, fetchStats, fetchIssues])

  useRealtimeEvent("mission.updated", handleRealtime)
  useRealtimeEvent("task.updated", handleRealtime)

  // -----------------------------------------------------------------------
  // Patch helper
  // -----------------------------------------------------------------------

  const patchProject = useCallback(
    async (fields: Record<string, unknown>) => {
      if (!project || !workspaceId) return
      try {
        const res = await fetch(
          `/api/v1/projects/${project.id}?workspace_id=${encodeURIComponent(workspaceId)}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(fields),
          },
        )
        if (res.ok) {
          toast.success("Updated")
          fetchProject()
          fetchStats()
        } else {
          toast.error("Failed to update")
        }
      } catch {
        toast.error("Network error — failed to update project")
      }
    },
    [project, workspaceId, fetchProject, fetchStats],
  )

  // -----------------------------------------------------------------------
  // Milestone handlers
  // -----------------------------------------------------------------------

  const handleAddMilestone = useCallback(async () => {
    if (!workspaceId || !newMilestoneName.trim()) return
    try {
      const res = await fetch(
        `/api/v1/projects/${encodeURIComponent(projectId)}/milestones?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            name: newMilestoneName.trim(),
            target_date: newMilestoneDate || null,
          }),
        },
      )
      if (res.ok) {
        toast.success("Milestone created")
        setNewMilestoneName("")
        setNewMilestoneDate("")
        setAddingMilestone(false)
        fetchMilestones()
      } else {
        toast.error("Failed to create milestone")
      }
    } catch {
      toast.error("Failed to create milestone")
    }
  }, [workspaceId, projectId, newMilestoneName, newMilestoneDate, fetchMilestones])

  const handleRenameMilestone = useCallback(
    async (milestoneId: string) => {
      if (!workspaceId || !editMilestoneName.trim()) return
      try {
        const res = await fetch(
          `/api/v1/projects/${encodeURIComponent(projectId)}/milestones/${milestoneId}?workspace_id=${encodeURIComponent(workspaceId)}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ name: editMilestoneName.trim() }),
          },
        )
        if (res.ok) {
          setEditingMilestoneId(null)
          fetchMilestones()
        }
      } catch {
        // silent
      }
    },
    [workspaceId, projectId, editMilestoneName, fetchMilestones],
  )

  // -----------------------------------------------------------------------
  // Donut chart data
  // -----------------------------------------------------------------------

  const donutSegments = useMemo(() => {
    if (!stats?.by_status) return []
    const entries = Object.entries(stats.by_status).filter(([, v]) => v > 0)
    const total = entries.reduce((sum, [, v]) => sum + v, 0)
    if (total === 0) return []
    return entries.map(([status, value]) => ({
      status,
      value,
      pct: (value / total) * 100,
      color: ISSUE_STATUS_COLORS[status] || CREW_COLOR_DEFAULT,
    }))
  }, [stats?.by_status])

  const donutPaths = useMemo(() => {
    if (donutSegments.length === 0) return []
    const radius = 16
    const circumference = 2 * Math.PI * radius
    let offset = 0
    return donutSegments.map((seg) => {
      const dashLen = (seg.pct / 100) * circumference
      const path = {
        ...seg,
        dasharray: `${dashLen} ${circumference - dashLen}`,
        dashoffset: -offset,
      }
      offset += dashLen
      return path
    })
  }, [donutSegments])

  // -----------------------------------------------------------------------
  // Loading / Error states
  // -----------------------------------------------------------------------

  if (wsLoading || loading) {
    return (
      <div className="flex items-center justify-center h-full">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  if (error || !project) {
    return (
      <div className="flex flex-col items-center justify-center h-full gap-3">
        <FolderKanban className="h-10 w-10 text-muted-foreground/30" />
        <p className="text-body text-muted-foreground">{error || "Project not found"}</p>
        <Button variant="ghost" size="sm" onClick={() => router.push("/orchestration")}>
          <ArrowLeft className="h-3.5 w-3.5 mr-1.5" />
          Back to Orchestration
        </Button>
      </div>
    )
  }

  // -----------------------------------------------------------------------
  // Render
  // -----------------------------------------------------------------------

  return (
    <div className="flex flex-col h-full bg-background">
      {/* ================================================================== */}
      {/* Top header bar                                                     */}
      {/* ================================================================== */}
      <div className="flex items-center gap-2 px-4 py-2.5 border-b border-white/[0.08] shrink-0 bg-card/50">
        {/* Back */}
        <button
          onClick={() => router.push("/orchestration")}
          className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
        >
          <ArrowLeft className="h-4 w-4" />
        </button>

        {/* Breadcrumb */}
        <div className="flex items-center gap-1.5 text-body text-muted-foreground min-w-0">
          <span className="hover:text-foreground cursor-pointer transition-colors" onClick={() => router.push("/orchestration")}>
            Projects
          </span>
          <ChevronRight className="h-3 w-3 shrink-0" />
          <span className="text-foreground/90 truncate font-medium">{project.name}</span>
        </div>

        <div className="flex-1" />

        {/* Star */}
        <button
          onClick={() => setStarred((v) => !v)}
          className={cn(
            "p-1.5 rounded hover:bg-accent transition-colors",
            starred ? "text-primary" : "text-muted-foreground hover:text-foreground",
          )}
        >
          <Star className={cn("h-3.5 w-3.5", starred && "fill-current")} />
        </button>

        {/* More */}
        <button className="p-1.5 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors">
          <MoreHorizontal className="h-3.5 w-3.5" />
        </button>

        {/* Share */}
        <button
          onClick={() => {
            navigator.clipboard.writeText(window.location.href)
            toast.success("URL copied")
          }}
          className="p-1.5 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
        >
          <Link2 className="h-3.5 w-3.5" />
        </button>
      </div>

      {/* ================================================================== */}
      {/* Tabs                                                               */}
      {/* ================================================================== */}
      <div className="flex items-center gap-0 px-4 border-b border-white/[0.08] shrink-0 bg-card/30">
        {(["overview", "issues", "activity"] as const).map((tab) => (
          <button
            key={tab}
            onClick={() => setActiveTab(tab)}
            className={cn(
              "text-label px-3 py-2 border-b-2 transition-colors capitalize",
              activeTab === tab
                ? "border-primary text-foreground font-medium"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {tab}
          </button>
        ))}
      </div>

      {/* ================================================================== */}
      {/* Body: main content + sidebar                                       */}
      {/* ================================================================== */}
      <div className="flex flex-1 min-h-0 overflow-hidden">
        {/* ---- Main content area ---- */}
        <div className="flex-1 overflow-y-auto min-w-0">
          {activeTab === "overview" && (
            <OverviewTab
              project={project}
              stats={stats}
              editingTitle={editingTitle}
              titleDraft={titleDraft}
              editingDesc={editingDesc}
              descDraft={descDraft}
              patchProject={patchProject}
              onEditTitle={() => { setTitleDraft(project.name); setEditingTitle(true) }}
              onTitleChange={setTitleDraft}
              onTitleSave={() => {
                if (titleDraft.trim() && titleDraft !== project.name) patchProject({ name: titleDraft.trim() })
                setEditingTitle(false)
              }}
              onTitleCancel={() => { setTitleDraft(project.name); setEditingTitle(false) }}
              onEditDesc={() => { setDescDraft(project.description || ""); setEditingDesc(true) }}
              onDescChange={setDescDraft}
              onDescSave={() => {
                patchProject({ description: descDraft.trim() || null })
                setEditingDesc(false)
              }}
              onDescCancel={() => { setDescDraft(project.description || ""); setEditingDesc(false) }}
              milestones={milestones}
            />
          )}

          {activeTab === "issues" && (
            <IssuesTab
              issues={issues}
              loading={loadingIssues}
              router={router}
            />
          )}

          {activeTab === "activity" && (
            <div className="p-6">
              <div className="flex flex-col items-center justify-center py-16 text-muted-foreground">
                <Clock className="h-8 w-8 mb-3" />
                <p className="text-body">Activity feed coming soon</p>
                <p className="text-label text-muted-foreground/60 mt-1">Project activity and updates will appear here</p>
              </div>
            </div>
          )}
        </div>

        {/* ---- Sidebar ---- */}
        <div className="hidden lg:block w-[360px] border-l border-white/[0.08] overflow-y-auto bg-card/30 shrink-0">
          <ProjectSidebar
            project={project}
            stats={stats}
            agents={agents}
            donutPaths={donutPaths}
            donutSegments={donutSegments}
            progressTab={progressTab}
            setProgressTab={setProgressTab}
            propertiesOpen={propertiesOpen}
            setPropertiesOpen={setPropertiesOpen}
            milestonesOpen={milestonesOpen}
            setMilestonesOpen={setMilestonesOpen}
            progressOpen={progressOpen}
            setProgressOpen={setProgressOpen}
            activityOpen={activityOpen}
            setActivityOpen={setActivityOpen}
            statusOpen={statusOpen}
            setStatusOpen={setStatusOpen}
            priorityOpen={priorityOpen}
            setPriorityOpen={setPriorityOpen}
            leadOpen={leadOpen}
            setLeadOpen={setLeadOpen}
            patchProject={patchProject}
            milestones={milestones}
            addingMilestone={addingMilestone}
            setAddingMilestone={setAddingMilestone}
            newMilestoneName={newMilestoneName}
            setNewMilestoneName={setNewMilestoneName}
            newMilestoneDate={newMilestoneDate}
            setNewMilestoneDate={setNewMilestoneDate}
            handleAddMilestone={handleAddMilestone}
            editingMilestoneId={editingMilestoneId}
            setEditingMilestoneId={setEditingMilestoneId}
            editMilestoneName={editMilestoneName}
            setEditMilestoneName={setEditMilestoneName}
            handleRenameMilestone={handleRenameMilestone}
          />
        </div>
      </div>
    </div>
  )
}

// ===========================================================================
// Overview tab
// ===========================================================================

function OverviewTab({
  project,
  stats,
  editingTitle,
  titleDraft,
  editingDesc,
  descDraft,
  patchProject,
  onEditTitle,
  onTitleChange,
  onTitleSave,
  onTitleCancel,
  onEditDesc,
  onDescChange,
  onDescSave,
  onDescCancel,
  milestones,
}: {
  project: Project
  stats: ProjectStats | null
  editingTitle: boolean
  titleDraft: string
  editingDesc: boolean
  descDraft: string
  patchProject: (fields: Record<string, unknown>) => Promise<void>
  onEditTitle: () => void
  onTitleChange: (v: string) => void
  onTitleSave: () => void
  onTitleCancel: () => void
  onEditDesc: () => void
  onDescChange: (v: string) => void
  onDescSave: () => void
  onDescCancel: () => void
  milestones: Milestone[]
}) {
  const statusInfo = PROJECT_STATUSES.find((s) => s.value === project.status)
  const priorityInfo = PRIORITY_OPTIONS.find((p) => p.value === project.priority)

  return (
    <div className="max-w-3xl mx-auto px-6 py-6 space-y-6">
      {/* Icon + Title */}
      <div className="flex items-start gap-4">
        <CrewIconPopover
          icon={project.icon || "folder"}
          color={project.color || "blue"}
          onIconChange={(icon) => patchProject({ icon })}
          onColorChange={(color) => patchProject({ color })}
        />
        <div className="flex-1 min-w-0">
          {editingTitle ? (
            <input
              className="text-display font-bold text-foreground bg-transparent border-b-2 border-primary outline-none w-full pb-1"
              value={titleDraft}
              onChange={(e) => onTitleChange(e.target.value)}
              onBlur={onTitleSave}
              onKeyDown={(e) => {
                if (e.key === "Enter") (e.target as HTMLInputElement).blur()
                if (e.key === "Escape") onTitleCancel()
              }}
              autoFocus
            />
          ) : (
            <h1
              className="text-display font-bold text-foreground cursor-pointer hover:text-foreground/80 transition-colors"
              onClick={onEditTitle}
            >
              {project.name}
            </h1>
          )}

          {/* Summary placeholder */}
          {!editingDesc && !project.description && (
            <button
              onClick={onEditDesc}
              className="text-body text-muted-foreground hover:text-foreground transition-colors mt-1"
            >
              Add a short summary...
            </button>
          )}
        </div>
      </div>

      {/* Properties bar */}
      <div className="flex items-center gap-3 flex-wrap">
        <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-surface-subtle border border-white/[0.08]">
          <ProjectStatusIcon status={project.status} className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-label text-foreground/80">{statusInfo?.label || project.status}</span>
        </div>
        <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-surface-subtle border border-white/[0.08]">
          <PriorityIcon priority={project.priority || "none"} className="h-3.5 w-3.5" />
          <span className="text-label text-foreground/80">{priorityInfo?.label || "No priority"}</span>
        </div>
        {project.lead_name && (
          <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-surface-subtle border border-white/[0.08]">
            {project.lead_id && (
              <img src={getAgentAvatarUrl(project.lead_id)} alt="" className="h-4 w-4 rounded-full" />
            )}
            <span className="text-label text-foreground/80">{project.lead_name}</span>
          </div>
        )}
        {project.target_date && (
          <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-surface-subtle border border-white/[0.08]">
            <Calendar className="h-3 w-3 text-muted-foreground" />
            <span className="text-label text-foreground/80">{formatShortDate(project.target_date)}</span>
          </div>
        )}
        {stats?.crews && stats.crews.length > 0 && stats.crews.map((crew) => (
          <div
            key={crew}
            className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-surface-subtle border border-white/[0.08]"
          >
            <span className="text-label text-foreground/80">{crew}</span>
          </div>
        ))}
      </div>

      <Separator className="bg-white/[0.08]" />

      {/* Resources placeholder */}
      <div>
        <h3 className="text-label font-semibold text-muted-foreground uppercase tracking-wider mb-2">Resources</h3>
        <button className="flex items-center gap-2 text-label text-muted-foreground hover:text-foreground transition-colors py-1.5">
          <Plus className="h-3.5 w-3.5" />
          Add document or link...
        </button>
      </div>

      <Separator className="bg-white/[0.08]" />

      {/* Project update placeholder */}
      <button className="w-full flex items-center justify-center gap-2 py-3 px-4 rounded-lg border border-dashed border-white/[0.12] hover:border-white/[0.2] text-muted-foreground hover:text-foreground transition-colors">
        <Pencil className="h-3.5 w-3.5" />
        <span className="text-body">Write first project update</span>
      </button>

      <Separator className="bg-white/[0.08]" />

      {/* Description */}
      <div>
        <h3 className="text-label font-semibold text-muted-foreground uppercase tracking-wider mb-3">Description</h3>
        {editingDesc ? (
          <div className="space-y-2">
            <textarea
              className="w-full min-h-[120px] bg-surface-subtle border border-white/[0.12] rounded-md px-3 py-2 text-body text-foreground placeholder:text-muted-foreground outline-none focus:border-primary/50 resize-y"
              value={descDraft}
              onChange={(e) => onDescChange(e.target.value)}
              placeholder="Describe the project..."
              autoFocus
            />
            <div className="flex items-center gap-2">
              <Button size="sm" variant="default" onClick={onDescSave} className="text-label h-7">
                Save
              </Button>
              <Button size="sm" variant="ghost" onClick={onDescCancel} className="text-label h-7">
                Cancel
              </Button>
            </div>
          </div>
        ) : project.description ? (
          <div
            className="cursor-pointer hover:bg-accent/40 rounded-md p-2 -m-2 transition-colors"
            onClick={onEditDesc}
          >
            <MarkdownContent className="text-body">{project.description}</MarkdownContent>
          </div>
        ) : (
          <button
            onClick={onEditDesc}
            className="text-body text-muted-foreground hover:text-foreground transition-colors"
          >
            Add description...
          </button>
        )}
      </div>

      <Separator className="bg-white/[0.08]" />

      {/* Milestones overview */}
      <div>
        <h3 className="text-label font-semibold text-muted-foreground uppercase tracking-wider mb-3">
          Milestones {milestones.length > 0 && `(${milestones.length})`}
        </h3>
        {milestones.length === 0 ? (
          <p className="text-label text-muted-foreground">No milestones yet. Add one from the sidebar.</p>
        ) : (
          <div className="space-y-2">
            {milestones.map((m) => {
              const progress = m.issue_count && m.issue_count > 0
                ? Math.round(((m.done_count ?? 0) / m.issue_count) * 100)
                : 0
              return (
                <div key={m.id} className="flex items-center gap-3">
                  <div className="flex-1 min-w-0">
                    <span className="text-label text-foreground/80 font-medium">{m.name}</span>
                    <div className="flex items-center gap-2 mt-0.5">
                      {m.target_date && (
                        <span className="text-micro text-muted-foreground">
                          {new Date(m.target_date).toLocaleDateString(undefined, { month: "short", day: "numeric" })}
                        </span>
                      )}
                      <span className="text-micro text-muted-foreground">{m.done_count ?? 0}/{m.issue_count ?? 0} done</span>
                    </div>
                  </div>
                  <div className="w-16 h-1 bg-white/[0.08] rounded-full overflow-hidden">
                    <div
                      className="h-full bg-primary rounded-full transition-all"
                      style={{ width: `${progress}%` }}
                    />
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}

// ===========================================================================
// Issues tab
// ===========================================================================

function IssuesTab({
  issues,
  loading,
  router,
}: {
  issues: Mission[]
  loading: boolean
  router: ReturnType<typeof useRouter>
}) {
  if (loading) {
    return (
      <div className="flex items-center justify-center py-16">
        <Spinner className="h-5 w-5" />
      </div>
    )
  }

  if (issues.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-16 text-muted-foreground">
        <FolderKanban className="h-8 w-8 mb-3" />
        <p className="text-body">No issues in this project</p>
      </div>
    )
  }

  return (
    <div className="p-4">
      <table className="w-full text-left">
        <thead>
          <tr className="border-b border-white/[0.08]">
            <th className="text-micro font-medium text-muted-foreground uppercase tracking-wider py-2 px-3 w-[80px]">ID</th>
            <th className="text-micro font-medium text-muted-foreground uppercase tracking-wider py-2 px-3">Title</th>
            <th className="text-micro font-medium text-muted-foreground uppercase tracking-wider py-2 px-3 w-[100px]">Status</th>
            <th className="text-micro font-medium text-muted-foreground uppercase tracking-wider py-2 px-3 w-[90px]">Priority</th>
            <th className="text-micro font-medium text-muted-foreground uppercase tracking-wider py-2 px-3 w-[120px]">Assignee</th>
          </tr>
        </thead>
        <tbody>
          {issues.map((issue) => (
            <tr
              key={issue.id}
              tabIndex={0}
              role="link"
              onClick={() => {
                if (issue.identifier) router.push(`/orchestration/issues/${issue.identifier}`)
              }}
              onKeyDown={(e) => {
                if ((e.key === "Enter" || e.key === " ") && issue.identifier) {
                  e.preventDefault()
                  router.push(`/orchestration/issues/${issue.identifier}`)
                }
              }}
              className="border-b border-white/[0.04] hover:bg-accent/40 transition-colors cursor-pointer focus:outline-none focus:bg-accent/60"
            >
              <td className="py-2 px-3">
                <span className="text-label font-mono text-muted-foreground">{issue.identifier || "--"}</span>
              </td>
              <td className="py-2 px-3">
                <span className="text-label text-foreground/80 truncate">{issue.title}</span>
              </td>
              <td className="py-2 px-3">
                <div className="flex items-center gap-1.5">
                  <StatusIcon status={issue.status} className="h-3.5 w-3.5" />
                  <span className="text-label text-muted-foreground">{statusLabel[issue.status] || issue.status}</span>
                </div>
              </td>
              <td className="py-2 px-3">
                <div className="flex items-center gap-1.5">
                  <PriorityIcon priority={issue.priority || "none"} className="h-3.5 w-3.5" />
                  <span className="text-label text-muted-foreground">{priorityLabel[issue.priority || "none"]}</span>
                </div>
              </td>
              <td className="py-2 px-3">
                {issue.assignee_name ? (
                  <div className="flex items-center gap-1.5">
                    {issue.assignee_id && (
                      <img
                        src={getAgentAvatarUrl(issue.assignee_id)}
                        alt=""
                        className="h-4 w-4 rounded-full"
                      />
                    )}
                    <span className="text-label text-muted-foreground truncate">{issue.assignee_name}</span>
                  </div>
                ) : (
                  <span className="text-label text-muted-foreground/60">--</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

