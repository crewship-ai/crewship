import { Network } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"

export default function CrewsPage() {
  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Crews" description="AI team orchestration with Crew Leaders and Virtual Director" />

      <EmptyState
        icon={Network}
        title="Orchestration (Phase 2)"
        description="Crew Leaders delegate tasks to Workers. Virtual Director coordinates across teams. Available after MVP."
      >
        <Badge variant="outline" className="mt-4">Coming Soon</Badge>
      </EmptyState>
    </div>
  )
}
