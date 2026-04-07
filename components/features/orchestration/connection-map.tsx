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
const SVG_WIDTH = 200
const SVG_HEIGHT = 140
const LABEL_PUSH = 18

interface NodePos {
  cx: number
  cy: number
  angle: number
}

function computePositions(crews: CrewSummary[]): Map<string, NodePos> {
  const positions = new Map<string, NodePos>()
  const centerX = SVG_WIDTH / 2
  const centerY = SVG_HEIGHT / 2
  const count = crews.length

  if (count === 1) {
    positions.set(crews[0].id, { cx: centerX, cy: centerY, angle: -Math.PI / 2 })
    return positions
  }

  if (count === 2) {
    positions.set(crews[0].id, { cx: centerX - 50, cy: centerY, angle: Math.PI })
    positions.set(crews[1].id, { cx: centerX + 50, cy: centerY, angle: 0 })
    return positions
  }

  const radius = Math.min(SVG_WIDTH, SVG_HEIGHT) * 0.28
  crews.forEach((crew, i) => {
    const angle = i * (2 * Math.PI / count) - Math.PI / 2
    positions.set(crew.id, {
      cx: centerX + radius * Math.cos(angle),
      cy: centerY + radius * Math.sin(angle),
      angle,
    })
  })
  return positions
}

function textAnchorForAngle(angle: number): "start" | "middle" | "end" {
  const deg = ((angle * 180) / Math.PI + 360) % 360
  if (deg > 110 && deg < 250) return "end"
  if (deg < 70 || deg > 290) return "start"
  return "middle"
}

function truncate(name: string, max = 12): string {
  return name.length > max ? name.slice(0, max - 1) + "\u2026" : name
}

export function ConnectionMap({ crews, connections, onConnectionClick }: ConnectionMapProps) {
  const positions = useMemo(() => computePositions(crews), [crews])

  if (crews.length === 0) {
    return (
      <div className="flex items-center justify-center h-[140px] text-xs text-white/20">
        No connections
      </div>
    )
  }

  const renderNodes = (crewList: CrewSummary[]) =>
    crewList.map((crew) => {
      const pos = positions.get(crew.id)
      if (!pos) return null
      const fill = resolveColor(crew.color)
      const lx = pos.cx + LABEL_PUSH * Math.cos(pos.angle)
      const ly = pos.cy + LABEL_PUSH * Math.sin(pos.angle)
      const anchor = crews.length <= 2 ? "middle" : textAnchorForAngle(pos.angle)
      const labelY = crews.length <= 2 ? pos.cy + LABEL_PUSH : ly + 3

      return (
        <g key={crew.id}>
          <circle cx={pos.cx} cy={pos.cy} r={NODE_RADIUS + 3} fill="none" stroke={fill} strokeWidth={0.5} opacity={0.3} />
          <circle cx={pos.cx} cy={pos.cy} r={NODE_RADIUS} fill={fill} opacity={0.8} />
          <text
            x={crews.length <= 2 ? pos.cx : lx}
            y={labelY}
            textAnchor={anchor}
            className="fill-white/40"
            fontSize={8}
          >
            {truncate(crew.name)}
          </text>
        </g>
      )
    })

  if (connections.length === 0) {
    return (
      <div className="space-y-2">
        <svg viewBox={`0 0 ${SVG_WIDTH} ${SVG_HEIGHT}`} className="w-full" style={{ height: SVG_HEIGHT }}>
          {renderNodes(crews)}
        </svg>
        <div className="text-center text-[10px] text-white/20">No connections</div>
      </div>
    )
  }

  return (
    <svg viewBox={`0 0 ${SVG_WIDTH} ${SVG_HEIGHT}`} className="w-full" style={{ height: SVG_HEIGHT }}>
      <defs>
        <marker id="arrow-bi" viewBox="0 0 6 6" refX={3} refY={3} markerWidth={6} markerHeight={6} orient="auto-start-reverse">
          <path d="M0,0 L6,3 L0,6 Z" fill="#06b6d4" />
        </marker>
        <marker id="arrow-uni" viewBox="0 0 6 6" refX={6} refY={3} markerWidth={6} markerHeight={6} orient="auto">
          <path d="M0,0 L6,3 L0,6 Z" fill="#f59e0b" />
        </marker>
      </defs>

      {connections.map((conn) => {
        const from = positions.get(conn.from_crew_id)
        const to = positions.get(conn.to_crew_id)
        if (!from || !to) return null

        const isBi = conn.direction === "bidirectional"
        const stroke = isBi ? "#06b6d4" : "#f59e0b"
        const mx = (from.cx + to.cx) / 2
        const my = (from.cy + to.cy) / 2
        const cx = SVG_WIDTH / 2
        const cy = SVG_HEIGHT / 2
        const pullX = mx + (cx - mx) * 0.4
        const pullY = my + (cy - my) * 0.4

        return (
          <g
            key={conn.id}
            className={cn("cursor-pointer", onConnectionClick && "hover:opacity-80")}
            onClick={() => onConnectionClick?.(conn)}
          >
            <path
              d={`M${from.cx},${from.cy} Q${pullX},${pullY} ${to.cx},${to.cy}`}
              fill="none"
              stroke={stroke}
              strokeWidth={1.5}
              strokeDasharray="4 2"
              opacity={0.6}
              markerEnd={isBi ? "url(#arrow-bi)" : "url(#arrow-uni)"}
              markerStart={isBi ? "url(#arrow-bi)" : undefined}
            />
            <path
              d={`M${from.cx},${from.cy} Q${pullX},${pullY} ${to.cx},${to.cy}`}
              fill="none"
              stroke="transparent"
              strokeWidth={12}
            />
          </g>
        )
      })}

      {renderNodes(crews)}
    </svg>
  )
}
