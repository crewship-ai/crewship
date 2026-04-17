"use client"

import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"
import { ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig } from "@/components/ui/chart"

export interface SpendDatum {
  /** Label for the X axis — crew or agent name. */
  name: string
  /** Immutable id used as the React key and selection handle. */
  id: string
  cost_usd: number
}

interface SpendChartProps {
  data: SpendDatum[]
  /** Optional selection handler — enables drilldown from crew → agent. */
  onSelect?: (id: string) => void
  /** Currently-selected id; highlighted bar. */
  selectedId?: string | null
  height?: number
  /** Bar colour accent — CSS var or direct colour string. */
  color?: string
}

const chartConfig: ChartConfig = {
  cost_usd: { label: "Cost", color: "var(--chart-1)" },
}

/**
 * Horizontal bar chart of spend. Uses a vertical layout (category on Y axis)
 * so long crew / agent names stay readable. Clicking a bar calls `onSelect`.
 */
export function SpendChart({ data, onSelect, selectedId, height = 260, color = "var(--chart-1)" }: SpendChartProps) {
  return (
    <ChartContainer config={chartConfig} className="w-full aspect-auto" style={{ height }}>
      <BarChart
        accessibilityLayer
        data={data}
        layout="vertical"
        margin={{ top: 4, right: 12, left: 4, bottom: 0 }}
      >
        <CartesianGrid horizontal={false} strokeDasharray="2 3" stroke="rgba(255,255,255,0.04)" />
        <XAxis
          type="number"
          tickLine={false}
          axisLine={false}
          tickMargin={6}
          tick={{ fontSize: 9, fill: "rgba(230,231,235,0.4)", fontFamily: "ui-monospace" }}
          tickFormatter={(v) => `$${Number(v).toFixed(2)}`}
        />
        <YAxis
          dataKey="name"
          type="category"
          tickLine={false}
          axisLine={false}
          width={100}
          tick={{ fontSize: 10, fill: "rgba(230,231,235,0.7)" }}
        />
        <ChartTooltip
          cursor={{ fill: "rgba(255,255,255,0.04)" }}
          content={
            <ChartTooltipContent
              indicator="dot"
              formatter={(value) => (
                <>
                  <span className="text-muted-foreground">Cost</span>
                  <span className="text-foreground font-mono font-medium tabular-nums ml-auto">
                    ${Number(value).toFixed(4)}
                  </span>
                </>
              )}
            />
          }
        />
        <Bar
          dataKey="cost_usd"
          radius={[0, 4, 4, 0]}
          fill={color}
          onClick={(entry: unknown) => {
            if (!onSelect) return
            const row = entry as { id?: string }
            if (row?.id) onSelect(row.id)
          }}
          style={{ cursor: onSelect ? "pointer" : "default" }}
          activeBar={selectedId ? { stroke: "var(--primary)", strokeWidth: 1 } : undefined}
        />
      </BarChart>
    </ChartContainer>
  )
}
