"use client"

import { type EdgeProps, BaseEdge, getBezierPath } from "@xyflow/react"

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
  const color = edgeData?.color || "#3b82f6"
  const active = edgeData?.active ?? false

  const [edgePath] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })

  const filterId = `glow-${id}`

  return (
    <>
      <defs>
        <filter id={filterId} x="-40%" y="-40%" width="180%" height="180%">
          <feGaussianBlur stdDeviation={active ? "4" : "2"} result="blur" />
          <feMerge>
            <feMergeNode in="blur" />
            <feMergeNode in="SourceGraphic" />
          </feMerge>
        </filter>
        {active && (
          <linearGradient id={`grad-${id}`} gradientUnits="userSpaceOnUse"
            x1={sourceX} y1={sourceY} x2={targetX} y2={targetY}>
            <stop offset="0%" stopColor={color} stopOpacity="0.4" />
            <stop offset="50%" stopColor={color} stopOpacity="1" />
            <stop offset="100%" stopColor={color} stopOpacity="0.4" />
          </linearGradient>
        )}
      </defs>

      {/* Glow layer underneath */}
      {active && (
        <path
          d={edgePath}
          fill="none"
          stroke={color}
          strokeWidth={6}
          strokeOpacity={0.15}
          filter={`url(#${filterId})`}
        />
      )}

      {/* Main edge line */}
      <BaseEdge
        id={id}
        path={edgePath}
        markerEnd={markerEnd}
        style={{
          ...style,
          stroke: active ? color : color,
          strokeWidth: active ? 2.5 : 1.5,
          strokeDasharray: active ? "none" : "6 4",
          strokeOpacity: active ? 1 : 0.4,
          filter: active ? `url(#${filterId})` : undefined,
        }}
      />

      {/* Animated particle */}
      {active && (
        <>
          <circle r="5" fill={color} filter={`url(#${filterId})`} opacity="0.8">
            <animateMotion dur="2s" repeatCount="indefinite" path={edgePath} />
          </circle>
          <circle r="2.5" fill="white" opacity="0.9">
            <animateMotion dur="2s" repeatCount="indefinite" path={edgePath} />
          </circle>
        </>
      )}
    </>
  )
}
