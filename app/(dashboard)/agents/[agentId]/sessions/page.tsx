import { Plus, MessageSquare } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import Link from "next/link"

export default async function SessionsPage({ params }: { params: Promise<{ agentId: string }> }) {
  const { agentId } = await params

  const sessions = [
    { id: 4, title: "SEO blog post — AI platforms Q1 2026", mode: "CHAT", status: "Active", messages: 12, duration: "23m", trigger: "User", started: "23 min ago" },
    { id: 3, title: "Keyword research — SaaS tools", mode: "TASK", status: "Completed", messages: 8, duration: "18m", trigger: "User", started: "1h ago" },
    { id: 2, title: "Meta descriptions batch update", mode: "TASK", status: "Completed", messages: 24, duration: "42m", trigger: "Webhook", started: "3h ago" },
    { id: 1, title: "Content brief — onboarding flow", mode: "CHAT", status: "Failed", messages: 3, duration: "2m", trigger: "User", started: "Yesterday" },
  ]

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Filters row */}
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" className="text-xs">All Modes</Button>
        <Button variant="outline" size="sm" className="text-xs">All Status</Button>
        <div className="ml-auto">
          <Button size="sm" className="gap-1.5" asChild>
            <Link href={`/agents/${agentId}/chat`}>
              <Plus className="h-3.5 w-3.5" /> New Session
            </Link>
          </Button>
        </div>
      </div>

      {/* Sessions table */}
      <div className="border rounded-lg overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b bg-muted/50 text-xs text-muted-foreground uppercase tracking-wide">
              <th className="text-left px-4 sm:px-6 py-3 font-medium">#</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Title</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Mode</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Status</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Messages</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Duration</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium hidden sm:table-cell">Trigger</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium hidden md:table-cell">Started</th>
            </tr>
          </thead>
          <tbody className="divide-y">
            {sessions.map((s) => (
              <tr key={s.id} className="hover:bg-muted/50">
                <td className="px-4 sm:px-6 py-3 font-mono text-xs text-muted-foreground">{s.id}</td>
                <td className="px-4 sm:px-6 py-3">
                  <Link href={`/agents/${agentId}/chat`} className="hover:underline flex items-center gap-1.5">
                    <MessageSquare className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                    <span className="truncate max-w-[200px] sm:max-w-none">{s.title}</span>
                  </Link>
                </td>
                <td className="px-4 sm:px-6 py-3">
                  <Badge variant={s.mode === "CHAT" ? "secondary" : "outline"} className="text-xs">{s.mode}</Badge>
                </td>
                <td className="px-4 sm:px-6 py-3">
                  <Badge
                    variant="secondary"
                    className={
                      s.status === "Active" ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400 text-xs"
                        : s.status === "Failed" ? "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-400 text-xs"
                          : "text-xs"
                    }
                  >
                    {s.status}
                  </Badge>
                </td>
                <td className="px-4 sm:px-6 py-3 font-mono text-xs">{s.messages}</td>
                <td className="px-4 sm:px-6 py-3 font-mono text-xs">{s.duration}</td>
                <td className="px-4 sm:px-6 py-3 text-muted-foreground hidden sm:table-cell">{s.trigger}</td>
                <td className="px-4 sm:px-6 py-3 text-xs text-muted-foreground hidden md:table-cell">{s.started}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {/* Footer */}
      <p className="text-xs text-muted-foreground">4 sessions total</p>
    </div>
  )
}
