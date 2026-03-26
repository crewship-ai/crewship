"use client"

import { memo } from "react"
import {
  getStraightPath,
  type EdgeProps,
  EdgeLabelRenderer,
  MarkerType,
} from "@xyflow/react"

interface PermissionEdgeData {
  direction: "bidirectional" | "unidirectional"
  status: string
  dimmed?: boolean
}

function PermissionEdgeInner({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  data,
  markerEnd,
  markerStart,
}: EdgeProps) {
  const d = data as unknown as PermissionEdgeData
  const dimmed = d?.dimmed ?? false
  const isBidirectional = d?.direction === "bidirectional"

  const [edgePath, labelX, labelY] = getStraightPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
  })

  const color = isBidirectional ? "#06b6d4" : "#f59e0b"
  const label = isBidirectional ? "↔" : "→"

  if (dimmed) {
    return (
      <path
        id={id}
        d={edgePath}
        fill="none"
        stroke="#334155"
        strokeWidth={1}
        strokeOpacity={0.1}
      />
    )
  }

  return (
    <>
      {/* Glow layer */}
      <path
        d={edgePath}
        fill="none"
        stroke={color}
        strokeWidth={8}
        strokeOpacity={0.06}
      />

      {/* Main line */}
      <path
        id={id}
        d={edgePath}
        fill="none"
        stroke={color}
        strokeWidth={2.5}
        strokeOpacity={0.6}
        strokeDasharray="8 6"
        markerEnd={markerEnd}
        markerStart={markerStart}
      >
        <animate
          attributeName="stroke-dashoffset"
          from="28"
          to="0"
          dur="2s"
          repeatCount="indefinite"
        />
      </path>

      {/* Label */}
      <EdgeLabelRenderer>
        <div
          className="nodrag nopan"
          style={{
            position: "absolute",
            transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`,
            pointerEvents: "all",
          }}
        >
          <div
            className="flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium"
            style={{
              backgroundColor: `${color}15`,
              border: `1px solid ${color}30`,
              color: `${color}`,
            }}
          >
            <span>{label}</span>
            <span className="text-white/30">
              {isBidirectional ? "both" : "one-way"}
            </span>
          </div>
        </div>
      </EdgeLabelRenderer>
    </>
  )
}

// Marker definitions for permission edges
export function getPermissionMarkers(direction: "bidirectional" | "unidirectional") {
  const color = direction === "bidirectional" ? "#06b6d4" : "#f59e0b"
  const end = {
    type: MarkerType.ArrowClosed,
    color,
    width: 16,
    height: 16,
  }

  if (direction === "bidirectional") {
    return { markerStart: end, markerEnd: end }
  }
  return { markerEnd: end }
}

export const PermissionEdge = memo(PermissionEdgeInner)
