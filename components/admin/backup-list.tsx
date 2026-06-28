"use client"

import {
  CheckCircle2,
  Download,
  Eye,
  Lock,
  RefreshCw,
  ShieldCheck,
  Trash2,
  Undo2,
  Unlock,
  XCircle,
} from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
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

// presetMeta keeps the Preset badge styling co-located with the
// labels. Tailwind colour classes match the visual hierarchy:
// Quick (light) → Standard (mid) → Full (strong).
const presetMeta: Record<
  string,
  { label: string; className: string; title: string }
> = {
  quick: {
    label: "Quick",
    className: "border-sky-500/40 text-sky-500",
    title: "Workspace + agent memory only",
  },
  standard: {
    label: "Standard",
    className: "border-emerald-500/40 text-emerald-500",
    title: "Quick + /home/agent + /opt/crew-tools",
  },
  full: {
    label: "Full",
    className: "border-violet-500/40 text-violet-500",
    title: "Standard + /var/lib (in-container service data)",
  },
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
            <Spinner className="h-3.5 w-3.5" />
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
                <th className="px-3 py-2 font-medium">Preset</th>
                <th className="px-3 py-2 font-medium">Size</th>
                <th className="px-3 py-2 font-medium">Encryption</th>
                <th className="px-3 py-2 font-medium">Created</th>
                <th className="px-3 py-2 font-medium text-right">Actions</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr
                  key={row.path}
                  className="border-t hover:bg-accent/20"
                >
                  <td className="px-3 py-2 font-mono text-[11px]">{row.file_name}</td>
                  <td className="px-3 py-2">
                    <Badge variant="outline" className="text-[10px] uppercase tracking-wider">
                      {row.scope}
                    </Badge>
                  </td>
                  <td className="px-3 py-2">
                    {(() => {
                      // Server omits scope_level on bundles produced before the
                      // preset feature shipped. Fall back to "standard" — that
                      // matches what the catalog migration backfills and what
                      // the collector did historically.
                      const lvl = row.scope_level ?? "standard"
                      const meta = presetMeta[lvl] ?? presetMeta.standard
                      return (
                        <Badge
                          variant="outline"
                          title={meta.title}
                          className={cn(
                            "text-[10px] uppercase tracking-wider",
                            meta.className,
                          )}
                        >
                          {meta.label}
                        </Badge>
                      )
                    })()}
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
                        // Disabled across ALL rows while a verify is
                        // in flight. Per-row guarding (the previous
                        // approach) lets the user click a different
                        // row mid-flight, kicking off a second mutation
                        // whose toast can interleave with the first.
                        // The mutation hook serialises by design when
                        // disabled is global.
                        disabled={verify.isPending}
                        onClick={() => onVerify(row)}
                        aria-label="Verify checksum"
                        title="Verify integrity (recompute sha256)"
                      >
                        {(() => {
                          // Match the predicate used by onVerify so the
                          // row icon and the toast cannot disagree —
                          // success means valid=true AND no error
                          // string. A non-empty error with valid=true
                          // (defensive: shouldn't happen but the API
                          // shape allows it) still flips the row to
                          // failure and matches the toast.
                          const ours =
                            verify.data && verify.variables === row.path
                          if (verify.isPending && verify.variables === row.path) {
                            return <Spinner className="h-3.5 w-3.5" />
                          }
                          if (ours && verify.data!.valid && !verify.data!.error) {
                            return <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" />
                          }
                          if (ours && (!verify.data!.valid || Boolean(verify.data!.error))) {
                            return <XCircle className="h-3.5 w-3.5 text-destructive" />
                          }
                          return <ShieldCheck className="h-3.5 w-3.5" />
                        })()}
                      </Button>
                      {workspaceId ? (
                        <Button
                          asChild
                          size="sm"
                          variant="ghost"
                          aria-label="Download bundle"
                          title="Download .tar.zst"
                        >
                          {/*
                           * Anchor with download attribute streams the
                           * bundle straight to disk through the browser.
                           * We do NOT go through React Query because a
                           * multi-GB bundle would otherwise be slurped
                           * into memory before landing on disk.
                           */}
                          <a
                            href={buildDownloadUrl(workspaceId, row.path)}
                            download={row.file_name}
                          >
                            <Download className="h-3.5 w-3.5" />
                          </a>
                        </Button>
                      ) : (
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled
                          aria-label="Download unavailable"
                          title="Select a workspace first"
                        >
                          <Download className="h-3.5 w-3.5" />
                        </Button>
                      )}
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
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
