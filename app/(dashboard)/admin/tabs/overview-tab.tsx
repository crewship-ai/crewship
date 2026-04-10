import React from "react"
import { Building, Users, Bot, Activity } from "lucide-react"
import { StatCard } from "@/components/layout/stat-card"
import { SectionCard } from "@/components/ui/section-card"
import { StatusDot } from "@/components/ui/status-badge"
import type { Stats } from "../types"

interface OverviewTabProps {
  stats: Stats | null
  runtimeAvailable: boolean | null
  runtimeInfo: { runtime: string; version: string; socket: string } | null
}

export const OverviewTab = React.memo(function OverviewTab({
  stats,
  runtimeAvailable,
  runtimeInfo,
}: OverviewTabProps) {
  const runtimeDesc =
    runtimeAvailable === null
      ? "Checking..."
      : runtimeAvailable
        ? `${runtimeInfo?.runtime === "apple" ? "Apple Containers" : (runtimeInfo?.runtime ?? "Unknown").charAt(0).toUpperCase() + (runtimeInfo?.runtime ?? "").slice(1)} ${runtimeInfo?.version ?? ""}`
        : "Not detected"

  const statCards = [
    { title: "Workspaces", value: stats?.workspaces ?? 0, subtitle: "total", icon: Building },
    { title: "Total Users", value: stats?.users ?? 0, subtitle: "across all workspaces", icon: Users },
    { title: "Total Agents", value: stats?.agents ?? 0, subtitle: "configured", icon: Bot },
    { title: "Running", value: stats?.running ?? 0, subtitle: "currently active", icon: Activity },
  ]

  const statusRows = [
    { name: "Database", status: true as const, desc: "SQLite (connected)", statusKey: "COMPLETED" },
    { name: "Engine", status: true as const, desc: "Running", statusKey: "COMPLETED" },
    {
      name: "Container Runtime",
      status: runtimeAvailable === true,
      desc: runtimeDesc,
      statusKey: runtimeAvailable === true ? "COMPLETED" : "BLOCKED",
    },
  ]

  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        {statCards.map((s) => (
          <StatCard key={s.title} {...s} />
        ))}
      </div>
      <SectionCard title="System Status" description="Core infrastructure health">
        <div className="space-y-3">
          {statusRows.map((s) => (
            <div key={s.name} className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <StatusDot status={s.statusKey} />
                <span className="text-body">{s.name}</span>
              </div>
              <span className="text-label text-muted-foreground">{s.desc}</span>
            </div>
          ))}
        </div>
      </SectionCard>
    </div>
  )
})
