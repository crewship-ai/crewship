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
  type Node,
  type NodeTypes,
  type EdgeTypes,
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import { Workflow } from "lucide-react"
import { EmptyState } from "@/components/layout/empty-state"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import type { PipelineDSL } from "@/lib/trace/types"
import { buildTraceGraph } from "@/lib/trace/build-trace-graph"
import { TraceStepNode, TraceTriggerNode } from "./trace-step-node"
import { TraceDataFlowEdge } from "./trace-data-flow-edge"
import type { HeatmapMode } from "@/lib/trace/percentile-heatmap"
import type { StepMetric } from "@/hooks/use-step-metrics"

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
}

const edgeTypes: EdgeTypes = {
  traceDataFlow: TraceDataFlowEdge,
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
  stepMetrics: ReadonlyMap<string, StepMetric>
  heatmapMode: HeatmapMode
}

export function TraceCanvas(props: TraceCanvasProps) {
  if (!props.run) {
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
    <ReactFlowProvider>
      <CanvasInner {...props} run={props.run} />
    </ReactFlowProvider>
  )
}

function CanvasInner({
  run,
  dsl,
  selectedStepId,
  onStepSelect,
  workspaceId,
  waitpointTokensByStepId,
  stepMetrics,
  heatmapMode,
}: TraceCanvasProps & { run: PipelineRun }) {
  const graphData = useMemo(
    () =>
      buildTraceGraph(run, dsl, {
        selectedStepId,
        workspaceId,
        waitpointTokensByStepId,
        stepMetrics,
        heatmapMode,
      }),
    [run, dsl, selectedStepId, workspaceId, waitpointTokensByStepId, stepMetrics, heatmapMode],
  )

  const [nodes, setNodes, onNodesChange] = useNodesState(graphData.nodes)
  const [edges, setEdges, onEdgesChange] = useEdgesState(graphData.edges)
  const { fitView } = useReactFlow()

  // Track user-dragged node positions so realtime status updates
  // don't snap a node back to its dagre-computed home. Same pattern
  // as the orchestration WorkflowGraph.
  const userPositions = useRef(new Map<string, { x: number; y: number }>())

  const onNodeDragStop = useCallback((_: React.MouseEvent, node: Node) => {
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
      const t = setTimeout(() => fitView({ duration: 400, padding: 0.25 }), 50)
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
