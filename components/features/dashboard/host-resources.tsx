"use client"

import { Cpu } from "lucide-react"
import { CartesianGrid, Line, LineChart, XAxis, YAxis } from "recharts"

import type { DashboardWindow, HostResourceResponse } from "@/app/(dashboard)/dashboard-types"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { ChartContainer, ChartTooltip, ChartTooltipContent } from "@/components/ui/chart"
import { cn } from "@/lib/utils"

const chartConfig = {
  cpu_percent: { label: "Server CPU", color: "var(--chart-1)" },
  memory_percent: { label: "Server RAM", color: "var(--chart-3)" },
} as const

function ResourceGauge({ label, value, detail, color }: { label: string; value: number; detail: string; color: string }) {
  const percent = Math.min(100, Math.max(0, value))
  const circumference = 2 * Math.PI * 34
  return (
    <div className="flex min-w-0 items-center gap-3 rounded-lg border border-border/60 bg-card/60 p-3">
      <div className="relative h-[76px] w-[76px] shrink-0" role="img" aria-label={`${label}: ${Math.round(percent)}%`}>
        <svg viewBox="0 0 80 80" className="h-full w-full -rotate-90" aria-hidden>
          <circle cx="40" cy="40" r="34" fill="none" stroke="var(--border)" strokeWidth="7" />
          <circle cx="40" cy="40" r="34" fill="none" stroke={color} strokeWidth="7" strokeLinecap="round"
            strokeDasharray={`${(percent / 100) * circumference} ${circumference}`} />
        </svg>
        <span className="absolute inset-0 flex items-center justify-center text-base font-semibold tabular-nums text-foreground" aria-hidden>{Math.round(percent)}%</span>
      </div>
      <div className="min-w-0">
        <div className="text-body font-medium text-foreground">{label}</div>
        <div className="mt-1 text-label text-muted-foreground">{detail}</div>
      </div>
    </div>
  )
}

export function HostResources({ data, window, loading, error }: { data: HostResourceResponse | null; window: DashboardWindow; loading: boolean; error: boolean }) {
  const latest = data?.latest
  const stale = latest ? Date.now() - new Date(latest.sampled_at).getTime() > 3 * 60_000 : false
  const measured = data?.series.filter((bucket) => bucket.cpu_percent != null || bucket.memory_percent != null).length ?? 0
  const recordingSince = data?.recording_since
    ? new Date(data.recording_since).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })
    : null

  return (
    <DashboardCard title="Server load" icon={Cpu} hint={error ? "unavailable" : latest ? stale ? "last reading is old" : "updated every minute" : loading ? "loading" : "waiting for first reading"}>
      <p className="mb-3 text-label text-muted-foreground">CPU and RAM of the server running Crewship, including other processes.</p>
      {latest ? (
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <ResourceGauge label="CPU" value={latest.cpu_percent} detail="Share of all host CPU cores" color="var(--chart-1)" />
          <ResourceGauge label="RAM" value={latest.memory_percent} detail={`${(latest.memory_used_mb / 1024).toFixed(1)} of ${(latest.memory_total_mb / 1024).toFixed(1)} GiB used`} color="var(--chart-3)" />
        </div>
      ) : (
        <div className="rounded-lg border border-border/60 p-4 text-label text-muted-foreground">{error ? "Could not load server measurements." : loading ? "Loading server measurements…" : "Host measurements are not available on this server yet."}</div>
      )}

      <div className="mt-4 flex flex-wrap items-center justify-between gap-2">
        <span className="text-label font-medium text-foreground/85">History · {window}</span>
        <span className={cn("text-micro text-muted-foreground", stale && "text-warn")}>{recordingSince ? `History available since ${recordingSince}` : error ? "History unavailable" : "Recording starts with this release"}</span>
      </div>
      {measured > 0 ? (
        <ChartContainer config={chartConfig} className="mt-2 h-[180px] w-full aspect-auto">
          <LineChart accessibilityLayer data={data?.series ?? []} margin={{ top: 8, right: 8, left: -22, bottom: 0 }}>
            <CartesianGrid vertical={false} strokeDasharray="2 4" stroke="rgba(255,255,255,0.055)" />
            <XAxis dataKey="ts" tickLine={false} axisLine={false} tickMargin={8} minTickGap={28}
              tick={{ fontSize: 11, fill: "var(--muted-foreground-soft)", fontFamily: "var(--font-mono)" }}
              tickFormatter={(value) => window === "24h" ? new Date(value).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" }) : new Date(value).toLocaleDateString(undefined, { day: "numeric", month: "short" })} />
            <YAxis domain={[0, 100]} ticks={[0, 25, 50, 75, 100]} tickLine={false} axisLine={false} width={38}
              tick={{ fontSize: 11, fill: "var(--muted-foreground-soft)", fontFamily: "var(--font-mono)" }} tickFormatter={(value) => `${value}%`} />
            <ChartTooltip content={<ChartTooltipContent indicator="line" labelFormatter={(value) => new Date(String(value)).toLocaleString()} />} />
            <Line type="monotone" dataKey="cpu_percent" stroke="var(--chart-1)" strokeWidth={2} connectNulls={false} dot={measured < 3} isAnimationActive={false} />
            <Line type="monotone" dataKey="memory_percent" stroke="var(--chart-3)" strokeWidth={2} connectNulls={false} dot={measured < 3} isAnimationActive={false} />
          </LineChart>
        </ChartContainer>
      ) : (
        <div className="mt-2 flex h-[140px] items-center justify-center rounded-lg border border-dashed border-border/60 text-center text-label text-muted-foreground">{error ? "Historical measurements could not be loaded." : "History will appear as measurements are collected."}</div>
      )}
      <div className="mt-2 flex items-center justify-center gap-4 text-label text-muted-foreground">
        <span className="flex items-center gap-1.5"><span className="h-2 w-2 rounded-full bg-chart-1" />CPU</span>
        <span className="flex items-center gap-1.5"><span className="h-2 w-2 rounded-full bg-chart-3" />RAM</span>
      </div>
    </DashboardCard>
  )
}
