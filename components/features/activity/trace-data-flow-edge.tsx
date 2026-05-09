"use client"

import { memo, useState } from "react"
import {
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
  type EdgeProps,
} from "@xyflow/react"
import { cn } from "@/lib/utils"
import type { TraceDataFlowEdgeData } from "@/lib/trace/types"

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

  const [edgePath, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })

  const active = d?.active ?? false
  const stroke = "rgb(96, 165, 250)" // blue-400
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
                "rounded border border-blue-500/30 bg-card px-1.5 py-0.5 font-mono text-[10px] text-blue-300 shadow-sm transition-colors",
                hovered && "bg-blue-500/15",
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
