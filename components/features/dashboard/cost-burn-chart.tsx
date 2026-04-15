"use client"

import * as React from "react"
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from "recharts"

import { ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig } from "@/components/ui/chart"

export interface CostBucket {
  ts: string
  [modelKey: string]: string | number
}

export interface CostSeries {
  key: string
  label: string
  color: string
}

interface CostBurnChartProps {
  buckets: CostBucket[]
  series: CostSeries[]
  height?: number
}

export function CostBurnChart({ buckets, series, height = 180 }: CostBurnChartProps) {
  const chartConfig = React.useMemo<ChartConfig>(() => {
    const cfg: ChartConfig = {}
    for (const s of series) {
      cfg[s.key] = { label: s.label, color: s.color }
    }
    return cfg
  }, [series])

  return (
    <ChartContainer config={chartConfig} className="w-full aspect-auto" style={{ height }}>
      <AreaChart accessibilityLayer data={buckets} margin={{ top: 8, right: 8, left: -20, bottom: 0 }}>
        <defs>
          {series.map((s) => (
            <linearGradient key={s.key} id={`cost-fill-${s.key}`} x1="0" x2="0" y1="0" y2="1">
              <stop offset="0%" stopColor={`var(--color-${s.key})`} stopOpacity={0.55} />
              <stop offset="100%" stopColor={`var(--color-${s.key})`} stopOpacity={0.02} />
            </linearGradient>
          ))}
        </defs>
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
            return d.toLocaleDateString("en-US", { weekday: "short" })
          }}
        />
        <YAxis
          tickLine={false}
          axisLine={false}
          width={28}
          tick={{ fontSize: 9, fill: "rgba(230,231,235,0.35)", fontFamily: "ui-monospace" }}
          tickFormatter={(v) => `$${Number(v).toFixed(2)}`}
        />
        <ChartTooltip
          cursor={{ stroke: "rgba(255,255,255,0.1)", strokeWidth: 1 }}
          content={
            <ChartTooltipContent
              indicator="dot"
              formatter={(value, name) => {
                const num = Number(value)
                return (
                  <>
                    <span className="text-muted-foreground">{name}</span>
                    <span className="text-foreground font-mono font-medium tabular-nums ml-auto">
                      ${num.toFixed(2)}
                    </span>
                  </>
                )
              }}
              labelFormatter={(v) => {
                const d = new Date(v as string)
                return d.toLocaleDateString("en-US", { weekday: "long", month: "short", day: "numeric" })
              }}
            />
          }
        />
        {series.map((s) => (
          <Area
            key={s.key}
            type="monotone"
            dataKey={s.key}
            stackId="1"
            stroke={`var(--color-${s.key})`}
            strokeWidth={1.5}
            fill={`url(#cost-fill-${s.key})`}
          />
        ))}
      </AreaChart>
    </ChartContainer>
  )
}
