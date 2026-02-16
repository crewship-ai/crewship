import { Users, Plus } from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import Link from "next/link"

export default function TeamsPage() {
  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Teams" description="Organize agents into departments">
        <Button asChild>
          <Link href="/teams/new">
            <Plus className="mr-2 h-4 w-4" />
            New Team
          </Link>
        </Button>
      </PageHeader>

      <EmptyState
        icon={Users}
        title="No teams yet"
        description="Create a team to group your agents by department or function."
      >
        <Button className="mt-4" asChild>
          <Link href="/teams/new">
            <Plus className="mr-2 h-4 w-4" />
            Create Team
          </Link>
        </Button>
      </EmptyState>
    </div>
  )
}
