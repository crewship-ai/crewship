import {
  Position,
  MarkerType,
  type Node,
  type Edge,
} from "@xyflow/react"
import type { Mission, MissionTask } from "@/lib/types/mission"
import type { CrewSummary, AgentSummary, CrewConnection } from "@/lib/types/orchestration"
import { Graph as DagreGraph, layout as dagreLayout } from "@dagrejs/dagre"
import { STATUS_COLORS, EDGE_COLOR_PALETTE, CREW_COLOR_DEFAULT, GRAPH_CHROME } from "@/lib/colors"
import { getPermissionMarkers } from "./permission-edge"

// Pure graph-data construction extracted from workflow-graph.tsx —
// pickEdgeColor / parseDependsOn helpers, BuildInput shape, layout
// constants, plus the two build*GraphData functions that map
// missions+crews into ReactFlow node/edge arrays. No JSX.

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


export {
  pickEdgeColor,
  parseDependsOn,
  TASK_WIDTH,
  TASK_HEIGHT,
  COLLAPSED_WIDTH,
  COLLAPSED_HEIGHT,
  CREW_PADDING_TOP,
  CREW_PADDING_SIDE,
  CREW_PADDING_BOTTOM,
  buildGraphData,
  buildFlatGraphData,
  buildPipelineNodes,
}
export type { BuildInput, PipelineForGraph }

// PipelineForGraph is the trimmed shape buildPipelineNodes consumes.
// Mirrors the relevant fields of usePipelines's Pipeline type so the
// orchestration page can pass its rows through without a transform.
interface PipelineForGraph {
  id: string
  slug: string
  name: string
  description?: string
  invocation_count: number
  last_invocation_status?: string
  author_crew_id?: string
}

// buildPipelineNodes returns React Flow nodes for the workspace's
// saved pipelines. The orchestration page calls this and concats
// the result onto whichever main builder it's using
// (buildGraphData / buildFlatGraphData).
//
// Layout strategy: pipelines lay out in a horizontal row beneath
// the main graph at y = baselineY. We use a fixed pitch so the
// row is predictable; if the workspace ever has 50+ pipelines
// dagre will be a better fit, but for the test-feature scope
// (a handful per workspace) this stays readable and dependency-
// free.
//
// Each pipeline becomes a "pipelineRun"-typed node so it picks up
// the PipelineRunNode component registered in WorkflowGraph's
// nodeTypes. We render the node in "completed" state with the
// invocation count as the displayed step count — this is a static
// "registry card" view, not a live run. When live runs land,
// buildPipelineRunNodes (a future helper) will emit per-run nodes
// from journal entries; until then, the registry view is enough
// to show the pipelines exist.
function buildPipelineNodes(
  pipelines: PipelineForGraph[],
  opts: { baselineY?: number; xStart?: number; pitch?: number; crewNameById?: Map<string, string> } = {},
): Node[] {
  if (!pipelines || pipelines.length === 0) return []
  const baselineY = opts.baselineY ?? 800
  const xStart = opts.xStart ?? 0
  const pitch = opts.pitch ?? 250
  const crewNames = opts.crewNameById ?? new Map<string, string>()

  return pipelines.map((p, i): Node => {
    // Status maps: COMPLETED → completed, FAILED → failed, anything
    // else → queued. The registry card represents the LAST observed
    // invocation; per-run "running" indicators come from a separate
    // future builder driven by WS events.
    const last = (p.last_invocation_status || "").toUpperCase()
    let status: "completed" | "failed" | "queued" = "queued"
    if (last === "COMPLETED") status = "completed"
    else if (last === "FAILED") status = "failed"

    const authorLabel = p.author_crew_id
      ? (crewNames.get(p.author_crew_id) ?? p.author_crew_id)
      : undefined

    return {
      id: `pipeline:${p.id}`,
      type: "pipelineRun",
      position: { x: xStart + i * pitch, y: baselineY },
      data: {
        pipelineSlug: p.slug,
        pipelineName: p.name,
        runId: p.id, // for click-through to detail
        status,
        // Use invocation_count in place of step progress on the
        // registry card — communicates "this pipeline ran N times"
        // at a glance.
        stepCount: p.invocation_count,
        stepIndex: p.invocation_count,
        authorCrewLabel: authorLabel,
      },
      draggable: false,
      selectable: true,
    }
  })
}
