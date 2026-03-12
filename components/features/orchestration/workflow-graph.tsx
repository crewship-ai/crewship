"use client"

import { useCallback, useMemo, useState } from "react"
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  type Node,
  type Edge,
  type NodeTypes,
  Position,
  MarkerType,
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import { Card, CardContent } from "@/components/ui/card"
import { EmptyState } from "@/components/layout/empty-state"
import { Workflow } from "lucide-react"
import type { Mission, MissionTask } from "@/lib/types/mission"
import { AgentNode } from "./agent-node"
import { TaskDetailPanel } from "./task-detail-panel"

interface WorkflowGraphProps {
  missions: Mission[]
}

const nodeTypes: NodeTypes = {
  agent: AgentNode,
}

function getStatusColor(status: string): string {
  switch (status) {
    case "COMPLETED":
      return "#22c55e"
    case "IN_PROGRESS":
      return "#3b82f6"
    case "FAILED":
      return "#ef4444"
    case "BLOCKED":
      return "#f59e0b"
    case "PENDING":
      return "#94a3b8"
    case "SKIPPED":
      return "#6b7280"
    default:
      return "#94a3b8"
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
    if (tasks.length === 0) continue

    // Mission label node
    nodes.push({
      id: `mission-${mission.id}`,
      type: "default",
      position: { x: 0, y: missionY },
      data: { label: mission.title },
      style: {
        background: "hsl(var(--muted))",
        border: "1px solid hsl(var(--border))",
        borderRadius: "8px",
        padding: "8px 16px",
        fontSize: "13px",
        fontWeight: 600,
        color: "hsl(var(--foreground))",
        width: 200,
      },
      sourcePosition: Position.Right,
      targetPosition: Position.Left,
    })

    // Position tasks in a grid
    const tasksByOrder = [...tasks].sort((a, b) => a.task_order - b.task_order)
    const deps = new Map<string, string[]>()
    for (const task of tasksByOrder) {
      try {
        const parsed = JSON.parse(task.depends_on || "[]")
        deps.set(task.id, parsed)
      } catch {
        deps.set(task.id, [])
      }
    }

    // Compute columns via topological levels
    const levels = new Map<string, number>()
    function getLevel(taskId: string): number {
      if (levels.has(taskId)) return levels.get(taskId)!
      const taskDeps = deps.get(taskId) || []
      if (taskDeps.length === 0) {
        levels.set(taskId, 0)
        return 0
      }
      const maxDepLevel = Math.max(...taskDeps.map(getLevel))
      const level = maxDepLevel + 1
      levels.set(taskId, level)
      return level
    }
    for (const task of tasksByOrder) getLevel(task.id)

    // Group tasks by level
    const levelGroups = new Map<number, MissionTask[]>()
    for (const task of tasksByOrder) {
      const level = levels.get(task.id) || 0
      if (!levelGroups.has(level)) levelGroups.set(level, [])
      levelGroups.get(level)!.push(task)
    }

    // Create task nodes
    const xOffset = 280
    for (const [level, levelTasks] of levelGroups) {
      levelTasks.forEach((task, idx) => {
        const x = xOffset + level * 280
        const y = missionY + idx * 100

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
            missionId: mission.id,
          },
          sourcePosition: Position.Right,
          targetPosition: Position.Left,
        })

        // Edge from mission label to first-level tasks
        if (level === 0) {
          edges.push({
            id: `e-mission-${mission.id}-${task.id}`,
            source: `mission-${mission.id}`,
            target: task.id,
            type: "smoothstep",
            animated: mission.status === "IN_PROGRESS",
            style: { stroke: "hsl(var(--border))", strokeWidth: 1.5 },
          })
        }

        // Dependency edges
        const taskDeps = deps.get(task.id) || []
        for (const depId of taskDeps) {
          edges.push({
            id: `e-${depId}-${task.id}`,
            source: depId,
            target: task.id,
            type: "smoothstep",
            animated: task.status === "IN_PROGRESS",
            style: {
              stroke: getStatusColor(task.status),
              strokeWidth: 2,
            },
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

    // Offset next mission
    const maxLevelSize = Math.max(...[...levelGroups.values()].map((g) => g.length), 1)
    missionY += maxLevelSize * 100 + 80
  }

  return { nodes, edges }
}

export function WorkflowGraph({ missions }: WorkflowGraphProps) {
  const [selectedTask, setSelectedTask] = useState<MissionTask | null>(null)
  const { nodes, edges } = useMemo(() => buildGraphData(missions), [missions])

  const onNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      if (node.id.startsWith("mission-")) return
      for (const m of missions) {
        const task = m.tasks?.find((t) => t.id === node.id)
        if (task) {
          setSelectedTask(task)
          return
        }
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
