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
  type NodeTypes,
  type EdgeTypes,
  type OnNodeDrag,
  BackgroundVariant,
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import { EmptyState } from "@/components/layout/empty-state"
import { Workflow } from "lucide-react"
import type { Mission, MissionTask } from "@/lib/types/mission"
import type { CrewSummary, AgentSummary, CrewConnection } from "@/lib/types/orchestration"
import { useAgentActivity } from "@/hooks/use-agent-activity"
import { AgentNode } from "./agent-node"
import { AgentCardNode } from "./agent-card-node"
import { AnimatedEdge } from "./animated-edge"
import { CrewGroupNode } from "./crew-group-node"
import { PipelineRunNode } from "./pipeline-run-node"
import { STATUS_COLORS, CREW_COLORS, CREW_COLOR_DEFAULT, GRAPH_CHROME } from "@/lib/colors"
import { PermissionEdge } from "./permission-edge"

import { buildFlatGraphData, buildGraphData, buildPipelineNodes, buildIssueRoutineEdges } from "./workflow-graph-builders"
import type { PipelineForGraph } from "./workflow-graph-builders"

export interface WorkflowGraphRef {
  focusActive: () => void
}

interface WorkflowGraphProps {
  missions: Mission[]
  crews?: CrewSummary[]
  agents?: AgentSummary[]
  connections?: CrewConnection[]
  /**
   * Optional list of saved pipelines. When supplied, each pipeline
   * renders as a `pipelineRun` node along the bottom of the graph
   * acting as the workspace's pipeline registry. Empty / nil hides
   * the pipeline row entirely so workspaces without pipelines don't
   * show an empty band.
   */
  pipelines?: PipelineForGraph[]
  /**
   * Click handler for pipeline-run nodes. Receives the pipeline id;
   * the parent layout typically opens the run-detail side-sheet.
   */
  onPipelineClick?: (pipelineId: string) => void
  onTaskClick?: (task: MissionTask) => void
  highlightAgentSlug?: string | null
}


const nodeTypes: NodeTypes = {
  agent: AgentNode,
  agentCard: AgentCardNode,
  crew: CrewGroupNode,
  pipelineRun: PipelineRunNode,
}

const edgeTypes: EdgeTypes = {
  animated: AnimatedEdge,
  permission: PermissionEdge,
}


function WorkflowGraphInner(
  { missions, crews, agents, connections, pipelines, onPipelineClick, onTaskClick, highlightAgentSlug }: WorkflowGraphProps,
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
    // Re-using ReactFlow's Edge type avoids a self-referential
    // `typeof graphData.edges` annotation which TS rejects (TS2502).
    let data: { nodes: Node[]; edges: import("@xyflow/react").Edge[] }
    if (hasCrewData) {
      data = buildGraphData({
        missions,
        crews: crews!,
        agents: agents!,
        connections: connections || [],
        collapsedCrews,
        onToggleCollapse: toggleCollapse,
      })
    } else {
      data = buildFlatGraphData(missions)
    }
    // Append the pipeline registry row beneath the main graph. We
    // build a crew_id → name map so the PipelineRunNode can render
    // "authored by Marketing" instead of the raw crew_id. Click
    // handler is threaded onto each node's data so the parent's
    // onPipelineClick fires when the user clicks a pipeline card.
    if (pipelines && pipelines.length > 0) {
      const crewNames = new Map<string, string>()
      for (const c of crews ?? []) {
        if (c.id && c.name) crewNames.set(c.id, c.name)
      }
      const pipelineNodes = buildPipelineNodes(pipelines, { crewNameById: crewNames })
      // Inject the click handler into each node's data shape — React
      // Flow passes data straight to the node component.
      for (const n of pipelineNodes) {
        if (n.data) (n.data as Record<string, unknown>).onClick = onPipelineClick
      }
      // Edges from each issue with a bound routine to the pipeline
      // node, so the disconnected registry strip becomes a wired
      // mesh ("this issue runs that routine"). The dashed blue
      // styling distinguishes binding edges from execution-flow
      // edges (solid arrows). Visible-node check skips edges to
      // pipelines the user filtered out via the registry strip.
      const visibleIDs = new Set([...data.nodes, ...pipelineNodes].map((n) => n.id))
      const routineEdges = buildIssueRoutineEdges(missions, pipelines, visibleIDs)
      data = {
        nodes: [...data.nodes, ...pipelineNodes],
        edges: [...data.edges, ...routineEdges],
      }
    }
    return data
  }, [missions, crews, agents, connections, pipelines, onPipelineClick, collapsedCrews, toggleCollapse, hasCrewData])

  const [nodes, setNodes, onNodesChange] = useNodesState(graphData.nodes)
  const [edgesState, setEdges, onEdgesChange] = useEdgesState(graphData.edges)
  const { fitView, setCenter } = useReactFlow()
  const prevDataRef = useRef(graphData)

  // Track user-dragged node positions so polling doesn't reset them
  const userPositions = useRef(new Map<string, { x: number; y: number }>())

  // Type via OnNodeDrag so the event param is inferred from the installed
  // @xyflow/react version (see trace-canvas.tsx for the version-skew rationale).
  const onNodeDragStop = useCallback<OnNodeDrag<Node>>((_, node) => {
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

  // Empty state ONLY when there's nothing to render at all. With
  // pipelines, the graph stays useful even when there are no
  // missions yet — the registry row of saved pipelines is reason
  // enough to show the canvas. Falling through to the canvas
  // when missions=0 but pipelines>0 is what makes the pipelines-
  // first workflow visible to fresh workspaces.
  if (missions.length === 0 && (!pipelines || pipelines.length === 0)) {
    return (
      <div className="rounded-xl border border-border bg-card p-16">
        <EmptyState
          icon={Workflow}
          title="No missions or pipelines yet"
          description="Create a mission from a crew's lead agent — or save a pipeline — to see the workflow graph here"
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
