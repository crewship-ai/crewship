"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useParams, useRouter } from "next/navigation"
import {
  ArrowLeft,
  ChevronRight,
  Clock,
  FolderKanban,
  Link2,
  MoreHorizontal,
  Star,
} from "lucide-react"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSession } from "@/hooks/use-auth"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { ProjectSidebar } from "@/components/features/orchestration/project-sidebar"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"
import { ISSUE_STATUS_COLORS, CREW_COLOR_DEFAULT } from "@/lib/colors"
import { toast } from "sonner"
import type {
  Milestone,
  Mission,
  Project,
  ProjectStats,
} from "@/lib/types/mission"

// ---------------------------------------------------------------------------

import { OverviewTab, IssuesTab } from "./project-detail-tabs"

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
