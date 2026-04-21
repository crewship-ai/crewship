"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  Workflow, Clock, Activity,
  FileText, PanelLeftClose, PanelLeftOpen,
  MessageSquare, Terminal, FileCode2, Container,
  ChevronUp, ChevronDown, ChevronLeft, X,
  CircleDot, FolderKanban,
} from "lucide-react"
// Tabs replaced with custom nav for orchestration toolbar
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { WorkflowGraph } from "@/components/features/orchestration/workflow-graph"
import { MissionTimeline } from "@/components/features/orchestration/mission-timeline"
import { OrchestrationActivity } from "@/components/features/orchestration/orchestration-activity"
// TemplateGallery removed — workflow templates not needed in orchestration UI yet
// MissionControlBar replaced by inline info strip in unified toolbar
// CrewConnections moved to Settings (CRE-105)
import { ProposalReview } from "@/components/features/orchestration/proposal-review"
import { type DetailContext } from "@/components/features/orchestration/context-detail-panel"
import { MissionYamlEditor } from "@/components/features/orchestration/mission-yaml-editor"
import { DockerOverview } from "@/components/features/orchestration/docker-overview"
import type { Mission, MissionTask, IssueLabel, IssueComment, Project, SavedView } from "@/lib/types/mission"
import type { CrewSummary, AgentSummary, CrewConnection } from "@/lib/types/orchestration"
import { useIsMobile } from "@/hooks/use-mobile"
import { IssuesBoardInline, IssuesListInline } from "@/components/features/orchestration/issues-inline"
import { UnifiedExplorer } from "@/components/features/orchestration/unified-explorer"
import { CreateIssueModal } from "@/components/features/orchestration/create-issue-modal"
import { CreateProjectModal } from "@/components/features/orchestration/create-project-modal"

import { toast } from "sonner"
import { useAppStore } from "@/lib/store"
import type { BreadcrumbItem } from "@/lib/store"
import { LiveMessagesPanel, ExecLogPanel } from "@/components/features/orchestration/orchestration-drawer-panels"
import { RightPanelContent } from "@/components/features/orchestration/right-panel-content"
import { IssuesToolbarStrip } from "@/components/features/orchestration/issues-toolbar-strip"

type DrawerTab = "messages" | "exec" | "yaml" | "docker"

export interface OrchestrationLayoutProps {
  missions: Mission[]
  crews: CrewSummary[]
  agents: AgentSummary[]
  connections: CrewConnection[]
  workspaceId: string
  selectedMissionId: string
  onMissionChange: (missionId: string) => void
  onRefresh: () => void
  onMissionCreated: () => void
}

const ORCH_DRAWER_TABS = [
  { id: "messages" as const, label: "Messages", icon: MessageSquare },
  { id: "exec" as const, label: "Exec Log", icon: Terminal },
  { id: "yaml" as const, label: "YAML", icon: FileCode2 },
  { id: "docker" as const, label: "Docker", icon: Container },
]

const ORCH_TABS = [
  { id: "issues", label: "Issues", icon: CircleDot },
  { id: "graph", label: "Graph", icon: Workflow },
  { id: "timeline", label: "Timeline", icon: Clock },
  { id: "activity", label: "Activity", icon: Activity },
  { id: "proposals", label: "Approvals", icon: FileText },
] as const

export function OrchestrationLayout({
  missions,
  crews,
  agents,
  connections,
  workspaceId,
  selectedMissionId,
  onMissionChange: _onMissionChange,
  onRefresh,
  onMissionCreated: _onMissionCreated,
}: OrchestrationLayoutProps) {
  const isMobile = useIsMobile()

  // Panel state
  const [leftCollapsed, setLeftCollapsed] = useState(false)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [drawerTab, setDrawerTab] = useState<DrawerTab>("messages")

  // Content state
  const [activeTab, setActiveTab] = useState("issues")
  const [_selectedTask, setSelectedTask] = useState<MissionTask | null>(null)
  const [selectedCrewId] = useState<string | null>(null)
  const [selectedAgentSlug] = useState<string | null>(null)
  const [detailContext, setDetailContext] = useState<DetailContext>({ type: "none" })

  // Issues state
  const [issues, setIssues] = useState<Mission[]>([])
  const [issueLabels, setIssueLabels] = useState<IssueLabel[]>([])
  const [issueViewMode, setIssueViewMode] = useState<"board" | "list">("board")
  const [issueSearch, setIssueSearch] = useState("")
  const [selectedIssue, setSelectedIssue] = useState<Mission | null>(null)
  const [issueComments, setIssueComments] = useState<IssueComment[]>([])
  const [projects, setProjects] = useState<Project[]>([])
  const [selectedProjectId, setSelectedProjectId] = useState<string | null>(null)
  // Project filter applied via saved views — does NOT open the detail panel.
  // `selectedProjectId` is for explicit project clicks (opens detail panel);
  // `filterProjectId` is only for filtering the issues list.
  const [filterProjectId] = useState<string | null>(null)
  const [filterCrewId, setFilterCrewId] = useState<string | null>(null)
  const [filterAgentId, setFilterAgentId] = useState<string | null>(null)
  const [showCreateIssue, setShowCreateIssue] = useState(false)
  const [showCreateProject, setShowCreateProject] = useState(false)

  // Saved views
  const [savedViews, setSavedViews] = useState<SavedView[]>([])
  const [activeViewId, setActiveViewId] = useState<string | null>(null)
  const [savedViewsOpen, setSavedViewsOpen] = useState(false)

  // graphRef removed — was unused

  // Auto-collapse left panel on mobile
  useEffect(() => {
    if (isMobile) setLeftCollapsed(true)
  }, [isMobile])

  // Derived data
  // When an issue is selected, filter to just that mission so Graph/Timeline/Activity focus on it
  const filteredMissions = useMemo(() => {
    if (selectedIssue) {
      const match = missions.find((m) => m.id === selectedIssue.id)
      return match ? [match] : missions
    }
    if (selectedMissionId === "all") return missions
    return missions.filter((m) => m.id === selectedMissionId)
  }, [missions, selectedMissionId, selectedIssue])

  const selectedMission = useMemo(() => {
    if (selectedIssue) {
      return missions.find((m) => m.id === selectedIssue.id) || null
    }
    if (selectedMissionId === "all") return null
    return missions.find((m) => m.id === selectedMissionId) || null
  }, [missions, selectedMissionId, selectedIssue])

  // Left panel filtered by selected mission
  const panelCrews = useMemo(() => {
    if (selectedMissionId === "all") return crews
    const mission = missions.find((m) => m.id === selectedMissionId)
    if (!mission) return crews
    const crewIds = new Set<string>()
    crewIds.add(mission.crew_id)
    for (const task of mission.tasks || []) {
      const agent = agents.find((a) => a.slug === task.agent_slug)
      if (agent?.crew_id) crewIds.add(agent.crew_id)
    }
    return crews.filter((c) => crewIds.has(c.id))
  }, [selectedMissionId, missions, crews, agents])


  const panelMissions = useMemo(() => {
    if (selectedMissionId === "all") return missions
    return missions.filter((m) => m.id === selectedMissionId)
  }, [selectedMissionId, missions])

  // Issue data fetching
  const fetchIssues = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/issues?workspace_id=${encodeURIComponent(workspaceId)}&limit=100`)
      if (res.ok) setIssues(await res.json())
    } catch { /* ignore */ }
  }, [workspaceId])

  const fetchIssueLabels = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/labels?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (res.ok) setIssueLabels(await res.json())
    } catch { /* ignore */ }
  }, [workspaceId])

  const fetchProjects = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/projects?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (res.ok) setProjects(await res.json())
    } catch { /* ignore */ }
  }, [workspaceId])

  const fetchSavedViews = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/saved-views?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (res.ok) {
        const data = await res.json()
        setSavedViews(Array.isArray(data) ? data : data.views ?? [])
      }
    } catch { /* ignore */ }
  }, [workspaceId])

  useEffect(() => {
    fetchIssues()
    fetchIssueLabels()
    fetchProjects()
    fetchSavedViews()
  }, [fetchIssues, fetchIssueLabels, fetchProjects, fetchSavedViews])

  const handleIssueSelect = useCallback(async (issue: Mission) => {
    // Toggle: clicking the same issue again deselects it
    if (selectedIssue?.id === issue.id) {
      setSelectedIssue(null)
      setIssueComments([])
      return
    }
    setSelectedIssue(issue)
    setDetailContext({ type: "none" })
    if (issue.crew_id && issue.identifier) {
      try {
        const res = await fetch(`/api/v1/crews/${encodeURIComponent(issue.crew_id)}/issues/${encodeURIComponent(issue.identifier)}/comments?workspace_id=${encodeURIComponent(workspaceId)}`)
        if (res.ok) setIssueComments(await res.json())
        else setIssueComments([])
      } catch { setIssueComments([]) }
    }
  }, [workspaceId, selectedIssue?.id])

  const filteredIssues = useMemo(() => {
    let filtered = issues
    // Prefer explicit selection (user clicked a project) over saved-view filter.
    const effectiveProjectId = selectedProjectId ?? filterProjectId
    if (effectiveProjectId) {
      filtered = filtered.filter((i) => i.project_id === effectiveProjectId)
    }
    if (filterCrewId) {
      filtered = filtered.filter((i) => i.crew_id === filterCrewId)
    }
    if (filterAgentId) {
      filtered = filtered.filter((i) => i.assignee_id === filterAgentId)
    }
    if (issueSearch) {
      const q = issueSearch.toLowerCase()
      filtered = filtered.filter((i) =>
        i.title.toLowerCase().includes(q) ||
        (i.identifier && i.identifier.toLowerCase().includes(q)) ||
        (i.assignee_name && i.assignee_name.toLowerCase().includes(q)) ||
        (i.crew_name && i.crew_name.toLowerCase().includes(q))
      )
    }
    return filtered
  }, [issues, issueSearch, selectedProjectId, filterProjectId, filterCrewId, filterAgentId])

  // Handlers
  const handleNodeClick = useCallback((task: MissionTask) => {
    setSelectedTask(task)
    const mission = missions.find((m) => m.tasks?.some((t) => t.id === task.id))
    if (mission) {
      setDetailContext({
        type: "task",
        task,
        mission,
        allTasks: mission.tasks || [],
      })
    }
  }, [missions])

  // Computed: which agent slugs are highlighted (agent click or crew click)
  const highlightSlugs = useMemo<Set<string> | null>(() => {
    if (selectedAgentSlug) return new Set([selectedAgentSlug])
    if (selectedCrewId) {
      const crewAgentSlugs = agents.filter((a) => a.crew_id === selectedCrewId).map((a) => a.slug)
      return crewAgentSlugs.length > 0 ? new Set(crewAgentSlugs) : null
    }
    return null
  }, [selectedAgentSlug, selectedCrewId, agents])

  const handleInboxTaskSelect = useCallback((task: MissionTask, mission: Mission) => {
    setSelectedTask(task)
    setDetailContext({
      type: "task",
      task,
      mission,
      allTasks: mission.tasks || [],
    })
  }, [])

  const handleDetailClose = useCallback(() => {
    setDetailContext({ type: "none" })
    setSelectedTask(null)
  }, [])

  const handleTaskAction = useCallback(async (action: "edit" | "retry" | "skip", taskId: string, missionId: string) => {
    const mission = missions.find(m => m.id === missionId)
    if (!mission) return
    const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`

    if (action === "retry") {
      await fetch(`/api/v1/crews/${mission.crew_id}/missions/${missionId}/tasks/${taskId}${qs}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ status: "PENDING" }),
      })
      toast.success("Task queued for retry")
      onRefresh()
    } else if (action === "skip") {
      await fetch(`/api/v1/crews/${mission.crew_id}/missions/${missionId}/tasks/${taskId}${qs}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ status: "SKIPPED" }),
      })
      toast.success("Task skipped")
      onRefresh()
    }
    // "edit" — detail panel is already visible
  }, [missions, workspaceId, onRefresh])

  const handleDrawerTabClick = useCallback((tab: DrawerTab) => {
    if (drawerOpen && drawerTab === tab) {
      setDrawerOpen(false)
    } else {
      setDrawerTab(tab)
      setDrawerOpen(true)
    }
  }, [drawerOpen, drawerTab])

  const selectedProject = selectedProjectId ? projects.find((p) => p.id === selectedProjectId) || null : null
  const showRightPanel = detailContext.type !== "none" || selectedIssue !== null || (selectedProjectId !== null && !selectedIssue)

  const handleIssueClose = useCallback(() => {
    setSelectedIssue(null)
    setIssueComments([])
  }, [])

  const handleIssueUpdated = useCallback(async () => {
    await fetchIssues()
    if (selectedIssue?.crew_id && selectedIssue?.identifier) {
      try {
        const res = await fetch(`/api/v1/issues/${encodeURIComponent(selectedIssue.identifier)}?workspace_id=${encodeURIComponent(workspaceId)}`)
        if (res.ok) {
          const fresh = await res.json()
          setSelectedIssue(fresh)
          const commRes = await fetch(`/api/v1/crews/${encodeURIComponent(fresh.crew_id)}/issues/${encodeURIComponent(fresh.identifier)}/comments?workspace_id=${encodeURIComponent(workspaceId)}`)
          if (commRes.ok) setIssueComments(await commRes.json())
        }
      } catch {}
    }
    fetchProjects()
  }, [fetchIssues, fetchProjects, selectedIssue?.crew_id, selectedIssue?.identifier, workspaceId])

  const handleProjectClose = useCallback(() => {
    setSelectedProjectId(null)
  }, [])

  // Mobile back button: close whichever detail view is currently visible so that
  // showRightPanel ends up false and the overlay sheet actually dismisses.
  const closeMobileDetail = useCallback(() => {
    if (selectedIssue) {
      handleIssueClose()
    } else if (selectedProjectId) {
      handleProjectClose()
    } else {
      handleDetailClose()
    }
  }, [selectedIssue, selectedProjectId, handleIssueClose, handleProjectClose, handleDetailClose])

  // Sync breadcrumbs to global store (rendered in app-toolbar)
  const setBreadcrumbs = useAppStore((s) => s.setBreadcrumbs)
  useEffect(() => {
    const items: BreadcrumbItem[] = []
    if (selectedProject) {
      items.push({ label: selectedProject.name, onClick: () => { setSelectedIssue(null); setIssueComments([]) } })
    }
    if (selectedIssue) {
      items.push({ label: selectedIssue.identifier || selectedIssue.title })
    }
    setBreadcrumbs(items)
    return () => setBreadcrumbs([])
  }, [selectedProject, selectedIssue, setBreadcrumbs])

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* ---- Toolbar: Tab navigation + context + actions (single row) ---- */}
      <div className="shrink-0 z-20 flex items-center h-9 bg-card border-b border-white/[0.08] px-2 sm:px-3 gap-0 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        {/* Tabs */}
        {ORCH_TABS.map(({ id, label, icon: Icon }) => (
          <button
            key={id}
            onClick={() => setActiveTab(id)}
            className={cn(
              "flex items-center gap-1.5 px-2.5 h-full text-xs font-medium border-b-2 transition-all duration-100 relative top-px whitespace-nowrap shrink-0",
              activeTab === id
                ? "border-blue-400 text-blue-400"
                : "border-transparent text-muted-foreground hover:text-foreground/80",
            )}
          >
            <Icon className="h-3 w-3 opacity-75" />
            {label}
          </button>
        ))}

        {/* spacer between tabs and actions */}

        <div className="flex-1" />

        {/* Create buttons */}
        <button
          onClick={() => setShowCreateIssue(true)}
          className="flex items-center gap-1.5 h-7 px-3 rounded-md text-xs font-medium transition-colors shrink-0 bg-primary/10 text-primary hover:bg-primary/20 border border-primary/20"
        >
          <CircleDot className="h-3 w-3" />
          New Issue
        </button>
        <button
          onClick={() => setShowCreateProject(true)}
          className="flex items-center gap-1.5 h-7 px-3 rounded-md text-xs font-medium transition-colors shrink-0 bg-accent text-accent-foreground hover:bg-accent/80 border border-white/[0.08]"
        >
          <FolderKanban className="h-3 w-3" />
          New Project
        </button>
      </div>

      {/* ---- Main 3-column layout ---- */}
      <div
        className="flex-1 min-h-0 grid transition-all duration-200 relative"
        style={{
          gridTemplateColumns: isMobile
            ? "1fr"
            : `${leftCollapsed ? "48px" : "300px"} 1fr ${showRightPanel ? "360px" : "0px"}`,
          gridTemplateRows: "1fr auto",
        }}
      >
        {/* ---- Left panel ---- */}
        {isMobile ? (
          <>
            {/* Mobile: explorer toggle button */}
            {leftCollapsed && (
              <button
                className="absolute top-2 left-2 z-20 h-8 w-8 min-h-[44px] min-w-[44px] rounded-md bg-card border border-white/[0.1] flex items-center justify-center text-muted-foreground hover:text-foreground"
                onClick={() => setLeftCollapsed(false)}
              >
                <PanelLeftOpen className="h-3.5 w-3.5" />
              </button>
            )}
            {/* Mobile: overlay panel */}
            <AnimatePresence>
              {!leftCollapsed && (
                <>
                  <motion.div
                    className="fixed inset-0 bg-black/50 z-30"
                    initial={{ opacity: 0 }}
                    animate={{ opacity: 1 }}
                    exit={{ opacity: 0 }}
                    onClick={() => setLeftCollapsed(true)}
                  />
                  <motion.div
                    className="fixed left-0 top-0 bottom-0 w-[280px] z-40 bg-card border-r border-white/[0.1] flex flex-col"
                    initial={{ x: -280 }}
                    animate={{ x: 0 }}
                    exit={{ x: -280 }}
                    transition={{ type: "spring", damping: 25, stiffness: 300 }}
                  >
                    <div className="flex items-center justify-between px-3 py-2 border-b border-white/[0.1]">
                      <span className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Explorer</span>
                      <button
                        onClick={() => setLeftCollapsed(true)}
                        className="h-8 w-8 min-h-[44px] min-w-[44px] flex items-center justify-center text-muted-foreground hover:text-foreground"
                      >
                        <X className="h-4 w-4" />
                      </button>
                    </div>
                    <div className="flex-1 min-h-0 flex flex-col">
                      <UnifiedExplorer
                        issues={issues}
                        projects={projects}
                        search={issueSearch}
                        onSearchChange={setIssueSearch}
                        selectedIssue={selectedIssue}
                        selectedProjectId={selectedProjectId}
                        onProjectSelect={(id) => {
                          const newId = id === selectedProjectId ? null : id
                          setSelectedProjectId(newId)
                          if (newId) { setSelectedIssue(null); setIssueComments([]) }
                        }}
                        onIssueSelect={handleIssueSelect}
                        crews={panelCrews}
                        missions={panelMissions}
                        onTaskSelect={handleInboxTaskSelect}
                        filterCrewId={filterCrewId}
                        onCrewFilter={setFilterCrewId}
                        filterAgentId={filterAgentId}
                        onAgentFilter={setFilterAgentId}
                      />
                    </div>
                  </motion.div>
                </>
              )}
            </AnimatePresence>
          </>
        ) : (
          /* Desktop: grid column left panel */
          <div className={cn(
            "row-span-1 border-r border-white/[0.1] bg-card flex flex-col min-h-0 transition-all duration-200 overflow-hidden",
          )}>
            {/* Toggle */}
            <div className="flex items-center justify-between px-2 py-1.5 border-b border-border shrink-0">
              {!leftCollapsed && (
                <span className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">
                  Explorer
                </span>
              )}
              <Button
                variant="ghost"
                size="icon-xs"
                className="text-muted-foreground/70 hover:text-foreground/70 ml-auto"
                onClick={() => setLeftCollapsed(!leftCollapsed)}
              >
                {leftCollapsed ? <PanelLeftOpen className="h-3.5 w-3.5" /> : <PanelLeftClose className="h-3.5 w-3.5" />}
              </Button>
            </div>

            <AnimatePresence mode="wait">
              {!leftCollapsed && (
                <motion.div
                  key={selectedMissionId}
                  initial={{ opacity: 0, x: -8 }}
                  animate={{ opacity: 1, x: 0 }}
                  exit={{ opacity: 0, x: -8 }}
                  transition={{ duration: 0.2, ease: "easeOut" }}
                  className="flex-1 min-h-0 flex flex-col"
                >
                  <UnifiedExplorer
                    issues={issues}
                    projects={projects}
                    search={issueSearch}
                    onSearchChange={setIssueSearch}
                    selectedIssue={selectedIssue}
                    selectedProjectId={selectedProjectId}
                    onProjectSelect={(id) => {
                      const newId = id === selectedProjectId ? null : id
                      setSelectedProjectId(newId)
                      setSelectedIssue(null); setIssueComments([])
                    }}
                    onIssueSelect={handleIssueSelect}
                    crews={panelCrews}
                    missions={panelMissions}
                    onTaskSelect={handleInboxTaskSelect}
                    filterCrewId={filterCrewId}
                    onCrewFilter={setFilterCrewId}
                    filterAgentId={filterAgentId}
                    onAgentFilter={setFilterAgentId}
                  />
                </motion.div>
              )}
            </AnimatePresence>
          </div>
        )}

        {/* ---- Center content area ---- */}
        <div className="row-span-1 relative overflow-hidden min-h-0">
          {activeTab === "issues" && (
            <div className="h-full overflow-auto">
              <IssuesToolbarStrip
                issueViewMode={issueViewMode}
                onViewModeChange={setIssueViewMode}
                savedViews={savedViews}
                savedViewsOpen={savedViewsOpen}
                onSavedViewsOpenChange={setSavedViewsOpen}
                activeViewId={activeViewId}
                onActiveViewChange={(id, viewType) => {
                  setActiveViewId(id)
                  if (viewType) setIssueViewMode(viewType)

                  // Clearing the selected view ("All Issues"): reset filters to
                  // the same defaults used when the page initialises.
                  if (id === null) {
                    setSelectedProjectId(null)
                    setFilterCrewId(null)
                    setFilterAgentId(null)
                    setIssueSearch("")
                    return
                  }

                  // Look up the saved view and apply any filter fields that
                  // map onto the state consumed by `filteredIssues`. The
                  // `filters_json` payload schema is flexible; we apply what
                  // is clearly mappable and ignore anything else.
                  const view = savedViews.find((v) => v.id === id)
                  if (!view) return
                  try {
                    const parsed: Record<string, unknown> = view.filters_json
                      ? JSON.parse(view.filters_json)
                      : {}
                    const projectId = parsed.project_id ?? parsed.projectId
                    setSelectedProjectId(
                      typeof projectId === "string" ? projectId : null,
                    )
                    const crewId = parsed.crew_id ?? parsed.crewId
                    setFilterCrewId(typeof crewId === "string" ? crewId : null)
                    const agentId =
                      parsed.assignee_id ?? parsed.assigneeId ?? parsed.agent_id
                    setFilterAgentId(
                      typeof agentId === "string" ? agentId : null,
                    )
                    const search = parsed.search ?? parsed.query
                    setIssueSearch(typeof search === "string" ? search : "")
                    // TODO: wire up status/label/priority filters and
                    // sort_json once the issue list supports them.
                  } catch {
                    /* ignore malformed filters_json */
                  }
                }}
              />
              {/* Board or List view */}
              <div className="p-4 h-[calc(100%-45px)]">
                <AnimatePresence mode="wait">
                  <motion.div
                    key={`${issueViewMode}-${filterCrewId || "all"}-${filterAgentId || "all"}-${selectedProjectId || filterProjectId || "all"}`}
                    initial={{ opacity: 0, y: 6 }}
                    animate={{ opacity: 1, y: 0 }}
                    exit={{ opacity: 0, y: -6 }}
                    transition={{ duration: 0.15, ease: "easeOut" }}
                    className="h-full"
                  >
                    {issueViewMode === "board" ? (
                      <IssuesBoardInline issues={filteredIssues} onIssueClick={handleIssueSelect} selectedIssueId={selectedIssue?.id} />
                    ) : (
                      <IssuesListInline issues={filteredIssues} onIssueClick={handleIssueSelect} selectedIssueId={selectedIssue?.id} />
                    )}
                  </motion.div>
                </AnimatePresence>
              </div>
            </div>
          )}

          {activeTab === "graph" && (
            <>
              <WorkflowGraph
                missions={filteredMissions}
                crews={crews}
                agents={agents}
                connections={connections}
                onTaskClick={handleNodeClick}
                highlightAgentSlug={selectedAgentSlug}
              />

            </>
          )}

          <AnimatePresence mode="wait">
            {activeTab === "timeline" && (
              <motion.div key="timeline" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.15 }} className="p-4 h-full overflow-auto">
                <MissionTimeline missions={filteredMissions} highlightSlugs={highlightSlugs} />
              </motion.div>
            )}

            {activeTab === "activity" && (
              <motion.div key="activity" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.15 }} className="p-4 h-full overflow-auto">
                <OrchestrationActivity missions={filteredMissions} highlightSlugs={highlightSlugs} />
              </motion.div>
            )}


            {activeTab === "proposals" && (
              <motion.div key="proposals" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.15 }} className="p-4 h-full overflow-auto">
                <ProposalReview workspaceId={workspaceId} />
              </motion.div>
            )}


          </AnimatePresence>
        </div>

        {/* ---- Right panel ---- */}
        {isMobile ? (
          <AnimatePresence>
            {showRightPanel && (
              <motion.div
                className="fixed inset-0 z-40 bg-card flex flex-col"
                initial={{ x: "100%" }}
                animate={{ x: 0 }}
                exit={{ x: "100%" }}
                transition={{ type: "spring", damping: 25, stiffness: 300 }}
              >
                <div className="flex items-center gap-2 px-3 py-2 border-b border-white/[0.1] shrink-0">
                  <button
                    onClick={closeMobileDetail}
                    className="h-8 w-8 min-h-[44px] min-w-[44px] flex items-center justify-center text-muted-foreground hover:text-foreground"
                  >
                    <ChevronLeft className="h-4 w-4" aria-hidden="true" />
                  </button>
                  <span className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Detail</span>
                </div>
                <div className="flex-1 overflow-y-auto">
                  <RightPanelContent
                    selectedIssue={selectedIssue}
                    issueComments={issueComments}
                    issueLabels={issueLabels}
                    projects={projects}
                    selectedProject={selectedProject}
                    workspaceId={workspaceId}
                    detailContext={detailContext}
                    onIssueClose={handleIssueClose}
                    onIssueUpdated={handleIssueUpdated}
                    onProjectClose={handleProjectClose}
                    onProjectUpdated={fetchProjects}
                    onDetailClose={handleDetailClose}
                    onTaskAction={handleTaskAction}
                  />
                </div>
              </motion.div>
            )}
          </AnimatePresence>
        ) : (
          <div className={cn(
            "row-span-1 transition-all duration-200 overflow-hidden min-h-0",
            showRightPanel ? "w-full" : "w-0",
          )}>
            <AnimatePresence mode="wait">
              {showRightPanel && (
                <motion.div
                  key={detailContext.type === "task" ? `task-${(detailContext as { task: MissionTask }).task.id}` : detailContext.type}
                  initial={{ opacity: 0, x: 12 }}
                  animate={{ opacity: 1, x: 0 }}
                  exit={{ opacity: 0, x: 12 }}
                  transition={{ duration: 0.15, ease: "easeOut" }}
                  className="h-full"
                >
                  <RightPanelContent
                    selectedIssue={selectedIssue}
                    issueComments={issueComments}
                    issueLabels={issueLabels}
                    projects={projects}
                    selectedProject={selectedProject}
                    workspaceId={workspaceId}
                    detailContext={detailContext}
                    onIssueClose={handleIssueClose}
                    onIssueUpdated={handleIssueUpdated}
                    onProjectClose={handleProjectClose}
                    onProjectUpdated={fetchProjects}
                    onDetailClose={handleDetailClose}
                    onTaskAction={handleTaskAction}
                  />
                </motion.div>
              )}
            </AnimatePresence>
          </div>
        )}

        {/* ---- Bottom drawer ---- */}
        <motion.div
          className={cn("border-t border-white/[0.1] bg-card flex flex-col overflow-hidden", isMobile ? "col-span-1" : "col-span-3")}
          animate={{ height: drawerOpen ? 240 : 32 }}
          transition={{ duration: 0.2, ease: "easeInOut" }}
        >
          {/* Drawer tab bar */}
          <div
            className="flex items-center gap-0 px-2 shrink-0 h-8 cursor-pointer select-none"
            onClick={() => {
              if (!drawerOpen) setDrawerOpen(true)
            }}
          >
            {ORCH_DRAWER_TABS.map(({ id, label, icon: Icon }) => (
              <button
                key={id}
                className={cn(
                  "flex items-center gap-1.5 px-3 py-1 text-[11px] font-medium rounded-t transition-colors",
                  drawerOpen && drawerTab === id
                    ? "text-foreground bg-accent/50"
                    : "text-muted-foreground hover:text-foreground/70",
                )}
                onClick={(e) => {
                  e.stopPropagation()
                  handleDrawerTabClick(id)
                }}
              >
                <Icon className="h-3 w-3" />
                {!isMobile && label}
              </button>
            ))}

            <div className="ml-auto">
              <Button
                variant="ghost"
                size="icon-xs"
                className="text-muted-foreground/70 hover:text-foreground/70"
                onClick={(e) => {
                  e.stopPropagation()
                  setDrawerOpen(!drawerOpen)
                }}
              >
                {drawerOpen ? <ChevronDown className="h-3 w-3" /> : <ChevronUp className="h-3 w-3" />}
              </Button>
            </div>
          </div>

          {/* Drawer content */}
          <AnimatePresence mode="wait">
            {drawerOpen && (
              <motion.div
                key={drawerTab}
                initial={{ opacity: 0, y: 8 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, y: 8 }}
                transition={{ duration: 0.15 }}
                className="flex-1 min-h-0 border-t border-border"
              >
                {drawerTab === "messages" && (
                  <LiveMessagesPanel />
                )}

                {drawerTab === "exec" && (
                  <ExecLogPanel />
                )}

                {drawerTab === "yaml" && (
                  <MissionYamlEditor
                    mission={selectedMission}
                    readOnly
                  />
                )}

                {drawerTab === "docker" && (
                  <DockerOverview crews={crews} />
                )}
              </motion.div>
            )}
          </AnimatePresence>
        </motion.div>
      </div>

      {/* Create modals */}
      <CreateIssueModal
        open={showCreateIssue}
        onOpenChange={setShowCreateIssue}
        crews={crews}
        labels={issueLabels}
        projects={projects}
        workspaceId={workspaceId}
        onCreated={() => { fetchIssues(); fetchProjects() }}
      />
      <CreateProjectModal
        open={showCreateProject}
        onOpenChange={setShowCreateProject}
        crews={crews}
        labels={issueLabels}
        workspaceId={workspaceId}
        onCreated={fetchProjects}
      />
    </div>
  )
}
