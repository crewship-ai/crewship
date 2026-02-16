import { Shield } from "lucide-react"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"

export default function AuditPage() {
  return (
    <div className="p-6 space-y-6">
      <PageHeader title="Audit Log" description="Track all actions in your organization" />

      <EmptyState
        icon={Shield}
        title="No activity yet"
        description="All state-changing actions will be logged here with who, what, and when."
      />
    </div>
  )
}
