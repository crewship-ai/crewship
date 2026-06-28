"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  Workflow, Clock, Activity, GitBranch,
  PanelLeftClose, PanelLeftOpen,
  FileCode2, Container,
  ChevronUp, ChevronDown, ChevronLeft, ChevronRight, X,
  CircleDot, FolderKanban, ScrollText,
  Play, GitCompareArrows, MessageCircle,
} from "lucide-react"
// Tabs replaced with custom nav for orchestration toolbar
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { WorkflowGraph } from "@/components/features/orchestration/workflow-graph"
import { PipelineDetailSheet } from "@/components/features/orchestration/pipeline-detail-sheet"
import { usePipelines } from "@/hooks/use-pipelines"
import { MissionTimeline } from "@/components/features/orchestration/mission-timeline"
import { OrchestrationActivity } from "@/components/features/orchestration/orchestration-activity"
// TemplateGallery removed — workflow templates not needed in orchestration UI yet
// MissionControlBar replaced by inline info strip in unified toolbar
// CrewConnections moved to Settings
import { type DetailContext } from "@/components/features/orchestration/context-detail-panel"
import { MissionYamlEditor } from "@/components/features/orchestration/mission-yaml-editor"
import { DockerOverview } from "@/components/features/orchestration/docker-overview"
import type { Mission, MissionTask, IssueLabel, Project, SavedView } from "@/lib/types/mission"
import type { CrewSummary, AgentSummary, CrewConnection } from "@/lib/types/orchestration"
import { useIsMobile } from "@/hooks/use-mobile"
import { useUserPreference } from "@/hooks/use-user-preference"
import { useFilteredIssues } from "@/hooks/use-filtered-issues"
import { useIssueDetail } from "@/hooks/use-issue-detail"
import { useProjectDetail } from "@/hooks/use-project-detail"
import { parseSavedViews, applySavedView } from "@/lib/saved-views"
import { IssuesBoardInline, IssuesListInline, IssueDetailInline, ProjectDetailInline } from "@/components/features/orchestration/issues-inline"
import { IssuesStatusChips } from "@/components/features/issues/issues-status-chips"
import type { IssuePriority, MissionStatus } from "@/lib/types/mission"
import { UnifiedExplorer } from "@/components/features/orchestration/unified-explorer"
import { CreateIssueModal } from "@/components/features/orchestration/create-issue-modal"
import { CreateProjectModal } from "@/components/features/orchestration/create-project-modal"

import { toast } from "sonner"
import { useAppStore } from "@/lib/store"
import type { BreadcrumbItem } from "@/lib/store"
import { ActivityTab } from "@/components/features/crews/bottom-panel/activity-tab"
import { RunsTab } from "@/components/features/crews/bottom-panel/runs-tab"
import { ChangesTab } from "@/components/features/crews/bottom-panel/changes-tab"
import { CommentsTab } from "@/components/features/crews/bottom-panel/comments-tab"
import type { BottomPanelContext } from "@/components/features/crews/bottom-panel/types"
import { RightPanelContent } from "@/components/features/orchestration/right-panel-content"
import { IssuesToolbarStrip } from "@/components/features/orchestration/issues-toolbar-strip"
import { RoutinesTab } from "@/components/features/routines/routines-tab"
import { RunsView } from "@/components/features/activity/runs-view"

// Issue-scoped drawer tabs. The old set (messages/exec) was agent-scoped
// and showed nothing on an issue with no agent selected; these are the
// entity the page is actually about — the issue/mission in focus.
type DrawerTab = "activity" | "runs" | "changes" | "comments" | "spec" | "docker"

// Page mode controls which top-level tabs are visible. Issues and
// Routines now live as their own top-level pages (/issues, /routines);
// /activity is the live observability surface (Graph + Timeline + Feed).
// "default" keeps the legacy 5-tab container — nothing in-tree links to
// it after the IA refactor, but it stays so /orchestration → /activity
// can be a soft redirect rather than a hard breaking change.
export type OrchestrationMode = "issues" | "activity" | "default"

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
  mode?: OrchestrationMode
}

const ORCH_DRAWER_TABS = [
  { id: "activity" as const, label: "Activity", icon: Activity },
  { id: "runs" as const, label: "Runs", icon: Play },
  { id: "changes" as const, label: "Changes", icon: GitCompareArrows },
  { id: "comments" as const, label: "Comments", icon: MessageCircle },
  { id: "spec" as const, label: "Spec", icon: FileCode2 },
  { id: "docker" as const, label: "Docker", icon: Container },
]

const ORCH_TABS = [
  { id: "issues", label: "Issues", icon: CircleDot },
  { id: "runs", label: "Runs", icon: GitBranch },
  { id: "graph", label: "Graph", icon: Workflow },
  { id: "timeline", label: "Timeline", icon: Clock },
  { id: "activity", label: "Feed", icon: Activity },
  { id: "routines", label: "Routines", icon: ScrollText },
] as const

// Tabs visible in each mode. /issues hides everything except its own
// content (no tab bar at all). /activity exposes "Runs" as the
// primary surface (the issue→routine→steps→agent tree) with the
// older Graph/Timeline/Feed kept as secondary observability views
// until they're either folded into Runs or retired.
const TAB_IDS_BY_MODE: Record<OrchestrationMode, ReadonlyArray<typeof ORCH_TABS[number]["id"]>> = {
  issues: [],
  activity: ["runs", "graph", "timeline", "activity"],
  default: ORCH_TABS.map((t) => t.id),
}

const DEFAULT_TAB_BY_MODE: Record<OrchestrationMode, typeof ORCH_TABS[number]["id"]> = {
  issues: "issues",
  activity: "runs",
  default: "issues",
}

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
  mode = "default",
}: OrchestrationLayoutProps) {
  const isMobile = useIsMobile()

  // Panel state
  const [leftCollapsed, setLeftCollapsed] = useState(false)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [drawerTab, setDrawerTab] = useState<DrawerTab>("activity")

  // Content state — initial active tab depends on the page mode.
  // /issues opens on Issues; /activity opens on Graph; /orchestration
  // (legacy) opens on Issues for backwards compatibility.
  const [activeTab, setActiveTab] = useState<typeof ORCH_TABS[number]["id"]>(DEFAULT_TAB_BY_MODE[mode])

  // Visible tabs filtered by mode. Issues mode hides the tab bar
  // entirely (length 0); activity mode shows only the three observability
  // sub-tabs; default mode shows everything.
  const visibleTabs = useMemo(
    () => ORCH_TABS.filter((t) => TAB_IDS_BY_MODE[mode].includes(t.id)),
    [mode],
  )

  // If the URL switches between modes mid-session (router back/forward),
  // make sure activeTab settles on a tab that's actually visible.
  useEffect(() => {
    if (visibleTabs.length === 0) {
      setActiveTab(DEFAULT_TAB_BY_MODE[mode])
      return
    }
    if (!visibleTabs.some((t) => t.id === activeTab)) {
      setActiveTab(DEFAULT_TAB_BY_MODE[mode])
    }
  }, [mode, visibleTabs, activeTab])
  const [_selectedTask, setSelectedTask] = useState<MissionTask | null>(null)
  const [selectedCrewId] = useState<string | null>(null)
  const [selectedAgentSlug] = useState<string | null>(null)
  const [detailContext, setDetailContext] = useState<DetailContext>({ type: "none" })

  // Issues state
  const [issues, setIssues] = useState<Mission[]>([])
  const [issueLabels, setIssueLabels] = useState<IssueLabel[]>([])
  // Persisted per-user — most teams stick with one of board/list and a
  // refresh shouldn't bounce them back to board if they prefer list.
  const [issueViewMode, setIssueViewMode] = useUserPreference<"board" | "list">(
    "orchestration.issues.viewMode",
    "board",
  )
  const [issueSearch, setIssueSearch] = useState("")
  const [projects, setProjects] = useState<Project[]>([])
  // Project filter applied via saved views — does NOT open the detail panel.
  // `selectedProjectId` is the authoritative "user navigated to this project"
  // state and is the only thing that opens the right-hand detail panel.
  // `filterProjectId` is the saved-view-scoped filter and feeds only
  // `useFilteredIssues`. Keeping them separate (issue #320) means applying
  // or clearing a saved view's project filter never opens or closes the
  // detail panel, and explicitly clicking a project never leaks back into
  // the saved-view's filter state.
  const [filterProjectId, setFilterProjectId] = useState<string | null>(null)
  const [filterCrewId, setFilterCrewId] = useState<string | null>(null)
  const [filterAgentId, setFilterAgentId] = useState<string | null>(null)
  // Multi-select status filter (empty = show all). Priority filter is
  // single-select because issues only have one priority value.
  const [filterStatuses, setFilterStatuses] = useState<MissionStatus[]>([])
  const [filterPriority, setFilterPriority] = useState<IssuePriority | null>(null)
  const [showCreateIssue, setShowCreateIssue] = useState(false)
  const [showCreateProject, setShowCreateProject] = useState(false)

  // Saved views
  const [savedViews, setSavedViews] = useState<SavedView[]>([])
  const [activeViewId, setActiveViewId] = useState<string | null>(null)
  const [savedViewsOpen, setSavedViewsOpen] = useState(false)

  // graphRef removed — was unused

  // Pipelines surface in the Graph tab as a registry row of
  // pipelineRun nodes along the bottom. Fetched once on mount + on
  // refresh; the WS hub will eventually push pipeline.run.* events
  // to update node status live, but for MVP polling on-demand is
  // enough — pipelines change rarely (agent saves, user invokes).
  const { pipelines } = usePipelines(workspaceId)

  // Pipeline detail side-sheet state. Opened by clicking a
  // PipelineRunNode in the graph; carries the slug so the sheet
  // can fetch detail + versions + runs against the public API.
  const [selectedPipelineSlug, setSelectedPipelineSlug] = useState<string | null>(null)
  const [pipelineSheetOpen, setPipelineSheetOpen] = useState(false)

  // Auto-collapse left panel on mobile
  useEffect(() => {
    if (isMobile) setLeftCollapsed(true)
  }, [isMobile])

  // Keyboard shortcuts. The shortcuts are scoped to the issues page —
  // they fire on any keystroke that isn't typed into a form control,
  // so the user can press `/` from the list and land in the search box
  // without picking up shortcuts while editing an inbox comment etc.
  useEffect(() => {
    if (activeTab !== "issues") return
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null
      const isInputContext =
        target &&
        (target.tagName === "INPUT" ||
          target.tagName === "TEXTAREA" ||
          target.isContentEditable)

      // `/` — focus the issues search input (works only outside inputs)
      if (e.key === "/" && !isInputContext) {
        const el = document.querySelector<HTMLInputElement>(
          "input[data-issues-search-input]",
        )
        if (el) {
          e.preventDefault()
          el.focus()
          el.select()
        }
        return
      }
      // Esc — clear active filters (status/priority/crew/agent/search).
      // Does NOT clear the saved-view choice; that's an explicit action.
      if (e.key === "Escape" && !isInputContext) {
        if (
          filterStatuses.length > 0 ||
          filterPriority ||
          filterCrewId ||
          filterAgentId ||
          issueSearch
        ) {
          e.preventDefault()
          setFilterStatuses([])
          setFilterPriority(null)
          setFilterCrewId(null)
          setFilterAgentId(null)
          setIssueSearch("")
        }
        return
      }
      // `c` — open the create-issue modal
      if (e.key === "c" && !isInputContext && !e.metaKey && !e.ctrlKey) {
        e.preventDefault()
        setShowCreateIssue(true)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [
    activeTab,
    filterStatuses.length,
    filterPriority,
    filterCrewId,
    filterAgentId,
    issueSearch,
  ])

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
      if (res.ok) setSavedViews(parseSavedViews(await res.json()))
    } catch { /* ignore */ }
  }, [workspaceId])

  useEffect(() => {
    fetchIssues()
    fetchIssueLabels()
    fetchProjects()
    fetchSavedViews()
  }, [fetchIssues, fetchIssueLabels, fetchProjects, fetchSavedViews])

  const {
    selectedIssue,
    issueComments,
    handleIssueSelect,
    handleIssueClose,
    handleIssueUpdated,
  } = useIssueDetail({
    workspaceId,
    onIssueSelected: () => setDetailContext({ type: "none" }),
    fetchIssues,
    fetchProjects,
  })

  const {
    selectedProjectId,
    setSelectedProjectId,
    selectedProject,
    handleProjectClose,
  } = useProjectDetail({ projects })

  // Derived data — defined after useIssueDetail so the selectedIssue
  // dependency resolves; when an issue is selected the Graph/Timeline/
  // Activity tabs focus on its single mission.
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

  // Bottom-drawer context for the shared dock tabs (Activity / Runs /
  // Changes / Comments). Prefer `selectedIssue` (the fully-loaded issue
  // detail — always has crew_id + identifier) over `selectedMission`, which
  // is resolved via missions.find() against the limit-50 list and is null
  // whenever the issue isn't in that page (or the list row omits crew_id).
  // That mismatch left every issue tab stuck on "Select an issue…" even with
  // an issue open. crew_id + identifier are required — the issue
  // sub-resource routes are nested under the crew and keyed by identifier.
  const missionCtx = useMemo<BottomPanelContext>(() => {
    const m = selectedIssue ?? selectedMission
    if (!m || !m.identifier || !m.crew_id) return null
    return {
      kind: "mission",
      missionId: m.id,
      identifier: m.identifier,
      title: m.title,
      crewId: m.crew_id,
      crewSlug: m.crew_slug ?? m.crew_id,
    }
  }, [selectedIssue, selectedMission])

  // selectedIssue / selectedProject take over the middle pane (same
  // pattern as /routines). When set, the board/list is hidden and the
  // detail renders full-width with a breadcrumb back-arrow at top.
  // Right panel is suppressed entirely for these selections.
  const issueDetailFullWidth = selectedIssue !== null && activeTab === "issues"
  const projectDetailFullWidth =
    selectedProjectId !== null && !selectedIssue && activeTab === "issues"

  const filteredIssues = useFilteredIssues({
    issues,
    search: issueSearch,
    selectedProjectId,
    filterProjectId,
    filterCrewId,
    filterAgentId,
    filterStatuses,
    filterPriority,
  })

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

  // Routines tab manages its own right-side detail (RoutinesDetailPanel
  // inside RoutinesTab). Suppress the orchestration-level detail pane
  // there so an issue/task selected from a previous tab doesn't bleed
  // into the routines layout and shrink the routines list view.
  // Right panel still shows for task drilldowns, but issue + project
  // detail both moved into the middle pane (full-width) — same pattern
  // as /routines. The right rail was cramped (520px); the editor /
  // activity / properties all benefit from real horizontal space.
  const showRightPanel =
    activeTab !== "routines" &&
    !issueDetailFullWidth &&
    !projectDetailFullWidth &&
    detailContext.type !== "none"

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
      items.push({ label: selectedProject.name, onClick: handleIssueClose })
    }
    if (selectedIssue) {
      items.push({ label: selectedIssue.identifier || selectedIssue.title })
    }
    setBreadcrumbs(items)
    return () => setBreadcrumbs([])
  }, [selectedProject, selectedIssue, setBreadcrumbs, handleIssueClose])

  // Toolbar surfaces are mode-dependent:
  //   - issues: hide tab bar, show New Issue + New Project buttons
  //   - activity: show Graph/Timeline/Feed sub-tabs, no create buttons
  //   - default: legacy — everything visible
  const showCreateButtons = mode === "issues" || mode === "default"
  const showToolbar = visibleTabs.length > 0 || showCreateButtons

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* ---- Toolbar: Tab navigation + context + actions (single row) ---- */}
      {showToolbar && (
        <div className="shrink-0 z-20 flex items-center h-9 bg-card border-b border-white/[0.08] px-2 sm:px-3 gap-0 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
          {/* Tabs */}
          {visibleTabs.map(({ id, label, icon: Icon }) => (
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

          {/* Create buttons — only relevant when issues UI is on this page */}
          {showCreateButtons && (
            <>
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
            </>
          )}
        </div>
      )}

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
                          if (newId) handleIssueClose()
                        }}
                        onIssueSelect={handleIssueSelect}
                        crews={panelCrews}
                        missions={panelMissions}
                        onTaskSelect={handleInboxTaskSelect}
                        filterCrewId={filterCrewId}
                        onCrewFilter={setFilterCrewId}
                        filterAgentId={filterAgentId}
                        onAgentFilter={setFilterAgentId}
                        filterPriority={filterPriority}
                        onPriorityFilter={setFilterPriority}
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
                      handleIssueClose()
                    }}
                    onIssueSelect={handleIssueSelect}
                    crews={panelCrews}
                    missions={panelMissions}
                    onTaskSelect={handleInboxTaskSelect}
                    filterCrewId={filterCrewId}
                    onCrewFilter={setFilterCrewId}
                    filterAgentId={filterAgentId}
                    onAgentFilter={setFilterAgentId}
                    filterPriority={filterPriority}
                    onPriorityFilter={setFilterPriority}
                  />
                </motion.div>
              )}
            </AnimatePresence>
          </div>
        )}

        {/* ---- Center content area ---- */}
        <div className="row-span-1 relative overflow-hidden min-h-0">
          {activeTab === "issues" && issueDetailFullWidth && selectedIssue && (
            <div className="flex h-full flex-col overflow-hidden">
              {/* Breadcrumb back-bar — clicking 'Issues' or the X closes
                  the detail and returns to the board/list. */}
              <div className="flex shrink-0 items-center gap-2 border-b border-border bg-card/40 px-4 py-2">
                <button
                  type="button"
                  onClick={handleIssueClose}
                  className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                >
                  <ChevronLeft className="h-3.5 w-3.5" />
                  Back to issues
                </button>
                <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground/40" />
                <span className="truncate font-mono text-xs text-muted-foreground">
                  {selectedIssue.identifier || selectedIssue.id.slice(0, 8)}
                </span>
                <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground/40" />
                <span className="truncate text-xs font-medium text-foreground/85">
                  {selectedIssue.title}
                </span>
              </div>
              {/* Full-width issue detail — slides + fades when switching
                  between issues (key on selectedIssue.id) so the
                  navigation feels continuous instead of a hard swap. */}
              <div className="relative flex-1 overflow-hidden">
                <AnimatePresence mode="wait">
                  <motion.div
                    key={`issue-${selectedIssue.id}`}
                    initial={{ opacity: 0, x: 12 }}
                    animate={{ opacity: 1, x: 0 }}
                    exit={{ opacity: 0, x: -12 }}
                    transition={{ duration: 0.18, ease: "easeOut" }}
                    className="absolute inset-0 overflow-hidden"
                  >
                    <IssueDetailInline
                      issue={selectedIssue}
                      comments={issueComments}
                      labels={issueLabels}
                      projects={projects}
                      routines={pipelines}
                      workspaceId={workspaceId}
                      onClose={handleIssueClose}
                      onUpdated={handleIssueUpdated}
                    />
                  </motion.div>
                </AnimatePresence>
              </div>
            </div>
          )}
          {activeTab === "issues" && projectDetailFullWidth && selectedProject && (
            <div className="flex h-full flex-col overflow-hidden">
              {/* Breadcrumb back-bar — matches the issue detail pattern. */}
              <div className="flex shrink-0 items-center gap-2 border-b border-border bg-card/40 px-4 py-2">
                <button
                  type="button"
                  onClick={handleProjectClose}
                  className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                >
                  <ChevronLeft className="h-3.5 w-3.5" />
                  Back to issues
                </button>
                <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground/40" />
                <span className="truncate text-xs font-medium text-muted-foreground">
                  Project
                </span>
                <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground/40" />
                <span className="truncate text-xs font-medium text-foreground/85">
                  {selectedProject.name}
                </span>
              </div>
              <div className="relative flex-1 overflow-hidden">
                <AnimatePresence mode="wait">
                  <motion.div
                    key={`project-${selectedProject.id}`}
                    initial={{ opacity: 0, x: 12 }}
                    animate={{ opacity: 1, x: 0 }}
                    exit={{ opacity: 0, x: -12 }}
                    transition={{ duration: 0.18, ease: "easeOut" }}
                    className="absolute inset-0 overflow-hidden"
                  >
                    <ProjectDetailInline
                      project={selectedProject}
                      workspaceId={workspaceId}
                      onClose={handleProjectClose}
                      onUpdated={fetchProjects}
                    />
                  </motion.div>
                </AnimatePresence>
              </div>
            </div>
          )}
          {activeTab === "issues" && !issueDetailFullWidth && !projectDetailFullWidth && (
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

                  // Clearing the selected view ("All Issues"): reset filters
                  // to the same defaults used when the page initialises.
                  // We deliberately clear `filterProjectId` (the saved-view
                  // filter) but leave `selectedProjectId` alone — explicit
                  // project-detail navigation should survive a saved-view
                  // change. The user can close the detail panel separately.
                  if (id === null) {
                    setFilterProjectId(null)
                    setFilterCrewId(null)
                    setFilterAgentId(null)
                    setFilterStatuses([])
                    setFilterPriority(null)
                    setIssueSearch("")
                    return
                  }

                  // Look up the saved view and apply any filter fields that
                  // map onto the state consumed by `filteredIssues`.
                  //
                  // Issue #320: write the view's project_id to
                  // `filterProjectId`, NOT `selectedProjectId`. Conflating
                  // the two (the pre-#318 behaviour) caused saved-view
                  // application to silently open the detail panel for the
                  // view's project, and explicit project clicks to leak
                  // back into the saved view's filter set.
                  //
                  // TODO: wire up status/label/priority filters and
                  // sort_json once the issue list supports them.
                  const view = savedViews.find((v) => v.id === id)
                  if (!view) return
                  const f = applySavedView(view)
                  setFilterProjectId(f.projectId)
                  setFilterCrewId(f.crewId)
                  setFilterAgentId(f.agentId)
                  setIssueSearch(f.search)
                }}
              />
              {/* Status filter chips — multi-select row over the list.
                  Counts derive from the pre-status-filter set so the
                  user can see how many issues are in each status without
                  losing the current crew/agent/project filter context. */}
              <IssuesStatusChips
                issues={filteredIssues}
                selected={filterStatuses}
                onToggle={(s) =>
                  setFilterStatuses((prev) =>
                    prev.includes(s) ? prev.filter((x) => x !== s) : [...prev, s],
                  )
                }
                onClear={() => setFilterStatuses([])}
              />
              {/* Board or List view */}
              <div className="px-4 pb-4 h-[calc(100%-90px)]">
                <AnimatePresence mode="wait">
                  <motion.div
                    key={`${issueViewMode}-${filterCrewId || "all"}-${filterAgentId || "all"}-${selectedProjectId || filterProjectId || "all"}-${filterStatuses.join(",") || "all"}-${filterPriority || "all"}`}
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

          {activeTab === "runs" && (
            <RunsView workspaceId={workspaceId} />
          )}

          {activeTab === "graph" && (
            <>
              <WorkflowGraph
                missions={filteredMissions}
                crews={crews}
                agents={agents}
                connections={connections}
                // When the user has narrowed to one issue, the pipeline
                // strip narrows with it: show only the bound routine
                // (if any), not every routine in the workspace. The
                // un-narrowed strip was the noise the user kept
                // calling out.
                pipelines={
                  selectedIssue
                    ? pipelines.filter((p) => p.id === selectedIssue.routine_id)
                    : pipelines
                }
                onPipelineClick={(id) => {
                  // The graph node carries the pipeline ID, but the
                  // detail sheet fetches by slug (public API path
                  // shape). Cross-reference via the pipelines list
                  // we already loaded above.
                  const p = pipelines.find((x) => x.id === id)
                  if (p) {
                    setSelectedPipelineSlug(p.slug)
                    setPipelineSheetOpen(true)
                  }
                }}
                onTaskClick={handleNodeClick}
                highlightAgentSlug={selectedAgentSlug}
              />
              <PipelineDetailSheet
                workspaceId={workspaceId}
                slug={selectedPipelineSlug}
                open={pipelineSheetOpen}
                onClose={() => setPipelineSheetOpen(false)}
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

            {activeTab === "routines" && (
              <motion.div key="routines" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.15 }} className="h-full overflow-hidden">
                <RoutinesTab workspaceId={workspaceId} />
              </motion.div>
            )}

          </AnimatePresence>
        </div>

        {/* ---- Right panel ---- */}
        {(() => {
          // RightPanelContent is rendered identically in mobile and desktop
          // layouts; bundling its props once avoids two duplicate 12-prop
          // call sites.
          const rightPanelProps = {
            selectedIssue,
            issueComments,
            issueLabels,
            projects,
            routines: pipelines,
            selectedProject,
            workspaceId,
            detailContext,
            onIssueClose: handleIssueClose,
            onIssueUpdated: handleIssueUpdated,
            onProjectClose: handleProjectClose,
            onProjectUpdated: fetchProjects,
            onDetailClose: handleDetailClose,
            onTaskAction: handleTaskAction,
          } as const

          return isMobile ? (
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
                      aria-label="Back"
                      className="h-8 w-8 min-h-[44px] min-w-[44px] flex items-center justify-center text-muted-foreground hover:text-foreground"
                    >
                      <ChevronLeft className="h-4 w-4" aria-hidden="true" />
                    </button>
                    <span className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Detail</span>
                  </div>
                  <div className="flex-1 overflow-y-auto">
                    <RightPanelContent {...rightPanelProps} />
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
                    <RightPanelContent {...rightPanelProps} />
                  </motion.div>
                )}
              </AnimatePresence>
            </div>
          )
        })()}

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
                {!missionCtx && drawerTab !== "docker" && (
                  <div className="h-full grid place-items-center text-xs text-muted-foreground p-4 text-center">
                    Select an issue to inspect its {drawerTab}.
                  </div>
                )}

                {missionCtx && drawerTab === "activity" && (
                  <ActivityTab workspaceId={workspaceId} context={missionCtx} />
                )}

                {missionCtx && drawerTab === "runs" && (
                  <RunsTab workspaceId={workspaceId} context={missionCtx} />
                )}

                {missionCtx && drawerTab === "changes" && (
                  <ChangesTab workspaceId={workspaceId} context={missionCtx} />
                )}

                {missionCtx && drawerTab === "comments" && (
                  <CommentsTab workspaceId={workspaceId} context={missionCtx} />
                )}

                {drawerTab === "spec" && (
                  <MissionYamlEditor
                    mission={selectedIssue ?? selectedMission}
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
        routines={pipelines}
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
