"use client"

import { cn } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"

/** Kind → colour mapping per spec. Unknown kinds fall back to gray. */
const KIND_CLASS: Record<string, string> = {
  destructive_op: "bg-destructive/15 text-destructive border-destructive/40",
  cost_threshold: "bg-warn/15 text-warn border-warn/40",
  target_environment: "bg-warn/15 text-warn border-warn/40",
  tool_call: "bg-info/15 text-info border-info/40",
  ephemeral_hire: "bg-purple/15 text-purple border-purple/40",
  custom: "bg-muted text-muted-foreground border-border",
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
