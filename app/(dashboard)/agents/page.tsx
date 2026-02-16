import { Bot, Plus, Filter } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import Link from "next/link"

export default function AgentsPage() {
  return (
    <div className="p-6 space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-lg font-semibold">Agents</h1>
          <p className="text-sm text-muted-foreground">Manage your AI virtual employees</p>
        </div>
        <div className="flex items-center gap-2">
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
        </div>
      </div>

      <div className="flex items-center gap-2">
        <Badge variant="secondary" className="cursor-pointer">All</Badge>
        <Badge variant="outline" className="cursor-pointer">Running</Badge>
        <Badge variant="outline" className="cursor-pointer">Idle</Badge>
        <Badge variant="outline" className="cursor-pointer">Error</Badge>
        <Badge variant="outline" className="cursor-pointer">Stopped</Badge>
      </div>

      <Card>
        <CardContent className="flex flex-col items-center justify-center py-16 text-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-muted mb-4">
            <Bot className="h-6 w-6 text-muted-foreground" />
          </div>
          <h3 className="text-sm font-semibold">No agents yet</h3>
          <p className="mt-1 text-sm text-muted-foreground max-w-sm">
            Create your first AI agent to start automating tasks.
          </p>
          <Button className="mt-4" asChild>
            <Link href="/agents/new">
              <Plus className="mr-2 h-4 w-4" />
              Create Agent
            </Link>
          </Button>
        </CardContent>
      </Card>
    </div>
  )
}
