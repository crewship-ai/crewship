"use client"

import { motion } from "motion/react"
import { Activity, AlertTriangle, HardDrive, Timer, Lock } from "lucide-react"

import { useBackupMetrics } from "@/hooks/use-backups"
import { cn } from "@/lib/utils"

function formatBytes(n: number): string {
  if (!n || n < 1024) return `${n ?? 0} B`
  const units = ["KB", "MB", "GB", "TB"]
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

function formatSeconds(s: number): string {
  if (!s || s < 1) return "—"
  if (s < 60) return `${s.toFixed(1)}s`
  const m = Math.floor(s / 60)
  const rem = s % 60
  return rem >= 1 ? `${m}m ${rem.toFixed(0)}s` : `${m}m`
}

interface MetricsRowProps {
  workspaceId: string | undefined
}

/**
 * Process-wide backup counters. Backend gates this endpoint on the
 * CREWSHIP_INSTANCE_OWNER_EMAIL env var; in self-hosted multi-tenant
 * deployments the metrics show ALL workspaces' counters, so a
 * workspace owner can't see them by design.
 *
 * UX policy: render an explanatory chip when the gate refuses us
 * rather than silently no-op. Operators who DO have instance-owner
 * email get the full counter row; everyone else sees one line
 * explaining how to enable it.
 */
export function BackupMetricsRow({ workspaceId }: MetricsRowProps) {
  const { data, isLoading, isError, error } = useBackupMetrics(workspaceId)

  if (isLoading) return null

  if (isError) {
    // Surface the gate explicitly so a workspace owner who notices the
    // missing row understands WHY rather than assuming the feature is
    // broken. Other errors (network, 5xx) get a generic line.
    const msg = error instanceof Error ? error.message : ""
    const isGated = /instance owner/i.test(msg)
    return (
      <div className="px-3 py-2 rounded-md border border-border/60 bg-muted/30 text-[11px] text-muted-foreground flex items-center gap-2">
        <AlertTriangle className="h-3 w-3 shrink-0 opacity-60" />
        {isGated ? (
          <span>
            Process-wide backup metrics are visible only to the instance owner — set{" "}
            <code className="font-mono text-foreground/80">CREWSHIP_INSTANCE_OWNER_EMAIL</code>{" "}
            in the server's <code className="font-mono text-foreground/80">.env.local</code> to
            your email and restart.
          </span>
        ) : (
          <span>Couldn't load backup metrics: {msg || "unknown error"}</span>
        )}
      </div>
    )
  }

  if (!data) return null

  const stats = [
    {
      label: "Created",
      value: data.created_total.toLocaleString(),
      icon: Activity,
      tone: "text-emerald-500",
      sub: data.created_by_scope
        ? Object.entries(data.created_by_scope)
            .map(([k, v]) => `${k}=${v}`)
            .join(" ")
        : "",
    },
    {
      label: "Failed",
      value: data.failed_total.toLocaleString(),
      icon: AlertTriangle,
      tone: data.failed_total > 0 ? "text-amber-500" : "text-muted-foreground",
      sub: data.failed_by_reason
        ? Object.entries(data.failed_by_reason)
            .slice(0, 2)
            .map(([k, v]) => `${k}=${v}`)
            .join(" ")
        : "",
    },
    {
      label: "Restored",
      value: data.restored_total.toLocaleString(),
      icon: Lock,
      tone: "text-sky-400",
      sub: "",
    },
    {
      label: "Total bytes",
      value: formatBytes(data.size_bytes_total),
      icon: HardDrive,
      tone: "text-violet-400",
      sub: "",
    },
    {
      label: "Duration p50",
      value: formatSeconds(data.duration_seconds_p50),
      icon: Timer,
      tone: "text-muted-foreground",
      sub: data.duration_seconds_p95 ? `p95 ${formatSeconds(data.duration_seconds_p95)}` : "",
    },
  ]

  return (
    <div className="grid grid-cols-2 md:grid-cols-5 gap-2">
      {stats.map((s, i) => {
        const Icon = s.icon
        return (
          <motion.div
            key={s.label}
            initial={{ opacity: 0, y: 4 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.16, delay: i * 0.03, ease: [0.32, 0.72, 0, 1] }}
            className="px-3 py-2.5 rounded-lg bg-card border border-border/60 flex items-start gap-2.5"
          >
            <Icon className={cn("h-3.5 w-3.5 mt-0.5 shrink-0", s.tone)} />
            <div className="min-w-0">
              <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                {s.label}
              </div>
              <div className="text-sm font-mono mt-0.5 truncate">{s.value}</div>
              {s.sub ? (
                <div className="text-[10px] text-muted-foreground mt-0.5 font-mono truncate">
                  {s.sub}
                </div>
              ) : null}
            </div>
          </motion.div>
        )
      })}
    </div>
  )
}
