"use client"

import { Plus } from "lucide-react"

import { Button } from "@/components/ui/button"
import { BackupCreateDialog } from "@/components/admin/backup-create-dialog"
import { BackupInspectPanel } from "@/components/admin/backup-inspect-panel"
import { BackupList } from "@/components/admin/backup-list"
import { BackupMetricsRow } from "@/components/admin/backup-metrics-row"
import { BackupRestoreDialog } from "@/components/admin/backup-restore-dialog"
import { BackupRetentionCard } from "@/components/admin/backup-retention-card"
import { BackupSelfTestCard } from "@/components/admin/backup-self-test-card"
import { BackupStatusBanner } from "@/components/admin/backup-status-banner"
import { useBackupStore } from "@/stores/backup-store"

export function BackupsTab({ workspaceId }: { workspaceId: string | undefined }) {
  const openCreate = useBackupStore((s) => s.openCreate)
  return (
    <div className="space-y-4">
      {/*
       * No local "Backups" header — admin/page.tsx already renders the
       * activeItem header (icon + name) above renderContent(). Dropping
       * this duplicate also restores the Create button position to the
       * top-right of the section, matching every other admin tab.
       */}
      <div className="flex items-center justify-end">
        <Button size="sm" onClick={openCreate} data-testid="backup-create-btn">
          <Plus className="h-3.5 w-3.5 mr-1" />
          Create Backup
        </Button>
      </div>

      {/*
       * Status banner sits above metrics: a stuck-lock alert is the
       * highest-priority signal — operators see it before they see the
       * KPI counters and can act immediately.
       */}
      <BackupStatusBanner workspaceId={workspaceId} />

      {/*
       * Lightweight live metrics row. Hidden until first load completes
       * so we don't flash empty placeholders. Polls 30s.
       */}
      <BackupMetricsRow workspaceId={workspaceId} />

      {/* Bundle list — primary content. */}
      <BackupList workspaceId={workspaceId} />

      {/*
       * Operations cards below the list — these are advanced / less
       * frequent actions. Order chosen by frequency-of-use:
       *  - Retention: occasional (operators trim disk weekly/monthly)
       *  - Self-test: quarterly cadence per Supabase backup playbook
       */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 pt-2">
        <BackupRetentionCard workspaceId={workspaceId} />
        <BackupSelfTestCard workspaceId={workspaceId} />
      </div>

      {/* Dialogs / panels mounted last so they overlay everything else. */}
      <BackupCreateDialog workspaceId={workspaceId} />
      <BackupRestoreDialog workspaceId={workspaceId} />
      <BackupInspectPanel workspaceId={workspaceId} />
    </div>
  )
}
