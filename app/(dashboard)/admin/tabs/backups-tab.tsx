"use client"

import { Plus, Database } from "lucide-react"

import { Button } from "@/components/ui/button"
import { BackupList } from "@/components/admin/backup-list"
import { BackupCreateDialog } from "@/components/admin/backup-create-dialog"
import { BackupRestoreDialog } from "@/components/admin/backup-restore-dialog"
import { BackupInspectPanel } from "@/components/admin/backup-inspect-panel"
import { BackupStatusBanner } from "@/components/admin/backup-status-banner"
import { useBackupStore } from "@/stores/backup-store"

export function BackupsTab({ workspaceId }: { workspaceId: string | undefined }) {
  const openCreate = useBackupStore((s) => s.openCreate)
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Database className="h-4 w-4 text-muted-foreground" />
          <h2 className="text-sm font-semibold">Backups</h2>
        </div>
        <Button size="sm" onClick={openCreate} data-testid="backup-create-btn">
          <Plus className="h-3.5 w-3.5 mr-1" />
          Create Backup
        </Button>
      </div>
      <BackupStatusBanner workspaceId={workspaceId} />
      <BackupList workspaceId={workspaceId} />
      <BackupCreateDialog workspaceId={workspaceId} />
      <BackupRestoreDialog workspaceId={workspaceId} />
      <BackupInspectPanel workspaceId={workspaceId} />
    </div>
  )
}
