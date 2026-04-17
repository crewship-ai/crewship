"use client"

import { cn } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"

/** Kind → colour mapping per spec. Unknown kinds fall back to gray. */
const KIND_CLASS: Record<string, string> = {
  destructive_op: "bg-red-500/15 text-red-300 border-red-500/40",
  cost_threshold: "bg-amber-500/15 text-amber-300 border-amber-500/40",
  target_environment: "bg-orange-500/15 text-orange-300 border-orange-500/40",
  tool_call: "bg-blue-500/15 text-blue-300 border-blue-500/40",
  custom: "bg-slate-500/15 text-slate-300 border-slate-500/40",
}

interface KindBadgeProps {
  kind: string
  className?: string
}

export function KindBadge({ kind, className }: KindBadgeProps) {
  const cls = KIND_CLASS[kind] ?? "bg-muted text-muted-foreground border-border"
  return (
    <Badge variant="outline" className={cn("text-[10px] font-mono uppercase tracking-wider border", cls, className)}>
      {kind.replace(/_/g, " ")}
    </Badge>
  )
}
