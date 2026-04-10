"use client"

import { memo, useId } from "react"
import { type EdgeProps, getBezierPath } from "@xyflow/react"
import { STATUS_COLORS, GRAPH_CHROME } from "@/lib/colors"

interface AnimatedEdgeData {
  color?: string
  active?: boolean
  dimmed?: boolean
  [key: string]: unknown
}

/**
 * Animated edge with CSS-only animations (no SVG animateMotion).
 * Uses CSS offset-path for the traveling dot so animations survive React re-renders.
 * Wrapped in memo() to prevent unnecessary re-renders from polling.
 */
function AnimatedEdgeInner({
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
  const color = edgeData?.color || STATUS_COLORS.IN_PROGRESS
  const active = edgeData?.active ?? false
  const dimmed = edgeData?.dimmed ?? false
  const uid = useId()

  const [edgePath] = getBezierPath({
    sourceX, sourceY, sourcePosition,
    targetX, targetY, targetPosition,
  })

  if (dimmed) {
    return (
      <path
        d={edgePath}
        fill="none"
        stroke={GRAPH_CHROME.dimmedEdge}
        strokeWidth={1}
        strokeOpacity={0.1}
      />
    )
  }

  const glowId = `glow-${uid}`
  const gradId = `grad-${uid}`

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

      {/* Main dashed line */}
      <path
        className={active ? "edge-dash-active" : "edge-dash-idle"}
        d={edgePath}
        fill="none"
        stroke={`url(#${gradId})`}
        strokeWidth={active ? 2.5 : 1.8}
        strokeDasharray={active ? "8 6" : "6 4"}
        strokeLinecap="round"
        markerEnd={markerEnd as string}
        filter={active ? `url(#${glowId})` : undefined}
      />

      {/* Moving dot on active edges — CSS offset-path */}
      {active && (
        <>
          <circle
            className="edge-dot-outer"
            r="4"
            fill={color}
            opacity="0.7"
            filter={`url(#${glowId})`}
            style={{ offsetPath: `path('${edgePath}')` }}
          />
          <circle
            className="edge-dot-inner"
            r="2"
            fill="white"
            opacity="0.9"
            style={{ offsetPath: `path('${edgePath}')` }}
          />
        </>
      )}

      {/* Global CSS — injected once via style tag in defs (deduped by browser) */}
      <defs>
        <style>{`
          .edge-dash-active {
            animation: edgeFlow 0.8s linear infinite;
          }
          .edge-dash-idle {
            animation: edgeFlowSlow 3s linear infinite;
          }
          @keyframes edgeFlow {
            to { stroke-dashoffset: -14; }
          }
          @keyframes edgeFlowSlow {
            to { stroke-dashoffset: -10; }
          }
          .edge-dot-outer, .edge-dot-inner {
            offset-distance: 0%;
            animation: dotTravel 2.5s ease-in-out infinite;
          }
          .edge-dot-inner {
            animation-delay: 0s;
          }
          @keyframes dotTravel {
            0% { offset-distance: 0%; opacity: 0; }
            5% { opacity: 1; }
            95% { opacity: 1; }
            100% { offset-distance: 100%; opacity: 0; }
          }
        `}</style>
      </defs>
    </>
  )
}

export const AnimatedEdge = memo(AnimatedEdgeInner, (prev, next) => {
  // Only re-render if data or geometry actually changed
  const prevData = prev.data as AnimatedEdgeData | undefined
  const nextData = next.data as AnimatedEdgeData | undefined
  return (
    prev.sourceX === next.sourceX &&
    prev.sourceY === next.sourceY &&
    prev.targetX === next.targetX &&
    prev.targetY === next.targetY &&
    prevData?.color === nextData?.color &&
    prevData?.active === nextData?.active &&
    prevData?.dimmed === nextData?.dimmed
  )
})
