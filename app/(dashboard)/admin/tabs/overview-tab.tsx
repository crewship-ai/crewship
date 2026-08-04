import React from "react"
import {
  AlertTriangle, Check, Container, Cpu, Database, HardDrive, Info,
  KeyRound, Radio, ShieldCheck, Sparkles,
} from "lucide-react"
import { StatusDot } from "@/components/ui/status-badge"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { runtimeBrand } from "@/components/icons/runtime-icons"
import { cn } from "@/lib/utils"
import { AppearStack } from "@/components/ui/detail"
import type {
  Stats, AdminHealth, LicenseInfo, TelemetryInfo, VersionInfo,
  SecurityPosture, JournalIntegrity, KeeperStatus,
} from "../types"

interface OverviewTabProps {
  stats: Stats | null
  runtimeAvailable: boolean | null
  runtimeInfo: { runtime: string; version: string; socket: string } | null
  health: AdminHealth | null
  license: LicenseInfo | null
  telemetry: TelemetryInfo | null
  version: VersionInfo | null
  posture: SecurityPosture | null
  journal: JournalIntegrity | null
  keeper: KeeperStatus | null
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

function formatBytes(b: number): string {
  if (b < 1000) return `${b} B`
  const units = ["kB", "MB", "GB", "TB", "PB"]
  let v = b / 1000
  let i = 0
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

/** "3 / 15" where a ceiling applies, the bare count where none does. */
function against(used: number, limit?: number): string {
  if (!limit || limit <= 0) return `${used}`
  return `${used} / ${limit}`
}

const SEVERITY_ORDER: Record<string, number> = { high: 0, medium: 1, low: 2, info: 3 }

/**
 * What "generated" costs you, in one line.
 *
 * The enum value alone is not a finding. An auto-bootstrapped key is written
 * to <dataDir>/secrets.env, next to the database it protects — so a copied
 * disk, a restored snapshot or a stray backup carries the ciphertext AND what
 * opens it. That is the sentence an operator needs, not the word.
 */
function keySourceLabel(src?: string): string {
  switch (src) {
    case "generated":
      return "generated — key file sits beside the database, so a disk copy carries both"
    case "external":
      return "external — injected by the environment"
    case undefined:
    case "":
      return "unknown"
    default:
      return src
  }
}

export const OverviewTab = React.memo(function OverviewTab({
  stats, runtimeAvailable, runtimeInfo, health, license, telemetry,
  version, posture, journal, keeper,
}: OverviewTabProps) {
  // runtimeInfo is the runtime actually IN USE, and it is null when runtimes
  // are installed but the server holds no container provider (--no-docker, or
  // one that failed to start). That is a different state from "not detected"
  // and the old label could not express it — it capitalised whatever string
  // arrived, so a machine running OrbStack read "Docker" and a machine running
  // nothing read "Unknown " (#1690).
  const runtimeLabel =
    runtimeAvailable === null
      ? "Checking…"
      : !runtimeAvailable
        ? "Not detected"
        : runtimeInfo
          ? `${runtimeBrand(runtimeInfo.runtime).label} ${runtimeInfo.version ?? ""}`.trim()
          : "Detected · none in use"

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

  const warnings = [...(posture?.warnings ?? [])].sort(
    (a, b) => (SEVERITY_ORDER[a.severity] ?? 9) - (SEVERITY_ORDER[b.severity] ?? 9),
  )
  const journalEntries = journal?.entries_verified ?? journal?.entries
  const journalOK = journal?.ok ?? journal?.valid

  return (
    <div className="space-y-5">
      <AppearStack>
      {/* ── Needs attention ──
          First, and present even when empty. The old overview rendered only
          things that were fine and therefore always looked fine, while the
          instance itself knew its rate limiter was off. An absent block reads
          as "not checked"; an explicit all-clear is a different claim, and
          only one of them is true at a time — so the block appears when the
          posture was READ, not when it was bad. */}
      {posture && (
        <section aria-label="Needs attention">
          <SettingsCard
            title="Needs attention"
            description={
              warnings.length === 0
                ? "The instance's own read of its security posture"
                : `${warnings.length} finding${warnings.length === 1 ? "" : "s"} from the instance's own read of its security posture`
            }
          >
            {warnings.length === 0 ? (
              <div className="flex items-center gap-2 px-4 py-3 text-[11px] text-muted-foreground">
                <Check className="size-3 text-success" />
                Nothing needs attention — no posture warnings on this instance.
              </div>
            ) : (
              warnings.map((wn, i) => {
                const high = wn.severity === "high"
                const medium = wn.severity === "medium"
                return (
                  <div
                    key={wn.key}
                    data-severity={wn.severity}
                    className={cn(
                      "flex items-start gap-2.5 px-4 py-2.5",
                      i < warnings.length - 1 && "border-b border-border/40",
                    )}
                  >
                    {high || medium ? (
                      <AlertTriangle
                        className={cn("mt-0.5 size-3 shrink-0", high ? "text-destructive" : "text-warn")}
                      />
                    ) : (
                      <Info className="mt-0.5 size-3 shrink-0 text-info" />
                    )}
                    <div className="min-w-0">
                      <span
                        className={cn(
                          "mr-2 font-mono text-[10px] uppercase tracking-wide",
                          high ? "text-destructive" : medium ? "text-warn" : "text-muted-foreground",
                        )}
                      >
                        {wn.severity}
                      </span>
                      <span className="text-[11px] text-foreground/80">{wn.message}</span>
                    </div>
                  </div>
                )
              })
            )}
          </SettingsCard>
        </section>
      )}

      {/* ── Instance ──
          Which build, how long, on what, with how much room left. */}
      <SettingsCard title="Instance" description="What is running, and what it is running on">
        <SettingsRow
          label={
            <span className="inline-flex items-center gap-2">
              <Sparkles className="h-3 w-3 text-muted-foreground" />
              Version
            </span>
          }
        >
          <span className="inline-flex items-center gap-2 text-[11px] text-muted-foreground">
            <span className="font-mono text-foreground/80">{version?.current ?? "unknown"}</span>
            {/* Only when there IS one: an "up to date" badge on every load is
                noise that trains the eye to skip the row. */}
            {version?.newer && version.latest && (
              <span className="rounded-full border border-info/40 bg-info/10 px-1.5 py-0.5 font-mono text-[10px] text-info">
                {version.latest} available
              </span>
            )}
          </span>
        </SettingsRow>
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
            {health?.log_level?.level && (
              <span className="font-mono text-[10px] text-muted-foreground/70">
                · log {health.log_level.level}
                {health.log_level.expires_at ? " (temporary)" : ""}
              </span>
            )}
          </span>
        </SettingsRow>
        <SettingsRow
          label={
            <span className="inline-flex items-center gap-2">
              <Container className="h-3 w-3 text-muted-foreground" />
              Container runtime
            </span>
          }
        >
          <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <StatusDot status={runtimeAvailable === true ? "COMPLETED" : "BLOCKED"} />
            {runtimeLabel}
          </span>
        </SettingsRow>
        <SettingsRow
          label={
            <span className="inline-flex items-center gap-2">
              <HardDrive className="h-3 w-3 text-muted-foreground" />
              Disk
            </span>
          }
          description={health?.disk?.path}
          border={false}
        >
          {/* The data volume is the one that fills in practice — database,
              agent outputs and logs all live under it. A missing measurement
              stays missing: "0 B free" would invent an emergency. */}
          {!health?.disk ? (
            <span className="text-[11px] text-muted-foreground">Not reported</span>
          ) : health.disk.error ? (
            <span className="text-[11px] text-muted-foreground">
              Unavailable — {health.disk.error}
            </span>
          ) : (
            <span className="flex items-center gap-2 text-[11px] text-muted-foreground">
              <span className="hidden h-1.5 w-24 overflow-hidden rounded-full bg-muted sm:block">
                <span
                  className={cn(
                    "block h-full rounded-full",
                    (health.disk.used_pct ?? 0) >= 90
                      ? "bg-destructive"
                      : (health.disk.used_pct ?? 0) >= 75
                        ? "bg-warn"
                        : "bg-success",
                  )}
                  style={{ width: `${Math.min(100, Math.max(0, health.disk.used_pct ?? 0))}%` }}
                />
              </span>
              <span className="font-mono tabular-nums text-foreground/80">
                {Math.round(health.disk.used_pct ?? 0)}%
              </span>
              <span>
                {formatBytes(health.disk.free_bytes ?? 0)} free of{" "}
                {formatBytes(health.disk.total_bytes ?? 0)}
              </span>
            </span>
          )}
        </SettingsRow>
      </SettingsCard>

      <div className="grid gap-5 lg:grid-cols-2">
        {/* ── Capacity ──
            Counts against the ceilings that apply to them. "8 agents" is not
            something anyone acts on; "3 / 15 crews" is. */}
        <section aria-label="Capacity">
          <SettingsCard title="Capacity" description="This workspace against its licensed limits">
            <SettingsRow label="Crews">
              <span className="font-mono text-xs tabular-nums text-foreground">
                {against(stats?.crews ?? 0, license?.max_crews)}
              </span>
            </SettingsRow>
            <SettingsRow
              label="Agents"
              description={license?.max_agents_per_crew ? `${license.max_agents_per_crew} per crew allowed` : undefined}
            >
              <span className="font-mono text-xs tabular-nums text-foreground">
                {stats?.agents ?? 0}
              </span>
            </SettingsRow>
            <SettingsRow label="Members">
              <span className="font-mono text-xs tabular-nums text-foreground">
                {against(stats?.users ?? 0, license?.max_members)}
              </span>
            </SettingsRow>
            <SettingsRow label="Running now" border={false}>
              <span
                className={cn(
                  "font-mono text-xs tabular-nums",
                  stats && stats.running > 0 ? "text-success" : "text-foreground",
                )}
              >
                {stats?.running ?? 0}
              </span>
            </SettingsRow>
          </SettingsCard>
        </section>

        {/* ── Integrity ──
            The instance's tamper-evidence, in the place someone would look
            for it. The journal chain verifies on demand and nothing rendered
            the answer anywhere. */}
        <section aria-label="Integrity">
          <SettingsCard title="Integrity" description="Tamper-evidence and key custody">
            <SettingsRow
              label={
                <span className="inline-flex items-center gap-2">
                  <ShieldCheck className="h-3 w-3 text-muted-foreground" />
                  Journal chain
                </span>
              }
            >
              {journal === null ? (
                <span className="text-[11px] text-muted-foreground">Not checked</span>
              ) : journalOK === false || journal.error ? (
                <span className="inline-flex items-center gap-1.5 text-[11px] text-destructive">
                  <StatusDot status="FAILED" />
                  {journal.error ?? "Verification failed"}
                </span>
              ) : (
                <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
                  <StatusDot status="COMPLETED" />
                  {(journalEntries ?? 0).toLocaleString()} entries verified
                </span>
              )}
            </SettingsRow>
            <SettingsRow
              label={
                <span className="inline-flex items-center gap-2">
                  <KeyRound className="h-3 w-3 text-muted-foreground" />
                  Encryption key
                </span>
              }
            >
              <span
                className={cn(
                  "text-right text-[11px]",
                  health?.encryption_key_source === "generated" ? "text-warn" : "text-muted-foreground",
                )}
              >
                {keySourceLabel(health?.encryption_key_source)}
              </span>
            </SettingsRow>
            <SettingsRow
              label={
                <span className="inline-flex items-center gap-2">
                  <ShieldCheck className="h-3 w-3 text-muted-foreground" />
                  Keeper
                </span>
              }
            >
              <span className="text-[11px] text-muted-foreground">
                {keeper === null
                  ? "Not checked"
                  : keeper.enabled
                    ? `On · ${keeper.deny_count} denied of ${keeper.total_requests}`
                    : "Off"}
              </span>
            </SettingsRow>
            <SettingsRow
              label={
                <span className="inline-flex items-center gap-2">
                  <Radio className="h-3 w-3 text-muted-foreground" />
                  Telemetry
                </span>
              }
              description="Toggle via `crewship telemetry on|off`"
              border={false}
            >
              <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <StatusDot status={telemetry?.enabled ? "COMPLETED" : "PENDING"} />
                {telemetry === null ? "Unknown" : telemetry.enabled ? "Enabled" : "Disabled"}
              </span>
            </SettingsRow>
          </SettingsCard>
        </section>
      </div>

      {/* ── Licence ── */}
      <SettingsCard title="License" description="Edition and what it permits">
        <SettingsRow
          label={
            <span className="inline-flex items-center gap-2">
              <KeyRound className="h-3 w-3 text-muted-foreground" />
              Edition
            </span>
          }
        >
          <span className="font-mono text-[11px] text-muted-foreground">
            {license?.edition ?? "unknown"}
            {license?.licensee_org ? ` · ${license.licensee_org}` : ""}
          </span>
        </SettingsRow>
        <SettingsRow label="Limits" border={!license?.features?.length}>
          <span className="font-mono text-[11px] text-muted-foreground">
            {license
              ? `${license.max_crews} crews · ${license.max_agents_per_crew} agents/crew · ${license.max_members} members`
              : "—"}
          </span>
        </SettingsRow>
        {license?.features && license.features.length > 0 && (
          <SettingsRow label="Features" border={false}>
            <span className="flex flex-wrap justify-end gap-1">
              {license.features.map((f) => (
                <span
                  key={f}
                  className="rounded border border-border/60 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
                >
                  {f}
                </span>
              ))}
            </span>
          </SettingsRow>
        )}
      </SettingsCard>
      </AppearStack>
    </div>
  )
})
