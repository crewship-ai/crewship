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
import {
  Background,
  BackgroundVariant,
  Controls,
  ReactFlow,
  type Edge,
  type Node,
  type ReactFlowInstance,
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"

import { overviewNodeTypes } from "@/components/features/activity/overview-nodes"
import { CHAIN_FIT_PADDING } from "@/lib/trace/build-chain-graph"

export interface ChainCanvasProps {
  nodes: Node[]
  edges: Edge[]
  onOpenNode?: (kind: string, ref: string) => void
}

/**
 * A chain is a handful of nodes; fitting it whole is the reading position,
 * unlike a 15-step run trace where fitting means starting in the middle of
 * the thing. `maxZoom: 1` keeps a two-node chain from being blown up to
 * billboard size.
 */
const FIT_OPTIONS = { padding: CHAIN_FIT_PADDING, maxZoom: 1 } as const

/** The one thing this component needs back from the instance. */
type Fitter = Pick<ReactFlowInstance, "fitView">

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

  const frameRef = React.useRef<HTMLDivElement>(null)
  const flowRef = React.useRef<Fitter | null>(null)
  // Once a reader has panned or zoomed, the viewport is theirs. Refitting
  // under someone mid-inspection is worse than the clipping it fixes.
  const readerMoved = React.useRef(false)
  const lastSize = React.useRef<{ w: number; h: number } | null>(null)

  const handleInit = React.useCallback((instance: Fitter) => {
    flowRef.current = instance
  }, [])

  // React Flow passes the originating event for a move it did not make
  // itself, and `null` for its own — including for the fits below, which
  // must not be mistaken for the reader taking over.
  const handleMoveEnd = React.useCallback((event: MouseEvent | TouchEvent | null) => {
    if (event) readerMoved.current = true
  }, [])

  const refit = React.useCallback(() => {
    // fitView() is queued inside React Flow until the nodes have been
    // measured, so calling it straight away is safe even mid-mount.
    flowRef.current?.fitView(FIT_OPTIONS)
  }, [])

  // ── Keep the graph inside the frame ──────────────────────────────
  //
  // The `fitView` prop fits ONCE. React Flow reads it into `fitViewQueued`
  // only when the prop's value changes, and ours is a constant `true`, so
  // the transform the graph gets at mount is the transform it keeps.
  // Narrowing the frame afterwards — collapsing the rail, opening a detail
  // pane, resizing the window — walked the right-hand nodes straight out of
  // the box: measured on a live instance, the viewport stayed at
  // `translate(188.5px, 97px) scale(1)` while the frame went 1177px → 477px,
  // with three of four nodes outside it and no way to tell from the picture
  // that anything was missing.
  React.useEffect(() => {
    const frame = frameRef.current
    if (!frame || typeof ResizeObserver === "undefined") return
    const observer = new ResizeObserver((entries) => {
      const box = entries[0]?.contentRect
      const w = Math.round(box?.width ?? frame.clientWidth)
      const h = Math.round(box?.height ?? frame.clientHeight)
      const previous = lastSize.current
      lastSize.current = { w, h }
      // The first callback is the observation itself, not a resize — the
      // mount fit already covers that size, and refitting here would race
      // it before the nodes are measured.
      if (!previous) return
      if (previous.w === w && previous.h === h) return
      if (readerMoved.current) return
      refit()
    })
    observer.observe(frame)
    return () => observer.disconnect()
  }, [refit])

  // ── Keep the graph fitted when the chain itself changes ──────────
  //
  // Picking workflow B while A is open does not remount this component —
  // same element, new nodes — and the init-only fit means B inherited A's
  // transform. A new chain also hands the viewport back: the reader asked
  // for a different picture, so they want to see all of it.
  const graphKey = nodes.map((n) => n.id).join("|")
  const mounted = React.useRef(false)
  React.useEffect(() => {
    readerMoved.current = false
    if (!mounted.current) {
      // The `fitView` prop owns the first one.
      mounted.current = true
      return
    }
    refit()
  }, [graphKey, refit])

  return (
    <div ref={frameRef} className="h-full w-full">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={overviewNodeTypes}
        onNodeClick={handleNodeClick}
        onInit={handleInit}
        onMoveEnd={handleMoveEnd}
        fitView
        fitViewOptions={FIT_OPTIONS}
        minZoom={0.35}
        maxZoom={1.4}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={Boolean(onOpenNode)}
        proOptions={{ hideAttribution: true }}
      >
        <Background variant={BackgroundVariant.Dots} gap={18} size={1} className="opacity-40" />
        {/* `@xyflow/react/dist/style.css` paints these buttons #fefefe with
            the page's own white text on top: on this surface that is a blank
            white block in the lower left, ~26×78px, which reads as a render
            artifact rather than a control. Every other canvas in the repo
            overrides it; this one shipped with only a position. */}
        <Controls
          showInteractive={false}
          className="!bottom-2 !left-2 !bg-muted !border-border !rounded-lg !shadow-xl [&_button]:!bg-muted [&_button]:!border-border [&_button]:!text-muted-foreground [&_button:hover]:!bg-accent"
        />
      </ReactFlow>
    </div>
  )
}
