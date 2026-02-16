import { Badge } from "@/components/ui/badge"
import Link from "next/link"

export default async function RunsPage({ params }: { params: Promise<{ agentId: string }> }) {
  const { agentId } = await params

  const runs = [
    { id: "a3f8c1d2", status: "Running", statusClass: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400", pulse: true, duration: "23m 14s", trigger: "User", apiKey: "ANTHROPIC_KEY_1", files: 1, started: "23 min ago" },
    { id: "b7e2f9a1", status: "Completed", statusClass: "bg-emerald-50 text-emerald-700", pulse: false, duration: "18m 42s", trigger: "User", apiKey: "ANTHROPIC_KEY_1", files: 3, started: "1h ago" },
    { id: "c4d1e8b3", status: "Completed", statusClass: "bg-emerald-50 text-emerald-700", pulse: false, duration: "6m 15s", trigger: "Webhook", apiKey: "ANTHROPIC_KEY_2", files: 1, started: "3h ago" },
    { id: "d9a7b2c4", status: "Failed", statusClass: "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-400", pulse: false, duration: "0m 42s", trigger: "Schedule", apiKey: "ANTHROPIC_KEY_1", files: 0, started: "6h ago" },
    { id: "e1f3d5a8", status: "Completed", statusClass: "bg-emerald-50 text-emerald-700", pulse: false, duration: "12m 08s", trigger: "User", apiKey: "ANTHROPIC_KEY_1", files: 2, started: "Yesterday" },
  ]

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Runs table */}
      <div className="border rounded-lg overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b bg-muted/50 text-xs text-muted-foreground uppercase tracking-wide">
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Run ID</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Status</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Duration</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium hidden sm:table-cell">Trigger</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium hidden md:table-cell">API Key</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium hidden sm:table-cell">Files</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Started</th>
            </tr>
          </thead>
          <tbody className="divide-y">
            {runs.map((r) => (
              <tr key={r.id} className="hover:bg-muted/50">
                <td className="px-4 sm:px-6 py-3">
                  <Link href={`/agents/${agentId}/logs`} className="font-mono text-xs hover:underline">{r.id}</Link>
                </td>
                <td className="px-4 sm:px-6 py-3">
                  <Badge variant="secondary" className={`${r.statusClass} text-xs gap-1`}>
                    {r.pulse && <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />}
                    {r.status}
                  </Badge>
                </td>
                <td className="px-4 sm:px-6 py-3 font-mono text-xs">{r.duration}</td>
                <td className="px-4 sm:px-6 py-3 text-muted-foreground hidden sm:table-cell">{r.trigger}</td>
                <td className="px-4 sm:px-6 py-3 font-mono text-xs hidden md:table-cell">{r.apiKey}</td>
                <td className="px-4 sm:px-6 py-3 font-mono text-xs hidden sm:table-cell">{r.files}</td>
                <td className="px-4 sm:px-6 py-3 text-xs text-muted-foreground">{r.started}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {/* Footer */}
      <p className="text-xs text-muted-foreground">5 runs total · 3 completed, 1 running, 1 failed</p>
    </div>
  )
}
