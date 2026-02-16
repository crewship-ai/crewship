import { Bot, Plus, Filter } from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { FilterBar } from "@/components/layout/filter-bar"
import Link from "next/link"

export default function AgentsPage() {
  return (
    <div className="p-6 space-y-6">
      <PageHeader title="Agents" description="Manage your AI virtual employees">
        <Button variant="outline" size="sm">
          <Filter className="mr-2 h-4 w-4" />
          Filter
        </Button>
        <Button asChild>
          <Link href="/agents/new">
            <Plus className="mr-2 h-4 w-4" />
            New Agent
          </Link>
        </Button>
      </PageHeader>

      <FilterBar filters={["All", "Running", "Idle", "Error", "Stopped"]} />

      <EmptyState
        icon={Bot}
        title="No agents yet"
        description="Create your first AI agent to start automating tasks."
      >
        <Button className="mt-4" asChild>
          <Link href="/agents/new">
            <Plus className="mr-2 h-4 w-4" />
            Create Agent
          </Link>
        </Button>
      </EmptyState>
    </div>
  )
}
