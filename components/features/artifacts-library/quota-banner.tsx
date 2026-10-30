"use client"

import { Gauge } from "lucide-react"
import type { ReactNode } from "react"
import { Button } from "@/components/ui/button"
import { fmtSize } from "@/lib/file-format"
import { cn } from "@/lib/utils"

/** History quota as the artifact snapshot reports it (`quota` in the response). */
export interface ArtifactHistoryQuota {
  used_bytes: number
  limit_bytes: number
  warning?: boolean
  /** none < near (80%) < critical (95%) < full; absent when the quota is withheld. */
  level?: string
  retention?: string
}

/** Owner-only dry run of the proposed history rule (`retention_preview`). */
export interface ArtifactRetentionPreview {
  policy: string
  recent_days: number
  dry_run: boolean
  would_thin: number[]
}

export type QuotaLevel = "none" | "near" | "critical" | "full"

const NEAR_PERCENT = 80
const CRITICAL_PERCENT = 95

/**
 * The level to show. The server's `level` wins; older servers only send
 * `warning`, so the level is derived from the same thresholds. A withheld
 * quota (limit 0, shared readers) has no level and shows nothing.
 */
export function quotaLevel(quota: ArtifactHistoryQuota | null | undefined): QuotaLevel | null {
  if (!quota || !(quota.limit_bytes > 0)) return null
  if (quota.level === "none" || quota.level === "near" || quota.level === "critical" || quota.level === "full") return quota.level
  const used = Math.max(0, quota.used_bytes)
  if (used >= quota.limit_bytes) return "full"
  if (used * 100 >= quota.limit_bytes * CRITICAL_PERCENT) return "critical"
  if (used * 100 >= quota.limit_bytes * NEAR_PERCENT) return "near"
  return "none"
}

const copy: Record<Exclude<QuotaLevel, "none">, { title: string; body: string }> = {
  near: { title: "History is getting full", body: "Saving still works. Review older versions before it fills up." },
  critical: { title: "History is almost full", body: "A few more saves may fail. Review older versions now." },
  full: { title: "History is full", body: "New versions cannot be saved. Your work and every existing version are kept." },
}

/**
 * Warns ahead of a full artifact history: near from 80%, critical from 95%,
 * full at the limit. History is never deleted by this banner; a retention
 * preview is shown only as what a rule would thin out.
 */
export function QuotaBanner({ quota, retentionPreview, onReviewVersions, className }: {
  quota: ArtifactHistoryQuota | null | undefined
  retentionPreview?: ArtifactRetentionPreview | null
  onReviewVersions?: () => void
  className?: string
}): ReactNode {
  const level = quotaLevel(quota)
  if (!quota || !level || level === "none") return null
  const pct = Math.min(100, Math.floor(Math.max(0, quota.used_bytes) / quota.limit_bytes * 100))
  const severe = level !== "near"
  const thin = retentionPreview?.dry_run ? retentionPreview.would_thin.length : 0
  return <div role={severe ? "alert" : "status"} data-level={level}
    className={cn("flex flex-col gap-1.5 border-b px-4 py-2 text-xs", severe ? "bg-destructive/10 text-destructive" : "bg-warn/10", className)}>
    <div className="flex items-center gap-2">
      <Gauge className="size-3.5 shrink-0" aria-hidden />
      <strong className="font-medium">{copy[level].title}</strong>
      <span className="text-muted-foreground">{fmtSize(quota.used_bytes)} of {fmtSize(quota.limit_bytes)} ({pct}%)</span>
      {onReviewVersions && <Button type="button" size="sm" variant="secondary" className="ml-auto h-7 shrink-0" onClick={onReviewVersions}>Review versions</Button>}
    </div>
    <span>{copy[level].body} Nothing in the history is deleted without you.</span>
    {thin > 0 && <span className="text-muted-foreground">
      Keeping the last {retentionPreview!.recent_days} days in full and one version per older day would free {thin} {thin === 1 ? "version" : "versions"}. This is only a preview.
    </span>}
    <div role="progressbar" aria-label="History storage used" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} className="h-1.5 overflow-hidden rounded-full bg-background/60">
      <div className={cn("h-full rounded-full", severe ? "bg-destructive" : "bg-warn")} style={{ width: `${pct}%` }} />
    </div>
  </div>
}
