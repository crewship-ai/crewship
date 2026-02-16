import { Bot, MessageSquare, ScrollText, Settings, Pause } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import Link from "next/link"

export default async function AgentOverviewPage({ params }: { params: Promise<{ agentId: string }> }) {
  const { agentId } = await params

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Quick Actions */}
      <div className="flex flex-wrap items-center gap-2 sm:gap-3">
        <Button variant="outline" size="sm" className="text-destructive border-destructive/30 hover:bg-destructive/10 gap-2">
          <Pause className="h-4 w-4" />
          Stop Agent
        </Button>
        <Button size="sm" className="gap-2" asChild>
          <Link href={`/agents/${agentId}/chat`}>
            <MessageSquare className="h-4 w-4" />
            Open Chat
          </Link>
        </Button>
        <Button variant="outline" size="sm" className="gap-2" asChild>
          <Link href={`/agents/${agentId}/logs`}>
            <ScrollText className="h-4 w-4" />
            View Logs
          </Link>
        </Button>
        <Button variant="outline" size="sm" className="gap-2" asChild>
          <Link href={`/agents/${agentId}/settings`}>
            <Settings className="h-4 w-4" />
            Edit Settings
          </Link>
        </Button>
      </div>

      {/* Identity + Current Run */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        {/* Identity Card */}
        <Card>
          <CardContent className="p-4 sm:p-6">
            <div className="flex items-center gap-4 mb-4">
              <div className="flex h-14 w-14 items-center justify-center rounded-xl bg-emerald-50 dark:bg-emerald-950/30">
                <Bot className="h-7 w-7 text-emerald-700 dark:text-emerald-400" />
              </div>
              <div>
                <h2 className="text-lg font-semibold">Claude -- SEO Writer</h2>
                <p className="text-sm text-muted-foreground">SEO Content Specialist</p>
              </div>
            </div>
            <div className="space-y-3 text-sm">
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">Slug</span>
                <code className="text-xs bg-muted px-2 py-0.5 rounded">claude-seo-writer</code>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">Team</span>
                <span className="flex items-center gap-1.5">
                  <span className="h-2 w-2 rounded-full bg-orange-500" />
                  Marketing
                </span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">CLI Adapter</span>
                <span className="font-medium">Claude Code</span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">Model</span>
                <code className="text-xs">claude-sonnet-4</code>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">Role</span>
                <Badge variant="outline" className="text-amber-600 border-amber-300 text-xs">LEADER</Badge>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">Tool Profile</span>
                <Badge variant="secondary" className="text-xs">CODING</Badge>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">Memory</span>
                <Badge variant="secondary" className="text-xs bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400">Enabled</Badge>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">Timeout</span>
                <span>30 min</span>
              </div>
            </div>
            <p className="mt-4 text-sm text-muted-foreground leading-relaxed border-t pt-4">
              Specialized in creating SEO-optimized blog posts, meta descriptions, and content briefs. Targets long-tail keywords with 1.5% density.
            </p>
          </CardContent>
        </Card>

        {/* Current Run */}
        <Card className="lg:col-span-2">
          <CardContent className="p-4 sm:p-6">
            <div className="flex items-center justify-between mb-4">
              <h3 className="text-base font-semibold flex items-center gap-2">
                <span className="h-2 w-2 rounded-full bg-emerald-500 animate-pulse" />
                Current Run
              </h3>
              <span className="text-xs text-muted-foreground">Run ID: <code>a3f8c1d2</code></span>
            </div>
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 sm:gap-4 mb-4">
              <div className="bg-muted/50 rounded-lg p-3 text-center">
                <div className="text-xl sm:text-2xl font-bold font-mono text-emerald-600">23:14</div>
                <div className="text-xs text-muted-foreground mt-1">Runtime</div>
              </div>
              <div className="bg-muted/50 rounded-lg p-3 text-center">
                <div className="text-sm font-medium">User Trigger</div>
                <div className="text-xs text-muted-foreground mt-1">Trigger Type</div>
              </div>
              <div className="bg-muted/50 rounded-lg p-3 text-center">
                <div className="text-sm font-medium">Session #4</div>
                <div className="text-xs text-muted-foreground mt-1">Active Session</div>
              </div>
              <div className="bg-muted/50 rounded-lg p-3 text-center">
                <div className="text-sm font-mono font-medium">ANTHROPIC_KEY_1</div>
                <div className="text-xs text-muted-foreground mt-1">API Key in Use</div>
              </div>
            </div>
            {/* Progress */}
            <div className="mb-4">
              <div className="flex items-center justify-between text-xs mb-1">
                <span className="text-muted-foreground">Timeout progress</span>
                <span className="font-mono">23:14 / 30:00</span>
              </div>
              <div className="w-full bg-muted rounded-full h-2">
                <div className="bg-emerald-500 h-2 rounded-full transition-all" style={{ width: "77%" }} />
              </div>
            </div>
            {/* Live activity */}
            <div className="bg-neutral-950 rounded-lg p-3 sm:p-4 font-mono text-[11px] sm:text-xs leading-relaxed overflow-x-auto">
              <div className="text-neutral-500">[10:23:01] <span className="text-neutral-300">Thinking: Analyzing SEO trends for Q1 2026...</span></div>
              <div className="text-neutral-500">[10:23:05] <span className="text-cyan-400">Tool: web-search</span> <span className="text-neutral-400">{`{"query": "AI management platforms SEO 2026"}`}</span></div>
              <div className="text-neutral-500">[10:23:12] <span className="text-green-400">Result:</span> <span className="text-neutral-300">Found 12 relevant articles...</span></div>
              <div className="text-neutral-500">[10:23:14] <span className="text-white">Writing blog post section 3 of 5...</span> <span className="animate-pulse text-emerald-400">|</span></div>
            </div>
          </CardContent>
        </Card>
      </div>

      {/* Stats Row */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 sm:gap-4">
        <Card>
          <CardContent className="p-4">
            <div className="text-xs text-muted-foreground uppercase tracking-wide font-medium">Total Runs</div>
            <div className="mt-1 text-2xl font-bold">24</div>
            <div className="mt-1 text-xs text-muted-foreground">18 success, 4 running, 2 failed</div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4">
            <div className="text-xs text-muted-foreground uppercase tracking-wide font-medium">Avg Runtime</div>
            <div className="mt-1 text-2xl font-bold font-mono">14m</div>
            <div className="mt-1 text-xs text-muted-foreground">Last 7 days</div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4">
            <div className="text-xs text-muted-foreground uppercase tracking-wide font-medium">Success Rate</div>
            <div className="mt-1 text-2xl font-bold text-emerald-600">92%</div>
            <div className="mt-1 text-xs text-muted-foreground">22 of 24 runs</div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4">
            <div className="text-xs text-muted-foreground uppercase tracking-wide font-medium">Files Generated</div>
            <div className="mt-1 text-2xl font-bold">18</div>
            <div className="mt-1 text-xs text-muted-foreground">2 pending review</div>
          </CardContent>
        </Card>
      </div>

      {/* Recent Runs */}
      <Card>
        <CardHeader className="pb-3">
          <div className="flex items-center justify-between">
            <CardTitle className="text-base">Recent Runs</CardTitle>
            <Link href={`/agents/${agentId}/runs`} className="text-xs text-primary hover:underline">View all</Link>
          </div>
        </CardHeader>
        <CardContent className="p-0">
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-t border-b text-xs text-muted-foreground uppercase tracking-wide">
                  <th className="text-left px-4 sm:px-6 py-3 font-medium">Run ID</th>
                  <th className="text-left px-4 sm:px-6 py-3 font-medium">Status</th>
                  <th className="text-left px-4 sm:px-6 py-3 font-medium">Duration</th>
                  <th className="text-left px-4 sm:px-6 py-3 font-medium">Trigger</th>
                  <th className="text-left px-4 sm:px-6 py-3 font-medium">Started</th>
                </tr>
              </thead>
              <tbody className="divide-y">
                <tr className="hover:bg-muted/50">
                  <td className="px-4 sm:px-6 py-3 font-mono text-xs">a3f8c1d2</td>
                  <td className="px-4 sm:px-6 py-3"><Badge variant="secondary" className="bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400 text-xs gap-1"><span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />Running</Badge></td>
                  <td className="px-4 sm:px-6 py-3 font-mono text-xs">23m 14s</td>
                  <td className="px-4 sm:px-6 py-3 text-muted-foreground">User</td>
                  <td className="px-4 sm:px-6 py-3 text-xs text-muted-foreground">23 min ago</td>
                </tr>
                <tr className="hover:bg-muted/50">
                  <td className="px-4 sm:px-6 py-3 font-mono text-xs">b7e2f9a1</td>
                  <td className="px-4 sm:px-6 py-3"><Badge variant="secondary" className="bg-emerald-50 text-emerald-700 text-xs">Completed</Badge></td>
                  <td className="px-4 sm:px-6 py-3 font-mono text-xs">18m 42s</td>
                  <td className="px-4 sm:px-6 py-3 text-muted-foreground">User</td>
                  <td className="px-4 sm:px-6 py-3 text-xs text-muted-foreground">1h ago</td>
                </tr>
                <tr className="hover:bg-muted/50">
                  <td className="px-4 sm:px-6 py-3 font-mono text-xs">c4d1e8b3</td>
                  <td className="px-4 sm:px-6 py-3"><Badge variant="secondary" className="bg-emerald-50 text-emerald-700 text-xs">Completed</Badge></td>
                  <td className="px-4 sm:px-6 py-3 font-mono text-xs">6m 15s</td>
                  <td className="px-4 sm:px-6 py-3 text-muted-foreground">Webhook</td>
                  <td className="px-4 sm:px-6 py-3 text-xs text-muted-foreground">3h ago</td>
                </tr>
              </tbody>
            </table>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
