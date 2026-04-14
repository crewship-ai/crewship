"use client"

import * as React from "react"
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"

import { ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig } from "@/components/ui/chart"

export interface ThroughputBucket {
  ts: string // ISO 8601
  // Dynamic keys — one per crew id
  [crewId: string]: string | number
}

export interface ThroughputSeries {
  key: string
  label: string
  color: string
}

interface ThroughputChartProps {
  buckets: ThroughputBucket[]
  series: ThroughputSeries[]
  height?: number
}

/** Stacked hourly bar chart — issues closed per hour by crew. */
export function ThroughputChart({ buckets, series, height = 180 }: ThroughputChartProps) {
  const chartConfig = React.useMemo<ChartConfig>(() => {
    const cfg: ChartConfig = {}
    for (const s of series) {
      cfg[s.key] = { label: s.label, color: s.color }
    }
    return cfg
  }, [series])

  return (
    <ChartContainer config={chartConfig} className="w-full aspect-auto" style={{ height }}>
      <BarChart accessibilityLayer data={buckets} margin={{ top: 8, right: 8, left: -24, bottom: 0 }}>
        <CartesianGrid vertical={false} strokeDasharray="2 3" stroke="rgba(255,255,255,0.04)" />
        <XAxis
          dataKey="ts"
          tickLine={false}
          axisLine={false}
          tickMargin={6}
          minTickGap={32}
          tick={{ fontSize: 9, fill: "rgba(230,231,235,0.4)", fontFamily: "ui-monospace" }}
          tickFormatter={(v) => {
            const d = new Date(v)
            return `${String(d.getHours()).padStart(2, "0")}h`
          }}
        />
        <YAxis
          tickLine={false}
          axisLine={false}
          width={28}
          tick={{ fontSize: 9, fill: "rgba(230,231,235,0.35)", fontFamily: "ui-monospace" }}
          allowDecimals={false}
        />
        <ChartTooltip cursor={{ fill: "rgba(255,255,255,0.04)" }} content={<ChartTooltipContent indicator="dot" />} />
        {series.map((s, i) => (
          <Bar
            key={s.key}
            dataKey={s.key}
            stackId="a"
            fill={`var(--color-${s.key})`}
            radius={i === series.length - 1 ? [2, 2, 0, 0] : 0}
          />
        ))}
      </BarChart>
    </ChartContainer>
  )
}
