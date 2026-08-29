"use client"

import * as React from "react"
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"
import { useReducedMotion } from "motion/react"

import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@/components/ui/chart"

export interface RunVolumeBucket {
  ts: string
  [crewId: string]: string | number
}

export interface RunVolumeSeries {
  key: string
  label: string
  color: string
}

export function RunVolumeChart({
  buckets,
  series,
  window,
}: {
  buckets: RunVolumeBucket[]
  series: RunVolumeSeries[]
  window: "24h" | "7d" | "30d"
}) {
  const reduce = useReducedMotion()
  const config = React.useMemo<ChartConfig>(() => {
    const out: ChartConfig = {}
    for (const item of series) out[item.key] = { label: item.label, color: item.color }
    return out
  }, [series])

  if (buckets.length === 0 || series.length === 0) {
    return (
      <div className="flex h-[220px] items-center justify-center text-label text-muted-foreground-soft">
        No run activity in this window
      </div>
    )
  }

  return (
    <div>
      <ChartContainer config={config} className="h-[220px] w-full aspect-auto">
        <BarChart accessibilityLayer data={buckets} margin={{ top: 8, right: 6, left: -22, bottom: 0 }}>
          <CartesianGrid vertical={false} strokeDasharray="2 4" stroke="rgba(255,255,255,0.055)" />
          <XAxis
            dataKey="ts"
            tickLine={false}
            axisLine={false}
            tickMargin={8}
            minTickGap={window === "24h" ? 28 : 18}
            tick={{ fontSize: 11, fill: "var(--muted-foreground-soft)", fontFamily: "var(--font-mono)" }}
            tickFormatter={(value) => {
              const date = new Date(value)
              if (window === "24h") return `${String(date.getHours()).padStart(2, "0")}h`
              return date.toLocaleDateString(undefined, { month: "short", day: "numeric" })
            }}
          />
          <YAxis
            allowDecimals={false}
            tickLine={false}
            axisLine={false}
            width={32}
            tick={{ fontSize: 11, fill: "var(--muted-foreground-soft)", fontFamily: "var(--font-mono)" }}
          />
          <ChartTooltip
            cursor={{ fill: "rgba(255,255,255,0.035)" }}
            content={
              <ChartTooltipContent
                indicator="dot"
                labelFormatter={(value) =>
                  new Date(String(value)).toLocaleString(undefined, {
                    month: "short",
                    day: "numeric",
                    hour: window === "24h" ? "numeric" : undefined,
                  })
                }
              />
            }
          />
          {series.map((item, index) => (
            <Bar
              key={item.key}
              dataKey={item.key}
              stackId="runs"
              fill={`var(--color-${item.key})`}
              radius={index === series.length - 1 ? [3, 3, 0, 0] : 0}
              isAnimationActive={!reduce}
              animationBegin={index * 90}
              animationDuration={700}
              animationEasing="ease-out"
            />
          ))}
        </BarChart>
      </ChartContainer>

      <div className="mt-2 flex flex-wrap items-center justify-center gap-x-4 gap-y-1.5">
        {series.map((item) => (
          <span key={item.key} className="inline-flex items-center gap-1.5 text-label text-muted-foreground">
            <span className="h-2 w-2 rounded-sm" style={{ backgroundColor: item.color }} />
            {item.label}
          </span>
        ))}
      </div>
    </div>
  )
}
