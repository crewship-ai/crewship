"use client"

import { useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { Loader2, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import {
  useRotateBackups,
  type BackupScope,
  type RotateBackupResponse,
} from "@/hooks/use-backups"

interface RetentionCardProps {
  workspaceId: string | undefined
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ["KB", "MB", "GB", "TB"]
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

/**
 * Manual retention card — applies the rotate policy on demand. Acts
 * as a UI-side complement to the auto-scheduling backend (which lives
 * in scheduled_jobs / gocron); operators who haven't yet set up an
 * automatic schedule can still tidy up old bundles from this card.
 *
 * Defaults (keep_last=14, keep_days=30) match the conservative side of
 * Coolify's recommendations: a fortnight of daily snapshots PLUS keep
 * anything from the last month so a rare weekly cadence isn't pruned
 * on the day it switches.
 *
 * Dry-run mode shows what WOULD be deleted without touching disk —
 * surfaced first as a "Preview rotation" button to encourage operators
 * to look before committing. The actual Apply requires the Preview to
 * have been viewed at least once in the current session (state lives
 * in `previewSeen`); this prevents accidental destructive clicks
 * without being heavy-handed about it.
 */
export function BackupRetentionCard({ workspaceId }: RetentionCardProps) {
  const rotate = useRotateBackups(workspaceId)

  const [keepLast, setKeepLast] = useState<number>(14)
  const [keepDays, setKeepDays] = useState<number>(30)
  const [scope, setScope] = useState<"all" | BackupScope>("all")
  const [previewSeen, setPreviewSeen] = useState(false)
  const [lastResult, setLastResult] = useState<RotateBackupResponse | null>(null)

  async function run(dry: boolean) {
    try {
      const res = await rotate.mutateAsync({
        scope: scope === "all" ? undefined : scope,
        keep_last: keepLast,
        keep_days: keepDays,
        dry_run: dry,
      })
      setLastResult(res)
      if (dry) {
        setPreviewSeen(true)
        toast.success(
          `Preview · ${res.deleted.length} bundle(s) eligible`,
          { description: `Would reclaim ${formatBytes(res.bytes_reclaimed)}` },
        )
      } else {
        toast.success(
          `Rotation applied · ${res.deleted.length} deleted`,
          { description: `Reclaimed ${formatBytes(res.bytes_reclaimed)}` },
        )
        // Apply consumed the preview consent — require a fresh look
        // before the next destructive run.
        setPreviewSeen(false)
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Rotation failed")
    }
  }

  return (
    <div className="rounded-lg border bg-card overflow-hidden">
      <div className="px-4 py-2.5 border-b border-border/60 flex items-center gap-3">
        <Trash2 className="h-3.5 w-3.5 text-muted-foreground" />
        <h3 className="text-sm font-semibold">Retention &amp; rotation</h3>
        <span className="ml-auto text-xs text-muted-foreground">
          Manual cleanup of old bundles
        </span>
      </div>
      <div className="p-4 space-y-4">
        <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
          <div className="space-y-1.5">
            <Label htmlFor="retention-keep-last" className="text-xs">
              Keep last (count)
            </Label>
            <Input
              id="retention-keep-last"
              type="number"
              min={1}
              max={9999}
              value={keepLast}
              onChange={(e) => setKeepLast(Math.max(1, Number(e.target.value) || 1))}
            />
            <p className="text-[11px] text-muted-foreground">
              Always keep N most recent bundles
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="retention-keep-days" className="text-xs">
              Keep days
            </Label>
            <Input
              id="retention-keep-days"
              type="number"
              min={1}
              max={3650}
              value={keepDays}
              onChange={(e) => setKeepDays(Math.max(1, Number(e.target.value) || 1))}
            />
            <p className="text-[11px] text-muted-foreground">
              Bundles older than N days are eligible
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="retention-scope" className="text-xs">
              Scope filter
            </Label>
            <select
              id="retention-scope"
              value={scope}
              onChange={(e) => setScope(e.target.value as typeof scope)}
              className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm"
            >
              <option value="all">All bundles</option>
              <option value="workspace">Workspace only</option>
              <option value="crew">Crew only</option>
            </select>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-3 pt-1">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={rotate.isPending}
            onClick={() => run(true)}
          >
            {rotate.isPending && rotate.variables?.dry_run ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin mr-1" />
            ) : null}
            Preview rotation
          </Button>
          <Button
            type="button"
            size="sm"
            variant="destructive"
            disabled={!previewSeen || rotate.isPending}
            onClick={() => run(false)}
            title={
              previewSeen
                ? "Apply rotation policy (deletes eligible bundles)"
                : "Run Preview first to confirm what will be deleted"
            }
          >
            {rotate.isPending && !rotate.variables?.dry_run ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin mr-1" />
            ) : null}
            Apply
          </Button>
          <span className="ml-auto inline-flex items-center gap-2 text-[11px] text-muted-foreground">
            <Switch
              id="retention-disclosure"
              checked
              disabled
              aria-label="Always-on disclosure indicator"
            />
            <span>Apply enabled after Preview</span>
          </span>
        </div>

        <AnimatePresence>
          {lastResult && (
            <motion.div
              key="rotate-result"
              initial={{ opacity: 0, height: 0 }}
              animate={{ opacity: 1, height: "auto" }}
              exit={{ opacity: 0, height: 0 }}
              transition={{ duration: 0.2, ease: "easeOut" }}
              className="overflow-hidden"
            >
              <div className="rounded-md border border-border/60 bg-muted/30 p-3 text-xs space-y-2">
                <div className="flex items-center gap-2">
                  <span className="text-[10px] uppercase tracking-wider font-semibold text-muted-foreground">
                    {lastResult.dry_run ? "Preview" : "Applied"}
                  </span>
                  <span className="text-foreground/80">
                    {lastResult.scanned} bundles scanned ·{" "}
                    {lastResult.deleted.length} {lastResult.dry_run ? "eligible" : "deleted"} ·{" "}
                    {formatBytes(lastResult.bytes_reclaimed)} reclaimed
                  </span>
                </div>
                {lastResult.deleted.length > 0 ? (
                  <ul className="font-mono text-[11px] text-muted-foreground space-y-0.5 max-h-40 overflow-y-auto">
                    {lastResult.deleted.map((d) => (
                      <li key={d.path} className="flex justify-between gap-3">
                        <span className="truncate">− {d.path.split("/").pop() ?? d.path}</span>
                        <span className="shrink-0 text-foreground/60">
                          {d.age_days}d · {formatBytes(d.size_bytes)}
                        </span>
                      </li>
                    ))}
                  </ul>
                ) : (
                  <div className="text-[11px] text-muted-foreground">
                    Nothing to delete. Retention thresholds keep all current bundles.
                  </div>
                )}
              </div>
            </motion.div>
          )}
        </AnimatePresence>
      </div>
    </div>
  )
}
