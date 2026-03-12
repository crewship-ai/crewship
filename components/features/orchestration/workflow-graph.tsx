"use client"

import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef } from "react"
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
import { AgentNode } from "./agent-node"
import { AnimatedEdge } from "./animated-edge"

export interface WorkflowGraphRef {
  focusActive: () => void
}

interface WorkflowGraphProps {
  missions: Mission[]
  onTaskClick?: (task: MissionTask) => void
}

const nodeTypes: NodeTypes = { agent: AgentNode }
const edgeTypes: EdgeTypes = { animated: AnimatedEdge }

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

// Distinct edge colors for visual variety (like Bleu)
const edgeColorPalette = [
  "#06b6d4", // cyan
  "#3b82f6", // blue
  "#8b5cf6", // violet
  "#22c55e", // green
  "#f59e0b", // amber
  "#ec4899", // pink
  "#14b8a6", // teal
  "#6366f1", // indigo
]

function pickEdgeColor(sourceId: string, targetId: string): string {
  let h = 0
  const key = sourceId + targetId
  for (let i = 0; i < key.length; i++) h = ((h << 5) - h + key.charCodeAt(i)) | 0
  return edgeColorPalette[Math.abs(h) % edgeColorPalette.length]
}

function buildGraphData(missions: Mission[]): { nodes: Node[]; edges: Edge[] } {
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
    const statusLabel = tasks.length === 0 && mission.status === "PLANNING"
      ? " — Planning..."
      : totalTokens > 0
        ? ` · ${(totalTokens / 1000).toFixed(1)}k tok`
        : ""

    // Mission node — styled as a header card
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
      try { deps.set(task.id, JSON.parse(task.depends_on || "[]")) }
      catch { deps.set(task.id, []) }
    }

    // Topological level assignment
    const levels = new Map<string, number>()
    function getLevel(taskId: string): number {
      if (levels.has(taskId)) return levels.get(taskId)!
      const taskDeps = deps.get(taskId) || []
      if (taskDeps.length === 0) { levels.set(taskId, 0); return 0 }
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

        // Edge from mission to first-level tasks
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

        // Dependency edges
        const taskDeps = deps.get(task.id) || []
        for (const depId of taskDeps) {
          const isActive = task.status === "IN_PROGRESS"
          const edgeColor = isActive
            ? statusColors.IN_PROGRESS
            : pickEdgeColor(depId, task.id)

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

function WorkflowGraphInner({ missions, onTaskClick }: WorkflowGraphProps, ref: React.ForwardedRef<WorkflowGraphRef>) {
  const graphData = useMemo(() => buildGraphData(missions), [missions])
  const [nodes, setNodes, onNodesChange] = useNodesState(graphData.nodes)
  const [edges, setEdges, onEdgesChange] = useEdgesState(graphData.edges)
  const { fitView, setCenter } = useReactFlow()
  const prevDataRef = useRef(graphData)

  useEffect(() => {
    if (prevDataRef.current === graphData) return
    prevDataRef.current = graphData
    setNodes(graphData.nodes)
    setEdges(graphData.edges)
  }, [graphData, setNodes, setEdges])

  useImperativeHandle(ref, () => ({
    focusActive() {
      const activeNode = nodes.find(
        (n) => !n.id.startsWith("mission-") && (n.data as Record<string, unknown>)?.status === "IN_PROGRESS"
      )
      if (activeNode) {
        setCenter(activeNode.position.x + 130, activeNode.position.y + 60, { zoom: 1.2, duration: 600 })
      } else {
        fitView({ duration: 600, padding: 0.2 })
      }
    },
  }), [nodes, setCenter, fitView])

  const onNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      if (node.id.startsWith("mission-")) return
      if (!onTaskClick) return
      for (const m of missions) {
        const task = m.tasks?.find((t) => t.id === node.id)
        if (task) { onTaskClick(task); return }
      }
    },
    [missions, onTaskClick]
  )

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
    <div className="rounded-xl border border-white/[0.06] overflow-hidden bg-[#0a0c10]">
      <div className="h-[calc(100vh-380px)] min-h-[450px] w-full">
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onNodeClick={onNodeClick}
          fitView
          fitViewOptions={{ padding: 0.3 }}
          minZoom={0.2}
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
