"use client"

import { motion } from "motion/react"
import { Activity, AlertTriangle, HardDrive, Clock } from "lucide-react"

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

function formatRelative(iso: string | undefined): string {
  if (!iso) return "—"
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return "—"
  const delta = Math.max(0, Date.now() - t)
  const min = Math.floor(delta / 60_000)
  if (min < 1) return "just now"
  if (min < 60) return `${min}m ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr}h ago`
  const d = Math.floor(hr / 24)
  return `${d}d ago`
}

interface MetricsRowProps {
  workspaceId: string | undefined
}

/**
 * Live metrics row at the top of the Backups tab. Polled every 30s
 * (configured in the hook) so an admin who leaves the tab open sees
 * the success/fail counters tick up after each scheduled run finishes.
 *
 * Designed to silently render zeros if the metrics endpoint is missing
 * (older self-hosted servers): the row never throws, never blocks the
 * rest of the tab. The endpoint is so cheap that the polling cost is
 * negligible even on a 30s interval.
 */
export function BackupMetricsRow({ workspaceId }: MetricsRowProps) {
  const { data, isLoading, isError } = useBackupMetrics(workspaceId)

  // Hidden until first load completes — saves a flash of empty placeholders
  // and avoids competing for attention with the status banner above us.
  if (isLoading) return null
  if (isError || !data) return null

  // The four metrics are colour-coded by category (success/failure/disk/age)
  // so the operator can scan in O(1). Tailwind colours match the rest of
  // the admin palette (emerald = good, amber = warn, sky = neutral info).
  const stats = [
    {
      label: "Successful (24h)",
      value: data.successes_24h,
      icon: Activity,
      tone: "text-emerald-500",
    },
    {
      label: "Failed (24h)",
      value: data.failures_24h,
      icon: AlertTriangle,
      tone: data.failures_24h > 0 ? "text-amber-500" : "text-muted-foreground",
    },
    {
      label: "Bundles on disk",
      value: `${data.total_bundles} · ${formatBytes(data.total_size_bytes)}`,
      icon: HardDrive,
      tone: "text-sky-400",
    },
    {
      label: "Latest backup",
      value: formatRelative(data.newest_at),
      icon: Clock,
      tone: "text-muted-foreground",
    },
  ]

  return (
    <div className="grid grid-cols-2 md:grid-cols-4 gap-2">
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
            </div>
          </motion.div>
        )
      })}
    </div>
  )
}
