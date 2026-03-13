"use client"

import { type EdgeProps, getBezierPath } from "@xyflow/react"

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
  markerEnd,
  data,
}: EdgeProps) {
  const edgeData = data as AnimatedEdgeData | undefined
  const color = edgeData?.color || "#3b82f6"
  const active = edgeData?.active ?? false

  const [edgePath] = getBezierPath({
    sourceX, sourceY, sourcePosition,
    targetX, targetY, targetPosition,
  })

  const glowId = `glow-${id}`
  const gradId = `grad-${id}`

  return (
    <>
      <defs>
        <filter id={glowId} x="-30%" y="-30%" width="160%" height="160%">
          <feGaussianBlur stdDeviation={active ? "3.5" : "1.5"} result="blur" />
          <feMerge>
            <feMergeNode in="blur" />
            <feMergeNode in="SourceGraphic" />
          </feMerge>
        </filter>
        <linearGradient id={gradId} gradientUnits="userSpaceOnUse"
          x1={sourceX} y1={sourceY} x2={targetX} y2={targetY}>
          <stop offset="0%" stopColor={color} stopOpacity={active ? 0.6 : 0.2} />
          <stop offset="50%" stopColor={color} stopOpacity={active ? 1 : 0.4} />
          <stop offset="100%" stopColor={color} stopOpacity={active ? 0.6 : 0.2} />
        </linearGradient>
      </defs>

      {/* Broad glow underneath for active edges */}
      {active && (
        <path
          d={edgePath}
          fill="none"
          stroke={color}
          strokeWidth={8}
          strokeOpacity={0.08}
          filter={`url(#${glowId})`}
        />
      )}

      {/* Main dashed line — always visible, Bleu-style flowing dash animation */}
      <path
        d={edgePath}
        fill="none"
        stroke={`url(#${gradId})`}
        strokeWidth={active ? 2.5 : 1.8}
        strokeDasharray={active ? "8 6" : "6 4"}
        strokeLinecap="round"
        markerEnd={markerEnd as string}
        filter={active ? `url(#${glowId})` : undefined}
        style={{
          animation: active
            ? "edgeFlow 0.8s linear infinite"
            : "edgeFlowSlow 3s linear infinite",
        }}
      />

      {/* Moving highlight dot on active edges */}
      {active && (
        <>
          <circle r="4" fill={color} opacity="0.7" filter={`url(#${glowId})`}>
            <animateMotion dur="1.8s" repeatCount="indefinite" path={edgePath} />
          </circle>
          <circle r="2" fill="white" opacity="0.9">
            <animateMotion dur="1.8s" repeatCount="indefinite" path={edgePath} />
          </circle>
        </>
      )}

    </>
  )
}
