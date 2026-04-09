"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { motion, AnimatePresence } from "motion/react"
import {
  Workflow, Clock, Activity, RefreshCw, Focus,
  FileText, PanelLeftClose, PanelLeftOpen,
  MessageSquare, Terminal, FileCode2, Container,
  ChevronUp, ChevronDown, ChevronLeft, X, Play, Square, Loader2,
  CircleDot, LayoutGrid, List, Plus, FolderKanban,
} from "lucide-react"
// Tabs replaced with custom nav for orchestration toolbar
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"
import { WorkflowGraph, type WorkflowGraphRef } from "@/components/features/orchestration/workflow-graph"
import { MissionTimeline } from "@/components/features/orchestration/mission-timeline"
import { OrchestrationActivity } from "@/components/features/orchestration/orchestration-activity"
// TemplateGallery removed — workflow templates not needed in orchestration UI yet
// MissionControlBar replaced by inline info strip in unified toolbar
import { CreateMissionWizard } from "@/components/features/orchestration/create-mission-wizard"
// CrewConnections moved to Settings (CRE-105)
import { ProposalReview } from "@/components/features/orchestration/proposal-review"
import { HierarchyTree } from "@/components/features/orchestration/hierarchy-tree"
import { UnifiedInbox } from "@/components/features/orchestration/unified-inbox"
import { ConnectionMap } from "@/components/features/orchestration/connection-map"
import { ContextDetailPanel, type DetailContext } from "@/components/features/orchestration/context-detail-panel"
import { A2AMessageStream } from "@/components/features/orchestration/a2a-message-stream"
import { MissionYamlEditor } from "@/components/features/orchestration/mission-yaml-editor"
import { DockerOverview } from "@/components/features/orchestration/docker-overview"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import type { Mission, MissionTask, IssueLabel, IssueComment, Project } from "@/lib/types/mission"
import type { CrewSummary, AgentSummary, CrewConnection } from "@/lib/types/orchestration"
import { useIsMobile } from "@/hooks/use-mobile"
import { IssuesExplorerPanel, IssuesBoardInline, IssuesListInline, IssueDetailInline, ProjectDetailInline } from "@/components/features/orchestration/issues-inline"
import { UnifiedExplorer } from "@/components/features/orchestration/unified-explorer"

import { toast } from "sonner"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

const MSG_TYPE_COLORS: Record<string, string> = {
  "task.updated": "text-blue-400",
  "mission.updated": "text-purple-400",
  "mission.started": "text-emerald-400",
  "agent.log": "text-cyan-400",
}
const MSG_TYPE_LABELS: Record<string, string> = {
  "task.updated": "task",
  "mission.updated": "mission",
  "mission.started": "started",
  "agent.log": "log",
}

interface LiveMessage { ts: string; type: string; agent: string; crew: string; content: string; status?: string }

/** Live messages panel — streams task.updated + mission.updated WebSocket events */
function LiveMessagesPanel() {
  const [messages, setMessages] = useState<LiveMessage[]>([])
  const [autoScroll, setAutoScroll] = useState(true)
  const endRef = useRef<HTMLDivElement>(null)

  const handleTaskUpdate = useCallback((ev: RealtimeEvent) => {
    const p = ev.payload
    const agent = (p.agent_slug ?? p.agent ?? "") as string
    const title = (p.title ?? p.task_title ?? "") as string
    const status = (p.status ?? "") as string
    if (!title) return
    setMessages((prev) => [...prev.slice(-150), {
      ts: new Date().toISOString(), type: "task.updated", agent,
      crew: (p.crew_name ?? "") as string,
      content: `${title} → ${status}`, status,
    }])
  }, [])

  const handleMissionUpdate = useCallback((ev: RealtimeEvent) => {
    const p = ev.payload
    const title = (p.title ?? "") as string
    const status = (p.status ?? "") as string
    if (!title) return
    setMessages((prev) => [...prev.slice(-150), {
      ts: new Date().toISOString(), type: "mission.updated", agent: (p.lead_agent_slug ?? "") as string,
      crew: "", content: `${title} → ${status}`, status,
    }])
  }, [])

  useRealtimeEvent("task.updated", handleTaskUpdate)
  useRealtimeEvent("mission.updated", handleMissionUpdate)

  useEffect(() => {
    if (autoScroll) endRef.current?.scrollIntoView({ behavior: "smooth" })
  }, [messages, autoScroll])

  if (messages.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground/50">
        <MessageSquare className="h-5 w-5 mb-1.5" />
        <p className="text-[11px]">No messages yet</p>
        <p className="text-[10px] text-muted-foreground/30 mt-0.5">Task and mission updates appear here in real-time</p>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full">
      <div className="flex items-center justify-between px-3 py-1 border-b border-white/[0.06] shrink-0">
        <span className="text-[10px] text-muted-foreground">{messages.length} messages</span>
        <button onClick={() => setAutoScroll(!autoScroll)} className={cn("text-[10px] px-1.5 py-0.5 rounded", autoScroll ? "text-blue-400 bg-blue-400/10" : "text-muted-foreground")}>
          Auto-scroll {autoScroll ? "ON" : "OFF"}
        </button>
      </div>
      <div className="flex-1 overflow-y-auto font-mono text-[11px] px-3 py-1">
        {messages.map((msg, i) => (
          <div key={i} className="flex items-start gap-2 py-0.5 hover:bg-white/[0.02]">
            <span className="text-muted-foreground/40 tabular-nums shrink-0 w-[52px]">{msg.ts.slice(11, 19)}</span>
            <span className={cn("shrink-0 text-[10px] px-1 rounded", MSG_TYPE_COLORS[msg.type] || "text-muted-foreground", "bg-white/[0.03]")}>
              {MSG_TYPE_LABELS[msg.type] || msg.type}
            </span>
            {msg.agent && <img src={getAgentAvatarUrl(msg.agent)} alt="" className="w-3.5 h-3.5 rounded-full shrink-0 mt-0.5" />}
            {msg.agent && <span className="text-muted-foreground shrink-0 w-[50px] truncate">@{msg.agent}</span>}
            <span className="text-foreground/80 truncate">{msg.content}</span>
          </div>
        ))}
        <div ref={endRef} />
      </div>
    </div>
  )
}

const EVENT_COLORS: Record<string, string> = {
  text: "text-foreground", thinking: "text-muted-foreground", tool_call: "text-cyan-400",
  tool_result: "text-emerald-400", error: "text-red-400", status: "text-amber-400",
  result: "text-purple-400", system: "text-blue-400", rate_limit: "text-amber-400",
}

interface LogEntry { ts: string; agent: string; event: string; content: string }

/** Live exec log panel — streams agent.log WebSocket events */
function ExecLogPanel() {
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [autoScroll, setAutoScroll] = useState(true)
  const endRef = useRef<HTMLDivElement>(null)

  const handleLog = useCallback((ev: RealtimeEvent) => {
    const agent = (ev.payload.agent ?? ev.payload.agent_slug ?? "") as string
    const content = (ev.payload.content ?? "") as string
    const event = (ev.payload.event ?? "text") as string
    if (!content) return
    setLogs((prev) => [...prev.slice(-200), { ts: new Date().toISOString(), agent, event, content: content.length > 200 ? content.slice(0, 197) + "..." : content }])
  }, [])

  useRealtimeEvent("agent.log", handleLog)

  useEffect(() => {
    if (autoScroll) endRef.current?.scrollIntoView({ behavior: "smooth" })
  }, [logs, autoScroll])

  if (logs.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground/50">
        <Terminal className="h-5 w-5 mb-1.5" />
        <p className="text-[11px]">Waiting for agent activity...</p>
        <p className="text-[10px] text-muted-foreground/30 mt-0.5">Logs appear here when agents run</p>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full">
      <div className="flex items-center justify-between px-3 py-1 border-b border-white/[0.06] shrink-0">
        <span className="text-[10px] text-muted-foreground">{logs.length} entries</span>
        <button onClick={() => setAutoScroll(!autoScroll)} className={cn("text-[10px] px-1.5 py-0.5 rounded", autoScroll ? "text-blue-400 bg-blue-400/10" : "text-muted-foreground")}>
          Auto-scroll {autoScroll ? "ON" : "OFF"}
        </button>
      </div>
      <div className="flex-1 overflow-y-auto font-mono text-[11px] px-3 py-1">
        {logs.map((log, i) => (
          <div key={i} className="flex items-start gap-2 py-0.5 hover:bg-white/[0.02]">
            <span className="text-muted-foreground/40 tabular-nums shrink-0 w-[52px]">{log.ts.slice(11, 19)}</span>
            <img src={getAgentAvatarUrl(log.agent)} alt="" className="w-3.5 h-3.5 rounded-full shrink-0 mt-0.5" />
            <span className="text-muted-foreground shrink-0 w-[60px] truncate">@{log.agent}</span>
            <span className={cn("truncate", EVENT_COLORS[log.event] || "text-foreground")}>{log.content}</span>
          </div>
        ))}
        <div ref={endRef} />
      </div>
    </div>
  )
}

type DrawerTab = "messages" | "exec" | "yaml" | "docker"

/** Compact action button for the toolbar (Start/Cancel) */
function MissionActionButton({ mission, action, workspaceId, onDone }: {
  mission: Mission; action: "start" | "cancel"; workspaceId: string; onDone: () => void
}) {
  const [loading, setLoading] = useState(false)
  const handleClick = useCallback(async () => {
    setLoading(true)
    try {
      const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
      const res = action === "start"
        ? await fetch(`/api/v1/crews/${mission.crew_id}/missions/${mission.id}/start${qs}`, { method: "POST" })
        : await fetch(`/api/v1/crews/${mission.crew_id}/missions/${mission.id}${qs}`, {
            method: "PATCH", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ status: "CANCELLED" }),
          })
      if (!res.ok) { const b = await res.json().catch(() => null); toast.error(b?.detail ?? `Failed to ${action}`); return }
      toast.success(action === "start" ? "Mission started" : "Mission cancelled")
      onDone()
    } catch { toast.error(`Failed to ${action}`) } finally { setLoading(false) }
  }, [mission.id, mission.crew_id, workspaceId, action, onDone])

  if (action === "start") {
    return (
      <button onClick={handleClick} disabled={loading} className="inline-flex items-center gap-1 h-[22px] px-2 rounded-[3px] text-[11.5px] font-medium bg-blue-500/15 border border-blue-500/35 text-blue-400 hover:bg-blue-500/25 transition-colors disabled:opacity-50">
        {loading ? <Loader2 className="h-3 w-3 animate-spin" /> : <Play className="h-3 w-3" />}
        Start
      </button>
    )
  }
  return (
    <button onClick={handleClick} disabled={loading} className="inline-flex items-center gap-1 h-[22px] px-2 rounded-[3px] text-[11.5px] font-medium bg-red-500/10 border border-red-500/30 text-red-400 hover:bg-red-500/20 transition-colors disabled:opacity-50">
      {loading ? <Loader2 className="h-3 w-3 animate-spin" /> : <Square className="h-3 w-3" />}
      Cancel
    </button>
  )
}

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

export function OrchestrationLayout({
  missions,
  crews,
  agents,
  connections,
  workspaceId,
  selectedMissionId,
  onMissionChange,
  onRefresh,
  onMissionCreated,
}: OrchestrationLayoutProps) {
  const isMobile = useIsMobile()
  const router = useRouter()

  // Navigate to full-page issue view (from board/list click)
  const handleIssueNavigate = useCallback((issue: Mission) => {
    if (issue.identifier) {
      router.push(`/orchestration/issues/${issue.identifier}`)
    }
  }, [router])

  // Panel state
  const [leftCollapsed, setLeftCollapsed] = useState(false)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [drawerTab, setDrawerTab] = useState<DrawerTab>("messages")

  // Content state
  const [activeTab, setActiveTab] = useState("issues")
  const [_selectedTask, setSelectedTask] = useState<MissionTask | null>(null)
  const [selectedCrewId, setSelectedCrewId] = useState<string | null>(null)
  const [selectedAgentSlug, setSelectedAgentSlug] = useState<string | null>(null)
  const [detailContext, setDetailContext] = useState<DetailContext>({ type: "none" })

  // A2A message filter
  const [a2aCrewFilter, setA2aCrewFilter] = useState<string | null>(null)

  // Issues state
  const [issues, setIssues] = useState<Mission[]>([])
  const [issueLabels, setIssueLabels] = useState<IssueLabel[]>([])
  const [issueViewMode, setIssueViewMode] = useState<"board" | "list">("board")
  const [issueSearch, setIssueSearch] = useState("")
  const [selectedIssue, setSelectedIssue] = useState<Mission | null>(null)
  const [issueComments, setIssueComments] = useState<IssueComment[]>([])
  const [projects, setProjects] = useState<Project[]>([])
  const [selectedProjectId, setSelectedProjectId] = useState<string | null>(null)
  const [filterCrewId, setFilterCrewId] = useState<string | null>(null)
  const [filterAgentId, setFilterAgentId] = useState<string | null>(null)

  const graphRef = useRef<WorkflowGraphRef>(null)

  // Auto-collapse left panel on mobile
  useEffect(() => {
    if (isMobile) setLeftCollapsed(true)
  }, [isMobile])

  // Derived data
  const filteredMissions = useMemo(() => {
    if (selectedMissionId === "all") return missions
    return missions.filter((m) => m.id === selectedMissionId)
  }, [missions, selectedMissionId])

  const selectedMission = useMemo(() => {
    if (selectedMissionId === "all") return null
    return missions.find((m) => m.id === selectedMissionId) || null
  }, [missions, selectedMissionId])

  const stats = useMemo(() => ({
    active: missions.filter((m) => m.status === "IN_PROGRESS").length,
    planning: missions.filter((m) => m.status === "PLANNING").length,
    completed: missions.filter((m) => m.status === "COMPLETED").length,
    failed: missions.filter((m) => m.status === "FAILED").length,
  }), [missions])

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

  const panelAgents = useMemo(() => {
    // Only show agents that have tasks in the visible missions
    const activeSlugs = new Set<string>()
    const visibleMissions = selectedMissionId === "all" ? missions : missions.filter((m) => m.id === selectedMissionId)
    for (const m of visibleMissions) {
      for (const t of m.tasks || []) {
        if (t.agent_slug) activeSlugs.add(t.agent_slug)
      }
    }
    if (activeSlugs.size === 0) return agents // fallback: show all if no missions
    return agents.filter((a) => activeSlugs.has(a.slug))
  }, [selectedMissionId, missions, agents])

  const panelConnections = useMemo(() => {
    if (selectedMissionId === "all") return connections
    const crewIds = new Set(panelCrews.map((c) => c.id))
    return connections.filter((c) => crewIds.has(c.from_crew_id) && crewIds.has(c.to_crew_id))
  }, [selectedMissionId, panelCrews, connections])

  const panelMissions = useMemo(() => {
    if (selectedMissionId === "all") return missions
    return missions.filter((m) => m.id === selectedMissionId)
  }, [selectedMissionId, missions])

  // Issue data fetching
  const fetchIssues = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/issues?workspace_id=${workspaceId}&limit=100`)
      if (res.ok) setIssues(await res.json())
    } catch { /* ignore */ }
  }, [workspaceId])

  const fetchIssueLabels = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/labels?workspace_id=${workspaceId}`)
      if (res.ok) setIssueLabels(await res.json())
    } catch { /* ignore */ }
  }, [workspaceId])

  const fetchProjects = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/projects?workspace_id=${workspaceId}`)
      if (res.ok) setProjects(await res.json())
    } catch { /* ignore */ }
  }, [workspaceId])

  useEffect(() => {
    fetchIssues()
    fetchIssueLabels()
    fetchProjects()
  }, [fetchIssues, fetchIssueLabels, fetchProjects])

  const handleIssueSelect = useCallback(async (issue: Mission) => {
    setSelectedIssue(issue)
    setDetailContext({ type: "none" })
    if (issue.crew_id && issue.identifier) {
      try {
        const res = await fetch(`/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/comments?workspace_id=${workspaceId}`)
        if (res.ok) setIssueComments(await res.json())
        else setIssueComments([])
      } catch { setIssueComments([]) }
    }
  }, [workspaceId])

  const filteredIssues = useMemo(() => {
    let filtered = issues
    if (selectedProjectId) {
      filtered = filtered.filter((i) => i.project_id === selectedProjectId)
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
  }, [issues, issueSearch, selectedProjectId, filterCrewId, filterAgentId])

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

  const handleCrewSelect = useCallback((crewId: string) => {
    setSelectedAgentSlug(null)
    setSelectedCrewId((prev) => {
      if (prev === crewId) {
        // Deselect — close detail panel
        setDetailContext({ type: "none" })
        return null
      }
      // Select — open crew detail
      const crew = crews.find((c) => c.id === crewId)
      if (crew) {
        setDetailContext({ type: "crew", crew, agents, connections })
      }
      return crewId
    })
  }, [crews, agents, connections])

  const handleAgentSelect = useCallback((agentSlug: string) => {
    setSelectedAgentSlug((prev) => prev === agentSlug ? null : agentSlug)
  }, [])

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

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* ---- Toolbar: Tab navigation + context + actions (single row) ---- */}
      <div className="shrink-0 z-20 flex items-center h-9 bg-card border-b border-white/[0.08] px-2 sm:px-3 gap-0 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        {/* Tabs */}
        {([
          { id: "issues", label: "Issues", icon: CircleDot },
          { id: "graph", label: "Graph", icon: Workflow },
          { id: "timeline", label: "Timeline", icon: Clock },
          { id: "activity", label: "Activity", icon: Activity },
          { id: "proposals", label: "Approvals", icon: FileText },
        ] as const).map(({ id, label, icon: Icon }) => (
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

        {/* Separator */}
        <div className="w-px h-4 bg-white/[0.08] mx-2" />

        {/* Context badge — shows selected issue */}
        {selectedIssue && (
          <div className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-blue-500/10 border border-blue-500/20 text-[11px] text-blue-400">
            <span className="font-mono">{selectedIssue.identifier}</span>
            <button
              onClick={() => { setSelectedIssue(null); setIssueComments([]) }}
              className="text-blue-400/60 hover:text-blue-300"
            >
              <X className="h-3 w-3" />
            </button>
          </div>
        )}

        <div className="flex-1" />

        {/* Action buttons */}
        <div className="flex items-center gap-1.5">
          <Button variant="ghost" size="sm" className="h-6 w-6 p-0 text-muted-foreground hover:text-foreground/80" onClick={onRefresh}>
            <RefreshCw className="h-3 w-3" />
          </Button>
          <CreateMissionWizard workspaceId={workspaceId} onCreated={onMissionCreated} />
        </div>
      </div>

      {/* ---- Main 3-column layout ---- */}
      <div
        className="flex-1 min-h-0 grid transition-all duration-200 relative"
        style={{
          gridTemplateColumns: isMobile
            ? "1fr"
            : `${leftCollapsed ? "48px" : "240px"} 1fr ${showRightPanel ? "360px" : "0px"}`,
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
                </motion.div>
              )}
            </AnimatePresence>
          </div>
        )}

        {/* ---- Center content area ---- */}
        <div className="row-span-1 relative overflow-hidden min-h-0">
          {activeTab === "issues" && (
            <div className="h-full overflow-auto">
              {/* Toolbar strip */}
              <div className="flex items-center gap-2 px-4 py-2 border-b border-white/[0.06] shrink-0">
                <div className="flex gap-1 bg-white/[0.04] rounded-md p-0.5">
                  <button
                    onClick={() => setIssueViewMode("board")}
                    className={cn("p-1.5 rounded", issueViewMode === "board" ? "bg-white/[0.1] text-foreground" : "text-muted-foreground")}
                  >
                    <LayoutGrid className="h-3.5 w-3.5" />
                  </button>
                  <button
                    onClick={() => setIssueViewMode("list")}
                    className={cn("p-1.5 rounded", issueViewMode === "list" ? "bg-white/[0.1] text-foreground" : "text-muted-foreground")}
                  >
                    <List className="h-3.5 w-3.5" />
                  </button>
                </div>
                <div className="flex-1" />
              </div>
              {/* Board or List view */}
              <div className="p-4 h-[calc(100%-45px)]">
                {issueViewMode === "board" ? (
                  <IssuesBoardInline issues={filteredIssues} onIssueClick={handleIssueSelect} />
                ) : (
                  <IssuesListInline issues={filteredIssues} onIssueClick={handleIssueSelect} />
                )}
              </div>
            </div>
          )}

          {activeTab === "graph" && (
            <>
              <WorkflowGraph
                ref={graphRef}
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
                    onClick={handleDetailClose}
                    className="h-8 w-8 min-h-[44px] min-w-[44px] flex items-center justify-center text-muted-foreground hover:text-foreground"
                  >
                    <ChevronLeft className="h-4 w-4" />
                  </button>
                  <span className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Detail</span>
                </div>
                <div className="flex-1 overflow-y-auto">
                  {selectedIssue ? (
                    <IssueDetailInline
                      issue={selectedIssue}
                      comments={issueComments}
                      labels={issueLabels}
                      projects={projects}
                      workspaceId={workspaceId}
                      onClose={() => { setSelectedIssue(null); setIssueComments([]) }}
                      onUpdated={async () => {
                        await fetchIssues()
                        if (selectedIssue?.crew_id && selectedIssue?.identifier) {
                          try {
                            const res = await fetch(`/api/v1/issues/${selectedIssue.identifier}?workspace_id=${workspaceId}`)
                            if (res.ok) {
                              const fresh = await res.json()
                              setSelectedIssue(fresh)
                              const commRes = await fetch(`/api/v1/crews/${fresh.crew_id}/issues/${fresh.identifier}/comments?workspace_id=${workspaceId}`)
                              if (commRes.ok) setIssueComments(await commRes.json())
                            }
                          } catch {}
                        }
                        fetchProjects()
                      }}
                    />
                  ) : selectedProject ? (
                    <ProjectDetailInline
                      project={selectedProject}
                      workspaceId={workspaceId}
                      onClose={() => setSelectedProjectId(null)}
                      onUpdated={fetchProjects}
                    />
                  ) : (
                    <ContextDetailPanel context={detailContext} onClose={handleDetailClose} onTaskAction={handleTaskAction} />
                  )}
                </div>
              </motion.div>
            )}
          </AnimatePresence>
        ) : (
          <div className={cn(
            "row-span-1 transition-all duration-200 overflow-hidden min-h-0",
            showRightPanel ? "w-[380px]" : "w-0",
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
                  {selectedIssue ? (
                    <IssueDetailInline
                      issue={selectedIssue}
                      comments={issueComments}
                      labels={issueLabels}
                      projects={projects}
                      workspaceId={workspaceId}
                      onClose={() => { setSelectedIssue(null); setIssueComments([]) }}
                      onUpdated={async () => {
                        await fetchIssues()
                        if (selectedIssue?.crew_id && selectedIssue?.identifier) {
                          try {
                            const res = await fetch(`/api/v1/issues/${selectedIssue.identifier}?workspace_id=${workspaceId}`)
                            if (res.ok) {
                              const fresh = await res.json()
                              setSelectedIssue(fresh)
                              const commRes = await fetch(`/api/v1/crews/${fresh.crew_id}/issues/${fresh.identifier}/comments?workspace_id=${workspaceId}`)
                              if (commRes.ok) setIssueComments(await commRes.json())
                            }
                          } catch {}
                        }
                        fetchProjects()
                      }}
                    />
                  ) : selectedProject ? (
                    <ProjectDetailInline
                      project={selectedProject}
                      workspaceId={workspaceId}
                      onClose={() => setSelectedProjectId(null)}
                      onUpdated={fetchProjects}
                    />
                  ) : (
                    <ContextDetailPanel
                      context={detailContext}
                      onClose={handleDetailClose}
                      onTaskAction={handleTaskAction}
                    />
                  )}
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
            {([
              { id: "messages" as const, label: "Messages", icon: MessageSquare },
              { id: "exec" as const, label: "Exec Log", icon: Terminal },
              { id: "yaml" as const, label: "YAML", icon: FileCode2 },
              { id: "docker" as const, label: "Docker", icon: Container },
            ]).map(({ id, label, icon: Icon }) => (
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
    </div>
  )
}
