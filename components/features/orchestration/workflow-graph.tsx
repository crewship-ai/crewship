"use client"

import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from "react"
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  useNodesState,
  useEdgesState,
  useReactFlow,
  ReactFlowProvider,
  type Node,
  type Edge,
  type NodeTypes,
  type EdgeTypes,
  Position,
  MarkerType,
  BackgroundVariant,
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import { EmptyState } from "@/components/layout/empty-state"
import { Workflow } from "lucide-react"
import type { Mission, MissionTask } from "@/lib/types/mission"
import type { CrewSummary, AgentSummary, CrewConnection } from "@/lib/types/orchestration"
import { Graph as DagreGraph, layout as dagreLayout } from "@dagrejs/dagre"
import { useAgentActivity } from "@/hooks/use-agent-activity"
import { AgentNode } from "./agent-node"
import { AgentCardNode } from "./agent-card-node"
import { AnimatedEdge } from "./animated-edge"
import { CrewGroupNode } from "./crew-group-node"
import { STATUS_COLORS, EDGE_COLOR_PALETTE, CREW_COLORS, CREW_COLOR_DEFAULT, GRAPH_CHROME } from "@/lib/colors"
import { PermissionEdge, getPermissionMarkers } from "./permission-edge"

export interface WorkflowGraphRef {
  focusActive: () => void
}

interface WorkflowGraphProps {
  missions: Mission[]
  crews?: CrewSummary[]
  agents?: AgentSummary[]
  connections?: CrewConnection[]
  onTaskClick?: (task: MissionTask) => void
  highlightAgentSlug?: string | null
}

const nodeTypes: NodeTypes = {
  agent: AgentNode,
  agentCard: AgentCardNode,
  crew: CrewGroupNode,
}
const edgeTypes: EdgeTypes = {
  animated: AnimatedEdge,
  permission: PermissionEdge,
}

function pickEdgeColor(sourceId: string, targetId: string): string {
  let h = 0
  const key = sourceId + targetId
  for (let i = 0; i < key.length; i++) h = ((h << 5) - h + key.charCodeAt(i)) | 0
  return EDGE_COLOR_PALETTE[Math.abs(h) % EDGE_COLOR_PALETTE.length]
}

// Safe parser for depends_on — handles null, non-array, malformed JSON
function parseDependsOn(raw: string | null | undefined): string[] {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw)
    return Array.isArray(parsed) ? parsed.filter((v): v is string => typeof v === "string") : []
  } catch {
    return []
  }
}

// -------------------------------------------------------------------
// Build graph with crew group nodes (sub-flows)
// -------------------------------------------------------------------

interface BuildInput {
  missions: Mission[]
  crews: CrewSummary[]
  agents: AgentSummary[]
  connections: CrewConnection[]
  collapsedCrews: Set<string>
  onToggleCollapse: (crewId: string) => void
}

// -------------------------------------------------------------------
// Dagre-based layout constants
// -------------------------------------------------------------------
const TASK_WIDTH = 260
const TASK_HEIGHT = 140
const COLLAPSED_WIDTH = 300
const COLLAPSED_HEIGHT = 50
const CREW_PADDING_TOP = 65
const CREW_PADDING_SIDE = 35
const CREW_PADDING_BOTTOM = 35

/**
 * Two-level dagre layout:
 *  1. Layout tasks WITHIN each crew using dagre (local LR graph)
 *  2. Layout crew groups as a global top-down grid based on cross-crew edges
 */
function buildGraphData(input: BuildInput): { nodes: Node[]; edges: Edge[] } {
  const { missions, crews, agents, connections, collapsedCrews, onToggleCollapse } = input
  const nodes: Node[] = []
  const edges: Edge[] = []

  // Build agent slug → agent map and slug → crew id map
  const agentBySlug = new Map<string, AgentSummary>()
  for (const agent of agents) {
    if (agent.slug) agentBySlug.set(agent.slug, agent)
  }
  const agentCrewMap = new Map<string, string>()
  const crewById = new Map<string, CrewSummary>()
  const crewBySlug = new Map<string, CrewSummary>()
  for (const crew of crews) {
    crewById.set(crew.id, crew)
    crewBySlug.set(crew.slug, crew)
  }
  for (const agent of agents) {
    if (agent.slug && agent.crew?.slug) {
      const crew = crewBySlug.get(agent.crew.slug)
      if (crew) agentCrewMap.set(agent.slug, crew.id)
    } else if (agent.slug && agent.crew_id) {
      agentCrewMap.set(agent.slug, agent.crew_id)
    }
  }

  // Select active or recent missions
  const activeMissions = missions.filter(
    (m) => m.status === "IN_PROGRESS" || m.status === "PLANNING" || m.status === "REVIEW"
  )
  if (activeMissions.length === 0) {
    const recent = [...missions]
      .sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime())
      .slice(0, 3)
    activeMissions.push(...recent)
  }

  // Collect tasks grouped by crew
  const crewTasks = new Map<string, { mission: Mission; task: MissionTask }[]>()
  const usedCrewIds = new Set<string>()
  for (const mission of activeMissions) {
    for (const task of mission.tasks || []) {
      const crewId = (task.agent_slug && agentCrewMap.get(task.agent_slug)) || mission.crew_id
      if (!crewId) continue
      usedCrewIds.add(crewId)
      if (!crewTasks.has(crewId)) crewTasks.set(crewId, [])
      crewTasks.get(crewId)!.push({ mission, task })
    }
  }

  // Collect all dependency info
  const allDeps: { source: string; target: string; crossCrew: boolean; task: MissionTask }[] = []
  for (const mission of activeMissions) {
    for (const task of mission.tasks || []) {
      const taskCrewId = (task.agent_slug && agentCrewMap.get(task.agent_slug)) || mission.crew_id
      for (const depId of parseDependsOn(task.depends_on)) {
        const depTask = mission.tasks?.find((t) => t.id === depId)
        if (!depTask) continue
        const depCrewId = (depTask.agent_slug && agentCrewMap.get(depTask.agent_slug)) || mission.crew_id
        allDeps.push({ source: depId, target: task.id, crossCrew: depCrewId !== taskCrewId, task })
      }
    }
  }

  // ---- LEVEL 1: Layout tasks within each crew using dagre ----
  // Returns relative positions of tasks inside each crew, plus computed crew size
  const crewLayouts = new Map<string, {
    width: number; height: number
    taskPositions: Map<string, { x: number; y: number }>
    agentPositions?: Map<string, { x: number; y: number }>
  }>()

  const sortedCrewIds = [...usedCrewIds].sort((a, b) =>
    (crewById.get(a)?.name || "").localeCompare(crewById.get(b)?.name || "")
  )

  // Agent card dimensions
  const AGENT_CARD_WIDTH = 200
  const AGENT_CARD_HEIGHT = 110
  const AGENT_CARD_GAP = 12

  for (const crewId of sortedCrewIds) {
    const tasks = crewTasks.get(crewId) || []
    if (collapsedCrews.has(crewId)) continue

    const crewAgents = agents.filter((a) => {
      if (a.crew_id === crewId) return true
      if (a.crew?.slug) {
        const c = crewBySlug.get(a.crew.slug)
        return c?.id === crewId
      }
      return false
    })

    if (tasks.length === 0) {
      // No tasks — show agent cards in a grid (2 columns)
      if (crewAgents.length === 0) continue
      const cols = Math.min(crewAgents.length, 2)
      const rows = Math.ceil(crewAgents.length / cols)
      const agentPositions = new Map<string, { x: number; y: number }>()

      crewAgents.forEach((agent, i) => {
        const col = i % cols
        const row = Math.floor(i / cols)
        agentPositions.set(agent.id, {
          x: CREW_PADDING_SIDE + col * (AGENT_CARD_WIDTH + AGENT_CARD_GAP),
          y: CREW_PADDING_TOP + row * (AGENT_CARD_HEIGHT + AGENT_CARD_GAP),
        })
      })

      const crewWidth = cols * AGENT_CARD_WIDTH + (cols - 1) * AGENT_CARD_GAP + CREW_PADDING_SIDE * 2
      const crewHeight = rows * AGENT_CARD_HEIGHT + (rows - 1) * AGENT_CARD_GAP + CREW_PADDING_TOP + CREW_PADDING_BOTTOM

      crewLayouts.set(crewId, { width: crewWidth, height: crewHeight, taskPositions: new Map(), agentPositions })
      continue
    }

    // Crew has tasks — layout with dagre
    const localG = new DagreGraph()
    localG.setGraph({ rankdir: "LR", ranksep: 80, nodesep: 25 })
    localG.setDefaultEdgeLabel(() => ({}))

    const localTaskIds = new Set(tasks.map((t) => t.task.id))
    for (const { task } of tasks) {
      localG.setNode(task.id, { width: TASK_WIDTH, height: TASK_HEIGHT })
    }
    for (const dep of allDeps) {
      if (localTaskIds.has(dep.source) && localTaskIds.has(dep.target) && !dep.crossCrew) {
        localG.setEdge(dep.source, dep.target)
      }
    }

    dagreLayout(localG)

    const taskPositions = new Map<string, { x: number; y: number }>()
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity
    for (const { task } of tasks) {
      const dn = localG.node(task.id)
      if (!dn) continue
      const x = dn.x - TASK_WIDTH / 2
      const y = dn.y - TASK_HEIGHT / 2
      taskPositions.set(task.id, { x, y })
      minX = Math.min(minX, x)
      minY = Math.min(minY, y)
      maxX = Math.max(maxX, x + TASK_WIDTH)
      maxY = Math.max(maxY, y + TASK_HEIGHT)
    }
    for (const [id, pos] of taskPositions) {
      taskPositions.set(id, { x: pos.x - minX + CREW_PADDING_SIDE, y: pos.y - minY + CREW_PADDING_TOP })
    }
    const crewWidth = (maxX - minX) + CREW_PADDING_SIDE * 2
    const crewHeight = (maxY - minY) + CREW_PADDING_TOP + CREW_PADDING_BOTTOM

    crewLayouts.set(crewId, { width: crewWidth, height: crewHeight, taskPositions })
  }

  // ---- LEVEL 2: Layout crew groups using dagre ----
  const crewG = new DagreGraph()
  crewG.setGraph({ rankdir: "LR", ranksep: 160, nodesep: 100 })
  crewG.setDefaultEdgeLabel(() => ({}))

  for (const crewId of sortedCrewIds) {
    const layout = crewLayouts.get(crewId)
    const w = layout?.width ?? COLLAPSED_WIDTH
    const h = layout?.height ?? COLLAPSED_HEIGHT
    crewG.setNode(`crew-${crewId}`, { width: w, height: h })
  }

  // Add cross-crew dependency edges between crew nodes (for ordering)
  const crewEdgesAdded = new Set<string>()
  for (const dep of allDeps) {
    if (!dep.crossCrew) continue
    // Find crew of source and target
    let srcCrew: string | null = null
    let tgtCrew: string | null = null
    for (const [cid, tasks] of crewTasks) {
      if (tasks.some((t) => t.task.id === dep.source)) srcCrew = cid
      if (tasks.some((t) => t.task.id === dep.target)) tgtCrew = cid
    }
    if (srcCrew && tgtCrew && srcCrew !== tgtCrew) {
      const key = `${srcCrew}-${tgtCrew}`
      if (!crewEdgesAdded.has(key)) {
        crewEdgesAdded.add(key)
        crewG.setEdge(`crew-${srcCrew}`, `crew-${tgtCrew}`)
      }
    }
  }

  // Also add connection-based edges for crews without task deps
  for (const conn of connections) {
    const fromKey = `crew-${conn.from_crew_id}`
    const toKey = `crew-${conn.to_crew_id}`
    if (crewG.hasNode(fromKey) && crewG.hasNode(toKey)) {
      const key = `${conn.from_crew_id}-${conn.to_crew_id}`
      if (!crewEdgesAdded.has(key)) {
        crewEdgesAdded.add(key)
        crewG.setEdge(fromKey, toKey)
      }
    }
  }

  dagreLayout(crewG)

  // ---- BUILD REACT FLOW NODES ----
  for (const crewId of sortedCrewIds) {
    const crew = crewById.get(crewId)
    if (!crew) continue

    const tasks = crewTasks.get(crewId) || []
    const collapsed = collapsedCrews.has(crewId)
    const crewNodeId = `crew-${crewId}`
    const dagrePos = crewG.node(crewNodeId)

    const taskCount = tasks.length
    const activeCount = tasks.filter((t) => t.task.status === "IN_PROGRESS").length
    const completedCount = tasks.filter((t) => t.task.status === "COMPLETED").length
    const failedCount = tasks.filter((t) => t.task.status === "FAILED").length

    const layout = crewLayouts.get(crewId)
    const w = layout?.width ?? COLLAPSED_WIDTH
    const h = layout?.height ?? COLLAPSED_HEIGHT

    // Crew group position from global dagre (center → top-left)
    const crewX = (dagrePos?.x ?? 0) - w / 2
    const crewY = (dagrePos?.y ?? 0) - h / 2

    const hasLayout = !!layout
    nodes.push({
      id: crewNodeId,
      type: "crew",
      position: { x: crewX, y: crewY },
      data: {
        label: crew.name, slug: crew.slug, color: crew.color, icon: crew.icon,
        agentCount: crew._count?.agents || 0,
        collapsed,
        taskCount, activeCount, completedCount, failedCount,
        onToggleCollapse, crewId,
      },
      style: { width: w, height: h },
    })

    if (collapsed || !hasLayout) continue

    // Add agent card nodes for crews with no tasks
    if (layout.agentPositions) {
      const crewAgents = agents.filter((a) => {
        if (a.crew_id === crewId) return true
        if (a.crew?.slug) {
          const c = crewBySlug.get(a.crew.slug)
          return c?.id === crewId
        }
        return false
      })
      for (const agent of crewAgents) {
        const pos = layout.agentPositions.get(agent.id)
        if (!pos) continue
        nodes.push({
          id: `agent-card-${agent.id}`,
          type: "agentCard",
          parentId: crewNodeId,
          extent: "parent" as const,
          position: pos,
          data: {
            name: agent.name, slug: agent.slug,
            avatarSeed: agent.avatar_seed, avatarStyle: agent.avatar_style,
            role: "", isLead: false,
            status: "idle", model: "",
            tokenCount: 0, cost: 0,
            skills: [], memoryEnabled: false,
            currentTask: null,
          },
        })
      }
    }

    // Add child task nodes with positions relative to crew
    for (const { task } of tasks) {
      const localPos = layout.taskPositions.get(task.id)
      if (!localPos) continue

      nodes.push({
        id: task.id,
        type: "agent",
        parentId: crewNodeId,
        extent: "parent" as const,
        position: localPos,
        data: {
          label: task.title, status: task.status,
          agentName: task.agent_name || "Unassigned", agentSlug: task.agent_slug,
          avatarSeed: (task.agent_slug && agentBySlug.get(task.agent_slug)?.avatar_seed) ?? null,
          avatarStyle: (task.agent_slug && agentBySlug.get(task.agent_slug)?.avatar_style) ?? null,
          iteration: task.iteration, maxIterations: task.max_iterations,
          tokenCount: task.token_count, estimatedCost: task.estimated_cost,
          durationMs: task.duration_ms, missionId: task.mission_id,
        },
        sourcePosition: Position.Right,
        targetPosition: Position.Left,
      })
    }
  }

  // ---- BUILD EDGES ----
  const visibleNodeIds = new Set(nodes.map((n) => n.id))

  for (const dep of allDeps) {
    if (!visibleNodeIds.has(dep.source) || !visibleNodeIds.has(dep.target)) continue
    const isActive = dep.task.status === "IN_PROGRESS"
    const edgeColor = dep.crossCrew
      ? STATUS_COLORS.REVIEW
      : isActive ? STATUS_COLORS.IN_PROGRESS : pickEdgeColor(dep.source, dep.target)

    edges.push({
      id: `e-${dep.crossCrew ? "x-" : ""}${dep.source}-${dep.target}`,
      source: dep.source,
      target: dep.target,
      type: "animated",
      data: { color: edgeColor, active: isActive },
      style: { strokeWidth: dep.crossCrew ? 2.5 : 2 },
      markerEnd: { type: MarkerType.ArrowClosed, color: edgeColor, width: 14, height: 14 },
    })
  }

  // Permission edges
  for (const conn of connections) {
    if (!usedCrewIds.has(conn.from_crew_id) || !usedCrewIds.has(conn.to_crew_id)) continue
    const markers = getPermissionMarkers(conn.direction)
    edges.push({
      id: `perm-${conn.id}`,
      source: `crew-${conn.from_crew_id}`,
      target: `crew-${conn.to_crew_id}`,
      sourceHandle: `crew-${conn.from_crew_id}-perm-source`,
      targetHandle: `crew-${conn.to_crew_id}-perm-target`,
      type: "permission",
      data: { direction: conn.direction, status: conn.status },
      ...markers,
    })
  }

  // Sort: crew nodes before children
  nodes.sort((a, b) => (a.type === "crew" ? 0 : 1) - (b.type === "crew" ? 0 : 1))

  return { nodes, edges }
}

// -------------------------------------------------------------------
// Fallback: flat graph when no crew data available
// -------------------------------------------------------------------

function buildFlatGraphData(missions: Mission[]): { nodes: Node[]; edges: Edge[] } {
  const nodes: Node[] = []
  const edges: Edge[] = []

  const activeMissions = missions.filter(
    (m) => m.status === "IN_PROGRESS" || m.status === "PLANNING" || m.status === "REVIEW"
  )
  if (activeMissions.length === 0) {
    const recent = [...missions]
      .sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime())
      .slice(0, 3)
    activeMissions.push(...recent)
  }

  let missionY = 0

  for (const mission of activeMissions) {
    const tasks = mission.tasks || []
    const accent = STATUS_COLORS[mission.status] || CREW_COLOR_DEFAULT
    const totalTokens = tasks.reduce((sum, t) => sum + (t.token_count || 0), 0)
    const statusLabel =
      tasks.length === 0 && (mission.status === "PLANNING" || mission.status === "IN_PROGRESS")
        ? " — Lead is planning tasks..."
        : totalTokens > 0
          ? ` · ${(totalTokens / 1000).toFixed(1)}k tok`
          : ""

    nodes.push({
      id: `mission-${mission.id}`,
      type: "default",
      position: { x: 0, y: missionY },
      data: { label: `${mission.title}${statusLabel}` },
      style: {
        background: "linear-gradient(135deg, rgba(30,35,50,0.95), rgba(20,22,30,0.98))",
        border: `2px solid ${accent}60`,
        borderRadius: "14px",
        padding: "12px 20px",
        fontSize: "14px",
        fontWeight: 700,
        color: GRAPH_CHROME.missionLabel,
        width: 220,
        boxShadow: `0 0 20px ${accent}15`,
      },
      sourcePosition: Position.Right,
      targetPosition: Position.Left,
    })

    if (tasks.length === 0) {
      missionY += 100
      continue
    }

    const tasksByOrder = [...tasks].sort((a, b) => a.task_order - b.task_order)
    const deps = new Map<string, string[]>()
    for (const task of tasksByOrder) {
      deps.set(task.id, parseDependsOn(task.depends_on))
    }

    const levels = new Map<string, number>()
    const visiting = new Set<string>()
    function getLevel(taskId: string): number {
      if (levels.has(taskId)) return levels.get(taskId)!
      if (visiting.has(taskId)) return 0 // cycle detected
      visiting.add(taskId)
      const taskDeps = deps.get(taskId) || []
      if (taskDeps.length === 0) {
        levels.set(taskId, 0)
        return 0
      }
      const level = Math.max(...taskDeps.map(getLevel)) + 1
      levels.set(taskId, level)
      return level
    }
    for (const task of tasksByOrder) getLevel(task.id)

    const levelGroups = new Map<number, MissionTask[]>()
    for (const task of tasksByOrder) {
      const level = levels.get(task.id) || 0
      if (!levelGroups.has(level)) levelGroups.set(level, [])
      levelGroups.get(level)!.push(task)
    }

    const xOffset = 300
    for (const [level, levelTasks] of levelGroups) {
      levelTasks.forEach((task, idx) => {
        const x = xOffset + level * 320
        const y = missionY + idx * 130

        nodes.push({
          id: task.id,
          type: "agent",
          position: { x, y },
          data: {
            label: task.title,
            status: task.status,
            agentName: task.agent_name || "Unassigned",
            agentSlug: task.agent_slug,
            avatarSeed: null,
            avatarStyle: null,
            iteration: task.iteration,
            maxIterations: task.max_iterations,
            tokenCount: task.token_count,
            estimatedCost: task.estimated_cost,
            durationMs: task.duration_ms,
            missionId: mission.id,
          },
          sourcePosition: Position.Right,
          targetPosition: Position.Left,
        })

        if (level === 0) {
          const edgeColor = pickEdgeColor(`mission-${mission.id}`, task.id)
          edges.push({
            id: `e-m-${mission.id}-${task.id}`,
            source: `mission-${mission.id}`,
            target: task.id,
            type: "animated",
            data: { color: edgeColor, active: mission.status === "IN_PROGRESS" },
            style: { strokeWidth: 2 },
          })
        }

        const taskDeps = deps.get(task.id) || []
        for (const depId of taskDeps) {
          const isActive = task.status === "IN_PROGRESS"
          const edgeColor = isActive ? STATUS_COLORS.IN_PROGRESS : pickEdgeColor(depId, task.id)
          edges.push({
            id: `e-${depId}-${task.id}`,
            source: depId,
            target: task.id,
            type: "animated",
            data: { color: edgeColor, active: isActive },
            style: { strokeWidth: 2 },
            markerEnd: {
              type: MarkerType.ArrowClosed,
              color: edgeColor,
              width: 14,
              height: 14,
            },
          })
        }
      })
    }

    const maxLevelSize = Math.max(...[...levelGroups.values()].map((g) => g.length), 1)
    missionY += maxLevelSize * 130 + 100
  }

  return { nodes, edges }
}

// -------------------------------------------------------------------
// React Flow component
// -------------------------------------------------------------------

function WorkflowGraphInner(
  { missions, crews, agents, connections, onTaskClick, highlightAgentSlug }: WorkflowGraphProps,
  ref: React.ForwardedRef<WorkflowGraphRef>
) {
  const [collapsedCrews, setCollapsedCrews] = useState<Set<string>>(new Set())
  const [highlightedNodeId, setHighlightedNodeId] = useState<string | null>(null)
  const activities = useAgentActivity()

  const toggleCollapse = useCallback((crewId: string) => {
    setCollapsedCrews((prev) => {
      const next = new Set(prev)
      if (next.has(crewId)) {
        next.delete(crewId)
      } else {
        next.add(crewId)
      }
      return next
    })
  }, [])

  const hasCrewData = !!(crews && crews.length > 0 && agents)

  const graphData = useMemo(() => {
    if (hasCrewData) {
      return buildGraphData({
        missions,
        crews: crews!,
        agents: agents!,
        connections: connections || [],
        collapsedCrews,
        onToggleCollapse: toggleCollapse,
      })
    }
    return buildFlatGraphData(missions)
  }, [missions, crews, agents, connections, collapsedCrews, toggleCollapse, hasCrewData])

  const [nodes, setNodes, onNodesChange] = useNodesState(graphData.nodes)
  const [edgesState, setEdges, onEdgesChange] = useEdgesState(graphData.edges)
  const { fitView, setCenter } = useReactFlow()
  const prevDataRef = useRef(graphData)

  // Track user-dragged node positions so polling doesn't reset them
  const userPositions = useRef(new Map<string, { x: number; y: number }>())

  const onNodeDragStop = useCallback((_: React.MouseEvent, node: Node) => {
    userPositions.current.set(node.id, { ...node.position })
  }, [])

  const prevNodeIdsRef = useRef<string>("")

  useEffect(() => {
    if (prevDataRef.current === graphData) return
    prevDataRef.current = graphData

    // Detect if this is a mission switch (node IDs changed significantly)
    const newNodeIds = graphData.nodes.map((n) => n.id).sort().join(",")
    const isMissionSwitch = prevNodeIdsRef.current !== "" && prevNodeIdsRef.current !== newNodeIds
    prevNodeIdsRef.current = newNodeIds

    if (isMissionSwitch) {
      // Mission changed — clear user positions and use fresh layout
      userPositions.current.clear()
      setNodes(graphData.nodes)
      setEdges(graphData.edges)
      // Animate zoom to fit new mission
      setTimeout(() => fitView({ duration: 500, padding: 0.25 }), 50)
      return
    }

    // Polling update — preserve user-dragged positions
    setNodes((prev) => {
      const prevMap = new Map(prev.map((n) => [n.id, n]))
      return graphData.nodes.map((newNode) => {
        const userPos = userPositions.current.get(newNode.id)
        if (userPos) return { ...newNode, position: userPos }
        const existing = prevMap.get(newNode.id)
        if (existing) return { ...newNode, position: existing.position }
        return newNode
      })
    })
    setEdges(graphData.edges)
  }, [graphData, setNodes, setEdges, fitView])

  // Inject activity snippets into agent nodes
  useEffect(() => {
    if (activities.size === 0) return
    setNodes((prev) =>
      prev.map((node) => {
        if (node.type !== "agent") return node
        const slug = (node.data as Record<string, unknown>)?.agentSlug as string | undefined
        const snippet = slug ? activities.get(slug) ?? null : null
        const current = (node.data as Record<string, unknown>)?.activitySnippet
        if (current === snippet) return node
        return { ...node, data: { ...node.data, activitySnippet: snippet } }
      })
    )
  }, [activities, setNodes])

  // Clear highlight if the highlighted node no longer exists (collapsed crew, graph rebuild)
  useEffect(() => {
    if (highlightedNodeId && !nodes.some((n) => n.id === highlightedNodeId)) {
      setHighlightedNodeId(null)
    }
  }, [nodes, highlightedNodeId])

  // Compute dimmed nodes/edges for Shift+Click highlighting
  const { dimmedNodeIds, dimmedEdgeIds } = useMemo(() => {
    if (!highlightedNodeId) return { dimmedNodeIds: new Set<string>(), dimmedEdgeIds: new Set<string>() }

    const connectedNodeIds = new Set<string>([highlightedNodeId])
    const connectedEdgeIds = new Set<string>()

    for (const e of edgesState) {
      if (e.source === highlightedNodeId || e.target === highlightedNodeId) {
        connectedEdgeIds.add(e.id)
        connectedNodeIds.add(e.source)
        connectedNodeIds.add(e.target)
      }
    }

    // Also include parent crew of highlighted node
    const hlNode = nodes.find((n) => n.id === highlightedNodeId)
    if (hlNode?.parentId) connectedNodeIds.add(hlNode.parentId)

    return {
      dimmedNodeIds: new Set(
        nodes
          .filter((n) => !connectedNodeIds.has(n.id) && !(n.parentId && connectedNodeIds.has(n.parentId)))
          .map((n) => n.id)
      ),
      dimmedEdgeIds: new Set(
        edgesState.filter((e) => !connectedEdgeIds.has(e.id)).map((e) => e.id)
      ),
    }
  }, [highlightedNodeId, nodes, edgesState])

  // Apply dimming styles to nodes and edges (Shift+Click highlight OR agent highlight from left panel)
  const displayNodes = useMemo(() => {
    // Agent highlight from left panel — dim nodes not belonging to that agent
    if (highlightAgentSlug) {
      return nodes.map((n) => {
        const nodeSlug = (n.data as Record<string, unknown>)?.agentSlug as string | undefined
        const isMatch = nodeSlug === highlightAgentSlug
        const isCrewParent = n.type === "crew" && nodes.some(
          (child) => child.parentId === n.id && (child.data as Record<string, unknown>)?.agentSlug === highlightAgentSlug
        )
        return {
          ...n,
          style: {
            ...n.style,
            opacity: isMatch || isCrewParent ? 1 : 0.15,
            transition: "opacity 0.3s ease",
          },
        }
      })
    }
    if (!highlightedNodeId) return nodes
    return nodes.map((n) => ({
      ...n,
      style: {
        ...n.style,
        opacity: dimmedNodeIds.has(n.id) ? 0.15 : 1,
        transition: "opacity 0.3s ease",
      },
    }))
  }, [nodes, highlightedNodeId, dimmedNodeIds, highlightAgentSlug])

  const displayEdges = useMemo(() => {
    if (!highlightedNodeId) return edgesState
    return edgesState.map((e) => ({
      ...e,
      data: { ...e.data, dimmed: dimmedEdgeIds.has(e.id) },
    }))
  }, [edgesState, highlightedNodeId, dimmedEdgeIds])

  useImperativeHandle(
    ref,
    () => ({
      focusActive() {
        const activeNode = nodes.find(
          (n) =>
            n.type === "agent" &&
            (n.data as Record<string, unknown>)?.status === "IN_PROGRESS"
        )
        if (activeNode) {
          // For child nodes, compute absolute position
          const parent = activeNode.parentId
            ? nodes.find((n) => n.id === activeNode.parentId)
            : null
          const absX = (parent?.position.x || 0) + activeNode.position.x + 130
          const absY = (parent?.position.y || 0) + activeNode.position.y + 60
          setCenter(absX, absY, { zoom: 1.2, duration: 600 })
        } else {
          fitView({ duration: 600, padding: 0.2 })
        }
      },
    }),
    [nodes, setCenter, fitView]
  )

  const onNodeClick = useCallback(
    (event: React.MouseEvent, node: Node) => {
      // Shift+Click: toggle highlight mode
      if (event.shiftKey && !node.id.startsWith("mission-")) {
        setHighlightedNodeId((prev) => (prev === node.id ? null : node.id))
        return
      }
      if (node.id.startsWith("mission-") || node.id.startsWith("crew-")) return
      if (!onTaskClick) return
      for (const m of missions) {
        const task = m.tasks?.find((t) => t.id === node.id)
        if (task) {
          onTaskClick(task)
          return
        }
      }
    },
    [missions, onTaskClick]
  )

  const onPaneClick = useCallback(() => {
    setHighlightedNodeId(null)
  }, [])

  if (missions.length === 0) {
    return (
      <div className="rounded-xl border border-border bg-card p-16">
        <EmptyState
          icon={Workflow}
          title="No missions yet"
          description="Create a mission from a crew's lead agent to see the workflow graph here"
        />
      </div>
    )
  }

  return (
    <div className="h-full w-full overflow-hidden bg-background">
      <div className="h-full w-full">
        <ReactFlow
          nodes={displayNodes}
          edges={displayEdges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onNodeClick={onNodeClick}
          onNodeDragStop={onNodeDragStop}
          onPaneClick={onPaneClick}
          fitView
          fitViewOptions={{ padding: 0.3 }}
          minZoom={0.1}
          maxZoom={2.5}
          proOptions={{ hideAttribution: true }}
          className="!bg-transparent"
          defaultEdgeOptions={{ type: "animated" }}
        >
          <Background
            variant={BackgroundVariant.Dots}
            gap={24}
            size={1.5}
            color="rgba(100, 116, 139, 0.15)"
          />
          <Controls
            showInteractive={false}
            className="!bg-muted !border-border !rounded-lg !shadow-xl [&_button]:!bg-muted [&_button]:!border-border [&_button]:!text-muted-foreground [&_button:hover]:!bg-accent"
          />
          <MiniMap
            nodeColor={(n) => {
              if (n.id.startsWith("crew-")) {
                const paletteName = (n.data as Record<string, unknown>)?.color as string | null
                if (paletteName) return CREW_COLORS[paletteName] || paletteName
                return GRAPH_CHROME.minimapNode
              }
              if (n.id.startsWith("mission-")) return GRAPH_CHROME.minimapNode
              return STATUS_COLORS[(n.data?.status as string) || "PENDING"] || CREW_COLOR_DEFAULT
            }}
            maskColor="rgba(10, 12, 16, 0.85)"
            className="!bg-card !border-border !rounded-lg"
            pannable
            zoomable
          />
        </ReactFlow>
      </div>
    </div>
  )
}

const WorkflowGraphWithRef = forwardRef(WorkflowGraphInner)

export const WorkflowGraph = forwardRef<WorkflowGraphRef, WorkflowGraphProps>(
  function WorkflowGraphWrapper(props, ref) {
    return (
      <ReactFlowProvider>
        <WorkflowGraphWithRef {...props} ref={ref} />
      </ReactFlowProvider>
    )
  }
)
