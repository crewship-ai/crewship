"use client"

import { memo, useState } from "react"
import {
  BaseEdge,
  EdgeLabelRenderer,
  getSmoothStepPath,
  type EdgeProps,
} from "@xyflow/react"
import { cn } from "@/lib/utils"
import { BRAND } from "@/lib/colors"
import type { TraceDataFlowEdgeData } from "@/lib/trace/types"
import { midpointOf, roundedPolylinePath, type Pt } from "@/lib/trace/edge-path"

// TraceDataFlowEdge — labeled bezier edge for "data flowed from
// step A to step B" relationships. Visually distinct from the gray
// sequencing edges:
//   - blue stroke (data carrier)
//   - thicker (2.5px vs 1.5px)
//   - animated when the source step is in a non-terminal state
//   - label chip showing the JSON path the consumer reads
//   - hover popover preview the resolved value
//
// Source: n8n's "items flow on edges" pattern. Edge data shape lives
// in lib/trace/types so the lib-level builder doesn't import back
// into components/.

export type { TraceDataFlowEdgeData }

function TraceDataFlowEdgeBase(props: EdgeProps) {
  const {
    id,
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition,
    targetPosition,
    markerEnd,
    data,
    style,
  } = props
  const d = data as unknown as TraceDataFlowEdgeData | undefined
  const [hovered, setHovered] = useState(false)

  // Same routing as the sequencing edges — a data-flow edge crosses
  // the same nodes and would tangle the same way. The label rides the
  // route's own midpoint rather than the straight-line midpoint of the
  // endpoints, which on a routed edge is a different place and can sit
  // on top of an unrelated node.
  const points = (data as { points?: Pt[] } | undefined)?.points
  let edgePath: string
  let labelX: number
  let labelY: number
  if (points && points.length >= 2) {
    const route: Pt[] = [
      { x: sourceX, y: sourceY },
      ...(points.length > 2 ? points.slice(1, -1) : []),
      { x: targetX, y: targetY },
    ]
    edgePath = roundedPolylinePath(route, 10)
    const mid = midpointOf(route) ?? { x: (sourceX + targetX) / 2, y: (sourceY + targetY) / 2 }
    labelX = mid.x
    labelY = mid.y
  } else {
    ;[edgePath, labelX, labelY] = getSmoothStepPath({
      sourceX,
      sourceY,
      sourcePosition,
      targetX,
      targetY,
      targetPosition,
      borderRadius: 10,
    })
  }

  const active = d?.active ?? false
  const stroke = BRAND.primary
  const strokeWidth = 2.5

  return (
    <>
      <BaseEdge
        id={id}
        path={edgePath}
        markerEnd={markerEnd}
        style={{
          stroke,
          strokeWidth,
          strokeDasharray: active ? "6 4" : undefined,
          ...style,
        }}
        className={cn(active && "animate-pulse")}
      />
      {/* Invisible thick path overlay so hover lands anywhere on the
        * edge, not just on the floating label chip. The visible path
        * is 2.5px; this hit area is 16px to match cursor expectations
        * on a busy canvas. pointer-events:stroke means only the
        * stroke (not the bounding box) catches the mouse. */}
      <path
        d={edgePath}
        fill="none"
        stroke="transparent"
        strokeWidth={16}
        style={{ pointerEvents: "stroke", cursor: "default" }}
        onMouseEnter={() => setHovered(true)}
        onMouseLeave={() => setHovered(false)}
      />
      <EdgeLabelRenderer>
        <div
          className="pointer-events-auto absolute"
          style={{
            transform: `translate(-50%, -50%) translate(${labelX}px,${labelY}px)`,
          }}
          onMouseEnter={() => setHovered(true)}
          onMouseLeave={() => setHovered(false)}
        >
          {d?.label && (
            <div
              className={cn(
                "rounded border border-primary/30 bg-card px-1.5 py-0.5 font-mono text-[10px] text-primary shadow-sm transition-colors",
                hovered && "bg-primary/15",
              )}
            >
              {d.label}
            </div>
          )}
          {hovered && d?.preview && (
            <div className="absolute left-1/2 top-full z-50 mt-1 -translate-x-1/2 whitespace-pre rounded border border-white/[0.08] bg-card px-2 py-1 font-mono text-[10px] text-foreground/80 shadow-xl">
              {d.preview}
            </div>
          )}
        </div>
      </EdgeLabelRenderer>
    </>
  )
}

export const TraceDataFlowEdge = memo(TraceDataFlowEdgeBase)
