"use client"

import { useMemo } from "react"
import { ReactFlow, Background, Controls, type Node, type Edge, MarkerType, ReactFlowProvider } from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import type { Pipeline } from "@/hooks/use-pipelines"
import { PipelineRunNode } from "@/components/features/orchestration/pipeline-run-node"

// RoutinesGraphView — composition graph. Shows routines as nodes;
// edges represent call_pipeline references (one routine that invokes
// another). Position: simple grid layout for MVP, dagre layout is a
// follow-up if the count grows past 30.
//
// Live runs render in /orchestration → Graph (existing). This view
// is the static composition map: "which routines call which", useful
// for understanding the workspace's automation graph at a glance.

interface Props {
  workspaceId: string
  routines: Pipeline[]
  onSelect: (slug: string) => void
}

const nodeTypes = {
  pipelineRun: PipelineRunNode,
}

export function RoutinesGraphView({ workspaceId: _workspaceId, routines, onSelect }: Props) {
  const { nodes, edges } = useMemo(() => buildGraph(routines, onSelect), [routines, onSelect])

  if (routines.length === 0) {
    return (
      <div className="flex h-full items-center justify-center p-12 text-center">
        <div>
          <p className="text-sm font-medium text-muted-foreground">No routines to graph</p>
          <p className="mt-1 max-w-md text-xs text-muted-foreground/70">
            Composition graph will draw nodes for each routine and edges for call_pipeline
            references between them.
          </p>
        </div>
      </div>
    )
  }

  return (
    <ReactFlowProvider>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        proOptions={{ hideAttribution: true }}
        nodesDraggable
        nodesConnectable={false}
        elementsSelectable
        edgesFocusable={false}
      >
        <Background gap={24} size={1} />
        <Controls position="bottom-right" showInteractive={false} />
      </ReactFlow>
    </ReactFlowProvider>
  )
}

// buildGraph maps routines to React Flow nodes positioned in a grid,
// and scans each routine's definition_json (when available) for
// call_pipeline references — those become edges. We only have the
// list-view shape of Pipeline here, which omits the definition; for
// MVP we render the nodes alone and rely on the detail panel to show
// composition. A follow-up will fetch definitions in batch and
// produce real edges.
function buildGraph(
  routines: Pipeline[],
  onSelect: (slug: string) => void,
): { nodes: Node[]; edges: Edge[] } {
  const COLS = 3
  const X_STEP = 260
  const Y_STEP = 110

  const nodes: Node[] = routines.map((r, i) => {
    const col = i % COLS
    const row = Math.floor(i / COLS)
    const status: "running" | "completed" | "failed" | "queued" =
      r.last_invocation_status?.toLowerCase() === "completed"
        ? "completed"
        : r.last_invocation_status?.toLowerCase() === "failed"
          ? "failed"
          : "queued"
    return {
      id: r.id,
      type: "pipelineRun",
      position: { x: col * X_STEP, y: row * Y_STEP },
      data: {
        pipelineSlug: r.slug,
        pipelineName: r.name,
        runId: r.id, // no run yet at composition view; reuse id for click target
        status,
        stepCount: undefined,
        stepIndex: undefined,
        tierLabel: undefined,
        costUsd: undefined,
        authorCrewLabel: r.author_crew_id ? `crew ${r.author_crew_id.slice(0, 8)}` : undefined,
        onClick: () => onSelect(r.slug),
      },
    }
  })

  // No edges in MVP — list-view Pipeline doesn't carry definition.
  // Composition edges land when the page-level fetch is upgraded to
  // include definitions (follow-up commit on this PR).
  const edges: Edge[] = []
  void MarkerType // keeps import live for follow-up that adds arrows

  return { nodes, edges }
}
