import { Send, PanelRightOpen, Bot, User, Wrench, Brain, Plus, ChevronDown } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Textarea } from "@/components/ui/textarea"

export default async function ChatPage({ params }: { params: Promise<{ agentId: string }> }) {
  const { agentId } = await params

  return (
    <div className="flex flex-col h-full">
      {/* Session selector bar */}
      <div className="flex flex-wrap items-center gap-2 border-b px-4 sm:px-6 py-2 bg-muted/30">
        <Button variant="outline" size="sm" className="gap-1.5 text-xs">
          Session #4 <ChevronDown className="h-3 w-3" />
        </Button>
        <Button variant="outline" size="sm" className="gap-1.5 text-xs">
          <Plus className="h-3 w-3" /> New Session
        </Button>
        <div className="hidden sm:flex items-center gap-3 ml-auto text-xs text-muted-foreground">
          <span>CLI: <strong className="text-foreground">Claude Code</strong></span>
          <span>Model: <code className="text-[11px]">claude-sonnet-4</code></span>
          <span>Key: <code className="text-[11px]">ANTHROPIC_KEY_1</code></span>
        </div>
      </div>

      {/* Chat area */}
      <div className="flex-1 flex overflow-hidden">
        {/* Messages */}
        <div className="flex-1 overflow-y-auto p-4 sm:p-6 space-y-4">
          {/* User message */}
          <div className="flex gap-3 max-w-2xl">
            <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-primary/10">
              <User className="h-4 w-4 text-primary" />
            </div>
            <div className="space-y-1">
              <p className="text-xs text-muted-foreground">You · 10:22 AM</p>
              <p className="text-sm">Write an SEO-optimized blog post about AI agent management platforms for Q1 2026.</p>
            </div>
          </div>

          {/* Agent thinking */}
          <div className="flex gap-3 max-w-2xl">
            <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-amber-100 dark:bg-amber-950">
              <Brain className="h-4 w-4 text-amber-600" />
            </div>
            <div className="space-y-1">
              <p className="text-xs text-muted-foreground">Agent · Thinking</p>
              <p className="text-sm text-muted-foreground italic">Analyzing SEO trends for Q1 2026 and identifying target keywords...</p>
            </div>
          </div>

          {/* Tool call */}
          <div className="flex gap-3 max-w-2xl">
            <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-cyan-100 dark:bg-cyan-950">
              <Wrench className="h-4 w-4 text-cyan-600" />
            </div>
            <div className="space-y-1">
              <p className="text-xs text-muted-foreground">Tool Call · web-search</p>
              <Card className="py-2">
                <CardContent className="p-3 font-mono text-xs">
                  {`{"query": "AI management platforms SEO 2026", "results": 12}`}
                </CardContent>
              </Card>
            </div>
          </div>

          {/* Agent response */}
          <div className="flex gap-3 max-w-2xl">
            <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-emerald-100 dark:bg-emerald-950">
              <Bot className="h-4 w-4 text-emerald-600" />
            </div>
            <div className="space-y-1">
              <p className="text-xs text-muted-foreground">Agent · 10:23 AM</p>
              <div className="text-sm space-y-2">
                <p>I&apos;ve drafted the blog post. Here&apos;s a summary:</p>
                <p><strong>Title:</strong> &quot;Top 10 AI Agent Management Platforms in 2026&quot;</p>
                <p>The post targets long-tail keywords with 1.5% density and includes 5 sections...</p>
                <Badge variant="secondary" className="text-xs">blog-post.md saved to /output/</Badge>
              </div>
            </div>
          </div>
        </div>

        {/* File preview panel (hint) */}
        <div className="hidden lg:flex w-80 border-l flex-col">
          <div className="flex items-center justify-between px-4 py-2 border-b">
            <span className="text-xs font-medium">File Preview</span>
            <PanelRightOpen className="h-4 w-4 text-muted-foreground" />
          </div>
          <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground p-4">
            Select a file to preview
          </div>
        </div>
      </div>

      {/* Input area */}
      <div className="border-t bg-background p-4 sm:px-6">
        <div className="flex items-end gap-2 max-w-2xl">
          <Textarea
            placeholder={`Message agent ${agentId}...`}
            className="min-h-[44px] max-h-32 resize-none"
            rows={1}
          />
          <Button size="icon" className="shrink-0 h-10 w-10">
            <Send className="h-4 w-4" />
          </Button>
        </div>
      </div>
    </div>
  )
}
