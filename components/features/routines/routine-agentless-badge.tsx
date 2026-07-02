"use client"

import { Zap } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

// AgentlessBadge — surfaces the `agentless: true` token-zero guarantee
// (internal/pipeline: save-time validation rejects agent_run/
// call_pipeline/eval.online so the routine can never invoke an LLM).
// Read-only visibility only; renders nothing when the flag isn't set.
//
// Sized to slot into either the compact list-row chip row (size="sm")
// or the detail hero pill row (size="md", default) — same visual
// family as the other outline Badges in routines-detail-panel.tsx /
// routines-list-view.tsx.
export function AgentlessBadge({
  agentless,
  size = "md",
  className,
}: {
  agentless?: boolean
  size?: "sm" | "md"
  className?: string
}) {
  if (!agentless) return null
  return (
    <Badge
      variant="outline"
      className={cn(
        "gap-1 border-emerald-500/30 bg-emerald-500/10 font-medium text-emerald-400",
        size === "sm" ? "px-1.5 py-0 text-[10px]" : "px-2 py-0 text-[11px]",
        className,
      )}
      title="Agentless routine — validated to never invoke an LLM (only deterministic http/code/transform steps). No agent tokens are spent when this runs."
    >
      <Zap className={size === "sm" ? "h-2.5 w-2.5" : "h-3 w-3"} aria-hidden />
      Agentless · token-zero
    </Badge>
  )
}
