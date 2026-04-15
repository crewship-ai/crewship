"use client"

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Skeleton } from "@/components/ui/skeleton"
import { useBackupStore } from "@/stores/backup-store"
import { useInspectBackup } from "@/hooks/use-backups"

export function BackupInspectPanel({ workspaceId }: { workspaceId: string | undefined }) {
  const dialog = useBackupStore((s) => s.dialog)
  const selectedPath = useBackupStore((s) => s.selectedPath)
  const close = useBackupStore((s) => s.close)
  const open = dialog === "inspect" && Boolean(selectedPath)
  const { data, isLoading, isError, error } = useInspectBackup(workspaceId, open ? selectedPath : null)

  return (
    <Dialog open={open} onOpenChange={(v) => !v && close()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>Inspect backup</DialogTitle>
          <DialogDescription className="font-mono text-xs">{selectedPath}</DialogDescription>
        </DialogHeader>
        {isLoading && <Skeleton className="h-40 rounded-md" />}
        {isError && (
          <div className="text-sm text-destructive">
            {error instanceof Error ? error.message : "Failed to inspect bundle"}
          </div>
        )}
        {data && (
          <dl className="grid grid-cols-[140px_1fr] gap-y-1.5 text-xs">
            <dt className="text-muted-foreground">Format</dt>
            <dd>v{data.format_version}</dd>
            <dt className="text-muted-foreground">Crewship</dt>
            <dd>{data.crewship_version_at_backup}</dd>
            <dt className="text-muted-foreground">Scope</dt>
            <dd>{data.scope}</dd>
            <dt className="text-muted-foreground">Created</dt>
            <dd>{new Date(data.created_at).toLocaleString()}</dd>
            <dt className="text-muted-foreground">Checksum</dt>
            <dd className="font-mono break-all">{data.checksums.payload_sha256}</dd>
            <dt className="text-muted-foreground">Encrypted</dt>
            <dd>
              {data.encryption.enabled
                ? `${data.encryption.algorithm ?? "age"} (${data.encryption.key_derivation ?? "recipient"})`
                : "no"}
            </dd>
            {data.contents.workspace && (
              <>
                <dt className="text-muted-foreground">Workspace</dt>
                <dd>
                  {data.contents.workspace.name} ({data.contents.workspace.slug})
                </dd>
              </>
            )}
            <dt className="text-muted-foreground">Crews</dt>
            <dd>
              {data.contents.crews.length === 0
                ? "—"
                : data.contents.crews.map((c) => (
                    <div key={c.id}>
                      {c.name} ({c.slug})
                      {c.agent_count !== undefined ? ` · ${c.agent_count} agents` : ""}
                    </div>
                  ))}
            </dd>
          </dl>
        )}
      </DialogContent>
    </Dialog>
  )
}
