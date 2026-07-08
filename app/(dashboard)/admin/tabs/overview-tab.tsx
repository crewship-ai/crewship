import React from "react"
import { Database, Cpu, Container, KeyRound, Radio } from "lucide-react"
import { StatusDot } from "@/components/ui/status-badge"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import type { Stats, AdminHealth, LicenseInfo, TelemetryInfo } from "../types"

interface OverviewTabProps {
  stats: Stats | null
  runtimeAvailable: boolean | null
  runtimeInfo: { runtime: string; version: string; socket: string } | null
  health: AdminHealth | null
  license: LicenseInfo | null
  telemetry: TelemetryInfo | null
}

// formatUptime renders seconds as a compact "3d 4h" / "5m" string.
function formatUptime(sec: number): string {
  if (sec < 60) return `${Math.max(0, Math.floor(sec))}s`
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

export const OverviewTab = React.memo(function OverviewTab({
  stats,
  runtimeAvailable,
  runtimeInfo,
  health,
  license,
  telemetry,
}: OverviewTabProps) {
  const runtimeLabel =
    runtimeAvailable === null
      ? "Checking…"
      : runtimeAvailable
        ? `${runtimeInfo?.runtime === "apple" ? "Apple Containers" : (runtimeInfo?.runtime ?? "unknown").charAt(0).toUpperCase() + (runtimeInfo?.runtime ?? "").slice(1)} ${runtimeInfo?.version ?? ""}`
        : "Not detected"

  // Real probes, not hardcoded green (#868). DB status comes from the health
  // endpoint's ping; the engine dot reflects whether the API process (the
  // single binary that runs the engine) answered the health probe at all.
  const dbConnected = health?.db?.connected
  const dbStatus = dbConnected === undefined ? "PENDING" : dbConnected ? "COMPLETED" : "FAILED"
  const dbLabel =
    dbConnected === undefined
      ? "Checking…"
      : dbConnected
        ? "SQLite · connected"
        : `SQLite · unreachable${health?.db?.error ? ` (${health.db.error})` : ""}`

  const engineUp = health !== null
  const engineStatus = engineUp ? "COMPLETED" : "FAILED"
  const engineLabel = engineUp
    ? `Running · up ${formatUptime(health!.uptime_seconds)}`
    : "Unreachable"

  return (
    <div className="space-y-5">
      {/* KPI strip — scoped to THIS workspace (the console is workspace admin). */}
      <div className="grid gap-3 grid-cols-2 lg:grid-cols-4">
        <KpiCard
          label="Workspace"
          value={stats?.workspaces ?? 1}
          subtitle="administering this one"
        />
        <KpiCard
          label="Users"
          value={stats?.users ?? 0}
          subtitle="in this workspace"
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
        description="Live health of this Crewship instance"
      >
        <SettingsRow
          label={
            <span className="inline-flex items-center gap-2">
              <Database className="h-3 w-3 text-muted-foreground" />
              Database
            </span>
          }
        >
          <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <StatusDot status={dbStatus} />
            {dbLabel}
          </span>
        </SettingsRow>
        <SettingsRow
          label={
            <span className="inline-flex items-center gap-2">
              <Cpu className="h-3 w-3 text-muted-foreground" />
              Engine
            </span>
          }
        >
          <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <StatusDot status={engineStatus} />
            {engineLabel}
          </span>
        </SettingsRow>
        <SettingsRow
          label={
            <span className="inline-flex items-center gap-2">
              <Container className="h-3 w-3 text-muted-foreground" />
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

      {/* License + telemetry — read-only; both are configured via the CLI. */}
      <div className="grid gap-3 md:grid-cols-2">
        <SettingsCard
          title="License"
          description="Edition and limits for this instance"
        >
          <SettingsRow
            label={
              <span className="inline-flex items-center gap-2">
                <KeyRound className="h-3 w-3 text-muted-foreground" />
                Edition
              </span>
            }
          >
            <span className="text-[11px] text-muted-foreground">
              {license ? license.edition : "—"}
              {license?.licensee_org ? ` · ${license.licensee_org}` : ""}
            </span>
          </SettingsRow>
          <SettingsRow label={<span className="text-muted-foreground">Limits</span>} border={false}>
            <span className="text-[11px] text-muted-foreground font-mono">
              {license
                ? `${license.max_crews} crews · ${license.max_agents_per_crew} agents/crew · ${license.max_members} members`
                : "—"}
            </span>
          </SettingsRow>
        </SettingsCard>

        <SettingsCard
          title="Telemetry"
          description="Crash + usage reporting consent (toggle via `crewship telemetry on|off`)"
        >
          <SettingsRow
            label={
              <span className="inline-flex items-center gap-2">
                <Radio className="h-3 w-3 text-muted-foreground" />
                Reporting
              </span>
            }
            border={false}
          >
            <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
              <StatusDot status={telemetry?.enabled ? "COMPLETED" : "PENDING"} />
              {telemetry === null ? "—" : telemetry.enabled ? "Enabled" : "Disabled"}
            </span>
          </SettingsRow>
        </SettingsCard>
      </div>
    </div>
  )
})
