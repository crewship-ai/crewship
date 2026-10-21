"use client"

import { AdminRouteGate } from "@/components/features/admin/admin-route-gate"
import { BackupsPage } from "@/components/features/admin/backups/backups-page"

export default function AdminBackupsPage() {
  return (
    <AdminRouteGate title="Backups">
      <BackupsPage />
    </AdminRouteGate>
  )
}
