"use client"

import * as React from "react"
import { motion, useReducedMotion } from "motion/react"

/**
 * A tiny line for a card corner. Draws itself on mount (stroke-dashoffset)
 * and stays still afterwards; under reduced motion it simply appears.
 *
 * Values are scaled to the box; a flat or empty series draws a baseline so
 * the card's geometry never jumps between "no data" and "data".
 */
export function Sparkline({
  values,
  color,
  width = 96,
  height = 30,
  className,
  strokeWidth = 2,
}: {
  values: number[]
  color: string
  width?: number
  height?: number
  className?: string
  strokeWidth?: number
}) {
  const reduce = useReducedMotion()
  const points = React.useMemo(() => sparklinePoints(values, width, height), [values, width, height])
  const d = points.map((p, i) => `${i === 0 ? "M" : "L"}${p[0].toFixed(1)},${p[1].toFixed(1)}`).join(" ")
  return (
    <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} className={className} aria-hidden="true">
      <motion.path
        d={d}
        fill="none"
        stroke={color}
        strokeWidth={strokeWidth}
        strokeLinejoin="round"
        strokeLinecap="round"
        initial={reduce ? false : { pathLength: 0, opacity: 0.4 }}
        animate={{ pathLength: 1, opacity: 1 }}
        transition={{ duration: 0.9, ease: [0.16, 1, 0.3, 1] }}
      />
    </svg>
  )
}

/** Pure: the polyline points for a series in a box, with 2px of padding so
 *  the stroke never clips at the extremes. Exported for the test. */
export function sparklinePoints(values: number[], width: number, height: number): Array<[number, number]> {
  const pad = 2
  if (values.length === 0) return [[pad, height - pad], [width - pad, height - pad]]
  if (values.length === 1) return [[pad, height / 2], [width - pad, height / 2]]
  const max = Math.max(...values)
  const min = Math.min(...values)
  const span = max - min
  const stepX = (width - pad * 2) / (values.length - 1)
  return values.map((v, i) => {
    const y = span === 0 ? height / 2 : height - pad - ((v - min) / span) * (height - pad * 2)
    return [pad + i * stepX, y]
  })
}
