"use client"

import { useCallback, useEffect, useMemo, useRef } from "react"
import {
  ReactFlow,
  Background,
  Controls,
  BackgroundVariant,
  ReactFlowProvider,
  useNodesState,
  useEdgesState,
  useReactFlow,
  type Edge,
  type Node,
  type NodeTypes,
  type EdgeTypes,
  type OnNodeDrag,
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import { Workflow } from "lucide-react"
import { EmptyState } from "@/components/layout/empty-state"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import type { PipelineDSL } from "@/lib/trace/types"
import { buildTraceGraph } from "@/lib/trace/build-trace-graph"
import { TraceStepNode, TraceTriggerNode } from "./trace-step-node"
import {
  OverviewIssueNode,
  OverviewRoutineNode,
  OverviewRunNode,
} from "./overview-nodes"
import { TraceDataFlowEdge } from "./trace-data-flow-edge"
import type { HeatmapBucket } from "@/lib/trace/percentile-heatmap"

// TraceCanvas — ReactFlow surface for the /activity trace view.
//
// Phase 2: renders the full step chain from (run, dsl) via
// buildTraceGraph, with sequencing edges and live status painting.
// Click a step → invokes onStepSelect(stepId), which the page
// translates into a URL update so the side panel opens.
//
// Inferring step status from run state is centralized in
// buildTraceGraph; this component is a thin React Flow wrapper.

const nodeTypes: NodeTypes = {
  traceStep: TraceStepNode,
  traceTrigger: TraceTriggerNode,
  overviewIssue: OverviewIssueNode,
  overviewRoutine: OverviewRoutineNode,
  overviewRun: OverviewRunNode,
}

const edgeTypes: EdgeTypes = {
  traceDataFlow: TraceDataFlowEdge,
}

// fitView's job is "show everything", which for a long routine means
// "show everything at 3 pixels tall". A 15-step graph is ~1900px deep;
// fit into a ~700px pane that is zoom 0.35, and the labels — the whole
// reason the node has a label — stop being readable.
//
// minZoom stops it shrinking past legibility: past that point the graph
// overflows and the user pans, which is the right trade. A step you can
// read and must scroll to beats a whole routine you cannot read.
// maxZoom keeps a two-step routine from filling the pane with two
// enormous cards.
//
// ReactFlow centres the bounds it fits, so the clamp lands the middle of
// the routine in view rather than a corner.
//
// Trace canvas only. The overview graph is built by build-overview-graph
// (still LR, its own node components) and keeps its own fit behaviour —
// this is a fix for the step tree, and widening it to a surface with a
// different topology would be guessing.
const FIT_VIEW = { padding: 0.18, minZoom: 0.62, maxZoom: 1.15 } as const

interface TraceCanvasProps {
  run: PipelineRun | null
  dsl: PipelineDSL | null
  selectedStepId: string | null
  onStepSelect: (stepId: string | null) => void
  // Workspace id is used by waitpoint nodes to call the workspace-
  // scoped decide endpoint when the user clicks Approve/Deny inline.
  workspaceId: string
  waitpointTokensByStepId: ReadonlyMap<string, string>
  // Pre-computed heatmap buckets; the page memoizes this so a step
  // metric flowing in over realtime doesn't force a dagre relayout.
  heatmapBuckets: ReadonlyMap<string, HeatmapBucket>
  // Per-step duration + cost — surfaced in the step hover card.
  // Same Map identity as the page's useStepMetrics output.
  stepMetrics: ReadonlyMap<string, { durationMs: number; costUsd: number }>
  // Overview graph (issues → routines → last-run chains) shown when
  // no run is selected. Caller computes it from missions + pipelines
  // + runs and memoizes; passing in keeps the canvas dumb.
  overview?: { nodes: Node[]; edges: Edge[] } | null
  onSelectRun?: (runId: string) => void
}

export function TraceCanvas(props: TraceCanvasProps) {
  if (!props.run) {
    // Overview mode: render workspace-level chains (issues → bound
    // routines → last run) when the caller supplied one. Falling
    // back to the empty state keeps the page useful when missions /
    // pipelines / runs are still loading on first paint.
    return (
      <ReactFlowProvider>
        <OverviewInner
          overview={props.overview}
          onSelectRun={props.onSelectRun}
        />
      </ReactFlowProvider>
    )
  }
  return (
    <ReactFlowProvider>
      <CanvasInner {...props} run={props.run} />
    </ReactFlowProvider>
  )
}

function OverviewInner({
  overview,
  onSelectRun,
}: {
  overview?: { nodes: Node[]; edges: Edge[] } | null
  onSelectRun?: (runId: string) => void
}) {
  const [nodes, setNodes, onNodesChange] = useNodesState(overview?.nodes ?? [])
  const [edges, setEdges, onEdgesChange] = useEdgesState(overview?.edges ?? [])
  const { fitView } = useReactFlow()

  useEffect(() => {
    setNodes(overview?.nodes ?? [])
    setEdges(overview?.edges ?? [])
    const t = setTimeout(() => fitView({ padding: 0.25, duration: 300 }), 50)
    return () => clearTimeout(t)
  }, [overview, setNodes, setEdges, fitView])

  const onNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      if (node.id.startsWith("run:") && onSelectRun) {
        const runId = node.id.slice("run:".length)
        onSelectRun(runId)
      }
      // Issue + Routine clicks navigate via the node-level <Link>
      // wrapper or are handled at the rail level; canvas does
      // nothing extra.
    },
    [onSelectRun],
  )

  if (!overview || overview.nodes.length === 0) {
    return (
      <div className="flex h-full items-center justify-center bg-background">
        <EmptyState
          icon={Workflow}
          title="Pick a run to inspect"
          description="Select a run from the timeline rail to see its full execution trace — every HTTP call, transform, agent run, and human approval gate, with the data that flowed between them."
        />
      </div>
    )
  }

  return (
    <div className="h-full w-full overflow-hidden bg-background">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onNodeClick={onNodeClick}
        fitView
        fitViewOptions={{ padding: 0.3 }}
        minZoom={0.1}
        maxZoom={2.5}
        proOptions={{ hideAttribution: true }}
        className="!bg-transparent"
      >
        <Background
          variant={BackgroundVariant.Dots}
          gap={24}
          size={1.5}
          color="rgba(100, 116, 139, 0.12)"
        />
        <Controls
          showInteractive={false}
          className="!bg-muted !border-border !rounded-lg !shadow-xl [&_button]:!bg-muted [&_button]:!border-border [&_button]:!text-muted-foreground [&_button:hover]:!bg-accent"
        />
      </ReactFlow>
    </div>
  )
}

function CanvasInner({
  run,
  dsl,
  selectedStepId,
  onStepSelect,
  workspaceId,
  waitpointTokensByStepId,
  heatmapBuckets,
  stepMetrics,
}: TraceCanvasProps & { run: PipelineRun }) {
  const graphData = useMemo(
    () =>
      buildTraceGraph(run, dsl, {
        selectedStepId,
        workspaceId,
        waitpointTokensByStepId,
        heatmapBuckets,
        stepMetrics,
      }),
    [run, dsl, selectedStepId, workspaceId, waitpointTokensByStepId, heatmapBuckets, stepMetrics],
  )

  const [nodes, setNodes, onNodesChange] = useNodesState(graphData.nodes)
  const [edges, setEdges, onEdgesChange] = useEdgesState(graphData.edges)
  const { fitView } = useReactFlow()

  // Track user-dragged node positions so realtime status updates
  // don't snap a node back to its dagre-computed home. Same pattern
  // as the orchestration WorkflowGraph.
  const userPositions = useRef(new Map<string, { x: number; y: number }>())

  // Type the callback via OnNodeDrag so the event param is inferred from the
  // installed @xyflow/react version rather than hard-coded. ReactFlow has typed
  // this param differently across versions (DOM `MouseEvent | TouchEvent` vs
  // React `MouseEvent`); pinning either literal breaks whichever environment
  // resolves the other. Sourcing the type from the package can't mismatch.
  const onNodeDragStop = useCallback<OnNodeDrag<Node>>((_, node) => {
    userPositions.current.set(node.id, { ...node.position })
  }, [])

  // Detect "different run" vs "same run, status changed". When the
  // run changes we reset positions + fitView. When the same run gets
  // re-rendered (status update), we preserve user-dragged positions.
  const prevRunIdRef = useRef<string>(run.id)
  useEffect(() => {
    const isRunSwitch = prevRunIdRef.current !== run.id
    prevRunIdRef.current = run.id

    if (isRunSwitch) {
      userPositions.current.clear()
      setNodes(graphData.nodes)
      setEdges(graphData.edges)
      // Slight delay so React Flow has the new graph in state
      // before we ask it to fit view.
      const t = setTimeout(() => fitView({ ...FIT_VIEW, duration: 400 }), 50)
      return () => clearTimeout(t)
    }

    // Same run — merge positions through, but pick up new status /
    // selected / heatmap from the rebuilt nodes.
    setNodes((prev) => {
      const prevById = new Map(prev.map((n) => [n.id, n]))
      return graphData.nodes.map((n) => {
        const userPos = userPositions.current.get(n.id)
        const existing = prevById.get(n.id)
        if (userPos) return { ...n, position: userPos }
        if (existing) return { ...n, position: existing.position }
        return n
      })
    })
    setEdges(graphData.edges)
  }, [graphData, run.id, setNodes, setEdges, fitView])

  const onNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      if (node.id === "__trigger__") {
        onStepSelect(null)
        return
      }
      onStepSelect(node.id)
    },
    [onStepSelect],
  )

  const onPaneClick = useCallback(() => {
    onStepSelect(null)
  }, [onStepSelect])

  // Empty trace — DSL has no steps and no outputs were captured.
  // Surface a friendly message rather than an empty canvas.
  if (graphData.nodes.length <= 1) {
    return (
      <div className="flex h-full items-center justify-center bg-background">
        <EmptyState
          icon={Workflow}
          title="No steps recorded"
          description="This run has no steps in its definition. The trace canvas needs at least one step to draw a chain."
        />
      </div>
    )
  }

  return (
    <div className="h-full w-full overflow-hidden bg-background">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onNodeClick={onNodeClick}
        onNodeDragStop={onNodeDragStop}
        onPaneClick={onPaneClick}
        fitView
        fitViewOptions={FIT_VIEW}
        minZoom={0.1}
        maxZoom={2.5}
        proOptions={{ hideAttribution: true }}
        className="!bg-transparent"
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
      </ReactFlow>
    </div>
  )
}
