"use client"

import { Lock, CheckCircle2 } from "lucide-react"
import { useBackupStatus } from "@/hooks/use-backups"
import { cn } from "@/lib/utils"

/**
 * Tiny live indicator at the top of the backups tab. Poll interval is
 * set in the hook (5s) so the banner clears promptly after a backup
 * run finishes.
 */
export function BackupStatusBanner({ workspaceId }: { workspaceId: string | undefined }) {
  const { data, isLoading } = useBackupStatus(workspaceId)

  if (isLoading || !data) {
    return null
  }

  if (!data.held) {
    return (
      <div
        className={cn(
          "flex items-center gap-2 px-3 py-2 rounded-md border text-xs",
          "bg-green-500/5 border-green-500/20 text-green-300",
        )}
      >
        <CheckCircle2 className="h-3.5 w-3.5" />
        <span>Idle — no backup in progress</span>
      </div>
    )
  }

  return (
    <div
      role="status"
      aria-live="polite"
      className={cn(
        "flex items-center gap-2 px-3 py-2 rounded-md border text-xs",
        "bg-amber-500/5 border-amber-500/20 text-amber-300",
      )}
    >
      <Lock className="h-3.5 w-3.5" />
      <span>
        Backup in progress — locked by {data.acquired_by}
        {data.expires_at
          ? ` (expires ${new Date(data.expires_at).toLocaleTimeString()})`
          : ""}
      </span>
    </div>
  )
}
