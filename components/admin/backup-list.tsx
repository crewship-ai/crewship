"use client"

import {
  CheckCircle2,
  Download,
  Eye,
  Lock,
  Loader2,
  RefreshCw,
  ShieldCheck,
  Trash2,
  Undo2,
  Unlock,
  XCircle,
} from "lucide-react"
import { motion } from "motion/react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  buildDownloadUrl,
  useBackups,
  useDeleteBackup,
  useVerifyBackup,
  type BackupListEntry,
} from "@/hooks/use-backups"
import { useBackupStore } from "@/stores/backup-store"

function formatBytes(n: number) {
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

export function BackupList({ workspaceId }: { workspaceId: string | undefined }) {
  const { data, isLoading, isError, refetch, isFetching } = useBackups(workspaceId)
  const del = useDeleteBackup(workspaceId)
  const verify = useVerifyBackup(workspaceId)
  const openRestore = useBackupStore((s) => s.openRestore)
  const openInspect = useBackupStore((s) => s.openInspect)

  if (isLoading) {
    return <Skeleton className="h-[200px] rounded-xl" />
  }

  if (isError) {
    return (
      <div className="p-4 rounded-md border border-destructive/30 bg-destructive/5 text-sm">
        Failed to list backups.{" "}
        <button className="underline" onClick={() => refetch()}>
          Retry
        </button>
      </div>
    )
  }

  const rows = data ?? []

  async function onDelete(entry: BackupListEntry) {
    if (!window.confirm(`Delete ${entry.file_name}? This cannot be undone.`)) return
    try {
      await del.mutateAsync(entry.path)
      toast.success("Backup deleted")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to delete backup")
    }
  }

  // Verify mutation reports outcome via toast — operators want a clear
  // pass/fail with the recomputed checksum visible. The mutation hook
  // does not auto-invalidate the list (verify is read-only) so the
  // surrounding state stays stable while the toast renders.
  //
  // Backend response is {valid, size_bytes, manifest, error}. valid=true
  // and error="" together mean the bundle's payload SHA matches the
  // manifest. Any other shape is a failure mode the operator needs to
  // see verbatim.
  async function onVerify(entry: BackupListEntry) {
    try {
      const result = await verify.mutateAsync(entry.path)
      if (result.valid && !result.error) {
        const checksum = result.manifest?.checksums?.payload_sha256 ?? ""
        toast.success(
          `Verified · ${entry.file_name}`,
          {
            description: checksum
              ? `sha256 ${checksum.replace(/^sha256:/, "").slice(0, 16)}… · ${formatBytes(result.size_bytes)}`
              : formatBytes(result.size_bytes),
          },
        )
      } else {
        toast.error(
          `Verify failed · ${entry.file_name}`,
          { description: result.error || "checksum mismatch" },
        )
      }
    } catch (err) {
      toast.error(
        `Verify failed · ${entry.file_name}`,
        { description: err instanceof Error ? err.message : "Unknown error" },
      )
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Button
          size="sm"
          variant="ghost"
          disabled={isFetching}
          onClick={() => refetch()}
          aria-label="Refresh backup list"
        >
          {isFetching ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <RefreshCw className="h-3.5 w-3.5" />
          )}
        </Button>
      </div>
      {rows.length === 0 ? (
        <div className="text-sm text-muted-foreground p-6 text-center border border-dashed rounded-md">
          No backups yet. Click <span className="font-medium">Create Backup</span> to make one.
        </div>
      ) : (
        <div className="rounded-md border overflow-hidden">
          <table className="w-full text-xs">
            <thead className="bg-muted/40 text-muted-foreground">
              <tr className="text-left">
                <th className="px-3 py-2 font-medium">File</th>
                <th className="px-3 py-2 font-medium">Scope</th>
                <th className="px-3 py-2 font-medium">Size</th>
                <th className="px-3 py-2 font-medium">Encryption</th>
                <th className="px-3 py-2 font-medium">Created</th>
                <th className="px-3 py-2 font-medium text-right">Actions</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row, i) => (
                <motion.tr
                  key={row.path}
                  // Stagger the rows in (recipe A6 from the cheat-sheet).
                  // Cap delay at 6 rows / 180ms total so a 50-bundle list
                  // does not animate for ~1.5s.
                  initial={{ opacity: 0, y: 4 }}
                  animate={{ opacity: 1, y: 0 }}
                  transition={{
                    duration: 0.16,
                    delay: Math.min(i, 6) * 0.03,
                    ease: [0.32, 0.72, 0, 1],
                  }}
                  className="border-t hover:bg-accent/20"
                >
                  <td className="px-3 py-2 font-mono text-[11px]">{row.file_name}</td>
                  <td className="px-3 py-2">
                    <Badge variant="outline" className="text-[10px] uppercase tracking-wider">
                      {row.scope}
                    </Badge>
                  </td>
                  <td className="px-3 py-2 font-mono">{formatBytes(row.size_bytes)}</td>
                  <td className="px-3 py-2">
                    {row.encrypted ? (
                      <span className="inline-flex items-center gap-1 text-emerald-500">
                        <Lock className="h-3 w-3" />
                        <span>encrypted</span>
                      </span>
                    ) : (
                      <span className="inline-flex items-center gap-1 text-amber-500">
                        <Unlock className="h-3 w-3" />
                        <span>plain</span>
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    {row.created_at ? new Date(row.created_at).toLocaleString() : "—"}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <div className="inline-flex gap-1">
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => openInspect(row.path)}
                        aria-label="Inspect manifest"
                        title="Inspect"
                      >
                        <Eye className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        // Disabled while a verify is in flight to keep the
                        // loading spinner anchored to the row that asked
                        // for it; clicking another row would otherwise let
                        // both toasts arrive in unpredictable order.
                        disabled={verify.isPending && verify.variables === row.path}
                        onClick={() => onVerify(row)}
                        aria-label="Verify checksum"
                        title="Verify integrity (recompute sha256)"
                      >
                        {verify.isPending && verify.variables === row.path ? (
                          <Loader2 className="h-3.5 w-3.5 animate-spin" />
                        ) : verify.data?.valid && verify.variables === row.path ? (
                          <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" />
                        ) : verify.data && !verify.data.valid && verify.variables === row.path ? (
                          <XCircle className="h-3.5 w-3.5 text-destructive" />
                        ) : (
                          <ShieldCheck className="h-3.5 w-3.5" />
                        )}
                      </Button>
                      <Button
                        asChild
                        size="sm"
                        variant="ghost"
                        aria-label="Download bundle"
                        title="Download .tar.zst"
                      >
                        {/*
                         * Anchor with download attribute streams the bundle
                         * straight to disk through the browser. We do NOT go
                         * through React Query because a multi-GB bundle
                         * would otherwise be slurped into memory before
                         * landing on disk.
                         */}
                        <a
                          href={workspaceId ? buildDownloadUrl(workspaceId, row.path) : "#"}
                          download={row.file_name}
                        >
                          <Download className="h-3.5 w-3.5" />
                        </a>
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => openRestore(row.path)}
                        aria-label="Restore from this bundle"
                        title="Restore"
                      >
                        <Undo2 className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={del.isPending}
                        onClick={() => onDelete(row)}
                        aria-label="Delete bundle"
                        title="Delete"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  </td>
                </motion.tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
