"use client"

import { AdminRouteGate } from "@/components/features/admin/admin-route-gate"
import { PeoplePage } from "@/components/features/admin/people/people-page"

export default function AdminPeoplePage() {
  return (
    <AdminRouteGate title="People">
      <PeoplePage />
    </AdminRouteGate>
  )
}
