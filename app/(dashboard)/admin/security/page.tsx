"use client"

import { SecurityPage } from "@/components/features/admin/security/security-page"
import { InstanceAdminGate } from "@/components/features/admin/instance-admin-gate"

export default function AdminSecurityPage() {
  return (
    <InstanceAdminGate area="Security">
      <SecurityPage />
    </InstanceAdminGate>
  )
}
