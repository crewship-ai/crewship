import { Bot, Hourglass, Key, Activity, Plus } from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { StatCard } from "@/components/layout/stat-card"
import { FilterBar } from "@/components/layout/filter-bar"
import Link from "next/link"

export default function DashboardPage() {
  return (
    <div className="p-6 space-y-6">
      <PageHeader title="Dashboard" description="Overview of your AI workforce">
        <Button asChild>
          <Link href="/agents/new">
            <Plus className="mr-2 h-4 w-4" />
            New Agent
          </Link>
        </Button>
      </PageHeader>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
        <StatCard title="Total Agents" value={0} subtitle="No agents yet" icon={Bot} iconClassName="bg-primary/10 text-primary" />
        <StatCard title="Running Now" value={0} subtitle="of 0 agents" icon={Activity} iconClassName="bg-emerald-500/10 text-emerald-600" />
        <StatCard title="Today's Runs" value={0} subtitle="No runs today" icon={Hourglass} />
        <StatCard title="API Keys Active" value={0} subtitle="Add credentials to get started" icon={Key} />
      </div>

      <div>
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-base font-semibold">All Agents</h2>
          <FilterBar filters={["All", "Running", "Idle", "Error"]} />
        </div>

        <EmptyState
          icon={Bot}
          title="No agents yet"
          description="Create your first AI agent to start automating tasks. Agents work in teams and can chat, run tasks, and produce files."
        >
          <Button className="mt-4" asChild>
            <Link href="/agents/new">
              <Plus className="mr-2 h-4 w-4" />
              Create First Agent
            </Link>
          </Button>
        </EmptyState>
      </div>
    </div>
  )
}
