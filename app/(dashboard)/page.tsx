import { Bot, Hourglass, Key, Activity, Plus } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import Link from "next/link"

function StatCard({
  title,
  value,
  subtitle,
  icon: Icon,
  iconClassName,
}: {
  title: string
  value: string | number
  subtitle: string
  icon: React.ElementType
  iconClassName?: string
}) {
  return (
    <Card>
      <CardContent className="p-5">
        <div className="flex items-center justify-between">
          <div className="text-xs text-muted-foreground uppercase tracking-wide font-medium">{title}</div>
          <div className={`flex h-8 w-8 items-center justify-center rounded-lg ${iconClassName ?? "bg-muted"}`}>
            <Icon className="h-4 w-4" />
          </div>
        </div>
        <div className="mt-1 text-3xl font-bold">{value}</div>
        <div className="mt-1 text-xs text-muted-foreground">{subtitle}</div>
      </CardContent>
    </Card>
  )
}

export default function DashboardPage() {
  return (
    <div className="p-6 space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-lg font-semibold">Dashboard</h1>
          <p className="text-sm text-muted-foreground">Overview of your AI workforce</p>
        </div>
        <Button asChild>
          <Link href="/agents/new">
            <Plus className="mr-2 h-4 w-4" />
            New Agent
          </Link>
        </Button>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
        <StatCard
          title="Total Agents"
          value={0}
          subtitle="No agents yet"
          icon={Bot}
          iconClassName="bg-primary/10 text-primary"
        />
        <StatCard
          title="Running Now"
          value={0}
          subtitle="of 0 agents"
          icon={Activity}
          iconClassName="bg-emerald-500/10 text-emerald-600"
        />
        <StatCard
          title="Today's Runs"
          value={0}
          subtitle="No runs today"
          icon={Hourglass}
        />
        <StatCard
          title="API Keys Active"
          value={0}
          subtitle="Add credentials to get started"
          icon={Key}
        />
      </div>

      <div>
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-base font-semibold">All Agents</h2>
          <div className="flex items-center gap-2">
            <Badge variant="secondary" className="cursor-pointer">All</Badge>
            <Badge variant="outline" className="cursor-pointer">Running</Badge>
            <Badge variant="outline" className="cursor-pointer">Idle</Badge>
            <Badge variant="outline" className="cursor-pointer">Error</Badge>
          </div>
        </div>

        <Card>
          <CardContent className="flex flex-col items-center justify-center py-16 text-center">
            <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-muted mb-4">
              <Bot className="h-6 w-6 text-muted-foreground" />
            </div>
            <h3 className="text-sm font-semibold">No agents yet</h3>
            <p className="mt-1 text-sm text-muted-foreground max-w-sm">
              Create your first AI agent to start automating tasks. Agents work in teams and can chat, run tasks, and produce files.
            </p>
            <Button className="mt-4" asChild>
              <Link href="/agents/new">
                <Plus className="mr-2 h-4 w-4" />
                Create First Agent
              </Link>
            </Button>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
