"use client"

import { Eye, RefreshCw, Trash2, Undo2, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useBackups, useDeleteBackup, type BackupListEntry } from "@/hooks/use-backups"
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
      toast.error((err as Error).message)
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
          {isFetching ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />}
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
                <th className="px-3 py-2 font-medium">Encrypted</th>
                <th className="px-3 py-2 font-medium">Created</th>
                <th className="px-3 py-2 font-medium text-right">Actions</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.path} className="border-t hover:bg-accent/20">
                  <td className="px-3 py-2 font-mono text-[11px]">{row.file_name}</td>
                  <td className="px-3 py-2">{row.scope}</td>
                  <td className="px-3 py-2">{formatBytes(row.size_bytes)}</td>
                  <td className="px-3 py-2">{row.encrypted ? "yes" : "no"}</td>
                  <td className="px-3 py-2">
                    {row.created_at ? new Date(row.created_at).toLocaleString() : "—"}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <div className="inline-flex gap-1">
                      <Button size="sm" variant="ghost" onClick={() => openInspect(row.path)} aria-label="Inspect">
                        <Eye className="h-3.5 w-3.5" />
                      </Button>
                      <Button size="sm" variant="ghost" onClick={() => openRestore(row.path)} aria-label="Restore">
                        <Undo2 className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={del.isPending}
                        onClick={() => onDelete(row)}
                        aria-label="Delete"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
