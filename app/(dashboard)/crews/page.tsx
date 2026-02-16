import { Network } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"

export default function CrewsPage() {
  return (
    <div className="p-6 space-y-6">
      <div>
        <h1 className="text-lg font-semibold">Crews</h1>
        <p className="text-sm text-muted-foreground">AI team orchestration with Crew Leaders and Virtual Director</p>
      </div>

      <Card>
        <CardContent className="flex flex-col items-center justify-center py-16 text-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-muted mb-4">
            <Network className="h-6 w-6 text-muted-foreground" />
          </div>
          <h3 className="text-sm font-semibold">Orchestration (Phase 2)</h3>
          <p className="mt-1 text-sm text-muted-foreground max-w-sm">
            Crew Leaders delegate tasks to Workers. Virtual Director coordinates across teams. Available after MVP.
          </p>
          <Badge variant="outline" className="mt-4">Coming Soon</Badge>
        </CardContent>
      </Card>
    </div>
  )
}
