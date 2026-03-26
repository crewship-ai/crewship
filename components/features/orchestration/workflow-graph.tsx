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
import { useAgentActivity } from "@/hooks/use-agent-activity"
import { AgentNode } from "./agent-node"
import { AnimatedEdge } from "./animated-edge"
import { CrewGroupNode, crewColorMap } from "./crew-group-node"
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
}

const nodeTypes: NodeTypes = {
  agent: AgentNode,
  crew: CrewGroupNode,
}
const edgeTypes: EdgeTypes = {
  animated: AnimatedEdge,
  permission: PermissionEdge,
}

const statusColors: Record<string, string> = {
  COMPLETED: "#22c55e",
  IN_PROGRESS: "#3b82f6",
  FAILED: "#ef4444",
  BLOCKED: "#f59e0b",
  PENDING: "#64748b",
  PLANNING: "#8b5cf6",
  REVIEW: "#a855f7",
  CANCELLED: "#6b7280",
  SKIPPED: "#6b7280",
}

const edgeColorPalette = [
  "#06b6d4", "#3b82f6", "#8b5cf6", "#22c55e",
  "#f59e0b", "#ec4899", "#14b8a6", "#6366f1",
]

function pickEdgeColor(sourceId: string, targetId: string): string {
  let h = 0
  const key = sourceId + targetId
  for (let i = 0; i < key.length; i++) h = ((h << 5) - h + key.charCodeAt(i)) | 0
  return edgeColorPalette[Math.abs(h) % edgeColorPalette.length]
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

function buildGraphData(input: BuildInput): { nodes: Node[]; edges: Edge[] } {
  const { missions, crews, agents, connections, collapsedCrews, onToggleCollapse } = input
  const nodes: Node[] = []
  const edges: Edge[] = []

  // Build agent slug → crew id map
  const agentCrewMap = new Map<string, string>()
  const crewById = new Map<string, CrewSummary>()
  for (const agent of agents) {
    if (agent.slug && agent.crew?.id) {
      agentCrewMap.set(agent.slug, agent.crew.id)
    }
  }
  for (const crew of crews) {
    crewById.set(crew.id, crew)
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

  // Collect all tasks grouped by crew
  const crewTasks = new Map<string, { mission: Mission; task: MissionTask }[]>()
  const usedCrewIds = new Set<string>()

  for (const mission of activeMissions) {
    const tasks = mission.tasks || []
    for (const task of tasks) {
      const crewId = (task.agent_slug && agentCrewMap.get(task.agent_slug)) || mission.crew_id
      if (!crewId) continue
      usedCrewIds.add(crewId)
      if (!crewTasks.has(crewId)) crewTasks.set(crewId, [])
      crewTasks.get(crewId)!.push({ mission, task })
    }
    // Ensure mission's crew is always shown
    if (mission.crew_id) usedCrewIds.add(mission.crew_id)
  }

  // Also include crews with connections even if no tasks
  for (const conn of connections) {
    if (crewById.has(conn.from_crew_id)) usedCrewIds.add(conn.from_crew_id)
    if (crewById.has(conn.to_crew_id)) usedCrewIds.add(conn.to_crew_id)
  }

  // Layout crew groups horizontally
  const sortedCrewIds = [...usedCrewIds].sort((a, b) => {
    const aName = crewById.get(a)?.name || ""
    const bName = crewById.get(b)?.name || ""
    return aName.localeCompare(bName)
  })

  let crewX = 0
  const CREW_GAP = 80
  const TASK_WIDTH = 260
  const TASK_HEIGHT = 150
  const TASK_H_GAP = 60
  const TASK_V_GAP = 15
  const CREW_PADDING_TOP = 60
  const CREW_PADDING_SIDE = 40
  const CREW_PADDING_BOTTOM = 40
  const COLLAPSED_WIDTH = 300
  const COLLAPSED_HEIGHT = 50

  for (const crewId of sortedCrewIds) {
    const crew = crewById.get(crewId)
    if (!crew) continue

    const tasks = crewTasks.get(crewId) || []
    const collapsed = collapsedCrews.has(crewId)

    // Compute task stats for header
    const taskCount = tasks.length
    const activeCount = tasks.filter((t) => t.task.status === "IN_PROGRESS").length
    const completedCount = tasks.filter((t) => t.task.status === "COMPLETED").length
    const failedCount = tasks.filter((t) => t.task.status === "FAILED").length

    if (collapsed || tasks.length === 0) {
      // Collapsed crew node
      nodes.push({
        id: `crew-${crewId}`,
        type: "crew",
        position: { x: crewX, y: 0 },
        data: {
          label: crew.name,
          slug: crew.slug,
          color: crew.color,
          icon: crew.icon,
          agentCount: crew._count?.agents || 0,
          collapsed: true,
          taskCount,
          activeCount,
          completedCount,
          failedCount,
          onToggleCollapse,
          crewId,
        },
        style: { width: COLLAPSED_WIDTH, height: COLLAPSED_HEIGHT },
      })
      crewX += COLLAPSED_WIDTH + CREW_GAP
      continue
    }

    // Topological layout of tasks inside this crew
    const sortedTasks = [...tasks].sort((a, b) => a.task.task_order - b.task.task_order)
    const deps = new Map<string, string[]>()
    for (const { task } of sortedTasks) {
      try {
        deps.set(task.id, JSON.parse(task.depends_on || "[]"))
      } catch {
        deps.set(task.id, [])
      }
    }

    // Only keep deps that are within this crew's tasks
    const taskIds = new Set(sortedTasks.map((t) => t.task.id))
    for (const [taskId, taskDeps] of deps) {
      deps.set(taskId, taskDeps.filter((d) => taskIds.has(d)))
    }

    const levels = new Map<string, number>()
    function getLevel(taskId: string): number {
      if (levels.has(taskId)) return levels.get(taskId)!
      const taskDeps = deps.get(taskId) || []
      if (taskDeps.length === 0) {
        levels.set(taskId, 0)
        return 0
      }
      const level = Math.max(...taskDeps.map(getLevel)) + 1
      levels.set(taskId, level)
      return level
    }
    for (const { task } of sortedTasks) getLevel(task.id)

    const levelGroups = new Map<number, MissionTask[]>()
    for (const { task } of sortedTasks) {
      const level = levels.get(task.id) || 0
      if (!levelGroups.has(level)) levelGroups.set(level, [])
      levelGroups.get(level)!.push(task)
    }

    const maxLevel = Math.max(...[...levelGroups.keys()], 0)
    const maxLevelSize = Math.max(...[...levelGroups.values()].map((g) => g.length), 1)

    const crewWidth = (maxLevel + 1) * (TASK_WIDTH + TASK_H_GAP) + CREW_PADDING_SIDE * 2
    const crewHeight = maxLevelSize * (TASK_HEIGHT + TASK_V_GAP) + CREW_PADDING_TOP + CREW_PADDING_BOTTOM

    // Create crew group node
    nodes.push({
      id: `crew-${crewId}`,
      type: "crew",
      position: { x: crewX, y: 0 },
      data: {
        label: crew.name,
        slug: crew.slug,
        color: crew.color,
        icon: crew.icon,
        agentCount: crew._count?.agents || 0,
        collapsed: false,
        taskCount,
        activeCount,
        completedCount,
        failedCount,
        onToggleCollapse,
        crewId,
      },
      style: { width: crewWidth, height: crewHeight },
    })

    // Create child task nodes (positions relative to crew group)
    for (const [level, levelTasks] of levelGroups) {
      levelTasks.forEach((task, idx) => {
        const x = CREW_PADDING_SIDE + level * (TASK_WIDTH + TASK_H_GAP)
        const y = CREW_PADDING_TOP + idx * (TASK_HEIGHT + TASK_V_GAP)

        nodes.push({
          id: task.id,
          type: "agent",
          parentId: `crew-${crewId}`,
          extent: "parent" as const,
          position: { x, y },
          data: {
            label: task.title,
            status: task.status,
            agentName: task.agent_name || "Unassigned",
            agentSlug: task.agent_slug,
            iteration: task.iteration,
            maxIterations: task.max_iterations,
            tokenCount: task.token_count,
            estimatedCost: task.estimated_cost,
            durationMs: task.duration_ms,
            missionId: task.mission_id,
          },
          sourcePosition: Position.Right,
          targetPosition: Position.Left,
        })

        // Dependency edges (within crew)
        const taskDeps = deps.get(task.id) || []
        for (const depId of taskDeps) {
          const isActive = task.status === "IN_PROGRESS"
          const edgeColor = isActive ? statusColors.IN_PROGRESS : pickEdgeColor(depId, task.id)
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

    crewX += crewWidth + CREW_GAP
  }

  // Cross-crew dependency edges (tasks in different crews)
  for (const mission of activeMissions) {
    const tasks = mission.tasks || []
    for (const task of tasks) {
      let taskDeps: string[] = []
      try {
        taskDeps = JSON.parse(task.depends_on || "[]")
      } catch {
        continue
      }
      const taskCrewId = (task.agent_slug && agentCrewMap.get(task.agent_slug)) || mission.crew_id
      for (const depId of taskDeps) {
        // Find the dep task's crew
        const depTask = tasks.find((t) => t.id === depId)
        if (!depTask) continue
        const depCrewId = (depTask.agent_slug && agentCrewMap.get(depTask.agent_slug)) || mission.crew_id
        if (depCrewId !== taskCrewId) {
          // Cross-crew edge
          const edgeColor = "#a855f7" // purple for cross-crew
          edges.push({
            id: `e-cross-${depId}-${task.id}`,
            source: depId,
            target: task.id,
            type: "animated",
            data: { color: edgeColor, active: task.status === "IN_PROGRESS" },
            style: { strokeWidth: 2 },
            markerEnd: {
              type: MarkerType.ArrowClosed,
              color: edgeColor,
              width: 14,
              height: 14,
            },
          })
        }
      }
    }
  }

  // Permission edges between crews
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
      data: {
        direction: conn.direction,
        status: conn.status,
      },
      ...markers,
    })
  }

  // Sort: crew group nodes must come before their children
  nodes.sort((a, b) => {
    const aIsCrew = a.type === "crew" ? 0 : 1
    const bIsCrew = b.type === "crew" ? 0 : 1
    return aIsCrew - bIsCrew
  })

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
    const accent = statusColors[mission.status] || "#64748b"
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
        color: "#e2e8f0",
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
      try {
        deps.set(task.id, JSON.parse(task.depends_on || "[]"))
      } catch {
        deps.set(task.id, [])
      }
    }

    const levels = new Map<string, number>()
    function getLevel(taskId: string): number {
      if (levels.has(taskId)) return levels.get(taskId)!
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
          const edgeColor = isActive ? statusColors.IN_PROGRESS : pickEdgeColor(depId, task.id)
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
  { missions, crews, agents, connections, onTaskClick }: WorkflowGraphProps,
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

  const hasCrewData = crews && crews.length > 0

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

  useEffect(() => {
    if (prevDataRef.current === graphData) return
    prevDataRef.current = graphData
    setNodes(graphData.nodes)
    setEdges(graphData.edges)
  }, [graphData, setNodes, setEdges])

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

  // Apply dimming styles to nodes and edges
  const displayNodes = useMemo(() => {
    if (!highlightedNodeId) return nodes
    return nodes.map((n) => ({
      ...n,
      style: {
        ...n.style,
        opacity: dimmedNodeIds.has(n.id) ? 0.15 : 1,
        transition: "opacity 0.3s ease",
      },
    }))
  }, [nodes, highlightedNodeId, dimmedNodeIds])

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
      <div className="rounded-xl border border-white/[0.06] bg-[#0d0f14] p-16">
        <EmptyState
          icon={Workflow}
          title="No missions yet"
          description="Create a mission from a crew's lead agent to see the workflow graph here"
        />
      </div>
    )
  }

  return (
    <div className="h-full w-full overflow-hidden bg-[#0a0c10]">
      <div className="h-full w-full">
        <ReactFlow
          nodes={displayNodes}
          edges={displayEdges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onNodeClick={onNodeClick}
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
            className="!bg-[#1a1d23] !border-white/10 !rounded-lg !shadow-xl [&_button]:!bg-[#1a1d23] [&_button]:!border-white/10 [&_button]:!text-white/60 [&_button:hover]:!bg-white/10"
          />
          <MiniMap
            nodeColor={(n) => {
              if (n.id.startsWith("crew-")) {
                const paletteName = (n.data as Record<string, unknown>)?.color as string | null
                if (paletteName) return crewColorMap[paletteName] || paletteName
                return "#1e2332"
              }
              if (n.id.startsWith("mission-")) return "#1e2332"
              return statusColors[(n.data?.status as string) || "PENDING"] || "#64748b"
            }}
            maskColor="rgba(10, 12, 16, 0.85)"
            className="!bg-[#0d0f14] !border-white/[0.06] !rounded-lg"
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
