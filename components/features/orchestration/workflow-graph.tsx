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
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import { Card, CardContent } from "@/components/ui/card"
import { EmptyState } from "@/components/layout/empty-state"
import { Workflow } from "lucide-react"
import type { Mission, MissionTask } from "@/lib/types/mission"
import { AgentNode } from "./agent-node"
import { AnimatedEdge } from "./animated-edge"
import { TaskDetailPanel } from "./task-detail-panel"

export interface WorkflowGraphRef {
  focusActive: () => void
}

interface WorkflowGraphProps {
  missions: Mission[]
}

const nodeTypes: NodeTypes = { agent: AgentNode }
const edgeTypes: EdgeTypes = { animated: AnimatedEdge }

function getStatusColor(status: string): string {
  switch (status) {
    case "COMPLETED": return "#22c55e"
    case "IN_PROGRESS": return "#3b82f6"
    case "FAILED": return "#ef4444"
    case "BLOCKED": return "#f59e0b"
    case "PENDING": return "#94a3b8"
    case "PLANNING": return "#94a3b8"
    case "REVIEW": return "#a855f7"
    case "CANCELLED": return "#6b7280"
    case "SKIPPED": return "#6b7280"
    default: return "#94a3b8"
  }
}

function buildGraphData(missions: Mission[]): { nodes: Node[]; edges: Edge[] } {
  const nodes: Node[] = []
  const edges: Edge[] = []

  const activeMissions = missions.filter(
    (m) => m.status === "IN_PROGRESS" || m.status === "PLANNING" || m.status === "REVIEW"
  )

  if (activeMissions.length === 0) {
    const recentMissions = [...missions]
      .sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime())
      .slice(0, 3)
    activeMissions.push(...recentMissions)
  }

  let missionY = 0

  for (const mission of activeMissions) {
    const tasks = mission.tasks || []

    const totalCost = tasks.reduce((sum, t) => sum + (t.estimated_cost || 0), 0)
    const totalTokens = tasks.reduce((sum, t) => sum + (t.token_count || 0), 0)
    const statusLabel = tasks.length === 0 && mission.status === "PLANNING"
      ? ` — Planning...`
      : totalTokens > 0
        ? ` (${(totalTokens / 1000).toFixed(1)}k tok · $${totalCost.toFixed(3)})`
        : ""

    nodes.push({
      id: `mission-${mission.id}`,
      type: "default",
      position: { x: 0, y: missionY },
      data: { label: `${mission.title}${statusLabel}` },
      style: {
        background: "hsl(var(--muted))",
        border: `2px solid ${getStatusColor(mission.status)}`,
        borderRadius: "10px",
        padding: "8px 16px",
        fontSize: "13px",
        fontWeight: 600,
        color: "hsl(var(--foreground))",
        width: 240,
      },
      sourcePosition: Position.Right,
      targetPosition: Position.Left,
    })

    if (tasks.length === 0) {
      missionY += 80
      continue
    }

    const tasksByOrder = [...tasks].sort((a, b) => a.task_order - b.task_order)
    const deps = new Map<string, string[]>()
    for (const task of tasksByOrder) {
      try { deps.set(task.id, JSON.parse(task.depends_on || "[]")) }
      catch { deps.set(task.id, []) }
    }

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
        const x = xOffset + level * 300
        const y = missionY + idx * 110

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
            missionId: mission.id,
          },
          sourcePosition: Position.Right,
          targetPosition: Position.Left,
        })

        if (level === 0) {
          edges.push({
            id: `e-mission-${mission.id}-${task.id}`,
            source: `mission-${mission.id}`,
            target: task.id,
            type: "animated",
            animated: false,
            data: { color: "hsl(var(--border))", active: mission.status === "IN_PROGRESS" },
            style: { strokeWidth: 1.5 },
          })
        }

        const taskDeps = deps.get(task.id) || []
        for (const depId of taskDeps) {
          const isActive = task.status === "IN_PROGRESS"
          edges.push({
            id: `e-${depId}-${task.id}`,
            source: depId,
            target: task.id,
            type: "animated",
            animated: false,
            data: { color: getStatusColor(task.status), active: isActive },
            style: { strokeWidth: 2 },
            markerEnd: {
              type: MarkerType.ArrowClosed,
              color: getStatusColor(task.status),
              width: 16,
              height: 16,
            },
          })
        }
      })
    }

    const maxLevelSize = Math.max(...[...levelGroups.values()].map((g) => g.length), 1)
    missionY += maxLevelSize * 110 + 80
  }

  return { nodes, edges }
}

function WorkflowGraphInner({ missions }: WorkflowGraphProps, ref: React.ForwardedRef<WorkflowGraphRef>) {
  const [selectedTask, setSelectedTask] = useState<MissionTask | null>(null)
  const graphData = useMemo(() => buildGraphData(missions), [missions])
  const [nodes, setNodes, onNodesChange] = useNodesState(graphData.nodes)
  const [edges, setEdges, onEdgesChange] = useEdgesState(graphData.edges)
  const { fitView, setCenter } = useReactFlow()
  const prevDataRef = useRef(graphData)

  // Update nodes/edges when missions data changes, but preserve positions
  useEffect(() => {
    if (prevDataRef.current === graphData) return
    prevDataRef.current = graphData
    setNodes(graphData.nodes)
    setEdges(graphData.edges)
  }, [graphData, setNodes, setEdges])

  useImperativeHandle(ref, () => ({
    focusActive() {
      const activeNode = nodes.find((n) => !n.id.startsWith("mission-") && (n.data as Record<string, unknown>)?.status === "IN_PROGRESS")
      if (activeNode) {
        setCenter(activeNode.position.x + 120, activeNode.position.y + 50, { zoom: 1.2, duration: 600 })
      } else {
        fitView({ duration: 600, padding: 0.2 })
      }
    },
  }), [nodes, setCenter, fitView])

  const onNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      if (node.id.startsWith("mission-")) return
      for (const m of missions) {
        const task = m.tasks?.find((t) => t.id === node.id)
        if (task) { setSelectedTask(task); return }
      }
    },
    [missions]
  )

  if (missions.length === 0) {
    return (
      <Card>
        <CardContent className="py-12">
          <EmptyState
            icon={Workflow}
            title="No missions yet"
            description="Create a mission from a crew page to see the workflow graph"
          />
        </CardContent>
      </Card>
    )
  }

  return (
    <div className="flex gap-4">
      <Card className="flex-1">
        <div className="h-[600px] w-full">
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            edgeTypes={edgeTypes}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onNodeClick={onNodeClick}
            fitView
            fitViewOptions={{ padding: 0.2 }}
            minZoom={0.3}
            maxZoom={2}
            proOptions={{ hideAttribution: true }}
            className="bg-background"
          >
            <Background gap={20} size={1} color="hsl(var(--border) / 0.3)" />
            <Controls showInteractive={false} />
            <MiniMap
              nodeColor={(n) => {
                if (n.id.startsWith("mission-")) return "hsl(var(--muted))"
                return getStatusColor((n.data?.status as string) || "PENDING")
              }}
              maskColor="hsl(var(--background) / 0.7)"
              className="!bg-background !border-border"
            />
          </ReactFlow>
        </div>
      </Card>

      {selectedTask && (
        <TaskDetailPanel task={selectedTask} onClose={() => setSelectedTask(null)} />
      )}
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
