"use client"

// The ReactFlow surface for a walked chain.
//
// Kept apart from TopologyCard so the card can `next/dynamic` it: React Flow
// is ~200 KB and nothing on the overview should pay for it before a reader
// asks for a graph.
//
// It registers the SAME node components the trace canvas does, imported from
// one place. A second copy of those cards is how the run trace and the
// routine preview drifted apart before someone had to merge them back.

import * as React from "react"
import { Background, BackgroundVariant, Controls, ReactFlow, type Edge, type Node } from "@xyflow/react"
import "@xyflow/react/dist/style.css"

import { overviewNodeTypes } from "@/components/features/activity/overview-nodes"

export interface ChainCanvasProps {
  nodes: Node[]
  edges: Edge[]
  onOpenNode?: (kind: string, ref: string) => void
}

export function ChainCanvas({ nodes, edges, onOpenNode }: ChainCanvasProps) {
  // Node ids are "kind:ref" — the same load-bearing prefix convention the
  // overview graph uses, so a click dispatches without the node component
  // needing to know what a route is.
  const handleNodeClick = React.useCallback(
    (_: React.MouseEvent, node: Node) => {
      if (!onOpenNode) return
      const at = node.id.indexOf(":")
      if (at <= 0) return
      onOpenNode(node.id.slice(0, at), node.id.slice(at + 1))
    },
    [onOpenNode],
  )

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      nodeTypes={overviewNodeTypes}
      onNodeClick={handleNodeClick}
      fitView
      // A chain is a handful of nodes; fitting it whole is the reading
      // position, unlike a 15-step run trace where fitting means starting
      // in the middle of the thing.
      fitViewOptions={{ padding: 0.18, maxZoom: 1 }}
      minZoom={0.35}
      maxZoom={1.4}
      nodesDraggable={false}
      nodesConnectable={false}
      elementsSelectable={Boolean(onOpenNode)}
      proOptions={{ hideAttribution: true }}
    >
      <Background variant={BackgroundVariant.Dots} gap={18} size={1} className="opacity-40" />
      <Controls showInteractive={false} className="!bottom-2 !left-2" />
    </ReactFlow>
  )
}
