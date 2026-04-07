"use client"

import { memo, useState } from "react"
import {
  getBezierPath,
  EdgeLabelRenderer,
  type EdgeProps,
} from "@xyflow/react"

interface A2AMessageEdgeData {
  messageCount: number
  lastMessageType: string
  lastMessagePreview: string
  direction: "bidirectional" | "unidirectional"
  active: boolean
  latencyMs: number
  dimmed: boolean
  onEdgeClick?: (fromCrewId: string, toCrewId: string) => void
  [key: string]: unknown
}

const messageTypeColors: Record<string, string> = {
  "@assign": "#3b82f6",
  "@ask": "#a855f7",
  "@broadcast": "#06b6d4",
  "@result": "#22c55e",
}

function A2AMessageEdgeInner({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  source,
  target,
  data,
}: EdgeProps) {
  const [hovered, setHovered] = useState(false)
  const d = data as unknown as A2AMessageEdgeData | undefined

  const messageCount = d?.messageCount ?? 0
  const lastMessageType = d?.lastMessageType ?? "@assign"
  const lastMessagePreview = d?.lastMessagePreview ?? ""
  const direction = d?.direction ?? "unidirectional"
  const active = d?.active ?? false
  const dimmed = d?.dimmed ?? false

  const isBidirectional = direction === "bidirectional"
  const color = isBidirectional ? "#06b6d4" : "#f59e0b"
  const typeColor = messageTypeColors[lastMessageType] ?? "#64748b"

  const [edgePath, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })

  // Reversed path for bidirectional reverse-flow dots
  const [reversePath] = getBezierPath({
    sourceX: targetX,
    sourceY: targetY,
    sourcePosition: targetPosition,
    targetX: sourceX,
    targetY: sourceY,
    targetPosition: sourcePosition,
  })

  // Dimmed mode: simple faded line, no animation
  if (dimmed) {
    return (
      <path
        d={edgePath}
        fill="none"
        stroke="#334155"
        strokeWidth={1}
        strokeOpacity={0.1}
      />
    )
  }

  const glowId = `a2a-glow-${id}`
  const gradId = `a2a-grad-${id}`

  return (
    <>
      <defs>
        <filter id={glowId} x="-30%" y="-30%" width="160%" height="160%">
          <feGaussianBlur stdDeviation={active ? "4" : "2"} result="blur" />
          <feMerge>
            <feMergeNode in="blur" />
            <feMergeNode in="SourceGraphic" />
          </feMerge>
        </filter>
        <linearGradient
          id={gradId}
          gradientUnits="userSpaceOnUse"
          x1={sourceX}
          y1={sourceY}
          x2={targetX}
          y2={targetY}
        >
          <stop offset="0%" stopColor={color} stopOpacity={active ? 0.5 : 0.2} />
          <stop offset="50%" stopColor={color} stopOpacity={active ? 0.9 : 0.4} />
          <stop offset="100%" stopColor={color} stopOpacity={active ? 0.5 : 0.2} />
        </linearGradient>
      </defs>

      {/* Broad glow underneath for active edges */}
      {active && (
        <path
          d={edgePath}
          fill="none"
          stroke={color}
          strokeWidth={10}
          strokeOpacity={0.06}
          filter={`url(#${glowId})`}
        />
      )}

      {/* Main line */}
      <path
        d={edgePath}
        fill="none"
        stroke={`url(#${gradId})`}
        strokeWidth={3}
        strokeLinecap="round"
        filter={active ? `url(#${glowId})` : undefined}
      />

      {/* Invisible wide hit area for hover */}
      <path
        d={edgePath}
        fill="none"
        stroke="transparent"
        strokeWidth={20}
        onMouseEnter={() => setHovered(true)}
        onMouseLeave={() => setHovered(false)}
        onClick={() => d?.onEdgeClick?.(source, target)}
        style={{ cursor: d?.onEdgeClick ? "pointer" : "default" }}
      />

      {/* Forward-direction flowing dots */}
      {active && (
        <>
          <circle r="4.5" fill={color} opacity="0.6" filter={`url(#${glowId})`}>
            <animateMotion dur="2s" repeatCount="indefinite" path={edgePath} />
          </circle>
          <circle r="2" fill="white" opacity="0.9">
            <animateMotion dur="2s" repeatCount="indefinite" path={edgePath} />
          </circle>
          {/* Staggered second dot */}
          <circle r="3.5" fill={color} opacity="0.4" filter={`url(#${glowId})`}>
            <animateMotion dur="2s" repeatCount="indefinite" path={edgePath} begin="1s" />
          </circle>
          <circle r="1.5" fill="white" opacity="0.7">
            <animateMotion dur="2s" repeatCount="indefinite" path={edgePath} begin="1s" />
          </circle>
        </>
      )}

      {/* Reverse-direction flowing dots for bidirectional */}
      {active && isBidirectional && (
        <>
          <circle r="4.5" fill={color} opacity="0.5" filter={`url(#${glowId})`}>
            <animateMotion dur="2.4s" repeatCount="indefinite" path={reversePath} />
          </circle>
          <circle r="2" fill="white" opacity="0.8">
            <animateMotion dur="2.4s" repeatCount="indefinite" path={reversePath} />
          </circle>
          <circle r="3" fill={color} opacity="0.3">
            <animateMotion dur="2.4s" repeatCount="indefinite" path={reversePath} begin="1.2s" />
          </circle>
          <circle r="1.5" fill="white" opacity="0.6">
            <animateMotion dur="2.4s" repeatCount="indefinite" path={reversePath} begin="1.2s" />
          </circle>
        </>
      )}

      {/* Edge label: message count + type pill */}
      <EdgeLabelRenderer>
        <div
          className="nodrag nopan"
          style={{
            position: "absolute",
            transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`,
            pointerEvents: "all",
          }}
          onMouseEnter={() => setHovered(true)}
          onMouseLeave={() => setHovered(false)}
        >
          <div className="flex items-center gap-1.5">
            {/* Message count badge */}
            <div
              className="flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium tabular-nums"
              style={{
                backgroundColor: `${color}18`,
                border: `1px solid ${color}35`,
                color,
              }}
            >
              <span>{messageCount}</span>
              <span className="text-white/30">
                {messageCount === 1 ? "msg" : "msgs"}
              </span>
            </div>

            {/* Last message type pill */}
            <div
              className="px-1.5 py-0.5 rounded text-[9px] font-mono font-medium"
              style={{
                backgroundColor: `${typeColor}18`,
                border: `1px solid ${typeColor}30`,
                color: typeColor,
              }}
            >
              {lastMessageType}
            </div>
          </div>

          {/* Hover tooltip with last message preview */}
          {hovered && lastMessagePreview && (
            <div
              className="absolute left-1/2 top-full mt-2 -translate-x-1/2 z-50"
              style={{ pointerEvents: "none" }}
            >
              <div
                className="px-3 py-2 rounded-lg text-[11px] text-white/70 max-w-[220px] leading-relaxed whitespace-pre-wrap"
                style={{
                  backgroundColor: "rgba(10, 18, 32, 0.95)",
                  border: `1px solid ${color}30`,
                  boxShadow: `0 4px 20px rgba(0, 0, 0, 0.5), 0 0 10px ${color}10`,
                  backdropFilter: "blur(12px)",
                }}
              >
                <div
                  className="text-[9px] font-mono font-semibold uppercase tracking-wider mb-1"
                  style={{ color }}
                >
                  Last message
                </div>
                <div className="truncate">{lastMessagePreview}</div>
                {d?.latencyMs != null && (
                  <div className="text-[9px] text-white/30 font-mono mt-1">
                    avg {d.latencyMs}ms
                  </div>
                )}
              </div>
            </div>
          )}
        </div>
      </EdgeLabelRenderer>
    </>
  )
}

export const A2AMessageEdge = memo(A2AMessageEdgeInner)
