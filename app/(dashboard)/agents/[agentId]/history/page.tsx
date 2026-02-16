import { Plus, Settings, Puzzle, Info } from "lucide-react"
import { Badge } from "@/components/ui/badge"

export default async function HistoryPage({ params }: { params: Promise<{ agentId: string }> }) {
  await params

  const events = [
    {
      icon: Puzzle,
      iconClass: "bg-blue-100 text-blue-600 dark:bg-blue-950 dark:text-blue-400",
      title: "Skill added: SEO Analyzer",
      description: "SEO Analyzer skill was assigned to this agent.",
      user: "Pavel Srba",
      time: "2h ago",
      badge: "Skill",
    },
    {
      icon: Settings,
      iconClass: "bg-amber-100 text-amber-600 dark:bg-amber-950 dark:text-amber-400",
      title: "Configuration updated",
      description: 'Timeout changed from 15 min → 30 min. Model changed from "claude-haiku-4" → "claude-sonnet-4".',
      user: "Pavel Srba",
      time: "1d ago",
      badge: "Config",
    },
    {
      icon: Plus,
      iconClass: "bg-emerald-100 text-emerald-600 dark:bg-emerald-950 dark:text-emerald-400",
      title: "Agent created",
      description: 'Agent "Claude — SEO Writer" was created and assigned to team Marketing.',
      user: "Pavel Srba",
      time: "3d ago",
      badge: "Created",
    },
  ]

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Backend requirement banner */}
      <div className="flex items-center gap-2 rounded-md bg-muted/10 border border-border px-3 py-2">
        <Info className="h-4 w-4 text-muted-foreground shrink-0" />
        <p className="text-xs text-muted-foreground">Configuration history will be populated by the audit log system (coming soon).</p>
      </div>

      <p className="text-sm text-muted-foreground">Configuration change history</p>

      {/* Timeline */}
      <div className="relative space-y-0">
        {events.map((event, i) => (
          <div key={i} className="flex gap-4 pb-8 last:pb-0 relative">
            {/* Timeline line */}
            {i < events.length - 1 && (
              <div className="absolute left-5 top-10 bottom-0 w-px bg-border" />
            )}

            {/* Icon */}
            <div className={`flex h-10 w-10 shrink-0 items-center justify-center rounded-full ${event.iconClass}`}>
              <event.icon className="h-4 w-4" />
            </div>

            {/* Content */}
            <div className="space-y-1 pt-1">
              <div className="flex items-center gap-2 flex-wrap">
                <h3 className="text-sm font-medium">{event.title}</h3>
                <Badge variant="outline" className="text-xs">{event.badge}</Badge>
              </div>
              <p className="text-xs text-muted-foreground leading-relaxed">{event.description}</p>
              <p className="text-xs text-muted-foreground">
                by <span className="text-foreground">{event.user}</span> · {event.time}
              </p>
            </div>
          </div>
        ))}
      </div>

      {/* Footer */}
      <p className="text-xs text-muted-foreground">3 events total</p>
    </div>
  )
}
