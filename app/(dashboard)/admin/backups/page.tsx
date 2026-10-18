"use client"

import { BackupsPage } from "@/components/features/admin/backups/backups-page"
import { InstanceAdminGate } from "@/components/features/admin/instance-admin-gate"

export default function AdminBackupsPage() {
  return (
    <InstanceAdminGate area="Backups">
      <BackupsPage />
    </InstanceAdminGate>
  )
}
