"use client"

import { PeoplePage } from "@/components/features/admin/people/people-page"
import { InstanceAdminGate } from "@/components/features/admin/instance-admin-gate"

export default function AdminPeoplePage() {
  return (
    <InstanceAdminGate area="People & workspaces">
      <PeoplePage />
    </InstanceAdminGate>
  )
}
