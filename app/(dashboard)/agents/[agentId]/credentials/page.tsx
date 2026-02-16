import { Plus, ShieldCheck } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"

export default async function CredentialsPage({ params }: { params: Promise<{ agentId: string }> }) {
  await params

  const credentials = [
    { envVar: "ANTHROPIC_API_KEY", type: "LLM", typeClass: "bg-violet-50 text-violet-700 dark:bg-violet-950 dark:text-violet-400", priority: 1, status: "Active", key: "ANTHROPIC_KEY_1" },
    { envVar: "ANTHROPIC_API_KEY", type: "LLM", typeClass: "bg-violet-50 text-violet-700 dark:bg-violet-950 dark:text-violet-400", priority: 2, status: "Standby", key: "ANTHROPIC_KEY_2" },
    { envVar: "BRAVE_API_KEY", type: "Tool", typeClass: "bg-cyan-50 text-cyan-700 dark:bg-cyan-950 dark:text-cyan-400", priority: 1, status: "Active", key: "BRAVE_KEY_1" },
  ]

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <ShieldCheck className="h-4 w-4 text-muted-foreground" />
          <p className="text-sm text-muted-foreground">3 credentials assigned · AES-256-GCM encrypted</p>
        </div>
        <Button size="sm" className="gap-1.5">
          <Plus className="h-3.5 w-3.5" /> Assign Credential
        </Button>
      </div>

      {/* Credentials table */}
      <div className="border rounded-lg overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b bg-muted/50 text-xs text-muted-foreground uppercase tracking-wide">
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Env Variable</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Key Name</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Type</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Priority</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Status</th>
            </tr>
          </thead>
          <tbody className="divide-y">
            {credentials.map((c, i) => (
              <tr key={i} className="hover:bg-muted/50">
                <td className="px-4 sm:px-6 py-3 font-mono text-xs">{c.envVar}</td>
                <td className="px-4 sm:px-6 py-3 font-mono text-xs">{c.key}</td>
                <td className="px-4 sm:px-6 py-3">
                  <Badge variant="secondary" className={`${c.typeClass} text-xs`}>{c.type}</Badge>
                </td>
                <td className="px-4 sm:px-6 py-3 text-center font-mono text-xs">{c.priority}</td>
                <td className="px-4 sm:px-6 py-3">
                  <Badge
                    variant="secondary"
                    className={
                      c.status === "Active"
                        ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400 text-xs"
                        : "text-xs"
                    }
                  >
                    {c.status}
                  </Badge>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {/* Info */}
      <p className="text-xs text-muted-foreground">
        Credentials are injected at container start. Priority-based failover rotates keys automatically on rate limit errors.
      </p>
    </div>
  )
}
