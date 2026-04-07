"use client"

import { useMemo } from "react"
import { cn } from "@/lib/utils"
import type { CrewSummary, CrewConnection } from "@/lib/types/orchestration"

const crewColorMap: Record<string, string> = {
  blue: "#3b82f6",
  emerald: "#10b981",
  violet: "#8b5cf6",
  amber: "#f59e0b",
  rose: "#f43f5e",
  cyan: "#06b6d4",
  lime: "#84cc16",
  fuchsia: "#d946ef",
}

function resolveColor(color: string | null): string {
  if (!color) return "#64748b"
  return crewColorMap[color] || color
}

export interface ConnectionMapProps {
  crews: CrewSummary[]
  connections: CrewConnection[]
  onConnectionClick?: (connection: CrewConnection) => void
}

const NODE_RADIUS = 10
const PADDING_X = 24
const LABEL_OFFSET = 18
const SVG_HEIGHT = 120

export function ConnectionMap({
  crews,
  connections,
  onConnectionClick,
}: ConnectionMapProps) {
  const layout = useMemo(() => {
    if (crews.length === 0) return { positions: new Map<string, number>(), width: 0 }

    const spacing = crews.length === 1 ? 0 : 1 / (crews.length - 1)
    const positions = new Map<string, number>()
    crews.forEach((crew, i) => {
      positions.set(crew.id, crews.length === 1 ? 0.5 : i * spacing)
    })
    return { positions, width: crews.length }
  }, [crews])

  if (crews.length === 0) {
    return (
      <div className="flex items-center justify-center h-[120px] text-xs text-white/20">
        No connections
      </div>
    )
  }

  if (connections.length === 0 && crews.length > 0) {
    return (
      <div className="space-y-2">
        <svg
          viewBox={`0 0 200 ${SVG_HEIGHT}`}
          className="w-full"
          style={{ height: SVG_HEIGHT }}
        >
          {crews.map((crew, i) => {
            const cx = PADDING_X + ((200 - PADDING_X * 2) * (crews.length === 1 ? 0.5 : i / Math.max(crews.length - 1, 1)))
            const cy = SVG_HEIGHT / 2 - 8
            const fill = resolveColor(crew.color)
            return (
              <g key={crew.id}>
                <circle cx={cx} cy={cy} r={NODE_RADIUS} fill={fill} opacity={0.8} />
                <text
                  x={cx}
                  y={cy + LABEL_OFFSET}
                  textAnchor="middle"
                  className="fill-white/40"
                  fontSize={8}
                >
                  {crew.name.length > 10 ? crew.name.slice(0, 9) + "\u2026" : crew.name}
                </text>
              </g>
            )
          })}
        </svg>
        <div className="text-center text-[10px] text-white/20">No connections</div>
      </div>
    )
  }

  const svgWidth = 200

  return (
    <svg
      viewBox={`0 0 ${svgWidth} ${SVG_HEIGHT}`}
      className="w-full"
      style={{ height: SVG_HEIGHT }}
    >
      <defs>
        <marker
          id="arrow-bi"
          viewBox="0 0 6 6"
          refX={3}
          refY={3}
          markerWidth={6}
          markerHeight={6}
          orient="auto-start-reverse"
        >
          <path d="M0,0 L6,3 L0,6 Z" fill="#06b6d4" />
        </marker>
        <marker
          id="arrow-uni"
          viewBox="0 0 6 6"
          refX={6}
          refY={3}
          markerWidth={6}
          markerHeight={6}
          orient="auto"
        >
          <path d="M0,0 L6,3 L0,6 Z" fill="#f59e0b" />
        </marker>
      </defs>

      {/* Connection lines */}
      {connections.map((conn) => {
        const fromT = layout.positions.get(conn.from_crew_id)
        const toT = layout.positions.get(conn.to_crew_id)
        if (fromT === undefined || toT === undefined) return null

        const usable = svgWidth - PADDING_X * 2
        const x1 = PADDING_X + usable * fromT
        const x2 = PADDING_X + usable * toT
        const cy = SVG_HEIGHT / 2 - 8
        const isBi = conn.direction === "bidirectional"
        const stroke = isBi ? "#06b6d4" : "#f59e0b"

        // Curve the line slightly above the nodes
        const mx = (x1 + x2) / 2
        const curveY = cy - 20

        return (
          <g
            key={conn.id}
            className={cn(
              "cursor-pointer",
              onConnectionClick && "hover:opacity-80",
            )}
            onClick={() => onConnectionClick?.(conn)}
          >
            <path
              d={`M${x1},${cy} Q${mx},${curveY} ${x2},${cy}`}
              fill="none"
              stroke={stroke}
              strokeWidth={1.5}
              strokeDasharray="4 2"
              opacity={0.6}
              markerEnd={isBi ? "url(#arrow-bi)" : "url(#arrow-uni)"}
              markerStart={isBi ? "url(#arrow-bi)" : undefined}
            />
            {/* Invisible wider hit area */}
            <path
              d={`M${x1},${cy} Q${mx},${curveY} ${x2},${cy}`}
              fill="none"
              stroke="transparent"
              strokeWidth={12}
            />
          </g>
        )
      })}

      {/* Crew nodes */}
      {crews.map((crew) => {
        const t = layout.positions.get(crew.id) ?? 0
        const usable = svgWidth - PADDING_X * 2
        const cx = PADDING_X + usable * t
        const cy = SVG_HEIGHT / 2 - 8
        const fill = resolveColor(crew.color)
        const label =
          crew.name.length > 10 ? crew.name.slice(0, 9) + "\u2026" : crew.name

        return (
          <g key={crew.id}>
            <circle
              cx={cx}
              cy={cy}
              r={NODE_RADIUS}
              fill={fill}
              opacity={0.8}
            />
            <circle
              cx={cx}
              cy={cy}
              r={NODE_RADIUS + 3}
              fill="none"
              stroke={fill}
              strokeWidth={0.5}
              opacity={0.3}
            />
            <text
              x={cx}
              y={cy + LABEL_OFFSET}
              textAnchor="middle"
              className="fill-white/40"
              fontSize={8}
            >
              {label}
            </text>
          </g>
        )
      })}
    </svg>
  )
}
