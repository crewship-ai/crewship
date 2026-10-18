"use client"

import { AdminRouteGate } from "@/components/features/admin/admin-route-gate"
import { SecurityPage } from "@/components/features/admin/security/security-page"

export default function AdminSecurityPage() {
  return (
    <AdminRouteGate title="Security">
      <SecurityPage />
    </AdminRouteGate>
  )
}
