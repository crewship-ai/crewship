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
import { TraceRoutedEdge } from "./trace-routed-edge"
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
  traceRouted: TraceRoutedEdge,
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

// Opening view, when initialFocus="start". Tighter padding and a higher
// ceiling than FIT_VIEW because it is fitting two ranks, not a whole
// routine: the first thing a reader sees should be legible at a glance,
// not a correct-but-tiny overview they have to zoom into before they
// can read a single node.
const START_FIT = { padding: 0.12, minZoom: 0.62, maxZoom: 1.6 } as const

// Fallbacks for centring a node before React Flow has measured it —
// the first click can land before measurement, and half of an
// unmeasured node is better than centring on its top-left corner.
// Same numbers buildTraceGraph hands dagre.
const NODE_W_FALLBACK = 200
const NODE_H_FALLBACK = 70

/**
 * The trigger plus the first rank or two, for initialFocus="start".
 *
 * Two ranks, not three: each extra rank fitted is a step further zoomed
 * out, and the opening view is judged on whether one node is readable,
 * not on how much of the routine is on screen.
 *
 * Ranks are read off the dagre-assigned y, not the step order: a fan-out
 * puts several nodes on one rank, and showing one of three siblings
 * would misrepresent the shape at the very moment the reader is forming
 * their first impression of it.
 */
function headNodeIds(nodes: Node[], ranks = 2): string[] {
  if (nodes.length === 0) return []
  const ys = Array.from(new Set(nodes.map((n) => Math.round(n.position.y)))).sort((a, b) => a - b)
  const cutoff = ys[Math.min(ranks, ys.length) - 1]
  return nodes.filter((n) => Math.round(n.position.y) <= cutoff).map((n) => n.id)
}

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
  // ── Reading aids, all opt-in ────────────────────────────────────
  // Activity keeps its existing behaviour by default; the routine
  // detail turns these on, where the graph is a document you read from
  // the top rather than a run you are watching.
  //
  // "start" opens on the trigger and the first ranks instead of fitting
  // the whole routine — a 14-step graph fitted whole is a graph you
  // start reading in the middle.
  initialFocus?: "all" | "start"
  // Centre a clicked node instead of leaving it wherever it was. Makes
  // the click say "show me this and its neighbours".
  centerOnSelect?: boolean
  // Centre this node when the id changes, without a click. Driven by
  // the editor caret: edit a step's lines, that step comes into view.
  focusStepId?: string | null
  // Keep what is on screen centred when the PANE resizes — opening a
  // side panel next to the canvas shrinks it, and without this the
  // graph stays where it was and half of it ends up under the panel.
  // Shifts by half the width delta, preserving zoom and position in the
  // graph: the reader keeps their place, the picture just slides over.
  recenterOnResize?: boolean
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
  initialFocus = "all",
  centerOnSelect = false,
  focusStepId = null,
  recenterOnResize = false,
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

  // Read by the deferred initial fit. Keeping it in a ref rather than a
  // dependency means a status repaint — which rebuilds graphData every
  // realtime tick — does not tear down and re-arm the size observer.
  const graphDataRef = useRef(graphData)
  graphDataRef.current = graphData

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

  const { setCenter, getZoom, getViewport, setViewport } = useReactFlow()

  // Hold the view steady across a pane resize. ReactFlow keeps its
  // transform when the container changes size, so shrinking the pane
  // from the right leaves the graph sitting under whatever took the
  // space. Shifting by half the delta puts it back in the middle of
  // what is still visible.
  const paneRef = useRef<HTMLDivElement | null>(null)
  const pendingFitRef = useRef(true)

  const applyInitialFit = useCallback(() => {
    if (initialFocus === "start") {
      // fitView centres what it is given, so handing it the head of the
      // graph lands the reader at the beginning at a readable zoom
      // rather than in the middle of the routine at a tiny one.
      const head = headNodeIds(graphDataRef.current.nodes)
      if (head.length > 0) {
        fitView({ ...START_FIT, nodes: head.map((id) => ({ id })), duration: 300 })
        return
      }
    }
    fitView({ ...FIT_VIEW, duration: 300 })
  }, [initialFocus, fitView])

  // One observer owns both size concerns.
  //
  // The opening fit waits for the pane to have real dimensions instead
  // of guessing with a timeout — a fit computed before layout settles
  // is computed against a zero-width box.
  //
  // The recentre keeps what is on screen in the middle when the pane
  // shrinks, so opening a side panel slides the graph over rather than
  // hiding it underneath.
  useEffect(() => {
    const el = paneRef.current
    if (!el) return
    let last = el.clientWidth

    const onSize = () => {
      const width = el.clientWidth
      if (width > 0 && pendingFitRef.current) {
        pendingFitRef.current = false
        last = width
        applyInitialFit()
        return
      }
      const delta = width - last
      if (delta === 0) return
      last = width
      if (!recenterOnResize) return
      const vp = getViewport()
      setViewport({ ...vp, x: vp.x + delta / 2 })
    }

    if (typeof ResizeObserver === "undefined") {
      onSize()
      return
    }
    const ro = new ResizeObserver(onSize)
    ro.observe(el)
    onSize()
    return () => ro.disconnect()
  }, [applyInitialFit, recenterOnResize, getViewport, setViewport])

  // Centre a node without changing zoom. Used by both the click handler
  // and the caret follower, so they cannot drift apart.
  const centerNode = useCallback(
    (id: string, duration = 320) => {
      const node = graphData.nodes.find((n) => n.id === id)
      if (!node) return
      const w = (node.measured?.width ?? node.width ?? NODE_W_FALLBACK) / 2
      const h = (node.measured?.height ?? node.height ?? NODE_H_FALLBACK) / 2
      setCenter(node.position.x + w, node.position.y + h, {
        zoom: Math.max(getZoom(), FIT_VIEW.minZoom),
        duration,
      })
    },
    [graphData.nodes, setCenter, getZoom],
  )

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
      // Ask for a fit; the size observer performs it once the pane
      // actually has dimensions. A fixed delay was a guess, and a fit
      // computed against a pane that has not laid out yet lands at the
      // wrong zoom — which is why the graph sometimes opened tiny and
      // sometimes right.
      pendingFitRef.current = true
      return
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
  }, [graphData, run.id, setNodes, setEdges, fitView, initialFocus])

  const onNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      if (node.id === "__trigger__") {
        onStepSelect(null)
        return
      }
      onStepSelect(node.id)
      if (centerOnSelect) centerNode(node.id)
    },
    [onStepSelect, centerOnSelect, centerNode],
  )

  // Caret follower. Centres whenever the requested id CHANGES, not on
  // every render: the caret moves constantly while typing, and
  // re-centring on the node already centred would fight the user for
  // control of the viewport.
  const lastFocusRef = useRef<string | null>(null)
  useEffect(() => {
    if (!focusStepId || focusStepId === lastFocusRef.current) {
      lastFocusRef.current = focusStepId
      return
    }
    lastFocusRef.current = focusStepId
    centerNode(focusStepId)
  }, [focusStepId, centerNode])

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
    <div ref={paneRef} className="h-full w-full overflow-hidden bg-background">
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
