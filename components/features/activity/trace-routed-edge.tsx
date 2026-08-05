"use client"

import { memo } from "react"
import { BaseEdge, getSmoothStepPath, type EdgeProps } from "@xyflow/react"

import { roundedPolylinePath, type Pt } from "@/lib/trace/edge-path"

// TraceRoutedEdge — sequencing edge drawn along dagre's own route.
//
// The layout engine already worked out a path that steps around the
// nodes between an edge's endpoints; we used to throw it away and draw
// a bezier corner to corner. On a one-rank hop the two are
// indistinguishable. On a skip edge — the accounting routine has one
// spanning six ranks — the bezier sweeps diagonally across every node
// in between, which is what made the canvas look like tangled string.
//
// Falls back to a smooth step path when there is no route: an edge
// added after layout, or a graph built without dagre in a test.

/**
 * Replace the route's endpoints with the real handle coordinates.
 *
 * dagre's first and last points sit on its idea of the node border,
 * which is a rectangle approximation and lands a few pixels off the
 * handle. Left alone that shows as a visible gap between the line and
 * the node it claims to touch. The interior points are the valuable
 * part and are kept exactly as computed.
 */
function snapToHandles(points: Pt[], source: Pt, target: Pt): Pt[] {
  const interior = points.length > 2 ? points.slice(1, -1) : []
  return [source, ...interior, target]
}

function TraceRoutedEdgeBase(props: EdgeProps) {
  const {
    id,
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition,
    targetPosition,
    markerEnd,
    style,
    data,
  } = props

  const points = (data as { points?: Pt[] } | undefined)?.points

  let path: string
  if (points && points.length >= 2) {
    path = roundedPolylinePath(
      snapToHandles(points, { x: sourceX, y: sourceY }, { x: targetX, y: targetY }),
      10,
    )
  } else {
    ;[path] = getSmoothStepPath({
      sourceX,
      sourceY,
      sourcePosition,
      targetX,
      targetY,
      targetPosition,
      borderRadius: 10,
    })
  }

  return <BaseEdge id={id} path={path} markerEnd={markerEnd} style={style} />
}

export const TraceRoutedEdge = memo(TraceRoutedEdgeBase)
