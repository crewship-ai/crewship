import React from "react"
import { Database, Cpu, Container } from "lucide-react"
import { StatusDot } from "@/components/ui/status-badge"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
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
  const runtimeLabel =
    runtimeAvailable === null
      ? "Checking…"
      : runtimeAvailable
        ? `${runtimeInfo?.runtime === "apple" ? "Apple Containers" : (runtimeInfo?.runtime ?? "unknown").charAt(0).toUpperCase() + (runtimeInfo?.runtime ?? "").slice(1)} ${runtimeInfo?.version ?? ""}`
        : "Not detected"

  return (
    <div className="space-y-5">
      {/* KPI strip */}
      <div className="grid gap-3 grid-cols-2 lg:grid-cols-4">
        <KpiCard
          label="Workspaces"
          value={stats?.workspaces ?? 0}
          subtitle="total on instance"
        />
        <KpiCard
          label="Users"
          value={stats?.users ?? 0}
          subtitle="across all workspaces"
        />
        <KpiCard
          label="Agents"
          value={stats?.agents ?? 0}
          subtitle="configured"
        />
        <KpiCard
          label="Running"
          value={stats?.running ?? 0}
          valueColor={stats && stats.running > 0 ? "rgb(52, 211, 153)" : undefined}
          subtitle="currently active"
        />
      </div>

      {/* System Status */}
      <SettingsCard
        title="System status"
        description="Core infrastructure health of this Crewship instance"
      >
        <SettingsRow
          label={
            <span className="inline-flex items-center gap-2">
              <Database className="h-3 w-3 text-muted-foreground/60" />
              Database
            </span>
          }
        >
          <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <StatusDot status="COMPLETED" />
            SQLite · connected
          </span>
        </SettingsRow>
        <SettingsRow
          label={
            <span className="inline-flex items-center gap-2">
              <Cpu className="h-3 w-3 text-muted-foreground/60" />
              Engine
            </span>
          }
        >
          <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <StatusDot status="COMPLETED" />
            Running
          </span>
        </SettingsRow>
        <SettingsRow
          label={
            <span className="inline-flex items-center gap-2">
              <Container className="h-3 w-3 text-muted-foreground/60" />
              Container runtime
            </span>
          }
          border={false}
        >
          <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <StatusDot status={runtimeAvailable === true ? "COMPLETED" : "BLOCKED"} />
            {runtimeLabel}
          </span>
        </SettingsRow>
      </SettingsCard>
    </div>
  )
})
