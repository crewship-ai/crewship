"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { useParams, useRouter } from "next/navigation"
import {
  ArrowLeft,
  Calendar,
  ChevronDown,
  ChevronRight,
  Clock,
  FolderKanban,
  Link2,
  MoreHorizontal,
  Pencil,
  Plus,
  Star,
  User,
} from "lucide-react"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSession } from "@/hooks/use-auth"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { CrewIconPopover } from "@/components/crew-icon-popover"
import { CrewIcon } from "@/components/ui/crew-icon"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import { Button } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import type {
  IssuePriority,
  Milestone,
  Mission,
  Project,
  ProjectStatus,
} from "@/lib/types/mission"

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const PROJECT_STATUSES: { value: ProjectStatus; label: string }[] = [
  { value: "backlog", label: "Backlog" },
  { value: "planned", label: "Planned" },
  { value: "in_progress", label: "In Progress" },
  { value: "paused", label: "Paused" },
  { value: "completed", label: "Completed" },
  { value: "cancelled", label: "Cancelled" },
]

const ALL_PRIORITIES: { value: IssuePriority; label: string }[] = [
  { value: "urgent", label: "Urgent" },
  { value: "high", label: "High" },
  { value: "medium", label: "Medium" },
  { value: "low", label: "Low" },
  { value: "none", label: "No priority" },
]

const STATUS_COLORS: Record<string, string> = {
  BACKLOG: "#6b7280",
  TODO: "#a3a3a3",
  IN_PROGRESS: "#3b82f6",
  REVIEW: "#a855f7",
  DONE: "#22c55e",
  COMPLETED: "#22c55e",
  CANCELLED: "#ef4444",
  FAILED: "#ef4444",
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ProjectStats {
  total_issues: number
  completed_issues: number
  by_status: Record<string, number>
  by_assignee: { agent_id: string; agent_name: string; total: number; completed: number }[]
  by_label: { label_name: string; color: string; count: number }[]
  crews: string[]
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function formatDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  })
}

function formatShortDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
  })
}

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

function ProjectStatusIcon({ status, className }: { status: ProjectStatus; className?: string }) {
  const cls = cn("h-4 w-4 shrink-0", className)
  switch (status) {
    case "backlog":
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" strokeDasharray="3 3" opacity="0.5" />
        </svg>
      )
    case "planned":
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.6" />
        </svg>
      )
    case "in_progress":
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.3" />
          <path d="M8 2a6 6 0 0 1 6 6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
        </svg>
      )
    case "paused":
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.4" />
          <rect x="6" y="5" width="1.5" height="6" rx="0.5" fill="currentColor" opacity="0.6" />
          <rect x="8.5" y="5" width="1.5" height="6" rx="0.5" fill="currentColor" opacity="0.6" />
        </svg>
      )
    case "completed":
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" fill="currentColor" opacity="0.15" stroke="currentColor" strokeWidth="1.5" />
          <path d="M5.5 8l2 2 3.5-3.5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      )
    case "cancelled":
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.3" />
          <path d="M5.5 5.5l5 5M10.5 5.5l-5 5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
        </svg>
      )
    default:
      return null
  }
}

function SectionHeader({
  title,
  open,
  onToggle,
  action,
}: {
  title: string
  open: boolean
  onToggle: () => void
  action?: React.ReactNode
}) {
  return (
    <div className="flex items-center justify-between">
      <button
        onClick={onToggle}
        className="flex items-center gap-1.5 text-[11px] font-medium text-muted-foreground/70 hover:text-muted-foreground/90 transition-colors"
      >
        {open ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
        {title}
      </button>
      {action}
    </div>
  )
}

function SidebarPropertyRow({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="flex items-center justify-between gap-3 py-1.5 min-h-[32px]">
      <span className="text-xs text-muted-foreground shrink-0 w-[80px]">{label}</span>
      <div className="flex-1 flex items-center justify-end min-w-0">{children}</div>
    </div>
  )
}

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
    if (!workspaceId) return
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

  // Realtime refresh
  const handleRealtime = useCallback(() => {
    fetchProject()
    fetchStats()
    fetchIssues()
  }, [fetchProject, fetchStats, fetchIssues])

  useRealtimeEvent("mission.updated", handleRealtime)
  useRealtimeEvent("task.updated", handleRealtime)

  // -----------------------------------------------------------------------
  // Patch helper
  // -----------------------------------------------------------------------

  const patchProject = useCallback(
    async (fields: Record<string, unknown>) => {
      if (!project || !workspaceId) return
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
      color: STATUS_COLORS[status] || "#6b7280",
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
        <p className="text-sm text-muted-foreground">{error || "Project not found"}</p>
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
      <div className="flex items-center gap-2 px-4 py-2.5 border-b border-white/[0.06] shrink-0 bg-card/50">
        {/* Back */}
        <button
          onClick={() => router.push("/orchestration")}
          className="p-1 rounded hover:bg-white/[0.06] text-muted-foreground/60 hover:text-foreground transition-colors"
        >
          <ArrowLeft className="h-4 w-4" />
        </button>

        {/* Breadcrumb */}
        <div className="flex items-center gap-1.5 text-[12px] text-muted-foreground/60 min-w-0">
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
            "p-1.5 rounded hover:bg-white/[0.06] transition-colors",
            starred ? "text-yellow-400" : "text-muted-foreground/40 hover:text-muted-foreground/60",
          )}
        >
          <Star className={cn("h-3.5 w-3.5", starred && "fill-current")} />
        </button>

        {/* More */}
        <button className="p-1.5 rounded hover:bg-white/[0.06] text-muted-foreground/40 hover:text-muted-foreground/60 transition-colors">
          <MoreHorizontal className="h-3.5 w-3.5" />
        </button>

        {/* Share */}
        <button
          onClick={() => {
            navigator.clipboard.writeText(window.location.href)
            toast.success("URL copied")
          }}
          className="p-1.5 rounded hover:bg-white/[0.06] text-muted-foreground/40 hover:text-muted-foreground/60 transition-colors"
        >
          <Link2 className="h-3.5 w-3.5" />
        </button>
      </div>

      {/* ================================================================== */}
      {/* Tabs                                                               */}
      {/* ================================================================== */}
      <div className="flex items-center gap-0 px-4 border-b border-white/[0.06] shrink-0 bg-card/30">
        {(["overview", "issues", "activity"] as const).map((tab) => (
          <button
            key={tab}
            onClick={() => setActiveTab(tab)}
            className={cn(
              "text-[12px] px-3 py-2 border-b-2 transition-colors capitalize",
              activeTab === tab
                ? "border-blue-500 text-foreground font-medium"
                : "border-transparent text-muted-foreground/50 hover:text-muted-foreground/80",
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
            <div className="p-8">
              <div className="flex flex-col items-center justify-center py-16 text-muted-foreground/40">
                <Clock className="h-8 w-8 mb-3" />
                <p className="text-sm">Activity feed coming soon</p>
                <p className="text-xs text-muted-foreground/30 mt-1">Project activity and updates will appear here</p>
              </div>
            </div>
          )}
        </div>

        {/* ---- Sidebar ---- */}
        <div className="hidden lg:block w-[360px] border-l border-white/[0.06] overflow-y-auto bg-card/30 shrink-0">
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
  const priorityInfo = ALL_PRIORITIES.find((p) => p.value === project.priority)

  return (
    <div className="max-w-3xl mx-auto px-8 py-8 space-y-8">
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
              className="text-2xl font-bold text-foreground bg-transparent border-b-2 border-blue-500 outline-none w-full pb-1"
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
              className="text-2xl font-bold text-foreground cursor-pointer hover:text-blue-400 transition-colors"
              onClick={onEditTitle}
            >
              {project.name}
            </h1>
          )}

          {/* Summary placeholder */}
          {!editingDesc && !project.description && (
            <button
              onClick={onEditDesc}
              className="text-sm text-muted-foreground/40 hover:text-muted-foreground/60 transition-colors mt-1"
            >
              Add a short summary...
            </button>
          )}
        </div>
      </div>

      {/* Properties bar */}
      <div className="flex items-center gap-3 flex-wrap">
        <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-white/[0.04] border border-white/[0.06]">
          <ProjectStatusIcon status={project.status} className="h-3.5 w-3.5 text-muted-foreground/70" />
          <span className="text-[12px] text-foreground/80">{statusInfo?.label || project.status}</span>
        </div>
        <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-white/[0.04] border border-white/[0.06]">
          <PriorityIcon priority={project.priority || "none"} className="h-3.5 w-3.5" />
          <span className="text-[12px] text-foreground/80">{priorityInfo?.label || "No priority"}</span>
        </div>
        {project.lead_name && (
          <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-white/[0.04] border border-white/[0.06]">
            {project.lead_id && (
              <img src={getAgentAvatarUrl(project.lead_id)} alt="" className="h-4 w-4 rounded-full" />
            )}
            <span className="text-[12px] text-foreground/80">{project.lead_name}</span>
          </div>
        )}
        {project.target_date && (
          <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-white/[0.04] border border-white/[0.06]">
            <Calendar className="h-3 w-3 text-muted-foreground/50" />
            <span className="text-[12px] text-foreground/80">{formatShortDate(project.target_date)}</span>
          </div>
        )}
        {stats?.crews && stats.crews.length > 0 && stats.crews.map((crew) => (
          <div
            key={crew}
            className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-white/[0.04] border border-white/[0.06]"
          >
            <span className="text-[12px] text-foreground/80">{crew}</span>
          </div>
        ))}
      </div>

      <Separator className="bg-white/[0.06]" />

      {/* Resources placeholder */}
      <div>
        <h3 className="text-[11px] font-semibold text-muted-foreground/60 uppercase tracking-wider mb-2">Resources</h3>
        <button className="flex items-center gap-2 text-[12px] text-muted-foreground/40 hover:text-muted-foreground/60 transition-colors py-1.5">
          <Plus className="h-3.5 w-3.5" />
          Add document or link...
        </button>
      </div>

      <Separator className="bg-white/[0.06]" />

      {/* Project update placeholder */}
      <button className="w-full flex items-center justify-center gap-2 py-3 px-4 rounded-lg border border-dashed border-white/[0.1] hover:border-white/[0.2] text-muted-foreground/50 hover:text-muted-foreground/70 transition-colors">
        <Pencil className="h-3.5 w-3.5" />
        <span className="text-[13px]">Write first project update</span>
      </button>

      <Separator className="bg-white/[0.06]" />

      {/* Description */}
      <div>
        <h3 className="text-[11px] font-semibold text-muted-foreground/60 uppercase tracking-wider mb-3">Description</h3>
        {editingDesc ? (
          <div className="space-y-2">
            <textarea
              className="w-full min-h-[120px] bg-white/[0.03] border border-white/[0.1] rounded-md px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground/40 outline-none focus:border-blue-500/50 resize-y"
              value={descDraft}
              onChange={(e) => onDescChange(e.target.value)}
              placeholder="Describe the project..."
              autoFocus
            />
            <div className="flex items-center gap-2">
              <Button size="sm" variant="default" onClick={onDescSave} className="text-xs h-7">
                Save
              </Button>
              <Button size="sm" variant="ghost" onClick={onDescCancel} className="text-xs h-7">
                Cancel
              </Button>
            </div>
          </div>
        ) : project.description ? (
          <div
            className="cursor-pointer hover:bg-white/[0.02] rounded-md p-2 -m-2 transition-colors"
            onClick={onEditDesc}
          >
            <MarkdownContent className="text-sm">{project.description}</MarkdownContent>
          </div>
        ) : (
          <button
            onClick={onEditDesc}
            className="text-sm text-muted-foreground/40 hover:text-muted-foreground/60 transition-colors"
          >
            Add description...
          </button>
        )}
      </div>

      <Separator className="bg-white/[0.06]" />

      {/* Milestones overview */}
      <div>
        <h3 className="text-[11px] font-semibold text-muted-foreground/60 uppercase tracking-wider mb-3">
          Milestones {milestones.length > 0 && `(${milestones.length})`}
        </h3>
        {milestones.length === 0 ? (
          <p className="text-[12px] text-muted-foreground/40">No milestones yet. Add one from the sidebar.</p>
        ) : (
          <div className="space-y-2">
            {milestones.map((m) => {
              const progress = m.issue_count && m.issue_count > 0
                ? Math.round(((m.done_count ?? 0) / m.issue_count) * 100)
                : 0
              return (
                <div key={m.id} className="flex items-center gap-3">
                  <div className="flex-1 min-w-0">
                    <span className="text-[12px] text-foreground/80 font-medium">{m.name}</span>
                    <div className="flex items-center gap-2 mt-0.5">
                      {m.target_date && (
                        <span className="text-[10px] text-muted-foreground/50">
                          {new Date(m.target_date).toLocaleDateString(undefined, { month: "short", day: "numeric" })}
                        </span>
                      )}
                      <span className="text-[10px] text-muted-foreground/50">{m.done_count ?? 0}/{m.issue_count ?? 0} done</span>
                    </div>
                  </div>
                  <div className="w-16 h-1 bg-white/[0.06] rounded-full overflow-hidden">
                    <div className="h-full bg-green-500/70 rounded-full transition-all" style={{ width: `${progress}%` }} />
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
      <div className="flex flex-col items-center justify-center py-16 text-muted-foreground/40">
        <FolderKanban className="h-8 w-8 mb-3" />
        <p className="text-sm">No issues in this project</p>
      </div>
    )
  }

  return (
    <div className="p-4">
      <table className="w-full text-left">
        <thead>
          <tr className="border-b border-white/[0.06]">
            <th className="text-[10px] font-medium text-muted-foreground/50 uppercase tracking-wider py-2 px-3 w-[80px]">ID</th>
            <th className="text-[10px] font-medium text-muted-foreground/50 uppercase tracking-wider py-2 px-3">Title</th>
            <th className="text-[10px] font-medium text-muted-foreground/50 uppercase tracking-wider py-2 px-3 w-[100px]">Status</th>
            <th className="text-[10px] font-medium text-muted-foreground/50 uppercase tracking-wider py-2 px-3 w-[90px]">Priority</th>
            <th className="text-[10px] font-medium text-muted-foreground/50 uppercase tracking-wider py-2 px-3 w-[120px]">Assignee</th>
          </tr>
        </thead>
        <tbody>
          {issues.map((issue) => (
            <tr
              key={issue.id}
              onClick={() => {
                if (issue.identifier) router.push(`/orchestration/issues/${issue.identifier}`)
              }}
              className="border-b border-white/[0.03] hover:bg-white/[0.03] transition-colors cursor-pointer"
            >
              <td className="py-2 px-3">
                <span className="text-[11px] font-mono text-muted-foreground/60">{issue.identifier || "--"}</span>
              </td>
              <td className="py-2 px-3">
                <span className="text-[12px] text-foreground/80 truncate">{issue.title}</span>
              </td>
              <td className="py-2 px-3">
                <div className="flex items-center gap-1.5">
                  <StatusIcon status={issue.status} className="h-3.5 w-3.5" />
                  <span className="text-[11px] text-muted-foreground/60">{statusLabel[issue.status] || issue.status}</span>
                </div>
              </td>
              <td className="py-2 px-3">
                <div className="flex items-center gap-1.5">
                  <PriorityIcon priority={issue.priority || "none"} className="h-3.5 w-3.5" />
                  <span className="text-[11px] text-muted-foreground/60">{priorityLabel[issue.priority || "none"]}</span>
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
                    <span className="text-[11px] text-muted-foreground/60 truncate">{issue.assignee_name}</span>
                  </div>
                ) : (
                  <span className="text-[11px] text-muted-foreground/30">--</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ===========================================================================
// Sidebar
// ===========================================================================

function ProjectSidebar({
  project,
  stats,
  agents,
  donutPaths,
  donutSegments,
  progressTab,
  setProgressTab,
  propertiesOpen,
  setPropertiesOpen,
  milestonesOpen,
  setMilestonesOpen,
  progressOpen,
  setProgressOpen,
  activityOpen,
  setActivityOpen,
  statusOpen,
  setStatusOpen,
  priorityOpen,
  setPriorityOpen,
  leadOpen,
  setLeadOpen,
  patchProject,
  milestones,
  addingMilestone,
  setAddingMilestone,
  newMilestoneName,
  setNewMilestoneName,
  newMilestoneDate,
  setNewMilestoneDate,
  handleAddMilestone,
  editingMilestoneId,
  setEditingMilestoneId,
  editMilestoneName,
  setEditMilestoneName,
  handleRenameMilestone,
}: {
  project: Project
  stats: ProjectStats | null
  agents: { id: string; name: string; slug?: string }[]
  donutPaths: { status: string; value: number; pct: number; color: string; dasharray: string; dashoffset: number }[]
  donutSegments: { status: string; value: number; pct: number; color: string }[]
  progressTab: "assignees" | "labels"
  setProgressTab: (v: "assignees" | "labels") => void
  propertiesOpen: boolean
  setPropertiesOpen: (v: boolean) => void
  milestonesOpen: boolean
  setMilestonesOpen: (v: boolean) => void
  progressOpen: boolean
  setProgressOpen: (v: boolean) => void
  activityOpen: boolean
  setActivityOpen: (v: boolean) => void
  statusOpen: boolean
  setStatusOpen: (v: boolean) => void
  priorityOpen: boolean
  setPriorityOpen: (v: boolean) => void
  leadOpen: boolean
  setLeadOpen: (v: boolean) => void
  patchProject: (fields: Record<string, unknown>) => Promise<void>
  milestones: Milestone[]
  addingMilestone: boolean
  setAddingMilestone: (v: boolean) => void
  newMilestoneName: string
  setNewMilestoneName: (v: string) => void
  newMilestoneDate: string
  setNewMilestoneDate: (v: string) => void
  handleAddMilestone: () => Promise<void>
  editingMilestoneId: string | null
  setEditingMilestoneId: (v: string | null) => void
  editMilestoneName: string
  setEditMilestoneName: (v: string) => void
  handleRenameMilestone: (id: string) => Promise<void>
}) {
  return (
    <div className="p-4 space-y-4">
      {/* ── Properties ─────────────────────────────────────────── */}
      <SectionHeader
        title="Properties"
        open={propertiesOpen}
        onToggle={() => setPropertiesOpen(!propertiesOpen)}
        action={
          <button className="p-0.5 rounded hover:bg-white/[0.06] text-muted-foreground/40 hover:text-muted-foreground/60 transition-colors">
            <Plus className="h-3 w-3" />
          </button>
        }
      />
      {propertiesOpen && (
        <div className="space-y-0.5">
          {/* Status */}
          <Popover open={statusOpen} onOpenChange={setStatusOpen}>
            <PopoverTrigger asChild>
              <div>
                <SidebarPropertyRow label="Status">
                  <button className="flex items-center gap-1.5 px-2 py-0.5 rounded hover:bg-white/[0.06] transition-colors">
                    <ProjectStatusIcon status={project.status} className="h-3.5 w-3.5 text-muted-foreground/70" />
                    <span className="text-xs text-foreground/80">
                      {PROJECT_STATUSES.find((s) => s.value === project.status)?.label || project.status}
                    </span>
                  </button>
                </SidebarPropertyRow>
              </div>
            </PopoverTrigger>
            <PopoverContent className="w-48 p-1" align="end">
              {PROJECT_STATUSES.map((s) => (
                <button
                  key={s.value}
                  onClick={() => { patchProject({ status: s.value }); setStatusOpen(false) }}
                  className={cn(
                    "flex items-center gap-2 w-full px-2 py-1.5 rounded text-xs hover:bg-white/[0.06]",
                    s.value === project.status && "bg-white/[0.04]",
                  )}
                >
                  <ProjectStatusIcon status={s.value as ProjectStatus} className="h-3.5 w-3.5" />
                  {s.label}
                </button>
              ))}
            </PopoverContent>
          </Popover>

          {/* Priority */}
          <Popover open={priorityOpen} onOpenChange={setPriorityOpen}>
            <PopoverTrigger asChild>
              <div>
                <SidebarPropertyRow label="Priority">
                  <button className="flex items-center gap-1.5 px-2 py-0.5 rounded hover:bg-white/[0.06] transition-colors">
                    <PriorityIcon priority={project.priority || "none"} className="h-3.5 w-3.5" />
                    <span className="text-xs text-foreground/80">
                      {priorityLabel[project.priority || "none"]}
                    </span>
                  </button>
                </SidebarPropertyRow>
              </div>
            </PopoverTrigger>
            <PopoverContent className="w-48 p-1" align="end">
              {ALL_PRIORITIES.map((p) => (
                <button
                  key={p.value}
                  onClick={() => { patchProject({ priority: p.value }); setPriorityOpen(false) }}
                  className={cn(
                    "flex items-center gap-2 w-full px-2 py-1.5 rounded text-xs hover:bg-white/[0.06]",
                    p.value === project.priority && "bg-white/[0.04]",
                  )}
                >
                  <PriorityIcon priority={p.value} className="h-3.5 w-3.5" />
                  {p.label}
                </button>
              ))}
            </PopoverContent>
          </Popover>

          {/* Lead */}
          <Popover open={leadOpen} onOpenChange={setLeadOpen}>
            <PopoverTrigger asChild>
              <div>
                <SidebarPropertyRow label="Lead">
                  <button className="flex items-center gap-1.5 px-2 py-0.5 rounded hover:bg-white/[0.06] transition-colors">
                    {project.lead_id ? (
                      <>
                        <img src={getAgentAvatarUrl(project.lead_id)} alt="" className="h-4 w-4 rounded-full" />
                        <span className="text-xs text-foreground/80">{project.lead_name || "Lead"}</span>
                      </>
                    ) : (
                      <span className="text-xs text-muted-foreground/40">Add lead</span>
                    )}
                  </button>
                </SidebarPropertyRow>
              </div>
            </PopoverTrigger>
            <PopoverContent className="w-52 p-0" align="end">
              <Command>
                <CommandInput placeholder="Search agents..." />
                <CommandList>
                  <CommandEmpty>No agents found</CommandEmpty>
                  <CommandGroup>
                    <CommandItem onSelect={() => { patchProject({ lead_type: null, lead_id: null }); setLeadOpen(false) }}>
                      <User className="h-3.5 w-3.5 text-muted-foreground/50 mr-2" />
                      No lead
                    </CommandItem>
                    {agents.map((a) => (
                      <CommandItem
                        key={a.id}
                        onSelect={() => { patchProject({ lead_type: "agent", lead_id: a.id }); setLeadOpen(false) }}
                      >
                        <img src={getAgentAvatarUrl(a.id)} alt="" className="h-4 w-4 rounded-full mr-2" />
                        {a.name}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </CommandList>
              </Command>
            </PopoverContent>
          </Popover>

          {/* Members */}
          <SidebarPropertyRow label="Members">
            {stats?.by_assignee && stats.by_assignee.length > 0 ? (
              <div className="flex -space-x-1">
                {stats.by_assignee.slice(0, 5).map((a) => (
                  <img
                    key={a.agent_id}
                    src={getAgentAvatarUrl(a.agent_id || a.agent_name)}
                    alt={a.agent_name}
                    title={a.agent_name}
                    className="h-5 w-5 rounded-full ring-1 ring-card"
                  />
                ))}
                {stats.by_assignee.length > 5 && (
                  <span className="text-[10px] text-muted-foreground/50 pl-1.5 self-center">
                    +{stats.by_assignee.length - 5}
                  </span>
                )}
              </div>
            ) : (
              <span className="text-xs text-muted-foreground/40">Add members</span>
            )}
          </SidebarPropertyRow>

          {/* Dates */}
          <SidebarPropertyRow label="Dates">
            <Popover>
              <PopoverTrigger asChild>
                <button className="flex items-center gap-1 px-2 py-0.5 rounded hover:bg-white/[0.06] transition-colors text-xs text-foreground/70">
                  {project.start_date || project.target_date
                    ? `${project.start_date ? formatShortDate(project.start_date) : "?"} → ${project.target_date ? formatShortDate(project.target_date) : "?"}`
                    : "Set dates"}
                </button>
              </PopoverTrigger>
              <PopoverContent className="w-auto p-3 space-y-2" align="end">
                <div>
                  <label className="text-[10px] text-muted-foreground/60 block mb-1">Start date</label>
                  <input
                    type="date"
                    className="bg-transparent border border-white/[0.1] rounded px-2 py-1 text-xs text-foreground outline-none w-full"
                    defaultValue={project.start_date || ""}
                    onChange={(e) => patchProject({ start_date: e.target.value || null })}
                  />
                </div>
                <div>
                  <label className="text-[10px] text-muted-foreground/60 block mb-1">Target date</label>
                  <input
                    type="date"
                    className="bg-transparent border border-white/[0.1] rounded px-2 py-1 text-xs text-foreground outline-none w-full"
                    defaultValue={project.target_date || ""}
                    onChange={(e) => patchProject({ target_date: e.target.value || null })}
                  />
                </div>
              </PopoverContent>
            </Popover>
          </SidebarPropertyRow>

          {/* Teams */}
          <SidebarPropertyRow label="Teams">
            {stats?.crews && stats.crews.length > 0 ? (
              <div className="flex items-center gap-1 flex-wrap justify-end">
                {stats.crews.map((crew) => (
                  <span
                    key={crew}
                    className="text-[10px] px-1.5 py-0.5 rounded bg-white/[0.06] text-muted-foreground/70"
                  >
                    {crew}
                  </span>
                ))}
              </div>
            ) : (
              <span className="text-xs text-muted-foreground/40">No teams</span>
            )}
          </SidebarPropertyRow>

          {/* Labels */}
          <SidebarPropertyRow label="Labels">
            {stats?.by_label && stats.by_label.length > 0 ? (
              <div className="flex items-center gap-1 flex-wrap justify-end">
                {stats.by_label.map((l) => (
                  <span
                    key={l.label_name}
                    className="text-[10px] px-1.5 py-0.5 rounded flex items-center gap-1"
                    style={{ backgroundColor: `${l.color}20`, color: l.color }}
                  >
                    <span className="w-1.5 h-1.5 rounded-full" style={{ backgroundColor: l.color }} />
                    {l.label_name}
                  </span>
                ))}
              </div>
            ) : (
              <span className="text-xs text-muted-foreground/40">Add label</span>
            )}
          </SidebarPropertyRow>
        </div>
      )}

      <Separator className="bg-white/[0.06]" />

      {/* ── Milestones ─────────────────────────────────────────── */}
      <SectionHeader
        title={`Milestones${milestones.length > 0 ? ` (${milestones.length})` : ""}`}
        open={milestonesOpen}
        onToggle={() => setMilestonesOpen(!milestonesOpen)}
        action={
          <button
            onClick={() => { setMilestonesOpen(true); setAddingMilestone(true) }}
            className="p-0.5 rounded hover:bg-white/[0.06] text-muted-foreground/40 hover:text-muted-foreground/60 transition-colors"
          >
            <Plus className="h-3 w-3" />
          </button>
        }
      />
      {milestonesOpen && (
        <div className="py-2 space-y-2">
          {milestones.length === 0 && !addingMilestone && (
            <p className="text-[12px] text-muted-foreground/40">No milestones yet</p>
          )}

          {milestones.map((m) => {
            const progress = m.issue_count && m.issue_count > 0
              ? Math.round(((m.done_count ?? 0) / m.issue_count) * 100)
              : 0
            const isEditing = editingMilestoneId === m.id

            return (
              <div
                key={m.id}
                className="group bg-white/[0.02] border border-white/[0.06] rounded-md px-3 py-2"
              >
                {isEditing ? (
                  <input
                    autoFocus
                    value={editMilestoneName}
                    onChange={(e) => setEditMilestoneName(e.target.value)}
                    onBlur={() => handleRenameMilestone(m.id)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") handleRenameMilestone(m.id)
                      if (e.key === "Escape") setEditingMilestoneId(null)
                    }}
                    className="bg-transparent text-[12px] text-foreground/80 font-medium outline-none w-full border-b border-blue-400/40 pb-0.5"
                  />
                ) : (
                  <button
                    onClick={() => {
                      setEditingMilestoneId(m.id)
                      setEditMilestoneName(m.name)
                    }}
                    className="text-[12px] text-foreground/80 font-medium hover:text-foreground transition-colors text-left w-full"
                  >
                    {m.name}
                  </button>
                )}
                <div className="flex items-center justify-between mt-1.5">
                  {m.target_date && (
                    <span className="text-[10px] text-muted-foreground/50">
                      <Clock className="h-2.5 w-2.5 inline mr-0.5" />
                      {new Date(m.target_date).toLocaleDateString(undefined, { month: "short", day: "numeric" })}
                    </span>
                  )}
                  <span className="text-[10px] text-muted-foreground/50 ml-auto">
                    {m.done_count ?? 0}/{m.issue_count ?? 0} done
                  </span>
                </div>
                {(m.issue_count ?? 0) > 0 && (
                  <div className="mt-1.5 h-1 bg-white/[0.06] rounded-full overflow-hidden">
                    <div
                      className="h-full bg-green-500/70 rounded-full transition-all"
                      style={{ width: `${progress}%` }}
                    />
                  </div>
                )}
              </div>
            )
          })}

          {addingMilestone && (
            <div className="bg-white/[0.02] border border-white/[0.08] rounded-md p-2.5 space-y-2">
              <input
                autoFocus
                placeholder="Milestone name"
                value={newMilestoneName}
                onChange={(e) => setNewMilestoneName(e.target.value)}
                onKeyDown={(e) => { if (e.key === "Enter") handleAddMilestone() }}
                className="w-full bg-transparent border border-white/[0.1] rounded px-2 py-1 text-[11px] text-foreground placeholder:text-muted-foreground/30 outline-none focus:border-blue-400/40"
              />
              <input
                type="date"
                value={newMilestoneDate}
                onChange={(e) => setNewMilestoneDate(e.target.value)}
                className="w-full bg-transparent border border-white/[0.1] rounded px-2 py-1 text-[11px] text-foreground outline-none focus:border-blue-400/40"
              />
              <div className="flex gap-1.5">
                <button
                  onClick={handleAddMilestone}
                  disabled={!newMilestoneName.trim()}
                  className={cn(
                    "flex-1 h-6 rounded text-[11px] font-medium transition-colors",
                    newMilestoneName.trim()
                      ? "bg-blue-600 text-white hover:bg-blue-500"
                      : "bg-white/[0.04] text-muted-foreground/30 cursor-not-allowed",
                  )}
                >
                  Add
                </button>
                <button
                  onClick={() => { setAddingMilestone(false); setNewMilestoneName(""); setNewMilestoneDate("") }}
                  className="flex-1 h-6 rounded text-[11px] bg-white/[0.04] text-muted-foreground/60 hover:bg-white/[0.08] transition-colors"
                >
                  Cancel
                </button>
              </div>
            </div>
          )}
        </div>
      )}

      <Separator className="bg-white/[0.06]" />

      {/* ── Progress ─────────────────────────────────────────── */}
      <SectionHeader
        title="Progress"
        open={progressOpen}
        onToggle={() => setProgressOpen(!progressOpen)}
      />
      {progressOpen && (
        <div className="space-y-3">
          {/* Stat boxes */}
          <div className="grid grid-cols-2 gap-2">
            <div className="bg-white/[0.03] border border-white/[0.06] rounded-md px-3 py-2">
              <div className="text-[10px] text-muted-foreground/50 uppercase tracking-wider">Scope</div>
              <div className="text-[18px] font-semibold text-foreground tabular-nums">
                {stats?.total_issues ?? project.issue_count}
              </div>
            </div>
            <div className="bg-white/[0.03] border border-white/[0.06] rounded-md px-3 py-2">
              <div className="text-[10px] text-muted-foreground/50 uppercase tracking-wider">Completed</div>
              <div className="text-[18px] font-semibold text-green-400 tabular-nums">
                {stats?.completed_issues ?? project.done_count}
              </div>
            </div>
          </div>

          {/* Donut chart */}
          {donutPaths.length > 0 && (
            <div className="flex items-center gap-4">
              <svg viewBox="0 0 40 40" className="w-14 h-14 shrink-0">
                {donutPaths.map((seg) => (
                  <circle
                    key={seg.status}
                    cx="20"
                    cy="20"
                    r="16"
                    fill="none"
                    stroke={seg.color}
                    strokeWidth="5"
                    strokeDasharray={seg.dasharray}
                    strokeDashoffset={seg.dashoffset}
                    transform="rotate(-90 20 20)"
                    className="transition-all duration-300"
                  />
                ))}
              </svg>
              <div className="space-y-0.5 flex-1 min-w-0">
                {donutSegments.map((seg) => (
                  <div key={seg.status} className="flex items-center gap-1.5">
                    <span className="w-2 h-2 rounded-sm shrink-0" style={{ backgroundColor: seg.color }} />
                    <span className="text-[10px] text-muted-foreground/70 truncate flex-1">
                      {seg.status.replace(/_/g, " ")}
                    </span>
                    <span className="text-[10px] text-muted-foreground/50 tabular-nums">{seg.value}</span>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Tabs */}
          <div className="flex items-center gap-0 border-b border-white/[0.06]">
            <button
              onClick={() => setProgressTab("assignees")}
              className={cn(
                "text-[11px] px-2 py-1.5 border-b-2 transition-colors",
                progressTab === "assignees"
                  ? "border-blue-500 text-foreground"
                  : "border-transparent text-muted-foreground/50 hover:text-muted-foreground/70",
              )}
            >
              Assignees
            </button>
            <button
              onClick={() => setProgressTab("labels")}
              className={cn(
                "text-[11px] px-2 py-1.5 border-b-2 transition-colors",
                progressTab === "labels"
                  ? "border-blue-500 text-foreground"
                  : "border-transparent text-muted-foreground/50 hover:text-muted-foreground/70",
              )}
            >
              Labels
            </button>
          </div>

          {/* Assignees tab */}
          {progressTab === "assignees" && (
            <div className="space-y-2">
              {stats?.by_assignee && stats.by_assignee.length > 0 ? (
                stats.by_assignee.map((a) => {
                  const pct = a.total > 0 ? Math.round((a.completed / a.total) * 100) : 0
                  return (
                    <div key={a.agent_id || a.agent_name} className="flex items-center gap-2">
                      <img
                        src={getAgentAvatarUrl(a.agent_id || a.agent_name)}
                        alt=""
                        className="h-5 w-5 rounded-full shrink-0"
                      />
                      <span className="text-[12px] text-foreground/80 flex-1 truncate">{a.agent_name}</span>
                      <span className="text-[10px] text-muted-foreground/50 tabular-nums shrink-0">
                        {pct}% of {a.total}
                      </span>
                      {/* Mini progress ring */}
                      <svg viewBox="0 0 20 20" className="w-4 h-4 shrink-0">
                        <circle cx="10" cy="10" r="8" fill="none" stroke="currentColor" strokeWidth="2" className="text-white/[0.06]" />
                        <circle
                          cx="10"
                          cy="10"
                          r="8"
                          fill="none"
                          stroke="#3b82f6"
                          strokeWidth="2"
                          strokeDasharray={`${(pct / 100) * 2 * Math.PI * 8} ${2 * Math.PI * 8}`}
                          strokeDashoffset={0}
                          transform="rotate(-90 10 10)"
                          strokeLinecap="round"
                          className="transition-all duration-300"
                        />
                      </svg>
                    </div>
                  )
                })
              ) : (
                <p className="text-[11px] text-muted-foreground/40">No assignees yet</p>
              )}
            </div>
          )}

          {/* Labels tab */}
          {progressTab === "labels" && (
            <div className="space-y-2">
              {stats?.by_label && stats.by_label.length > 0 ? (
                stats.by_label.map((l) => (
                  <div key={l.label_name} className="flex items-center gap-2">
                    <span className="w-2.5 h-2.5 rounded-full shrink-0" style={{ backgroundColor: l.color }} />
                    <span className="text-[12px] text-foreground/80 flex-1 truncate">{l.label_name}</span>
                    <span className="text-[11px] text-muted-foreground/50 tabular-nums">{l.count}</span>
                  </div>
                ))
              ) : (
                <p className="text-[11px] text-muted-foreground/40">No labels yet</p>
              )}
            </div>
          )}
        </div>
      )}

      <Separator className="bg-white/[0.06]" />

      {/* ── Activity ─────────────────────────────────────────── */}
      <SectionHeader
        title="Activity"
        open={activityOpen}
        onToggle={() => setActivityOpen(!activityOpen)}
        action={
          <button className="text-[10px] text-muted-foreground/40 hover:text-muted-foreground/60 transition-colors">
            See all
          </button>
        }
      />
      {activityOpen && (
        <div className="space-y-2 pb-4">
          <div className="flex items-start gap-2">
            <div className="w-4 h-4 rounded-full bg-white/[0.06] flex items-center justify-center shrink-0 mt-0.5">
              <svg className="h-2.5 w-2.5 text-muted-foreground/50" viewBox="0 0 16 16" fill="currentColor">
                <polygon points="8,2 10,6 14,7 11,10 12,14 8,12 4,14 5,10 2,7 6,6" />
              </svg>
            </div>
            <div className="min-w-0">
              <p className="text-[11px] text-muted-foreground/60">
                Created
                <span className="text-muted-foreground/40 ml-1.5">
                  {formatDate(project.created_at)}
                </span>
              </p>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
