import { Shield } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"

export default function AuditPage() {
  return (
    <div className="p-6 space-y-6">
      <div>
        <h1 className="text-lg font-semibold">Audit Log</h1>
        <p className="text-sm text-muted-foreground">Track all actions in your organization</p>
      </div>

      <Card>
        <CardContent className="flex flex-col items-center justify-center py-16 text-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-muted mb-4">
            <Shield className="h-6 w-6 text-muted-foreground" />
          </div>
          <h3 className="text-sm font-semibold">No activity yet</h3>
          <p className="mt-1 text-sm text-muted-foreground max-w-sm">
            All state-changing actions will be logged here with who, what, and when.
          </p>
        </CardContent>
      </Card>
    </div>
  )
}
