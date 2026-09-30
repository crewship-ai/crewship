import Link from "next/link"
import React, { type CSSProperties } from "react"
import { motion, useReducedMotion } from "motion/react"
import {
  Activity, AlertTriangle, Archive, BadgeCheck, Boxes, Bot, Check, ChevronRight, Container, Cpu, Database,
  HardDrive, Info, KeyRound, Loader2, Plug, Radio, ScrollText, ShieldCheck, Sparkles, Users,
  type LucideIcon,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { StatusPill } from "@/components/ui/status-pill"
import { StatusDot } from "@/components/ui/status-badge"
import { Skeleton } from "@/components/ui/skeleton"
import { AppearStack } from "@/components/ui/detail"
import { CrewshipLogo } from "@/components/branding/crewship-logo"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { runtimeBrand } from "@/components/icons/runtime-icons"
import type {
  Stats, AdminHealth, LicenseInfo, TelemetryInfo, VersionInfo, SecurityPosture, JournalIntegrity,
  KeeperStatus, DaemonStatus, AuxStatus, AgentsStatus, KeeperHealth, TimeseriesPoint,
} from "../types"

/**
 * Admin › Overview — the instance at a glance, most urgent first.
 *
 *   1. Identity + verdict: which build, which edition, up how long, and one
 *      line of checks (database, engine, runtime, host daemon, disk, journal).
 *   2. Capacity against the licence, as bars.
 *   3. What needs attention (posture findings, each with its fix) beside the
 *      week's runs.
 *   4. Platform, integrity, AI helpers and the licence, as Settings-style cards.
 *
 * Every figure is one the server measures itself, so the page reads the same
 * on Linux, macOS and Windows, on a laptop or a cloud VM; the one host-level
 * figure, disk capacity, says "unavailable" where the server cannot read it.
 * Each prop may still be loading (null) — a card shows a skeleton line, never
 * a made-up zero.
 */
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
  daemon?: DaemonStatus | null
  aux?: AuxStatus | null
  agents?: AgentsStatus | null
  keeperHealth?: KeeperHealth | null
  runs?: TimeseriesPoint[] | null
  cost?: TimeseriesPoint[] | null
  /** True while the journal chain is still being walked. */
  journalPending?: boolean
  /** An instance admin in no workspace: the runs and the host daemon are read
   *  per workspace, so they say so instead of loading forever. */
  noWorkspace?: boolean
}

// ── formatting ───────────────────────────────────────────────────────────

export function formatUptime(sec: number): string {
  if (sec < 60) return `${Math.max(0, Math.floor(sec))}s`
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

/** Go duration ("5m53.3s", "2h1m0s") → seconds, for the daemon's uptime. */
export function goDurationSeconds(s: string | undefined): number | null {
  if (!s) return null
  let total = 0
  let matched = false
  for (const [, n, unit] of s.matchAll(/([\d.]+)(h|ms|m|s|µs|us|ns)/g)) {
    matched = true
    const v = Number(n)
    total += unit === "h" ? v * 3600 : unit === "m" ? v * 60 : unit === "s" ? v : 0
  }
  return matched ? total : null
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

/** True when a licensed ceiling is exceeded — the row turns red and says what
 *  that means, instead of printing "101 / 15" in the same grey as "1 / 5". */
export function overLimit(used: number, limit?: number): boolean {
  return Boolean(limit && limit > 0 && used > limit)
}

const EDITION_LABEL: Record<string, string> = { community: "Community Edition", team: "Team Edition", enterprise: "Enterprise Edition" }
export const editionLabel = (edition?: string) =>
  edition ? EDITION_LABEL[edition] ?? `${edition.charAt(0).toUpperCase()}${edition.slice(1)} Edition` : "Edition unknown"

/**
 * What a person can DO about a posture finding, by key. A finding without a
 * verb is a worry; with one it is a task. Keys mirror
 * internal/api/admin_security_posture.go; an unknown key gets no action, not
 * a made-up one.
 */
export const FINDING_ACTIONS: Record<string, { label: string; href: string }> = {
  no_backup_recorded: { label: "Create a backup", href: "/admin?tab=backups" },
  rate_limit_disabled: { label: "Limits", href: "/admin?tab=ratelimits" },
  signup_open: { label: "People", href: "/admin/people" },
  seed_account_default_password: { label: "People", href: "/admin/people" },
  privileged_credentials_enabled: { label: "Access & Secrets", href: "/settings?tab=access" },
  private_endpoints_in_use: { label: "Security", href: "/admin/security" },
  private_endpoints_ceiling_open: { label: "Security", href: "/admin/security" },
  encryption_key_generated: { label: "Key custody", href: "/admin?tab=backups" },
  encryption_key_missing: { label: "Key custody", href: "/admin?tab=backups" },
  plaintext_secrets_allowed: { label: "Access & Secrets", href: "/settings?tab=access" },
}

const SEVERITY_ORDER: Record<string, number> = { high: 0, medium: 1, low: 2, info: 3 }

/**
 * What "generated" costs you, in one line. An auto-bootstrapped key is written
 * to <dataDir>/secrets.env, next to the database it protects — so a copied
 * disk, a restored snapshot or a stray backup carries the ciphertext AND what
 * opens it.
 */
function keySourceLabel(src?: string): string {
  switch (src) {
    case "generated":
      return "Generated"
    case "external":
      return "External"
    case undefined:
    case "":
      return "Unknown"
    default:
      return src
  }
}

function keySourceNote(src?: string): string | undefined {
  if (src === "generated") return "The key file sits beside the database, so a disk copy carries both"
  if (src === "external") return "Injected by the environment"
  return undefined
}

type Check = { key: string; icon: LucideIcon; label: string; detail: string; state: "ok" | "warn" | "bad" | "pending" | "na" }

const STATE_DOT: Record<Check["state"], string> = {
  ok: "COMPLETED", warn: "BLOCKED", bad: "FAILED", pending: "PENDING", na: "UNKNOWN",
}

// ── the tab ──────────────────────────────────────────────────────────────

export const OverviewTab = React.memo(function OverviewTab({
  stats, runtimeAvailable, runtimeInfo, health, license, telemetry,
  version, posture, journal, keeper, daemon = null, aux = null, agents = null,
  keeperHealth = null, runs = null, cost = null, journalPending = false, noWorkspace = false,
}: OverviewTabProps) {
  // runtimeInfo is the runtime actually IN USE, and it is null when runtimes
  // are installed but the server holds no container provider (--no-docker, or
  // one that failed to start) — a different state from "not detected" (#1690).
  const runtimeLabel =
    runtimeAvailable === null
      ? "Checking…"
      : !runtimeAvailable
        ? "Not detected"
        : runtimeInfo
          ? `${runtimeBrand(runtimeInfo.runtime).label} ${runtimeInfo.version ?? ""}`.trim()
          : "Detected · none in use"

  const warnings = [...(posture?.warnings ?? [])].sort(
    (a, b) => (SEVERITY_ORDER[a.severity] ?? 9) - (SEVERITY_ORDER[b.severity] ?? 9),
  )
  const journalEntries = journal?.count ?? journal?.entries_verified ?? journal?.entries
  const journalOK = journal?.ok ?? journal?.valid
  const diskPct = health?.disk && !health.disk.error ? Math.round(health.disk.used_pct ?? 0) : null
  const daemonUp = goDurationSeconds(daemon?.uptime)
  const noBackup = warnings.some((w) => w.key === "no_backup_recorded")

  // Real probes, never a hardcoded green (#868).
  const checks: Check[] = [
    {
      key: "db", icon: Database, label: "Database",
      detail: health === null ? "Checking…" : health.db?.connected ? "Connected" : "Unreachable",
      state: health === null ? "pending" : health.db?.connected ? "ok" : "bad",
    },
    {
      key: "engine", icon: Cpu, label: "Engine",
      detail: health ? `Up ${formatUptime(health.uptime_seconds)}` : "Checking…",
      state: health ? "ok" : "pending",
    },
    {
      key: "runtime", icon: Container, label: "Containers", detail: runtimeLabel,
      state: runtimeAvailable === null ? "pending" : runtimeAvailable && runtimeInfo ? "ok" : runtimeAvailable ? "warn" : "bad",
    },
    {
      key: "daemon", icon: Plug, label: "Host daemon",
      detail: daemon ? `${daemon.connections} connection${daemon.connections === 1 ? "" : "s"}` : noWorkspace ? "In a workspace" : "Checking…",
      state: daemon === null ? (noWorkspace ? "na" : "pending") : daemon.status === "ok" ? "ok" : "bad",
    },
    {
      key: "disk", icon: HardDrive, label: "Disk",
      detail: health === null ? "Checking…" : diskPct === null ? "Unavailable" : `${diskPct}% used`,
      state: health === null ? "pending" : diskPct === null ? "warn" : diskPct >= 90 ? "bad" : diskPct >= 75 ? "warn" : "ok",
    },
    {
      key: "journal", icon: ScrollText, label: "Journal",
      detail: journalPending ? "Verifying…" : journal === null ? "Not checked" : journalOK === false || journal.error ? "Broken chain" : "Chain intact",
      state: journalPending ? "pending" : journal === null ? "warn" : journalOK === false || journal.error ? "bad" : "ok",
    },
  ]
  const bad = checks.filter((c) => c.state === "bad").length
  const findings = warnings.filter((w) => w.severity === "high" || w.severity === "medium").length

  return (
    <div className="space-y-4">
      <AppearStack>
        {/* ── Identity + verdict ── */}
        <section aria-label="Instance status" className="panel-surface overflow-hidden rounded-card border border-border" data-slot="admin-hero">
          <div className="flex flex-wrap items-center gap-4 px-5 py-4">
            <span className="icon-tile grid h-12 w-12 shrink-0 place-items-center rounded-xl" aria-hidden>
              <CrewshipLogo tight className="h-6 w-auto" />
            </span>
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <h2 className="text-lg font-semibold tracking-[-0.02em]">Crewship</h2>
                {license ? (
                  <span className="rounded-full border border-primary/30 bg-primary/10 px-2 py-0.5 text-[11px] font-medium text-primary-hover">{editionLabel(license.edition)}</span>
                ) : (
                  <Skeleton className="h-5 w-32 rounded-full" />
                )}
                {version?.newer && version.latest && (
                  <a href={version.url ?? undefined} target="_blank" rel="noopener noreferrer" className="rounded-full border border-info/40 bg-info/10 px-2 py-0.5 font-mono text-[10.5px] text-info">
                    {version.latest} available
                  </a>
                )}
              </div>
              <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 font-mono text-[11.5px] text-muted-foreground [&>span]:whitespace-nowrap">
                {version ? (
                  <>
                    <span className="text-foreground/85">{version.current}</span>
                    {version.commit && <span title={version.commit}>· {version.commit.slice(0, 9)}{version.dirty ? "+dirty" : ""}</span>}
                    {version.build_time && <span>· built {new Date(version.build_time).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })}</span>}
                    {version.os && <span>· {version.os}/{version.arch}</span>}
                    {version.go_version && <span className="hidden sm:inline">· {version.go_version}</span>}
                  </>
                ) : (
                  <Skeleton className="h-3.5 w-64" />
                )}
              </p>
            </div>
            <div className="flex w-full items-center justify-between gap-2 sm:w-auto sm:flex-col sm:items-end sm:gap-1">
              {health === null || posture === null ? (
                <Skeleton className="h-6 w-36 rounded-full" />
              ) : bad > 0 ? (
                <StatusPill tone="danger" label={`${bad} check${bad === 1 ? "" : "s"} failing`} />
              ) : findings > 0 ? (
                <StatusPill tone="warn" label={`${findings} finding${findings === 1 ? "" : "s"} to review`} />
              ) : (
                <StatusPill tone="success" label="All systems healthy" />
              )}
              <span className="font-mono text-[11px] text-muted-foreground">{health ? `up ${formatUptime(health.uptime_seconds)}` : ""}</span>
            </div>
          </div>
          <ul className="grid grid-cols-2 border-t border-border sm:grid-cols-3 lg:grid-cols-6" aria-label="Health checks">
            {checks.map((c) => (
              <li key={c.key} data-check={c.key} data-state={c.state}
                className="flex min-w-0 items-center gap-2 border-b border-r border-border/60 px-4 py-2.5 lg:border-b-0 [&:nth-child(6)]:border-r-0">
                <c.icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
                <span className="min-w-0 flex-1">
                  <span className="block text-[12px] font-medium">{c.label}</span>
                  <span className={cn("flex items-center gap-1.5 truncate text-[11px]", c.state === "bad" ? "text-destructive" : c.state === "warn" ? "text-warn" : "text-muted-foreground")}>
                    {c.state === "pending" ? <Loader2 className="h-3 w-3 animate-spin" aria-hidden /> : <StatusDot status={STATE_DOT[c.state]} />}
                    <span className="truncate">{c.detail}</span>
                  </span>
                </span>
              </li>
            ))}
          </ul>
        </section>

        {/* ── Capacity against the licence ── */}
        <section aria-label="Capacity" className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          <Meter icon={Boxes} tint="var(--primary)" label="Crews" used={stats?.crews} limit={license?.max_crews} testId="admin-capacity-crews"
            note={overLimit(stats?.crews ?? 0, license?.max_crews) ? "Over the licensed limit — new crews are refused" : undefined} />
          <Meter icon={Users} tint="var(--purple)" label="Members" used={stats?.users} limit={license?.max_members}
            note={overLimit(stats?.users ?? 0, license?.max_members) ? "Over the licensed seats — invitations are refused" : undefined} />
          <Meter icon={Bot} tint="var(--info)" label="Agents" used={stats?.agents}
            note={agents ? `${agents.running} running · ${agents.queued} queued${agents.error ? ` · ${agents.error} in error` : ""}` : license?.max_agents_per_crew ? `Up to ${license.max_agents_per_crew} per crew` : undefined}
            tone={agents?.error ? "bad" : undefined} />
          <Meter icon={Activity} tint="var(--success)" label="Running now" used={stats?.running} tone={stats && stats.running > 0 ? "live" : undefined}
            note={runs ? `${runs.reduce((s, p) => s + p.value, 0)} runs in 7 days` : undefined} />
        </section>

        <div className="grid items-stretch gap-4 lg:grid-cols-5">
          {/* ── Needs attention ── First among the cards and present even
              when empty: an absent block reads as "not checked", an explicit
              all-clear is a different claim. It appears when the posture was
              READ, not when it was bad. Findings are one line each (the
              message's first sentence) and open for the rest; a long list
              scrolls inside the card instead of stretching the page. */}
          <div className="flex min-w-0 lg:col-span-3">
            {posture ? (
              <SettingsCard icon={AlertTriangle} tint={warnings.length ? "var(--warn)" : "var(--success)"} className="flex w-full flex-col"
                title="Needs attention"
                description={warnings.length === 0 ? "The instance's own read of its security posture" : `${warnings.length} finding${warnings.length === 1 ? "" : "s"} from the instance's own read of its security posture`}>
                {warnings.length === 0 ? (
                  <div className="flex items-center gap-2 px-4 py-4 text-[12px] text-muted-foreground">
                    <Check className="size-3.5 text-success" />
                    Nothing needs attention — no posture warnings on this instance.
                  </div>
                ) : (
                  <div className="max-h-[288px] overflow-y-auto" data-slot="admin-findings">
                    {warnings.map((wn) => <Finding key={wn.key} finding={wn} />)}
                  </div>
                )}
              </SettingsCard>
            ) : (
              <Skeleton className="min-h-40 w-full rounded-card" />
            )}
          </div>

          {/* ── The week's runs ── */}
          <div className="flex min-w-0 lg:col-span-2">
            <SettingsCard icon={Activity} tint="var(--success)" title="Runs this week" className="flex w-full flex-col" bodyClassName="flex flex-1 flex-col"
              description={cost ? `${formatUsd(cost.reduce((s, p) => s + p.value, 0))} spent in 7 days` : "Agent and routine runs per day"}>
              <RunBars runs={runs} noWorkspace={noWorkspace} />
            </SettingsCard>
          </div>
        </div>

        {/* ── Platform and integrity ── Five rows each, so the pair lines up;
            every row is icon · name · status on the right. */}
        <div className="grid items-stretch gap-4 lg:grid-cols-2">
          <SettingsCard icon={Cpu} tint="var(--purple)" title="Platform" description="What is running, and what it is running on" className="h-full">
            <Row icon={Cpu} label="Engine" description={health?.log_level?.level ? `Log level ${health.log_level.level}${health.log_level.expires_at ? " (temporary)" : ""}` : undefined}>
              {health ? <><StatusDot status="COMPLETED" /> Up {formatUptime(health.uptime_seconds)}</> : "Checking…"}
            </Row>
            <Row icon={Database} label="Database" description="SQLite">
              <StatusDot status={checks[0].state === "ok" ? "COMPLETED" : checks[0].state === "bad" ? "FAILED" : "PENDING"} />
              {checks[0].state === "bad" && health?.db?.error ? `Unreachable (${health.db.error})` : checks[0].detail}
            </Row>
            <Row icon={Container} label="Container runtime" description={runtimeInfo?.socket}>
              <StatusDot status={runtimeAvailable === true ? "COMPLETED" : "BLOCKED"} /> {runtimeLabel}
            </Row>
            <Row icon={Plug} label="Host daemon" description="How agents reach the host">
              {daemon ? <><StatusDot status={daemon.status === "ok" ? "COMPLETED" : "FAILED"} /> {daemon.connections} conn.{daemonUp !== null ? ` · up ${formatUptime(daemonUp)}` : ""}</> : noWorkspace ? "Read in a workspace" : "Checking…"}
            </Row>
            <SettingsRow label={<Label icon={HardDrive}>Disk</Label>} description={health?.disk?.path} border={false}>
              {/* The data volume is the one that fills in practice. A missing
                  measurement stays missing: "0 B free" would invent an emergency. */}
              {!health?.disk ? (
                <span className="text-[11px] text-muted-foreground">Not reported</span>
              ) : health.disk.error ? (
                <span className="text-[11px] text-muted-foreground">Unavailable — {health.disk.error}</span>
              ) : (
                <span className="flex items-center gap-2 text-[11px] text-muted-foreground">
                  <Bar pct={health.disk.used_pct ?? 0} className="hidden w-20 sm:block" />
                  <span className="font-mono tabular-nums text-foreground/80">{Math.round(health.disk.used_pct ?? 0)}%</span>
                  <span>{formatBytes(health.disk.free_bytes ?? 0)} free of {formatBytes(health.disk.total_bytes ?? 0)}</span>
                </span>
              )}
            </SettingsRow>
          </SettingsCard>

          {/* ── Integrity ── The instance's tamper-evidence and key custody. */}
          <SettingsCard icon={ShieldCheck} tint="var(--success)" title="Integrity" description="Tamper-evidence, key custody and the credential judge" className="h-full">
            <Row icon={ScrollText} label="Journal chain" description={journal?.checkpoints ? `${journal.checkpoints} checkpoints` : undefined}>
              {journalPending ? (
                <><Loader2 className="h-3 w-3 animate-spin" /> Verifying…</>
              ) : journal === null ? (
                "Not checked"
              ) : journalOK === false || journal.error ? (
                <span className="text-destructive"><StatusDot status="FAILED" /> {journal.error ?? "Verification failed"}</span>
              ) : (
                <><StatusDot status="COMPLETED" /> {(journalEntries ?? 0).toLocaleString()} entries verified</>
              )}
            </Row>
            <Row icon={KeyRound} label="Encryption key" description={keySourceNote(health?.encryption_key_source)}>
              <StatusDot status={health?.encryption_key_source === "generated" ? "BLOCKED" : health?.encryption_key_source ? "COMPLETED" : "PENDING"} />
              <span className={cn("whitespace-nowrap", health?.encryption_key_source === "generated" && "text-warn")}>{keySourceLabel(health?.encryption_key_source)}</span>
            </Row>
            <Row icon={Archive} label="Backups" description={noBackup ? "Nothing to restore from yet" : undefined}>
              {posture === null ? "Checking…" : noBackup ? <><StatusDot status="BLOCKED" /><span className="text-warn">None recorded</span></> : <><StatusDot status="COMPLETED" /> Recorded</>}
            </Row>
            <Row icon={ShieldCheck} label="Keeper" description={keeperHealth && keeperHealth.samples > 0 ? `p95 ${keeperHealth.p95_latency_ms} ms · ${keeperHealth.judge_failures} judge failures` : "Judges every credential read"}>
              {keeper === null ? "Checking…" : keeper.enabled ? <><StatusDot status="COMPLETED" /> On · {keeper.deny_count} denied of {keeper.total_requests}</> : <><StatusDot status="BLOCKED" /> Off</>}
            </Row>
            <SettingsRow label={<Label icon={Radio}>Telemetry</Label>} description="crewship telemetry on|off" border={false}>
              <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <StatusDot status={telemetry?.enabled ? "COMPLETED" : "PENDING"} />
                {telemetry === null ? "Unknown" : telemetry.enabled ? "Enabled" : "Disabled"}
              </span>
            </SettingsRow>
          </SettingsCard>
        </div>

        <div className="grid items-stretch gap-4 lg:grid-cols-2">
          {/* ── AI helpers ── The models behind the judge, the curator and the
              monitors, and whether each one answers. */}
          <SettingsCard icon={Bot} tint="var(--info)" title="AI helpers" description="The models behind the judge, reviews and monitors" className="h-full">
            {aux === null ? (
              <div className="space-y-2 p-4"><Skeleton className="h-4 w-full" /><Skeleton className="h-4 w-4/5" /></div>
            ) : aux.subsystems.length === 0 ? (
              <p className="px-4 py-4 text-[12px] text-muted-foreground">No helper models are configured.</p>
            ) : (
              aux.subsystems.map((s) => (
                <div key={s.id} className="flex items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0" data-slot="admin-aux">
                  <StatusDot status={s.healthy === false || s.reachable === false ? "FAILED" : s.reachable ? "COMPLETED" : "PENDING"} />
                  <span className="min-w-0 flex-1 truncate text-xs" title={s.label}>{s.label}</span>
                  <span className="max-w-[55%] shrink-0 truncate rounded-md border border-border bg-muted/40 px-1.5 py-0.5 font-mono text-[10.5px] text-muted-foreground" title={[s.provider, s.model].filter(Boolean).join(" · ")}>
                    {[s.provider, s.model].filter(Boolean).join(" · ") || "not set"}
                  </span>
                </div>
              ))
            )}
          </SettingsCard>

          {/* ── Licence ── */}
          <SettingsCard icon={BadgeCheck} title="License" description="Edition and what it permits" className="h-full">
            {license ? (
              <>
                <Row icon={BadgeCheck} label="Edition" description={license.licensee_org || undefined}>
                  <span className="font-medium text-foreground/90">{editionLabel(license.edition)}</span>
                </Row>
                <Row icon={Boxes} label="Crews"><Limit used={stats?.crews} limit={license.max_crews} /></Row>
                <Row icon={Users} label="Members"><Limit used={stats?.users} limit={license.max_members} /></Row>
                <Row icon={Bot} label="Agents per crew"><span className="font-mono">up to {license.max_agents_per_crew}</span></Row>
                <SettingsRow label={<Label icon={Sparkles}>Features</Label>} border={false}>
                  {license.features?.length ? (
                    <span className="flex flex-wrap justify-end gap-1">
                      {license.features.map((f) => (
                        <span key={f} className="rounded border border-border px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">{f}</span>
                      ))}
                    </span>
                  ) : (
                    <span className="text-[11px] text-muted-foreground">Core features</span>
                  )}
                </SettingsRow>
              </>
            ) : (
              <div className="space-y-2 p-4"><Skeleton className="h-4 w-full" /><Skeleton className="h-4 w-3/5" /></div>
            )}
          </SettingsCard>
        </div>
      </AppearStack>
    </div>
  )
})

// ── pieces ───────────────────────────────────────────────────────────────

function Label({ icon: Icon, children }: { icon: LucideIcon; children: React.ReactNode }) {
  return (
    <span className="inline-flex items-center gap-2">
      <Icon className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
      {children}
    </span>
  )
}

function Row({ icon, label, description, children }: { icon: LucideIcon; label: string; description?: string; children: React.ReactNode }) {
  return (
    <SettingsRow label={<Label icon={icon}>{label}</Label>} description={description}>
      <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">{children}</span>
    </SettingsRow>
  )
}

function Bar({ pct, className }: { pct: number; className?: string }) {
  const reduce = useReducedMotion()
  const p = Math.min(100, Math.max(0, pct))
  return (
    <span className={cn("block h-1.5 overflow-hidden rounded-full bg-muted", className)} aria-hidden>
      <motion.span
        className={cn("block h-full rounded-full", p >= 90 ? "bg-destructive" : p >= 75 ? "bg-warn" : "bg-primary")}
        initial={reduce ? false : { width: 0 }}
        animate={{ width: `${p}%` }}
        transition={{ duration: 0.6, ease: [0.2, 0.7, 0.2, 1] }}
      />
    </span>
  )
}

function Limit({ used, limit }: { used?: number; limit?: number }) {
  if (used === undefined) return <Skeleton className="h-3 w-16" />
  return (
    <span className={cn("flex items-center gap-2 font-mono text-[11px] tabular-nums", overLimit(used, limit) ? "text-destructive" : "text-foreground/85")}>
      {limit ? <Bar pct={(used / limit) * 100} className="w-20" /> : null}
      {against(used, limit)}
    </span>
  )
}

/** One capacity figure: an icon tile, the count against its ceiling, a bar. */
function Meter({ icon: Icon, tint, label, used, limit, note, tone, testId }: {
  icon: LucideIcon; tint: string; label: string; used?: number; limit?: number; note?: string; tone?: "bad" | "live"; testId?: string
}) {
  const over = overLimit(used ?? 0, limit)
  return (
    <div className="flex flex-col rounded-card border border-border bg-card px-4 py-3.5" data-slot="admin-meter">
      <div className="flex items-center gap-3">
        <span className="icon-tile grid h-9 w-9 shrink-0 place-items-center rounded-lg" style={{ "--ic": over || tone === "bad" ? "var(--destructive)" : tint } as CSSProperties} aria-hidden>
          <Icon className="h-4 w-4" />
        </span>
        <div className="min-w-0">
          <div className="eyebrow text-muted-foreground">{label}</div>
          {used === undefined ? (
            <Skeleton className="mt-1 h-6 w-16" />
          ) : (
            <div className={cn("font-mono text-xl font-semibold leading-7 tabular-nums", over || tone === "bad" ? "text-destructive" : tone === "live" ? "text-success" : "text-foreground")} data-testid={testId}>
              {against(used, limit)}
            </div>
          )}
        </div>
      </div>
      {/* The bar row stays (empty without a ceiling) so every tile's note
          sits on the same line. */}
      <div className="mt-3 flex h-4 items-center">
        {limit ? <Bar pct={((used ?? 0) / limit) * 100} className="w-full" /> : null}
      </div>
      <p className={cn("mt-1 truncate text-[11px]", over ? "text-destructive" : "text-muted-foreground")} title={note}>
        {note ?? (limit ? `${Math.max(0, limit - (used ?? 0))} left on this licence` : "\u00a0")}
      </p>
    </div>
  )
}

/** A posture finding: its first sentence as the line, the rest on demand. */
function Finding({ finding }: { finding: { key: string; severity: string; message: string } }) {
  const [open, setOpen] = React.useState(false)
  const high = finding.severity === "high"
  const medium = finding.severity === "medium"
  const tint = high ? "var(--destructive)" : medium ? "var(--warn)" : "var(--info)"
  const cut = finding.message.search(/\.\s/)
  const head = cut > 0 ? finding.message.slice(0, cut + 1) : finding.message
  const rest = cut > 0 ? finding.message.slice(cut + 2) : ""
  const action = FINDING_ACTIONS[finding.key]
  return (
    <div data-severity={finding.severity} className="flex items-start gap-3 border-b border-border/60 px-4 py-3 last:border-b-0">
      <span className="icon-tile mt-0.5 grid h-7 w-7 shrink-0 place-items-center rounded-lg" style={{ "--ic": tint } as CSSProperties} aria-hidden>
        {high || medium ? <AlertTriangle className="h-3.5 w-3.5" /> : <Info className="h-3.5 w-3.5" />}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <StatusPill tone={high ? "danger" : medium ? "warn" : "muted"} label={finding.severity.charAt(0).toUpperCase() + finding.severity.slice(1)} />
          <p className="min-w-0 flex-1 text-[12.5px] font-medium leading-snug text-foreground">{head}</p>
        </div>
        {rest && (
          <p className={cn("mt-1 text-[12px] leading-relaxed text-muted-foreground", !open && "line-clamp-1")}>{rest}</p>
        )}
        {rest && (
          <button type="button" onClick={() => setOpen(!open)} aria-expanded={open} className="mt-0.5 text-[11px] font-medium text-primary-hover hover:underline">
            {open ? "Less" : "Details"}
          </button>
        )}
      </div>
      {action && (
        <Link href={action.href} data-testid="admin-finding-action"
          className="inline-flex shrink-0 items-center gap-1 rounded-md border border-border px-2 py-1 text-[11px] font-medium text-foreground/90 transition-colors hover:border-primary/50 hover:text-primary-hover">
          {action.label} <ChevronRight className="h-3 w-3" />
        </Link>
      )}
    </div>
  )
}

function formatUsd(v: number): string {
  return v < 0.01 && v > 0 ? "<$0.01" : `$${v.toFixed(2)}`
}

const WEEKDAY = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]

/** Seven days of runs as bars, today on the right; fills the card's height. */
function RunBars({ runs, noWorkspace }: { runs: TimeseriesPoint[] | null; noWorkspace: boolean }) {
  const reduce = useReducedMotion()
  if (runs === null && noWorkspace) {
    return <p className="flex flex-1 items-center justify-center p-4 text-center text-[12px] text-muted-foreground">Runs are counted per workspace — open one to see its week.</p>
  }
  if (runs === null) return <div className="flex-1 p-4"><Skeleton className="h-full min-h-24 w-full" /></div>
  const max = Math.max(1, ...runs.map((r) => r.value))
  const total = runs.reduce((s, p) => s + p.value, 0)
  const cols = { gridTemplateColumns: `repeat(${Math.max(1, runs.length)}, minmax(0, 1fr))` }
  return (
    <div className="flex flex-1 flex-col px-4 pb-3 pt-4" data-slot="admin-run-bars">
      <div className="flex items-baseline gap-2">
        <span className="font-mono text-2xl font-semibold tabular-nums">{total}</span>
        <span className="text-[12px] text-muted-foreground">runs in the last 7 days</span>
      </div>
      <div className="relative mt-3 min-h-28 flex-1">
        <div className="absolute inset-0 grid items-end gap-2" style={cols}>
          {runs.map((r, i) => (
            <div key={r.ts} className="flex h-full flex-col justify-end" title={`${WEEKDAY[new Date(r.ts).getUTCDay()]} ${new Date(r.ts).getUTCDate()}: ${r.value} runs`}>
              {r.value > 0 && <span className="mb-1 text-center font-mono text-[10px] text-muted-foreground">{r.value}</span>}
              <motion.span
                className={cn("block rounded-[4px]", r.value ? "bg-primary/70" : "bg-border/70")}
                style={{ height: r.value ? `${Math.max(6, (r.value / max) * 80)}%` : 3, transformOrigin: "bottom" }}
                initial={reduce ? false : { scaleY: 0 }}
                animate={{ scaleY: 1 }}
                transition={{ duration: 0.45, delay: i * 0.04, ease: [0.2, 0.7, 0.2, 1] }}
              />
            </div>
          ))}
        </div>
      </div>
      <div className="mt-1.5 grid gap-2 text-center font-mono text-[10px] text-muted-foreground-soft" style={cols} aria-hidden>
        {runs.map((r) => <span key={r.ts}>{WEEKDAY[new Date(r.ts).getUTCDay()]}</span>)}
      </div>
    </div>
  )
}
