"use client"

import * as React from "react"
import { RadialBar, RadialBarChart, PolarAngleAxis } from "recharts"

import { ChartContainer, type ChartConfig } from "@/components/ui/chart"

export interface CrewHealthEntry {
  id: string
  name: string
  slug: string
  color: string
  runningCount: number
  totalAgents: number
  healthPct: number // 0..100
}

interface CrewRadialProps {
  crews: CrewHealthEntry[]
}

const emptyConfig: ChartConfig = {
  value: { label: "Health" },
}

/** Mini-card grid of radial progress rings — one per crew. */
export function CrewRadial({ crews }: CrewRadialProps) {
  if (crews.length === 0) {
    return (
      <div className="flex items-center justify-center h-[200px] text-[11px] text-muted-foreground/50">
        No crews
      </div>
    )
  }

  return (
    <div className="grid grid-cols-2 gap-2">
      {crews.map((c) => (
        <div key={c.id} className="flex flex-col items-center rounded-lg border border-border/60 bg-card p-3 text-center">
          <div className="text-[11px] font-medium text-foreground/90 mb-1 truncate w-full">{c.name}</div>
          <div className="relative">
            <ChartContainer config={emptyConfig} className="h-[80px] w-[80px] aspect-square">
              <RadialBarChart
                data={[{ name: c.id, value: c.healthPct, fill: c.color }]}
                innerRadius={28}
                outerRadius={42}
                startAngle={90}
                endAngle={90 - (360 * c.healthPct) / 100}
              >
                <PolarAngleAxis type="number" domain={[0, 100]} angleAxisId={0} tick={false} />
                <RadialBar background dataKey="value" cornerRadius={6} angleAxisId={0} />
              </RadialBarChart>
            </ChartContainer>
            <div
              className="absolute inset-0 flex items-center justify-center text-[13px] font-semibold tabular-nums"
              style={{ color: c.color }}
            >
              {c.healthPct}%
            </div>
          </div>
          <div className="text-[9px] font-mono text-muted-foreground/60 uppercase tracking-wider mt-1">
            {c.runningCount}/{c.totalAgents} {c.runningCount === 0 ? "idle" : "running"}
          </div>
        </div>
      ))}
    </div>
  )
}
