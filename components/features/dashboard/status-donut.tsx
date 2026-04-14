"use client"

import * as React from "react"
import { Pie, PieChart, Cell, Label } from "recharts"

import { ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig } from "@/components/ui/chart"

export interface StatusDonutDatum {
  key: string
  label: string
  count: number
  color: string
}

interface StatusDonutProps {
  data: StatusDonutDatum[]
}

/** Donut chart for mission status distribution. */
export function StatusDonut({ data }: StatusDonutProps) {
  const total = React.useMemo(() => data.reduce((a, d) => a + d.count, 0), [data])

  // Build Recharts chart config dynamically from the status list.
  const chartConfig = React.useMemo<ChartConfig>(() => {
    const cfg: ChartConfig = {}
    for (const d of data) {
      cfg[d.key] = { label: d.label, color: d.color }
    }
    return cfg
  }, [data])

  return (
    <div className="flex items-center gap-4">
      <ChartContainer config={chartConfig} className="h-[160px] w-[160px] aspect-square shrink-0">
        <PieChart>
          <ChartTooltip cursor={false} content={<ChartTooltipContent hideLabel />} />
          <Pie
            data={data}
            dataKey="count"
            nameKey="key"
            innerRadius={52}
            outerRadius={72}
            paddingAngle={2}
            strokeWidth={0}
          >
            {data.map((d) => (
              <Cell key={d.key} fill={d.color} />
            ))}
            <Label
              content={({ viewBox }) => {
                if (viewBox && "cx" in viewBox && "cy" in viewBox) {
                  return (
                    <text x={viewBox.cx} y={viewBox.cy} textAnchor="middle" dominantBaseline="middle">
                      <tspan x={viewBox.cx} y={viewBox.cy} className="fill-foreground text-[20px] font-semibold tabular-nums">{total}</tspan>
                      <tspan x={viewBox.cx} y={(viewBox.cy ?? 0) + 14} className="fill-muted-foreground text-[9px] uppercase tracking-wider">missions</tspan>
                    </text>
                  )
                }
                return null
              }}
            />
          </Pie>
        </PieChart>
      </ChartContainer>

      {/* Legend */}
      <div className="flex-1 flex flex-col gap-1 text-[11px]">
        {data.map((d) => (
          <div key={d.key} className="flex items-center justify-between gap-2">
            <span className="inline-flex items-center gap-1.5 text-foreground/80">
              <span className="w-2 h-2 rounded-sm" style={{ background: d.color }} />
              {d.label}
            </span>
            <span className="font-mono text-foreground/60 tabular-nums">{d.count}</span>
          </div>
        ))}
      </div>
    </div>
  )
}
