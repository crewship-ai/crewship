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
  /** Word under the total in the centre. Defaults to the mission usage. */
  centerLabel?: string
  /** When given, legend rows become buttons that call back with the key. */
  onSelect?: (key: string) => void
}

/** Donut chart for a status distribution — missions, routines, anything counted. */
export function StatusDonut({ data, centerLabel = "missions", onSelect }: StatusDonutProps) {
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
    // Stacked on a phone: at 390px the ring and a six-row legend
    // cannot share a line, and side-by-side clipped the labels off the
    // right edge — leaving colour dots against no words at all.
    <div className="flex flex-col items-center gap-3 sm:flex-row sm:items-center sm:gap-4">
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
                      <tspan x={viewBox.cx} y={(viewBox.cy ?? 0) + 14} className="fill-muted-foreground text-[9px] uppercase tracking-wider">{centerLabel}</tspan>
                    </text>
                  )
                }
                return null
              }}
            />
          </Pie>
        </PieChart>
      </ChartContainer>

      {/* Legend. A row is a button only when the caller can do
          something with the click — a cursor that promises navigation
          and then does nothing is worse than plain text. */}
      <div className="flex w-full flex-1 flex-col gap-1 text-[11px]">
        {data.map((d) => {
          const row = (
            <>
              <span className="inline-flex items-center gap-1.5 text-foreground/80">
                <span className="w-2 h-2 rounded-sm" style={{ background: d.color }} />
                {d.label}
              </span>
              <span className="font-mono text-foreground/60 tabular-nums">{d.count}</span>
            </>
          )
          return onSelect ? (
            <button
              key={d.key}
              type="button"
              onClick={() => onSelect(d.key)}
              className="flex items-center justify-between gap-2 rounded px-1 -mx-1 py-0.5 text-left transition-colors hover:bg-white/[0.04]"
            >
              {row}
            </button>
          ) : (
            <div key={d.key} className="flex items-center justify-between gap-2">
              {row}
            </div>
          )
        })}
      </div>
    </div>
  )
}
