"use client"

import { type EdgeProps, BaseEdge, getSmoothStepPath } from "@xyflow/react"

interface AnimatedEdgeData {
  color?: string
  active?: boolean
  [key: string]: unknown
}

export function AnimatedEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  style = {},
  markerEnd,
  data,
}: EdgeProps) {
  const edgeData = data as AnimatedEdgeData | undefined
  const color = edgeData?.color || "hsl(var(--border))"
  const active = edgeData?.active ?? false

  const [edgePath] = getSmoothStepPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })

  return (
    <>
      <BaseEdge
        id={id}
        path={edgePath}
        markerEnd={markerEnd}
        style={{ ...style, stroke: color, opacity: active ? 1 : 0.6 }}
      />
      {active && (
        <>
          <circle r="4" fill={color} filter="url(#glow)">
            <animateMotion dur="1.5s" repeatCount="indefinite" path={edgePath} />
          </circle>
          <circle r="2.5" fill="white" opacity="0.8">
            <animateMotion dur="1.5s" repeatCount="indefinite" path={edgePath} />
          </circle>
          <defs>
            <filter id="glow" x="-50%" y="-50%" width="200%" height="200%">
              <feGaussianBlur stdDeviation="3" result="blur" />
              <feMerge>
                <feMergeNode in="blur" />
                <feMergeNode in="SourceGraphic" />
              </feMerge>
            </filter>
          </defs>
        </>
      )}
    </>
  )
}
