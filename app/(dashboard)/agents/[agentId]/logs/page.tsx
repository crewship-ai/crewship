import { Download, Trash2, ArrowDownToLine } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"

export default async function LogsPage({ params }: { params: Promise<{ agentId: string }> }) {
  await params

  const levels = ["ALL", "INFO", "WARN", "ERROR"]

  const logLines = [
    { time: "10:22:58", level: "INFO", msg: "Session #4 started (mode=CHAT, trigger=user)" },
    { time: "10:22:59", level: "INFO", msg: "Container crewship-agent-claude-seo-writer started (image: crewship/agent-runtime:latest)" },
    { time: "10:23:00", level: "INFO", msg: "Credential injected: ANTHROPIC_API_KEY (priority=1, key=ANTHROPIC_KEY_1)" },
    { time: "10:23:01", level: "INFO", msg: "Agent thinking: Analyzing SEO trends for Q1 2026..." },
    { time: "10:23:05", level: "INFO", msg: 'Tool call: web-search {"query": "AI management platforms SEO 2026"}' },
    { time: "10:23:12", level: "INFO", msg: "Tool result: Found 12 relevant articles (latency=6.8s)" },
    { time: "10:23:13", level: "WARN", msg: "Token usage approaching 80% of context window (148k/200k)" },
    { time: "10:23:14", level: "INFO", msg: "Writing blog post section 3 of 5..." },
    { time: "10:23:18", level: "INFO", msg: "File written: /output/blog-post.md (14.2 KB)" },
    { time: "10:23:19", level: "ERROR", msg: "Rate limit hit on ANTHROPIC_KEY_1, failing over to ANTHROPIC_KEY_2" },
    { time: "10:23:20", level: "INFO", msg: "Credential rotated: ANTHROPIC_API_KEY (priority=2, key=ANTHROPIC_KEY_2)" },
    { time: "10:23:22", level: "INFO", msg: "Resumed generation with fallback key" },
  ]

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Toolbar */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex items-center gap-1">
          {levels.map((lvl) => (
            <Button key={lvl} variant={lvl === "ALL" ? "default" : "outline"} size="sm" className="text-xs h-7 px-2.5">
              {lvl}
            </Button>
          ))}
        </div>
        <div className="flex items-center gap-1 ml-auto">
          <Button variant="outline" size="sm" className="gap-1.5 text-xs">
            <ArrowDownToLine className="h-3.5 w-3.5" /> Auto-scroll
          </Button>
          <Button variant="outline" size="sm" className="gap-1.5 text-xs">
            <Download className="h-3.5 w-3.5" /> Download
          </Button>
          <Button variant="outline" size="sm" className="gap-1.5 text-xs text-destructive">
            <Trash2 className="h-3.5 w-3.5" /> Clear
          </Button>
        </div>
      </div>

      {/* Log viewer */}
      <div className="bg-neutral-950 rounded-lg p-3 sm:p-4 font-mono text-[11px] sm:text-xs leading-relaxed overflow-x-auto max-h-[600px] overflow-y-auto">
        {logLines.map((line, i) => {
          const levelColor = line.level === "ERROR" ? "text-red-400"
            : line.level === "WARN" ? "text-yellow-400"
              : "text-neutral-500"
          const msgColor = line.level === "ERROR" ? "text-red-300"
            : line.level === "WARN" ? "text-yellow-200"
              : "text-neutral-300"

          return (
            <div key={i} className="hover:bg-white/5 px-1 -mx-1 rounded">
              <span className="text-neutral-600">[{line.time}]</span>{" "}
              <Badge variant="outline" className={`${levelColor} border-current/20 text-[10px] px-1 py-0 font-mono`}>
                {line.level}
              </Badge>{" "}
              <span className={msgColor}>{line.msg}</span>
            </div>
          )
        })}
      </div>

      {/* Footer */}
      <p className="text-xs text-muted-foreground">12 log entries · Streaming from run a3f8c1d2</p>
    </div>
  )
}
